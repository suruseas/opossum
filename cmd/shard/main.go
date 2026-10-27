// Command shard runs a part of a `go test` command, so that the gate can be
// spread over several machines and still be the whole gate.
//
// With OPOSSUM_GATE_SHARD unset it is not there at all: it replaces itself with
// the command it was given, so the gate run on a workstation or in the push
// sieve is the same `go test` it always was.
//
// With OPOSSUM_GATE_SHARD=k/N it asks `go test -list` for every test, fuzz
// target and example of the packages (with the command's own flags, so the
// list is of the binaries that will run), takes the ones whose name falls in
// part k of N, and runs only those. A name decides its part on its own, so the
// same name in two packages is in the same part, and a `-run` built from the
// names runs each of them once, in every package that has it. What it listed,
// what it took and what ran are appended to the file OPOSSUM_GATE_SHARD_MANIFEST
// names; `shard -verify <dir>` reads one such file per part and refuses unless
// the parts together ran every test listed, each exactly once.
//
// Usage:
//
//	shard go test [flags] <packages>
//	shard -verify <dir>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "-verify" {
		summary, err := verifyDir(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "shard: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(summary)
		return
	}
	if len(args) < 2 || args[0] != "go" || args[1] != "test" {
		fmt.Fprintln(os.Stderr, "usage: shard go test [flags] <packages>\n       shard -verify <dir>")
		os.Exit(2)
	}
	spec := os.Getenv("OPOSSUM_GATE_SHARD")
	if spec == "" {
		path, err := exec.LookPath(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "shard: %v\n", err)
			os.Exit(2)
		}
		err = syscall.Exec(path, args, os.Environ())
		fmt.Fprintf(os.Stderr, "shard: could not run %s: %v\n", path, err)
		os.Exit(2)
	}
	part, err := parsePart(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shard: OPOSSUM_GATE_SHARD: %v\n", err)
		os.Exit(2)
	}
	manifest := os.Getenv("OPOSSUM_GATE_SHARD_MANIFEST")
	if manifest == "" {
		fmt.Fprintln(os.Stderr, "shard: OPOSSUM_GATE_SHARD is set and OPOSSUM_GATE_SHARD_MANIFEST is not; a part whose record is nowhere cannot be counted toward the whole")
		os.Exit(2)
	}
	// ^C reaches the whole process group, so `go test` hears it too and ends
	// its packages; this stays to write down what ran and pass its status on.
	// Caught rather than ignored: an ignored signal stays ignored in a child,
	// and `go test` would not hear it either.
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	os.Exit(runPart(part, args[2:], manifest, goTool{}, os.Stdout, os.Stderr))
}
