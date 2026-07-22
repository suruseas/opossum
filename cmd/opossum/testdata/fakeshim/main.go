// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// CLI-level tests, replacing a per-test /bin/sh script. A compiled binary spawns
// in ~1-2ms versus ~50-80ms for a shell script. It logs each invocation to
// $FAKE_LOG and returns plausible output for the handful of commands the CLI
// tests drive (system dns list, network create, inspect).
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	if logPath := os.Getenv("FAKE_LOG"); logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	// SYSTEM_STOPPED simulates a stopped runtime for `system status`, but a
	// SYSTEM_START_FLAG file (created by `system start`) flips it to running — so a
	// test can drive the whole auto-start flow: status=stopped → `system start` →
	// status=running → the command proceeds.
	systemRunning := func() bool {
		if os.Getenv("SYSTEM_STOPPED") == "" {
			return true
		}
		flag := os.Getenv("SYSTEM_START_FLAG")
		if flag == "" {
			return false
		}
		_, err := os.Stat(flag)
		return err == nil
	}
	switch arg(0) {
	case "system":
		if arg(1) == "dns" && arg(2) == "list" {
			fmt.Print("DOMAIN\nopossum\n")
		}
		// `system status` is the daemon-liveness probe; report running (or stopped
		// under SYSTEM_STOPPED until `system start` runs).
		if arg(1) == "status" {
			if systemRunning() {
				fmt.Print("FIELD  VALUE\nstatus  running\n")
			} else {
				fmt.Print("FIELD  VALUE\nstatus  stopped\n")
			}
		}
		// `system start` starts the runtime: mark it running for subsequent status.
		if arg(1) == "start" {
			if flag := os.Getenv("SYSTEM_START_FLAG"); flag != "" {
				os.WriteFile(flag, []byte("started"), 0o644)
			}
			fmt.Println("started")
		}
	case "network":
		if arg(1) == "create" {
			fmt.Println(arg(2))
		}
	case "inspect":
		fmt.Println(`[{"status":{"state":"running","networks":[{"ipv4Address":"192.168.66.9/24"}]},"configuration":{"labels":{},"publishedPorts":[{"containerPort":80,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]`)
	case "stats":
		// `stats --no-stream --format json <names…>` returns a guest-view JSON array.
		jsonForm := false
		var names []string
		for i, a := range args[1:] {
			switch {
			case a == "json" && args[i] == "--format":
				jsonForm = true
			case strings.HasPrefix(a, "-") || a == "json":
			default:
				names = append(names, a)
			}
		}
		if jsonForm {
			var objs []string
			for _, n := range names {
				objs = append(objs, fmt.Sprintf(`{"id":"%s","memoryUsageBytes":49283072,"memoryLimitBytes":1073741824}`, n))
			}
			fmt.Printf("[%s]\n", strings.Join(objs, ","))
		}
	}
}
