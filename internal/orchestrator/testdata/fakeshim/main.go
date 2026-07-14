// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// orchestrator tests. It's a faithful port of the shell shim fakeShim used to
// write per-test — but compiled once and reused, so each test spawns a ~1-2ms
// binary instead of a ~50-80ms /bin/sh, which dominated the suite's runtime.
//
// It logs each invocation's arguments (space-joined) to $FAKE_LOG and returns
// output shaped like the real CLI. Behaviour is steered entirely through the
// environment (FAKE_LOG, STATE_DIR, INSPECT_STATE, NET_EXISTS, RUN_FAIL,
// HEALTH_*, VOLUME_*, LS_*, IMAGE_ABSENT), so tests need no t.Setenv and stay
// isolated: the orchestrator passes these per-Runtime via RunOptions-style Env.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	// Log the invocation (args space-joined), matching the old `echo "$*"`.
	if logPath := os.Getenv("FAKE_LOG"); logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	if len(args) == 0 {
		return 0
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}

	switch args[0] {
	case "inspect":
		// Build the labels object from INSPECT_PROJECT and any recorded config-hash.
		var labels []string
		if p := os.Getenv("INSPECT_PROJECT"); p != "" {
			labels = append(labels, `"opossum.project":"`+p+`"`)
		}
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			if h, err := os.ReadFile(filepath.Join(dir, arg(1)+".hash")); err == nil {
				labels = append(labels, `"opossum.config-hash":"`+strings.TrimSpace(string(h))+`"`)
			}
		}
		state := os.Getenv("INSPECT_STATE")
		if state == "" {
			state = "running"
		}
		fmt.Printf(`[{"status":{"state":"%s","networks":[{"network":"n","ipv4Address":"192.168.64.10/24","ipv4Gateway":"192.168.64.1"}]},"configuration":{"labels":{%s},"publishedPorts":[{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]`+"\n",
			state, strings.Join(labels, ","))

	case "network":
		if arg(1) == "create" {
			if os.Getenv("NET_EXISTS") != "" {
				fmt.Fprintf(os.Stderr, "network %s already exists\n", arg(2))
				return 1
			}
			fmt.Printf("created network %s\n", arg(2))
		}

	case "run":
		// Record the config-hash (from -l opossum.config-hash=…) keyed by --name,
		// so a later inspect reports it and up-idempotency evals can detect it.
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			var cname, chash string
			for i, a := range args {
				if i > 0 && args[i-1] == "--name" {
					cname = a
				}
				if v, ok := strings.CutPrefix(a, "opossum.config-hash="); ok {
					chash = v
				}
			}
			if cname != "" && chash != "" {
				os.WriteFile(filepath.Join(dir, cname+".hash"), []byte(chash), 0o644)
			}
		}
		// A foreground run of $RUN_FAIL exits non-zero (drives failure evals).
		if fail := os.Getenv("RUN_FAIL"); fail != "" {
			for i, a := range args {
				if i > 0 && args[i-1] == "--name" && a == fail {
					return 1
				}
			}
		}

	case "exec":
		if os.Getenv("HEALTH_HANG") != "" {
			time.Sleep(30 * time.Second) // never returns within the probe timeout
		}
		counter := os.Getenv("HEALTH_COUNTER")
		n := 0
		if b, err := os.ReadFile(counter); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n)
		}
		n++
		os.WriteFile(counter, []byte(fmt.Sprintf("%d", n)), 0o644)
		okAt := 1
		if v := os.Getenv("HEALTH_OK_AT"); v != "" {
			fmt.Sscanf(v, "%d", &okAt)
		}
		if n < okAt {
			return 1
		}

	case "volume":
		if os.Getenv("VOLUME_LS_FAIL") != "" {
			return 1
		}
		if arg(1) == "ls" {
			fmt.Println(os.Getenv("VOLUME_LS"))
		}

	case "logs":
		last := ""
		if len(args) > 0 {
			last = args[len(args)-1]
		}
		fmt.Printf("log-line %s\n", last)

	case "ls":
		var items []string
		for _, n := range strings.Fields(os.Getenv("LS_CONTAINERS")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"%s"}}}`, n, os.Getenv("LS_PROJECT")))
		}
		for _, n := range strings.Fields(os.Getenv("LS_FOREIGN")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"otherproj"}}}`, n))
		}
		fmt.Printf("[%s]", strings.Join(items, ","))

	case "image":
		if arg(1) == "inspect" {
			for _, m := range strings.Fields(os.Getenv("IMAGE_ABSENT")) {
				if arg(2) == m {
					return 1
				}
			}
		}
	}
	return 0
}
