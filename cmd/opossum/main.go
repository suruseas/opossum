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

	"github.com/spf13/cobra"
	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/doctor"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
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
		importCmd(), configCmd(), doctorCmd(), cpCmd(), watchCmd(),
	)
	return root
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
	return &cobra.Command{
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
			if !doctor.Run(cmd.OutOrStdout(), rt, dnsDomain, proj, hostMemMB()) {
				// A failed check (❌) means the environment isn't ready — return an
				// error so the process exits non-zero and `opossum doctor && …` / CI
				// gate on it. The report above already explains what and how to fix.
				return errEnvUnhealthy
			}
			return nil
		},
	}
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
	var forceRecreate, build, noBuild, removeOrphans, fromDocker bool
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Build and start services in dependency order (all, or the named services plus their dependencies)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if build && noBuild {
				return fmt.Errorf("--build and --no-build are mutually exclusive")
			}
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			o.SetUpOptions(forceRecreate, build, noBuild, removeOrphans, fromDocker)
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
	cmd.Flags().BoolVar(&fromDocker, "from-docker", false, "for services with a build, import the image from Docker instead of building it (needs the docker CLI)")
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
// so ask the terminal driver.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func runCmd() *cobra.Command {
	var rm, noDeps, noTTY, ssh bool
	var profiles []string
	cmd := &cobra.Command{
		Use:   "run [--rm] [--no-deps] <service> [command...]",
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
			return o.RunOneOff(args[0], args[1:], orchestrator.RunOneOffOptions{Rm: rm, NoDeps: noDeps, TTY: stdinIsTerminal() && !noTTY, SSH: ssh})
		},
	}
	cmd.Flags().BoolVar(&rm, "rm", false, "remove the container after it exits")
	cmd.Flags().BoolVar(&noDeps, "no-deps", false, "don't start linked services")
	cmd.Flags().BoolVarP(&noTTY, "no-tty", "T", false, "don't allocate a pseudo-terminal, so piped output (e.g. opossum run web cmd | jq) stays clean")
	cmd.Flags().StringArrayVar(&profiles, "profile", nil, "enable services gated behind this compose profile (repeatable; also honors COMPOSE_PROFILES)")
	cmd.Flags().BoolVar(&ssh, "ssh", false, "forward the host SSH agent into the container, so private git over SSH works with your host keys")
	// Flags after the service name belong to the executed command, not opossum.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

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
	var noStream bool
	cmd := &cobra.Command{
		Use:   "stats [service...]",
		Short: "Show live resource usage (CPU / memory / net / block I/O) for services",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Stats(args, noStream)
		},
	}
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "print a single snapshot instead of streaming live")
	return cmd
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
