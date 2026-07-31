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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

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
	// nameWithoutFlag is the project name the compose file and its directory imply,
	// recorded by loadOrchestrator before -p overrides it.
	nameWithoutFlag string

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
		destroyCmd(),
		superviseCmd(),
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

func cmdReadOnlyRuntime(cmd *cobra.Command) bool {
	if runtimeReadOnly[cmd.Name()] {
		return true
	}
	// `destroy --dry-run` only prints a list. Starting a VM to answer a question
	// about what would be removed is the same surprise `ps` and `images` avoid —
	// and it is a poor introduction for someone who ran it to decide whether to
	// keep opossum at all.
	if cmd.Name() == "destroy" {
		if f := cmd.Flags().Lookup("dry-run"); f != nil && f.Value.String() == "true" {
			return true
		}
	}
	return false
}

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
	var forceRecreate, build, noBuild, removeOrphans, fromDockerCompose, fromDockerLegacy, dryRun, noSupervisor bool
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
			upErr := o.Up(!foreground, args...)
			// Deliberately also on the error path. Most failures roll the stack back,
			// leaving nothing to watch — but one doesn't: a service that exits right
			// after starting fails the up as a post-start health report, and every
			// container stays. Those carry the compose file's `restart:` policy, which
			// docker applies whatever `up` exited with, and bailing out here left them
			// unwatched. Started() reports what is running when Up finishes — after a
			// rollback that is whatever the failed call left alone, which may be
			// nothing — so this starts a supervisor exactly when something really is up.
			//
			// That includes a bring-up the user interrupted: Ctrl-C stops what opossum
			// is doing, not what was already running. A service left up from an earlier
			// `up`, with a `restart:` policy the compose file still asks for, is not
			// something to stop watching because a later command was cut short. `down`
			// is how you stop it. The reach of that: if no supervisor was running — the
			// project was brought up with --no-supervisor, or its watcher died — an
			// interrupted `up` can leave a background process where there was none.
			// --no-supervisor still opts out of that, and `down` still ends it.
			//
			// Still after Up returns, never during it: a watcher started earlier would
			// see the half-built stack and try to "fix" containers still being made.
			// --dry-run resolves without starting anything, so there is nothing to
			// watch. A foreground `up` means "run it here until it ends" — leaving a
			// watcher to restart the service the user just watched finish would
			// contradict it.
			if !dryRun && !foreground {
				startSupervisorFor(cmd.ErrOrStderr(), o, noSupervisor, profiles, upErr != nil)
			}
			return upErr
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
	cmd.Flags().BoolVar(&noSupervisor, "no-supervisor", false, "don't leave a background process watching `restart:` services (they won't be brought back automatically)")
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
			// Stop the supervisor BEFORE the compose file is needed. A watcher is a
			// resident process, and `down` is the only thing that stops it — so it
			// must not be reachable only when the compose file still parses. Deleting
			// or renaming the file would otherwise strand a process the user has no
			// opossum command to remove.
			if name := projectNameWithoutCompose(); name != "" {
				if orchestrator.StopSupervisor(name) {
					fmt.Fprintln(cmd.ErrOrStderr(), "opossum: stopped the restart supervisor")
				}
				orchestrator.ClearWatched(name)
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

// destroyCmd is the exit from a trial run. `down` is the daily command; this is
// the one that leaves no trace, so it is deliberately louder: it lists what it
// will remove and asks, and it refuses to guess when nobody can answer.
func destroyCmd() *cobra.Command {
	var force, dryRun, keepOverlay, keepImages, keepLocal bool
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Remove everything opossum created for this project (containers, volumes, images, state)",
		Long: "Remove everything opossum created for this project: containers, the project " +
			"network, named volumes, images it built or pulled, the restart supervisor, the " +
			"`.opossum/` state directory and the generated `compose.opossum.yaml`.\n\n" +
			"Your own files are never touched — the compose file, `.env` and your sources are " +
			"left exactly as they are. Nothing shared beyond this project is removed either: " +
			"volumes declared `external: true`, other projects' containers, the DNS domain and " +
			"the builder cache all stay (the last two are reported with the command to remove " +
			"them, since they are shared and one needs sudo). Workspace snapshots stay too: a " +
			"`.opossum-snapshots/` directory belongs to the directory that was snapshotted, not " +
			"to a project, so any found in this directory or one below it are reported " +
			"rather than removed.\n\n" +
			"Destroy asks before it acts. Use --force in a script or agent loop, and --dry-run " +
			"to see the list without removing anything. (There is no -f shorthand: -f is the " +
			"global --file.)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			plan, err := o.DestroyPlanFor(keepOverlay, keepImages, keepLocal)
			if err != nil {
				return err
			}
			// Only a name given on the command line can be mistargeted. A compose file
			// whose `name:` differs from its directory is ordinary and means nothing here.
			// Fail closed: if a name was given on the command line and we could not work
			// out what this directory calls itself, treat it as mistargeted. A safety
			// guard that goes quiet when its input is missing is not a guard.
			if projectName != "" && nameWithoutFlag != o.Project.Name {
				plan.MistargetedName = nameWithoutFlag
				if plan.MistargetedName == "" {
					plan.MistargetedName = "(unknown)"
				}
			}
			out := cmd.OutOrStdout()
			if plan.Empty() {
				fmt.Fprintf(out, "Nothing to remove: opossum has nothing left for project %q.\n", o.Project.Name)
				printSystemLeftovers(out, plan.SnapshotDirs)
				return nil
			}
			// A project named on the command line is not the project this directory
			// belongs to. The runtime objects are the named project's and removing them
			// is the whole point; the generated files are *this* directory's and removing
			// them almost certainly is not what was meant. Interactively the plan says so
			// and the question that follows is the consent. With --force there is nobody
			// to read a warning, so this refuses rather than act on the guess.
			// dry-run excluded: it removes nothing, so refusing it would fail a preview
			// and explain the failure with something that was never going to happen.
			if plan.MistargetedName != "" && !keepLocal && len(plan.LocalPaths) > 0 && force && !dryRun {
				return fmt.Errorf("refusing to destroy %q from a directory that belongs to %q: "+
					"--force would remove this directory's generated files (%s) under another "+
					"project's name, with nothing asked and nobody to read a warning.\n"+
					"  To remove only %[1]q's containers, volumes, images and supervisor, add "+
					"--keep-local.\n"+
					"  To take this directory apart, run it here without -p.",
					o.Project.Name, plan.MistargetedName, strings.Join(plan.LocalPaths, ", "))
			}
			printDestroyPlan(out, o.Project.Name, plan, dryRun)
			if dryRun {
				// The preview is where this list is worth most: it is read before
				// deciding, and what destroy leaves behind is part of that decision.
				// Printing it only after the fact meant the one mode for looking first
				// was the one that didn't say.
				printSystemLeftovers(out, plan.SnapshotDirs)
				return nil
			}
			if !force {
				ok, err := confirmDestroy(cmd, o.Project.Name)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "Left everything as it was.")
					return nil
				}
			}
			if err := o.Destroy(plan); err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed everything opossum created for %q. Your compose file, .env and sources are untouched.\n", o.Project.Name)
			printSystemLeftovers(out, plan.SnapshotDirs)
			return nil
		},
	}
	// No `-f` shorthand: that is the global `--file`, and a destroy that could be
	// spelled the same way as "use this compose file" is a trap worth avoiding.
	cmd.Flags().BoolVar(&force, "force", false, "don't ask for confirmation (for scripts and agents)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed and stop")
	cmd.Flags().BoolVar(&keepOverlay, "keep-overlay", false, "keep compose.opossum.yaml even when opossum generated it — including one you edited, which is otherwise removed (one you wrote from scratch is always kept)")
	cmd.Flags().BoolVar(&keepImages, "keep-images", false, "keep the images: a pulled one may be shared with other projects and slow to fetch again")
	cmd.Flags().BoolVar(&keepLocal, "keep-local", false, "leave this directory's generated files (`.opossum/`, compose.opossum.yaml) alone and remove only the runtime objects — what you want when destroying a project by name from somewhere else")
	return cmd
}

// printDestroyPlan writes the plan as a list of concrete names. Groups with
// nothing in them are left out: a heading with no items under it reads like
// something was missed.
func printDestroyPlan(out io.Writer, project string, p orchestrator.DestroyPlan, dryRun bool) {
	verb := "will remove"
	if dryRun {
		verb = "would remove"
	}
	fmt.Fprintf(out, "Destroying project %q %s:\n", project, verb)
	if p.SupervisorRunning {
		fmt.Fprintln(out, "  restart supervisor:")
		fmt.Fprintln(out, "    - the background process watching this project")
	}
	for _, g := range []struct {
		label string
		items []string
	}{
		{"containers", p.Containers},
		{"networks", p.Networks},
		{"volumes (data in them is lost)", p.Volumes},
		{"images", p.Images},
	} {
		if len(g.items) == 0 {
			continue
		}
		fmt.Fprintf(out, "  %s:\n", g.label)
		for _, item := range g.items {
			fmt.Fprintf(out, "    - %s\n", item)
		}
	}
	// Named separately from the runtime objects above, and with the directory
	// spelled out: those belong to a project name, these belong to a place. Reading
	// "destroying project X" as a promise about X's directory is exactly the mistake
	// that made `destroy -p other --force` dangerous.
	if len(p.LocalPaths) > 0 {
		where := "files opossum generated"
		if p.LocalDir != "" {
			where = fmt.Sprintf("files opossum generated in %s", p.LocalDir)
		}
		fmt.Fprintf(out, "  %s:\n", where)
		for _, item := range p.LocalPaths {
			fmt.Fprintf(out, "    - %s\n", item)
		}
		if p.MistargetedName != "" {
			fmt.Fprintf(out, "    ! this directory belongs to project %q, not %q — these files are "+
				"%[1]q's. Use --keep-local to remove only %[2]q's containers, volumes and images.\n",
				p.MistargetedName, project)
		}
	}
	// Keyed by the project name, not by this directory: worth its own line so the
	// heading above stays true.
	var byName []string
	local := map[string]bool{}
	for _, l := range p.LocalPaths {
		local[l] = true
	}
	for _, path := range p.Paths {
		if !local[path] {
			byName = append(byName, path)
		}
	}
	if len(byName) > 0 {
		fmt.Fprintf(out, "  state opossum keeps for the project %q:\n", project)
		for _, item := range byName {
			fmt.Fprintf(out, "    - %s\n", item)
		}
	}
	if len(p.StrandedVolumes) > 0 {
		fmt.Fprintln(out, "  named for this project but not claimed by any service — NOT removed:")
		for _, item := range p.StrandedVolumes {
			fmt.Fprintf(out, "    - %s\n", item)
		}
		fmt.Fprintln(out, "    One of these may be left over from a service you renamed or deleted, in")
		fmt.Fprintln(out, "    which case it is yours to remove. It may equally belong to another project")
		fmt.Fprintf(out, "    whose name starts with %q, or be an `external: true` volume — opossum\n", project+"_")
		fmt.Fprintln(out, "    cannot tell from the name, which is why it leaves them alone. Check what")
		fmt.Fprintln(out, "    uses one (`container volume inspect <name>`) before removing it.")
	}
	if p.KeptOverlay != "" {
		fmt.Fprintln(out, "  kept, not removed:")
		fmt.Fprintf(out, "    - %s\n", p.KeptOverlay)
	}
	fmt.Fprintln(out, "  your compose file, .env and sources are NOT touched.")
}

// printSystemLeftovers names what destroy leaves alone because it is shared with
// every other project, and gives the command for each. It prints them rather than
// running them: removing a DNS domain needs sudo, and clearing the builder cache
// would slow down every unrelated project on the machine.
//
// It runs after the confirmation, not before it, and that is deliberate. `--dry-run`
// prints this because a preview is read to decide with; the interactive question is
// not the same thing. Everything here is what destroy will NOT touch, so none of it
// changes the answer to "remove all of it?" — what does is the plan above, which is
// already on screen. Putting this between the plan and the question would separate the
// two — and push the question further down — to say nothing that bears on it.
//
// The ordering is pinned by a test rather than left to this comment, because a
// comment does not stop the call from being moved.
func printSystemLeftovers(out io.Writer, snapshotDirs []string) {
	fmt.Fprintln(out, "Left alone, because it isn't this project's to remove:")
	fmt.Fprintf(out, "  - the %q DNS domain — remove with: sudo container system dns delete %s\n", dnsDomain, dnsDomain)
	fmt.Fprintln(out, "  - the build cache and unused images — reclaim with: container builder delete --force && container image prune -a")
	// Snapshots belong to the directory that was snapshotted, not to a project, so
	// destroy has no business removing them — but they are usually the biggest
	// thing left behind, and silence here is how you find gigabytes a month later.
	//
	// `ls` rather than `opossum ws ls`: `ws` reads its snapshots from beside the
	// workspace given to --path, so the bare command would list a different
	// directory than the one on this line whenever the workspace isn't the default.
	for _, dir := range snapshotDirs {
		fmt.Fprintf(out, "  - workspace snapshots in %s — they belong to that directory, not to "+
			"this project; see them with: ls %[1]s, remove with: rm -rf %[1]s\n", dir)
	}
}

// confirmDestroy asks, once, on the command's own streams. Without a terminal
// there is nobody to ask: rather than assume yes (destructive) or assume no
// (silently doing nothing in a script), it says which flag to pass.
func confirmDestroy(cmd *cobra.Command, project string) (bool, error) {
	if !stdinIsTerminal() {
		return false, fmt.Errorf("destroy removes data and asks first, but stdin isn't a terminal "+
			"so there's nobody to ask — re-run with --force to destroy %q without asking, or "+
			"--dry-run to see what it would remove", project)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Remove all of it? [y/N] ")
	var answer string
	if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil {
		return false, nil // no answer (EOF, a bare newline) means no
	}
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
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
	// What this project is called when nobody overrides it: the compose file's own
	// `name:`, or failing that its directory. Remembered before -p is applied so a
	// destructive command can tell "you named another project" from "this file names
	// its project something other than its folder", which is ordinary.
	if proj.Name != "" {
		nameWithoutFlag = compose.SanitizeName(proj.Name)
	} else {
		nameWithoutFlag = compose.SanitizeName(filepath.Base(proj.BaseDir))
	}
	switch {
	case projectName != "":
		proj.Name = compose.SanitizeName(projectName)
	default:
		proj.Name = nameWithoutFlag
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

// superviseCmd is the background watcher `up` starts for a project with
// `restart:` policies. It is hidden because it isn't a thing to run by hand: the
// lifecycle is `up` starts it, `down` stops it, and running a second one would
// mean two watchers racing to restart the same container.
func superviseCmd() *cobra.Command {
	var profiles []string
	var watch []string
	cmd := &cobra.Command{
		Use:    "__supervise",
		Short:  "internal: watch this project's restart: services",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			o.EnableProfiles(profiles)
			o.EnableProfiles(strings.Split(os.Getenv("COMPOSE_PROFILES"), ","))
			// The set is decided by the `up` that started this watcher and handed
			// over here — what it started plus what was already watched and still
			// exists. Re-deriving it from the compose file would put services nobody
			// started under supervision; re-deriving it from `up`'s own arguments
			// would drop the ones it carried over.
			services := o.SupervisedServices(watch)
			if len(services) == 0 {
				return nil
			}
			// Claim the project before watching anything. Losing the race means
			// another supervisor is already on it, which is success, not failure —
			// exit quietly rather than become a second watcher nothing can stop.
			if err := orchestrator.ClaimSupervisor(o.Project.Name); err != nil {
				if orchestrator.ErrAlreadySupervised(err) {
					return nil
				}
				return err
			}
			// Record what this watcher took on, so a later `up` can tell whether the
			// compose file has moved on since.
			_ = orchestrator.RecordWatched(o.Project.Name, services)
			// Exit cleanly when `down` asks, so the pid file doesn't outlive us.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)
			go func() { <-sig; cancel() }()
			// Write through a bounded log rather than the inherited stdout: a service
			// that fails permanently is restarted for as long as its policy says, and
			// each attempt is a line. stdout stays connected to the same file, so a
			// panic — the one thing that doesn't come through here — is still recorded.
			logw, err := orchestrator.OpenSupervisorLog(o.Project.Name)
			if err != nil {
				// Say so rather than degrade quietly: stdout goes to the same file, so
				// the log looks identical while the cap silently stops applying.
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", time.Now().Format(time.RFC3339),
					orchestrator.NoticeSupervisorLogUncapped(err))
				return o.Supervise(ctx, services, cmd.OutOrStdout())
			}
			defer logw.Close()
			return o.Supervise(ctx, services, logw)
		},
	}
	cmd.Flags().StringArrayVar(&profiles, "profile", nil, "profiles the supervised project was started with")
	cmd.Flags().StringArrayVar(&watch, "watch-service", nil, "the exact services to watch, as worked out by the `up` that started this watcher")
	return cmd
}

// startSupervisorFor leaves a watcher running for this project when the compose
// file asks for one. Enabled by default: a `restart:` policy is the user saying
// they want the service kept up, and honouring it is what compose parity means.
// The notice makes the process visible, and --no-supervisor / OPOSSUM_NO_SUPERVISOR
// turn it off for the cases where a background process outliving the command is
// worse than a service staying down (CI, one-shot agent runs).
// mergeServices returns the union of two service lists, sorted, without
// duplicates. The supervisor sorts again when it records the set, so the sort
// here is for the notice: a set that reads the same way every time.
func mergeServices(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// upFailed reports whether the `up` that is asking for a supervisor returned an
// error. It only ever makes this function do less: a failed `up` may not take an
// existing supervisor away, because what it started is not a reliable statement
// about what is running.
func startSupervisorFor(stderr io.Writer, o *orchestrator.Orchestrator, disabled bool, profiles []string, upFailed bool) {
	if disabled || os.Getenv("OPOSSUM_NO_SUPERVISOR") != "" {
		return
	}
	// What `up` actually started, not the whole compose file: `up web` must not
	// announce (or poll for) services nobody asked to run, and a profile-gated
	// service that stayed down isn't supervised either.
	order := o.Started()
	if len(order) == 0 {
		return
	}
	services := o.SupervisedServices(order)
	// …plus whatever a previous `up` was watching that is still there. `up web`
	// makes no statement about `db`, so replacing a supervisor watching [db web]
	// with one watching [web] would silently drop a running service's `restart:`
	// policy — the notice would be accurate and the behaviour still wrong. The
	// union keeps both true: only services with a live container are carried over,
	// so this can't go back to announcing services that were never started.
	services = mergeServices(services, o.StillSupervised(orchestrator.Watched(o.Project.Name)))
	if len(services) == 0 {
		return
	}
	logPath, err := orchestrator.SupervisorLogFile(o.Project.Name)
	if err != nil {
		return
	}
	// Re-invoke ourselves with the same compose selection, so the watcher resolves
	// exactly the project that was just started.
	// The child runs with its own working directory, so every path has to be
	// absolute: a relative `-f` would be re-resolved against a different directory
	// and the watcher would die on startup — while `up` had just announced that
	// supervision was running.
	args := []string{"__supervise", "-p", o.Project.Name, "--dns-domain", dnsDomain}
	for _, f := range composeFiles {
		args = append(args, "-f", absPathOr(f))
	}
	for _, f := range envFiles {
		args = append(args, "--env-file", absPathOr(f))
	}
	// The child re-resolves the project, so it needs the same profiles — otherwise
	// it would decide a different set of services is in play.
	for _, p := range profiles {
		args = append(args, "--profile", p)
	}
	// …and the exact set worked out above — what this `up` started plus what was
	// already watched and is still there. Passing the started set alone would make
	// the child re-narrow to it the moment a supervisor actually has to be
	// (re)started, so the union would live only in the notice and the comparison:
	// true on paper, and the carried-over services unwatched in fact.
	for _, n := range services {
		args = append(args, "--watch-service", n)
	}
	// A watcher started before the compose file changed would keep enforcing the
	// old policies while this `up` announced the new ones. Replace it rather than
	// print a notice describing a supervisor that isn't watching those services.
	//
	// Not when the up failed, though. `up web` that fails leaves a Started() of
	// just [web], and replacing a supervisor watching [db web] with one watching
	// [web] would drop `db` — still running, still asking to be restarted —
	// precisely by way of the change that exists to stop services going unwatched.
	// A failed up may add a supervisor where there was none; it may not narrow one.
	if orchestrator.SupervisorPID(o.Project.Name) != 0 && !orchestrator.WatchedMatches(o.Project.Name, services) {
		// Not when the up failed, though. `up web` that fails leaves a Started() of
		// just [web], and replacing a supervisor watching [db web] with one watching
		// [web] would drop `db` — still running, still asking to be restarted —
		// precisely by way of the change that exists to stop services going
		// unwatched. A failed up may add a supervisor where there was none; it may
		// not narrow one. What is being watched stays visible in `opossum ps`.
		if upFailed {
			return
		}
		orchestrator.StopSupervisor(o.Project.Name)
	}
	if _, err := orchestrator.StartSupervisor(o.Project.Name, o.Project.BaseDir, args); err != nil {
		fmt.Fprintf(stderr, "opossum: couldn't start the restart supervisor (%v) — services with `restart:` "+
			"will not be brought back automatically\n", err)
		return
	}
	fmt.Fprintln(stderr, "opossum: "+orchestrator.NoticeSupervisorStarted(o.Project.Name, services, logPath))
}

// absPathOr makes a path absolute, leaving it alone if that isn't possible.
func absPathOr(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// projectNameWithoutCompose derives the project name the way loadOrchestrator
// would, minus the part that needs the compose file (its `name:`). It is enough
// to find a running supervisor: `-p` is authoritative when given, and otherwise
// the directory name is what `up` used unless the file named the project.
func projectNameWithoutCompose() string {
	if projectName != "" {
		return compose.SanitizeName(projectName)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return compose.SanitizeName(filepath.Base(wd))
}
