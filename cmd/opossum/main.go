// Command opossum is a Docker Compose-like orchestrator for Apple's `container`
// runtime on macOS 26+.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/doctor"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
	"github.com/suruseas/opossum/internal/workspace"
	"golang.org/x/term"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

var (
	composeFiles []string
	projectName  string
	dnsDomain    string
	verbose      bool
	envFiles     []string
)

// newRootCmd builds the command tree. Extracted from main so tests can execute
// the CLI with arbitrary arguments.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "opossum",
		Short:         "Docker Compose-like orchestration for Apple's container runtime",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.PersistentFlags().StringArrayVarP(&composeFiles, "file", "f", nil, "path to a compose file (repeatable; later files override earlier ones). Default: a discovered compose file plus its override")
	root.PersistentFlags().StringVarP(&projectName, "project-name", "p", "", "project name (defaults to the compose file's directory)")
	root.PersistentFlags().StringVar(&dnsDomain, "dns-domain", "opossum", "local DNS domain for bare-name service discovery (create once: sudo container system dns create <domain>)")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "print each underlying container command as it runs (useful for bug reports)")
	root.PersistentFlags().StringArrayVar(&envFiles, "env-file", nil, "env file(s) for ${VAR} interpolation, replacing the default .env (repeatable; later files win)")

	root.AddCommand(
		upCmd(), downCmd(), psCmd(), imagesCmd(), logsCmd(), statsCmd(),
		stopCmd(), restartCmd(), startCmd(), execCmd(),
		buildCmd(), pullCmd(), killCmd(), runCmd(),
		importCmd(), configCmd(), doctorCmd(), cpCmd(), watchCmd(), wsCmd(),
	)

	// Preflight: every runtime-touching command needs Apple's `container` CLI on
	// PATH. Check once here so all of them fail the same way — a coded, actionable
	// error (OPSM-404) and a non-zero exit — instead of each command inventing its
	// own signal (an empty `ps` table, a raw exec error, a bespoke message).
	// Commands that don't touch the runtime are exempt (see runtimePreflightExempt).
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmdSkipsRuntimePreflight(cmd) {
			return nil
		}
		rt := runtime.New()
		if !rt.Available() {
			return orchestrator.ErrRuntimeAbsent()
		}
		// A command that acts on the runtime needs it *running*, not just installed.
		// Since the runtime's service doesn't start on demand, opossum starts it here
		// (a light, idempotent launchd start) so an agent's edit→run loop doesn't
		// stall on "system start". Opt out with OPOSSUM_NO_AUTO_START. The `ps`/
		// `images` "is anything here?" queries never auto-start (an empty answer is
		// meaningful, and a read shouldn't have a side effect) — they report OPSM-405
		// themselves. Every other command (up/run/logs/stats/…) needs the runtime up
		// to do anything, so auto-starting it is the useful move.
		if !cmdReadOnlyRuntime(cmd) && !rt.SystemRunning() {
			if os.Getenv("OPOSSUM_NO_AUTO_START") != "" {
				return orchestrator.ErrRuntimeStopped()
			}
			fmt.Fprintln(os.Stderr, "opossum: "+orchestrator.NoticeRuntimeAutoStart())
			if err := rt.StartSystem(); err != nil {
				return orchestrator.ErrRuntimeAutoStartFailed(err)
			}
		}
		return nil
	}
	return root
}

// runtimeReadOnly names the "is anything here?" query commands that must never
// auto-start the runtime — a read shouldn't have a side effect, and an empty
// answer is itself meaningful. They report a stopped runtime (OPSM-405) themselves
// (see Ps/Images). Every other command needs the runtime up to do useful work, so
// it auto-starts; logs/stats included, since they'd otherwise just fail.
var runtimeReadOnly = map[string]bool{"ps": true, "images": true}

func cmdReadOnlyRuntime(cmd *cobra.Command) bool { return runtimeReadOnly[cmd.Name()] }

// runtimePreflightExempt names commands that must run without the `container` CLI
// installed: `config` only parses/renders compose, `doctor` self-diagnoses the
// runtime (and reports its absence itself), and cobra's own help/completion
// machinery must never be gated. A command is exempt if its own name or any
// ancestor's name is listed. The root command is handled separately (bare
// `opossum` prints help and touches no runtime), so "opossum" is deliberately
// absent here — listing it would exempt every command via the ancestor walk.
var runtimePreflightExempt = map[string]bool{
	"config":           true,
	"doctor":           true,
	"ws":               true, // operates on a directory, never the container runtime
	"help":             true,
	"completion":       true,
	"__complete":       true, // cobra's hidden shell-completion driver
	"__completeNoDesc": true,
}

func cmdSkipsRuntimePreflight(cmd *cobra.Command) bool {
	if cmd.Parent() == nil {
		return true // the root command itself: prints help, touches no runtime
	}
	for c := cmd; c != nil; c = c.Parent() {
		if runtimePreflightExempt[c.Name()] {
			return true
		}
	}
	return false
}

func watchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Sync host file changes into running containers per each service's develop.watch rules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			// Ctrl-C stops watching cleanly.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)
			go func() { <-sig; cancel() }()
			return o.Watch(ctx)
		},
	}
}

// wsCmd groups the workspace snapshot/rollback commands. A workspace is just a
// directory (the agent's `./work` by default) — these never touch the container
// runtime, so `ws` is exempt from the runtime preflight.
func wsCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "ws",
		Short: "Snapshot and roll back a workspace directory (fast, copy-on-write on APFS)",
		Long: "Snapshot and roll back a workspace directory using APFS file clones, so an " +
			"agent can try something risky and reset in an instant.\n\nA snapshot is copy-on-write: " +
			"near-instant and almost no extra disk until the workspace and the snapshot diverge. " +
			"Snapshots live in a `.opossum-snapshots/` directory beside the workspace (add it to " +
			"`.gitignore`; it isn't part of the workspace). On a non-APFS filesystem, snapshotting " +
			"falls back to a full copy and says so.",
	}
	cmd.PersistentFlags().StringVar(&path, "path", "./work", "the workspace directory to snapshot")

	snapshot := &cobra.Command{
		Use:   "snapshot [name]",
		Short: "Save the current workspace (name defaults to a timestamp)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := workspace.DefaultName()
			if len(args) == 1 {
				name = args[0]
			}
			fastClone, err := workspace.New(path).Snapshot(name)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Saved workspace snapshot %q\n", name)
			if !fastClone {
				fmt.Fprintln(out, "note: this filesystem doesn't support cloning — made a full copy (slower, uses disk)")
			}
			return nil
		},
	}
	list := &cobra.Command{
		Use:     "ls",
		Short:   "List saved workspace snapshots (oldest first)",
		Aliases: []string{"list"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			snaps, err := workspace.New(path).List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(snaps) == 0 {
				fmt.Fprintln(out, "No snapshots yet.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSAVED")
			for _, s := range snaps {
				fmt.Fprintf(tw, "%s\t%s\n", s.Name, s.ModTime.Format("2006-01-02 15:04:05"))
			}
			return tw.Flush()
		},
	}
	rollback := &cobra.Command{
		Use:   "rollback <name>",
		Short: "Restore the workspace from a snapshot (saves the current state first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			autosave, err := workspace.New(path).Rollback(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rolled workspace back to %q (the previous state was saved as %q)\n", args[0], autosave)
			return nil
		},
	}
	rm := &cobra.Command{
		Use:     "rm <name>...",
		Short:   "Delete named snapshots",
		Aliases: []string{"remove"},
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := workspace.New(path).Remove(args...); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %d snapshot(s): %s\n", len(args), strings.Join(args, ", "))
			return nil
		},
	}
	var keep int
	var all bool
	prune := &cobra.Command{
		Use:   "prune",
		Short: "Remove snapshots to reclaim space (auto-save snapshots by default)",
		Long: "Remove snapshots to reclaim space.\n\nBy default it removes only the auto-save " +
			"snapshots (the `before-rollback-…` ones Rollback makes automatically); pass --all to " +
			"include ones you named. --keep N keeps the N newest of whichever set is targeted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, err := workspace.New(path).Prune(keep, all)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(removed) == 0 {
				fmt.Fprintln(out, "Nothing to prune.")
				return nil
			}
			fmt.Fprintf(out, "Removed %d snapshot(s): %s\n", len(removed), strings.Join(removed, ", "))
			return nil
		},
	}
	prune.Flags().IntVar(&keep, "keep", 0, "keep the N newest of the targeted snapshots")
	prune.Flags().BoolVar(&all, "all", false, "target every snapshot, not just auto-saves")

	cmd.AddCommand(snapshot, list, rollback, rm, prune)
	return cmd
}

func servicesCmd(use, short string, fn func(*orchestrator.Orchestrator, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return fn(o, args)
		},
	}
}

func buildCmd() *cobra.Command {
	return servicesCmd("build [service...]", "Build images for services with a build",
		func(o *orchestrator.Orchestrator, args []string) error { return o.Build(args) })
}

func pullCmd() *cobra.Command {
	return servicesCmd("pull [service...]", "Pull images for services",
		func(o *orchestrator.Orchestrator, args []string) error { return o.Pull(args) })
}

func importCmd() *cobra.Command {
	return servicesCmd("import [service...]", "Import services' Docker-built images (reuse Docker builds, skip Apple's builder)",
		func(o *orchestrator.Orchestrator, args []string) error { return o.Import(args...) })
}

func cpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Copy files between a service's container and the host (each path is a host path or service:path)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Copy(args[0], args[1])
		},
	}
}

// errEnvUnhealthy makes `opossum doctor` exit non-zero when a check fails, in a
// way tests can assert (vs. calling os.Exit, which would kill the test process).
var errEnvUnhealthy = errors.New("environment checks failed (see the report above)")

func doctorCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the environment for common problems (runtime, DNS, network, builder, memory)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := runtime.New()
			rt.Verbose = verbose
			// A compose file is optional — it only enables the memory estimate.
			var proj *compose.Project
			if o, err := loadOrchestrator(io.Discard); err == nil {
				proj = o.Project
			}
			var healthy bool
			switch format {
			case "text":
				healthy = doctor.Run(cmd.OutOrStdout(), rt, dnsDomain, proj, hostMemMB())
			case "json":
				var err error
				if healthy, err = doctor.RunJSON(cmd.OutOrStdout(), rt, dnsDomain, proj, hostMemMB()); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown --format %q (want \"text\" or \"json\")", format)
			}
			if !healthy {
				// A failed check (❌ / status:"fail") means the environment isn't ready —
				// return an error so the process exits non-zero and `opossum doctor && …`
				// / CI gate on it, regardless of --format. The report already explains
				// what and how to fix.
				return errEnvUnhealthy
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", `output format: "text" (default, human-readable) or "json" (machine-readable)`)
	return cmd
}

// hostMemMB returns the Mac's physical RAM in MB, or 0 if it can't be read.
func hostMemMB() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	b, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return int(b / (1024 * 1024))
}

func startCmd() *cobra.Command {
	return servicesCmd("start [service...]", "Start existing (stopped) service containers",
		func(o *orchestrator.Orchestrator, args []string) error { return o.Start(args) })
}

func killCmd() *cobra.Command {
	var signal string
	cmd := servicesCmd("kill [service...]", "Send a signal (default KILL) to running services",
		func(o *orchestrator.Orchestrator, args []string) error { return o.Kill(args, signal) })
	cmd.Flags().StringVarP(&signal, "signal", "s", "", "signal to send (default KILL)")
	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "opossum: "+err.Error())
		os.Exit(1)
	}
}

func upCmd() *cobra.Command {
	var foreground bool
	var profiles []string
	var forceRecreate, build, noBuild, removeOrphans, fromDockerCompose, fromDockerLegacy, dryRun bool
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Build and start services in dependency order (all, or the named services plus their dependencies)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if build && noBuild {
				return fmt.Errorf("--build and --no-build are mutually exclusive")
			}
			// --from-docker is the old name for --from-docker-compose. Accept it
			// (same behaviour) and point at the new name rather than failing: the old
			// name is in published docs an agent may have learned.
			if fromDockerLegacy {
				fmt.Fprintln(cmd.ErrOrStderr(), "opossum: --from-docker is deprecated — use --from-docker-compose (same behaviour)")
			}
			fromDocker := fromDockerCompose || fromDockerLegacy
			announceOverlay(cmd.ErrOrStderr())
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			// Bringing a docker compose project over: write the known fixes into an
			// overlay and reload, so `up` reaches startup in one command instead of
			// making the user read a warning and edit their file.
			if fromDocker {
				reloaded, err := adaptProject(cmd.ErrOrStderr(), o, dryRun)
				if err != nil {
					return err
				}
				if reloaded != nil {
					o = reloaded
				}
			}
			o.SetUpOptions(forceRecreate, build, noBuild, removeOrphans, fromDocker)
			// --dry-run resolves everything but executes nothing: it prints the plan
			// (startup order, recreate/skip decisions, and the container commands it
			// would issue) so the plan can be validated before acting.
			o.SetDryRun(dryRun)
			// Activate compose profiles from --profile and COMPOSE_PROFILES so
			// `profiles:`-gated services start.
			o.EnableProfiles(profiles)
			o.EnableProfiles(strings.Split(os.Getenv("COMPOSE_PROFILES"), ","))
			// First Ctrl-C cancels the run so a partial `up` rolls back cleanly; a
			// second one forces an immediate exit (as docker compose does).
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sig := make(chan os.Signal, 2)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)
			go func() {
				<-sig
				cancel()
				<-sig
				os.Exit(130)
			}()
			o.OnSignal(ctx)
			return o.Up(!foreground, args...)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run a single service attached in the foreground instead of detached (rejected for multiple long-running services)")
	cmd.Flags().StringArrayVar(&profiles, "profile", nil, "enable services gated behind this compose profile (repeatable; also honors COMPOSE_PROFILES)")
	cmd.Flags().BoolVar(&forceRecreate, "force-recreate", false, "recreate containers even if their configuration is unchanged")
	cmd.Flags().BoolVar(&build, "build", false, "build images before starting, even if already present")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "don't build images (error if one is missing)")
	cmd.Flags().BoolVar(&removeOrphans, "remove-orphans", false, "remove containers for services no longer in the compose file")
	cmd.Flags().BoolVar(&fromDockerCompose, "from-docker-compose", false, "bring an existing docker compose project up here: for services with a build, import the image from Docker instead of building it (needs the docker CLI)")
	// The old name, kept working so published examples and agents that learned it
	// don't break. Hidden from --help (the new name is the one to advertise), with
	// the run-time notice above steering to it. MarkHidden rather than
	// MarkDeprecated because the notice is hand-rolled: MarkDeprecated would print
	// its own on top of ours, and ours is worded for this migration ("same
	// behaviour") and pinned to cmd.ErrOrStderr() — so it stays on stderr even if a
	// caller redirects the command's out-writer. Both mark it hidden and keep it
	// accepted, so nothing is lost.
	cmd.Flags().BoolVar(&fromDockerLegacy, "from-docker", false, "deprecated alias for --from-docker-compose")
	_ = cmd.Flags().MarkHidden("from-docker")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve and print the plan (startup order and the container commands that would run) without executing anything")
	return cmd
}

func downCmd() *cobra.Command {
	var volumes bool
	var rmi string
	var removeOrphans bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop and remove all services and the project network",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch rmi {
			case "", "local", "all":
			default:
				return fmt.Errorf("--rmi must be \"local\" or \"all\", got %q", rmi)
			}
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Down(volumes, rmi, removeOrphans)
		},
	}
	cmd.Flags().BoolVarP(&volumes, "volumes", "v", false, "also remove named volumes declared by services")
	cmd.Flags().StringVar(&rmi, "rmi", "", "also remove images: \"local\" (opossum-built) or \"all\" (built + pulled)")
	cmd.Flags().BoolVar(&removeOrphans, "remove-orphans", false, "also remove containers for services no longer in the compose file")
	return cmd
}

func imagesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "images",
		Short: "List the image each service uses, and whether it's present locally",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Images()
		},
	}
}

func psCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List services with their container, IP, ports, and status",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Ps()
		},
	}
}

func configCmd() *cobra.Command {
	var servicesOnly bool
	var profiles []string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate and print the resolved compose configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Surface an auto-merged overlay before the resolved YAML, on stderr so
			// it never pollutes the config stdout consumers parse.
			announceOverlay(cmd.ErrOrStderr())
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			// Mirror what `up` would start: gated services appear only when their
			// profile is active (docker compose parity).
			o.EnableProfiles(profiles)
			o.EnableProfiles(strings.Split(os.Getenv("COMPOSE_PROFILES"), ","))
			// Reject the same projects `up` would (an enabled service depending on a
			// gated-inactive one), rather than printing a dangling reference.
			if err := o.ValidateProfiles(); err != nil {
				return err
			}
			enabled := o.EnabledServices()
			w := cmd.OutOrStdout()
			if servicesOnly {
				order, err := o.Project.StartupOrder()
				if err != nil {
					return err
				}
				for _, name := range order {
					if enabled[name] {
						fmt.Fprintln(w, name)
					}
				}
				return nil
			}
			proj := o.Project
			if len(enabled) < len(proj.Services) {
				cp := *proj
				cp.Services = map[string]*compose.Service{}
				for n, s := range proj.Services {
					if enabled[n] {
						cp.Services[n] = s
					}
				}
				proj = &cp
			}
			rendered, err := compose.RenderConfig(proj)
			if err != nil {
				return err
			}
			fmt.Fprint(w, rendered)
			return nil
		},
	}
	cmd.Flags().BoolVar(&servicesOnly, "services", false, "print only the service names")
	cmd.Flags().StringArrayVar(&profiles, "profile", nil, "include services gated behind this compose profile (repeatable; also honors COMPOSE_PROFILES)")
	return cmd
}

// stdinIsTerminal reports whether our stdin is an interactive terminal — the
// cue for `run` to allocate a TTY (-t). Piped or /dev/null stdin (scripts,
// stdio protocols, tests) must NOT get one: a pseudo-terminal would echo input
// back into the stream. A char-device check is not enough (/dev/null is one),
// so ask the terminal driver. It's a var so tests can force the terminal case
// (a test's stdin is never a real TTY).
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// announceOverlay prints a one-line notice on stderr naming an auto-merged opossum
// overlay, so the commands that surface or start the resolved config (`config`,
// `up`) never present something that silently differs from the user's base compose
// file. It's scoped to those two commands on purpose: a `compose.opossum.yaml` is
// meant to live in the repo, so echoing it on every `ps`/`logs`/`stats` (or in a
// watch loop) would be noise. The notice goes to the command's stderr — never the
// config stdout it must not pollute — and is emitted unconditionally (not gated on
// a TTY), so an AI agent capturing stderr sees it too. Only the no-`-f` path
// auto-merges the overlay, so nothing is announced when `-f` was given.
func announceOverlay(stderr io.Writer) {
	if len(composeFiles) != 0 {
		return
	}
	if ol := compose.DiscoverOpossumOverlay("."); ol != "" {
		fmt.Fprintf(stderr, "opossum: merging %s (opossum overlay, highest precedence) — delete it to opt out\n", filepath.Base(ol))
	}
}

func runCmd() *cobra.Command {
	var rm, noDeps, noTTY, ssh, audit bool
	var auditFormat string
	var profiles []string
	cmd := &cobra.Command{
		Use:   "run [--rm] [--no-deps] [--audit] <service> [command...]",
		Short: "Run a one-off command in a new container for a service",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Progress goes to stderr: a one-off's stdout belongs to the container
			// (docker compose does the same), so piping `opossum run` output —
			// e.g. an MCP server's JSON-RPC over stdio — stays clean.
			o, err := loadOrchestrator(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			o.EnableProfiles(profiles)
			o.EnableProfiles(strings.Split(os.Getenv("COMPOSE_PROFILES"), ","))
			opts := orchestrator.RunOneOffOptions{Rm: rm, NoDeps: noDeps, TTY: stdinIsTerminal() && !noTTY, SSH: ssh}
			if audit {
				if auditFormat != "json" && auditFormat != "text" {
					return fmt.Errorf("invalid --audit-format %q (want text or json)", auditFormat)
				}
				report, err := o.RunAudited(args[0], args[1:], opts)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if auditFormat == "json" {
					if err := report.WriteJSON(out); err != nil {
						return err
					}
				} else {
					report.WriteSummary(out)
				}
				if report.ExitCode != 0 {
					return errRunFailed // non-zero container exit -> non-zero opossum exit
				}
				return nil
			}
			return o.RunOneOff(args[0], args[1:], opts)
		},
	}
	cmd.Flags().BoolVar(&rm, "rm", false, "remove the container after it exits")
	cmd.Flags().BoolVar(&noDeps, "no-deps", false, "don't start linked services")
	cmd.Flags().BoolVarP(&noTTY, "no-tty", "T", false, "don't allocate a pseudo-terminal, so piped output (e.g. opossum run web cmd | jq) stays clean")
	cmd.Flags().StringArrayVar(&profiles, "profile", nil, "enable services gated behind this compose profile (repeatable; also honors COMPOSE_PROFILES)")
	cmd.Flags().BoolVar(&ssh, "ssh", false, "forward the host SSH agent into the container, so private git over SSH works with your host keys")
	cmd.Flags().BoolVar(&audit, "audit", false, "after the run, report what it did (workspace file diff, egress, exit) — the container's stdout goes to stderr so the report owns stdout")
	cmd.Flags().StringVar(&auditFormat, "audit-format", "text", "audit report format: text (human summary) or json")
	// Flags after the service name belong to the executed command, not opossum.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// errRunFailed makes `opossum run --audit` exit non-zero when the audited container
// exited non-zero, without discarding the audit report (already printed).
var errRunFailed = errors.New("the audited run exited non-zero (see the report above)")

func execCmd() *cobra.Command {
	var interactive, tty bool
	cmd := &cobra.Command{
		Use:   "exec [-it] <service> <command> [args...]",
		Short: "Run a command in a running service's container",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Exec(args[0], args[1:], runtime.ExecOptions{Interactive: interactive, TTY: tty})
		},
	}
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "keep stdin open (-i)")
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "allocate a TTY (-t)")
	// Flags after the service name belong to the executed command, not opossum.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [service...]",
		Short: "Stop services without removing them (all, or the named services)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Stop(args)
		},
	}
}

func restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart [service...]",
		Short: "Stop and start services in place (all, or the named services)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Restart(args)
		},
	}
}

func logsCmd() *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs [service...]",
		Short: "Show logs for services (all by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Logs(args, runtime.LogsOptions{Follow: follow, Tail: tail})
		},
	}
	// No -f shorthand: the root reserves -f for --file.
	cmd.Flags().BoolVar(&follow, "follow", false, "follow log output (several services are multiplexed, each line prefixed with its name)")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "number of lines to show from the end of the logs (0 = all)")
	return cmd
}

func statsCmd() *cobra.Command {
	var noStream, host bool
	cmd := &cobra.Command{
		Use:   "stats [service...]",
		Short: "Show live resource usage (CPU / memory / net / block I/O) for services",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if host {
				// Host view: a one-shot table of each service's real memory cost to
				// the Mac (its VM's resident size) — something a shared-VM tool can't
				// report per service.
				return o.StatsHost(args)
			}
			return o.Stats(args, noStream)
		},
	}
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "print a single snapshot instead of streaming live")
	cmd.Flags().BoolVar(&host, "host", false, "show each service's host memory footprint (its VM's resident size on the Mac) instead of streaming guest-view stats")
	return cmd
}

// adaptProject writes a compose.opossum.yaml adapting the project to Apple
// `container` and returns a reloaded orchestrator that has it merged, or nil when
// nothing was written. Returning a fresh orchestrator (rather than mutating) keeps
// the overlay on the same code path as a hand-written one: it's discovered and
// merged by the normal loader, so what `up` runs is exactly what `config` prints.
//
// It declines to act in three cases, all of which would take a decision away from
// the user:
//   - an explicit -f was given: the overlay is only auto-merged on the discovery
//     path, so writing one would produce a file that silently does nothing;
//   - an overlay already exists (either spelling): it may be hand-edited, and
//     shadowing or clobbering it would destroy work;
//   - nothing matches the known patterns.
//
// dryRun plans and reports without touching the filesystem, so `--dry-run` shows
// the project as it would actually start rather than as it stands unadapted.
func adaptProject(stderr io.Writer, o *orchestrator.Orchestrator, dryRun bool) (*orchestrator.Orchestrator, error) {
	if len(composeFiles) != 0 {
		// Explain the no-op: the user asked for the migration and would otherwise
		// see nothing at all happen.
		if _, changes := o.PlanOverlay(); len(changes) > 0 {
			fmt.Fprintf(stderr, "opossum: found %d change(s) this project needs, but -f was given — "+
				"an overlay is only merged when opossum discovers the compose file itself. "+
				"Re-run without -f to have them written, or apply them by hand (see the warnings below).\n", len(changes))
		}
		return nil, nil
	}
	body, changes := o.PlanOverlay()

	// Check both spellings via the same discovery the loader uses. Checking only
	// the name we write would let us drop a compose.opossum.yaml next to a
	// hand-written compose.opossum.yml — which takes precedence, silently making
	// the user's file inert.
	if existing := compose.DiscoverOpossumOverlay(o.Project.BaseDir); existing != "" {
		if len(changes) > 0 {
			// Say so rather than skipping in silence: the user asked for the fixes
			// and would otherwise hit the failure with no idea one was available.
			fmt.Fprintf(stderr, "opossum: %s already exists, so it was left alone — but opossum found more:\n",
				filepath.Base(existing))
			reportEntries(stderr, changes)
			fmt.Fprintf(stderr, "opossum: add them by hand, or delete %s and re-run to regenerate.\n", filepath.Base(existing))
		}
		return nil, nil
	}
	if body == "" {
		return nil, nil
	}
	// Notes alone don't justify the file. It is never overwritten once written, so
	// a comment-only overlay would burn that one chance and block a real fix later
	// — and it would make every command print the "merging an overlay" notice for
	// a file that changes nothing. The notes are still reported on stderr.
	if !hasActionable(changes) {
		reportNotesOnly(stderr, changes)
		return nil, nil
	}
	if dryRun {
		reportOverlay(stderr, "would write", changes)
		// Plan against the adapted project so the printed commands match a real run.
		return reloadWith(stderr, o, body)
	}
	if err := writeOverlay(o.Project.BaseDir, body); err != nil {
		// The overlay is a convenience. A directory we can't write to, or a file
		// that doesn't survive its own self-check, must not turn an `up` that used
		// to work into a failure — fall back to the warnings `up` already prints.
		fmt.Fprintf(stderr, "opossum: couldn't write %s (%v) — starting without it; "+
			"the warnings below say what to change by hand\n", orchestrator.OverlayFileName, err)
		return nil, nil
	}
	reportOverlay(stderr, "wrote", changes)

	return loadOrchestrator(o.Out())
}

// writeOverlay lands the overlay in dir, atomically and without clobbering.
//
// It writes to a temp file in the same directory, MERGES THAT FILE to prove it
// parses and resolves, and only then links it into place. The self-check matters
// because opossum will never overwrite the result: a rendering bug that produced
// unparseable YAML would otherwise be committed to the user's project and break
// every later command until a human found and deleted a file they never wrote.
// Linking (rather than renaming) keeps the no-clobber guarantee — it fails if the
// name already exists — and a short write can never leave a truncated overlay
// under the real name.
func writeOverlay(dir, body string) error {
	tmp, err := os.CreateTemp(dir, ".compose.opossum.*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // also removes the temp name after a successful link
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := checkOverlayResolves(dir, tmp.Name()); err != nil {
		return fmt.Errorf("the generated overlay did not resolve (%w) — this is an opossum bug; "+
			"nothing was written", err)
	}
	return os.Link(tmp.Name(), filepath.Join(dir, orchestrator.OverlayFileName))
}

// checkOverlayResolves merges an overlay candidate the way a later run would, so a
// file that can't load never reaches the project.
func checkOverlayResolves(dir, candidate string) error {
	base, err := compose.Discover(dir)
	if err != nil {
		return err
	}
	files := []string{base}
	if ov := compose.DiscoverOverride(dir); ov != "" {
		files = append(files, ov)
	}
	_, err = compose.LoadFiles(append(files, candidate), envFiles)
	return err
}

// reloadWith re-resolves the project with an unwritten overlay merged in, so
// --dry-run can plan against what a real run would start. The overlay goes to a
// temp directory and is passed explicitly; nothing is written to the project.
// Falling back to the unadapted project is better than failing the dry run, but
// never silently: it would print a plan that contradicts the "would write" summary
// above it, which is exactly the confusion --dry-run exists to prevent.
func reloadWith(stderr io.Writer, o *orchestrator.Orchestrator, body string) (*orchestrator.Orchestrator, error) {
	fallback := func(err error) (*orchestrator.Orchestrator, error) {
		fmt.Fprintf(stderr, "opossum: couldn't plan against the adapted project (%v) — "+
			"the plan below is for the project as it stands, WITHOUT the changes listed above\n", err)
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "opossum-dryrun")
	if err != nil {
		return fallback(err)
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, orchestrator.OverlayFileName)
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fallback(err)
	}
	base, err := compose.Discover(o.Project.BaseDir)
	if err != nil {
		return fallback(err)
	}
	files := []string{base}
	if ov := compose.DiscoverOverride(o.Project.BaseDir); ov != "" {
		files = append(files, ov)
	}
	files = append(files, tmp)
	proj, err := compose.LoadFiles(files, envFiles)
	if err != nil {
		return fallback(err)
	}
	proj.Name = o.Project.Name
	rt := runtime.New()
	rt.Verbose = verbose
	return orchestrator.New(proj, rt, dnsDomain, o.Out()), nil
}

func loadOrchestrator(out io.Writer) (*orchestrator.Orchestrator, error) {
	files := composeFiles
	if len(files) == 0 {
		// No -f: discover a standard compose file, plus its override if present
		// (docker compose auto-merges compose.override.yaml / docker-compose.override.yml).
		found, err := compose.Discover(".")
		if err != nil {
			return nil, err
		}
		files = []string{found}
		if ov := compose.DiscoverOverride("."); ov != "" {
			files = append(files, ov)
		}
		// An opossum overlay merges last, at the highest precedence. It's
		// opossum-specific (docker compose doesn't read it); the commands that
		// surface or start the resolved config announce it (see announceOverlay).
		if ol := compose.DiscoverOpossumOverlay("."); ol != "" {
			files = append(files, ol)
		}
	}
	proj, err := compose.LoadFiles(files, envFiles)
	if err != nil {
		return nil, err
	}
	switch {
	case projectName != "":
		proj.Name = compose.SanitizeName(projectName)
	case proj.Name != "":
		proj.Name = compose.SanitizeName(proj.Name)
	default:
		proj.Name = compose.SanitizeName(filepath.Base(proj.BaseDir))
	}
	rt := runtime.New()
	rt.Verbose = verbose
	return orchestrator.New(proj, rt, dnsDomain, out), nil
}

// reportOverlay prints what the overlay contains, grouped by what opossum is
// actually claiming. Applied entries are changes; suggestions and notes are not,
// and lumping them together would overstate what happened.
func reportOverlay(stderr io.Writer, verb string, changes []orchestrator.Adaptation) {
	var applied, suggested, noted []orchestrator.Adaptation
	for _, c := range changes {
		switch c.Kind {
		case "suggestion":
			suggested = append(suggested, c)
		case "note":
			noted = append(noted, c)
		default:
			applied = append(applied, c)
		}
	}
	if len(applied) > 0 {
		fmt.Fprintf(stderr, "opossum: %s %s — %d change(s) so this project runs on Apple container:\n",
			verb, orchestrator.OverlayFileName, len(applied))
		for _, c := range applied {
			fmt.Fprintf(stderr, "opossum:   [%s] %s\n", c.Code, c.Summary)
		}
	} else {
		fmt.Fprintf(stderr, "opossum: %s %s — no automatic fix was needed or possible:\n",
			verb, orchestrator.OverlayFileName)
	}
	if len(suggested) > 0 {
		fmt.Fprintf(stderr, "opossum: %d suggestion(s) written but NOT applied — they change what the project means, so they're yours to decide:\n", len(suggested))
		for _, c := range suggested {
			fmt.Fprintf(stderr, "opossum:   [%s] %s\n", c.Code, c.Summary)
		}
	}
	if len(noted) > 0 {
		fmt.Fprintf(stderr, "opossum: %d note(s) about things a compose change can't fix:\n", len(noted))
		for _, c := range noted {
			fmt.Fprintf(stderr, "opossum:   [%s] %s\n", c.Code, c.Summary)
		}
	}
	fmt.Fprintf(stderr, "opossum: each entry says why and how to undo it. Your compose file was not modified;\n"+
		"opossum: delete %s to opt out.\n", orchestrator.OverlayFileName)
}

// hasActionable reports whether anything in the overlay would actually do
// something — an applied change, or a suggestion the user can uncomment.
func hasActionable(changes []orchestrator.Adaptation) bool {
	for _, c := range changes {
		if c.Kind != "note" {
			return true
		}
	}
	return false
}

// reportNotesOnly says what opossum found when there is nothing to write.
func reportNotesOnly(stderr io.Writer, changes []orchestrator.Adaptation) {
	fmt.Fprintf(stderr, "opossum: nothing to fix or suggest, but %d thing(s) here can't be fixed by a compose change:\n", len(changes))
	for _, c := range changes {
		fmt.Fprintf(stderr, "opossum:   [%s] %s\n", c.Code, c.Summary)
	}
	fmt.Fprintln(stderr, "opossum: no overlay was written (it would only hold comments).")
}

// reportEntries prints entries grouped by what opossum is claiming.
func reportEntries(stderr io.Writer, changes []orchestrator.Adaptation) {
	label := map[string]string{
		"applied":    "change(s) opossum would apply",
		"suggestion": "suggestion(s) — NOT applied; they change what the project means",
		"note":       "note(s) about things a compose change can't fix",
	}
	for _, kind := range []string{"applied", "suggestion", "note"} {
		var got []orchestrator.Adaptation
		for _, c := range changes {
			if c.Kind == kind {
				got = append(got, c)
			}
		}
		if len(got) == 0 {
			continue
		}
		fmt.Fprintf(stderr, "opossum: %d %s:\n", len(got), label[kind])
		for _, c := range got {
			fmt.Fprintf(stderr, "opossum:   [%s] %s\n", c.Code, c.Summary)
		}
	}
}
