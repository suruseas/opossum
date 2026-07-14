// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// runtime tests, replacing per-test /bin/sh scripts. A compiled binary spawns in
// ~1-2ms versus ~50-80ms for a shell script, and the suite spawns it many times.
//
// Behaviour is steered through the environment so each test stays isolated:
//   - SHIM_LOG:  append the space-joined args to this file (arg-capture shims)
//   - SHIM_OUT:  copy this file's contents to stdout (replay/inspect shims)
//   - SHIM_EXIT: exit with this status code
package main

import (
	"os"
	"strconv"
	"strings"
)

func main() {
	if lp := os.Getenv("SHIM_LOG"); lp != "" {
		if f, err := os.OpenFile(lp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.WriteString(strings.Join(os.Args[1:], " ") + "\n")
			f.Close()
		}
	}
	if of := os.Getenv("SHIM_OUT"); of != "" {
		if b, err := os.ReadFile(of); err == nil {
			os.Stdout.Write(b)
		}
	}
	if e := os.Getenv("SHIM_EXIT"); e != "" {
		if n, err := strconv.Atoi(e); err == nil {
			os.Exit(n)
		}
	}
}
