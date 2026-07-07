// Command opossum is a Docker Compose-like orchestrator for Apple's `container`
// runtime on macOS 26+.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

var (
	composeFile string
	projectName string
	dnsDomain   string
	verbose     bool
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
	root.PersistentFlags().StringVarP(&composeFile, "file", "f", "", "path to the compose file (default: the first of compose.yaml, compose.yml, docker-compose.yaml, docker-compose.yml)")
	root.PersistentFlags().StringVarP(&projectName, "project-name", "p", "", "project name (defaults to the compose file's directory)")
	root.PersistentFlags().StringVar(&dnsDomain, "dns-domain", "opossum", "local DNS domain for bare-name service discovery (create once: sudo container system dns create <domain>)")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "print each underlying container command as it runs (useful for bug reports)")

	root.AddCommand(
		upCmd(), downCmd(), psCmd(), imagesCmd(), logsCmd(), statsCmd(),
		stopCmd(), restartCmd(), startCmd(), execCmd(),
		buildCmd(), pullCmd(), killCmd(), runCmd(),
		configCmd(),
	)
	return root
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
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Build and start services in dependency order (all, or the named services plus their dependencies)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.Up(!foreground, args...)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run a single service attached in the foreground instead of detached (rejected for multiple long-running services)")
	return cmd
}

func downCmd() *cobra.Command {
	var volumes bool
	var rmi string
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
			return o.Down(volumes, rmi)
		},
	}
	cmd.Flags().BoolVarP(&volumes, "volumes", "v", false, "also remove named volumes declared by services")
	cmd.Flags().StringVar(&rmi, "rmi", "", "also remove images: \"local\" (opossum-built) or \"all\" (built + pulled)")
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
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate and print the resolved compose configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if servicesOnly {
				order, err := o.Project.StartupOrder()
				if err != nil {
					return err
				}
				for _, name := range order {
					fmt.Fprintln(w, name)
				}
				return nil
			}
			rendered, err := compose.RenderConfig(o.Project)
			if err != nil {
				return err
			}
			fmt.Fprint(w, rendered)
			return nil
		},
	}
	cmd.Flags().BoolVar(&servicesOnly, "services", false, "print only the service names")
	return cmd
}

func runCmd() *cobra.Command {
	var rm, noDeps bool
	cmd := &cobra.Command{
		Use:   "run [--rm] [--no-deps] <service> [command...]",
		Short: "Run a one-off command in a new container for a service",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := loadOrchestrator(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return o.RunOneOff(args[0], args[1:], orchestrator.RunOneOffOptions{Rm: rm, NoDeps: noDeps})
		},
	}
	cmd.Flags().BoolVar(&rm, "rm", false, "remove the container after it exits")
	cmd.Flags().BoolVar(&noDeps, "no-deps", false, "don't start linked services")
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
	cmd.Flags().BoolVar(&follow, "follow", false, "follow log output (requires exactly one service)")
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
	// No -f: discover a standard compose file in the working directory.
	file := composeFile
	if file == "" {
		found, err := compose.Discover(".")
		if err != nil {
			return nil, err
		}
		file = found
	}
	proj, err := compose.Load(file)
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
