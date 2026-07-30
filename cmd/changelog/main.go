// Command changelog assembles CHANGELOG.md from the fragments in changelog.d/.
//
// It is a repository tool, not part of the opossum binary (goreleaser builds only
// ./cmd/opossum).
//
//	changelog preview        print what `## [Unreleased]` should contain
//	changelog sync           write that into CHANGELOG.md
//	changelog release X.Y.Z  fold the fragments into a released section and delete them
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/suruseas/opossum/internal/changelog"
)

const (
	fragmentDir   = "changelog.d"
	changelogPath = "CHANGELOG.md"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "changelog: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: changelog preview|sync|release <version>")
	}
	frags, err := changelog.Load(fragmentDir)
	if err != nil {
		return err
	}
	switch args[0] {
	case "preview":
		fmt.Print(changelog.RenderBody(frags))
		return nil
	case "sync":
		return rewrite(func(s string) (string, error) {
			return changelog.WithUnreleased(s, changelog.RenderBody(frags))
		})
	case "release":
		if len(args) < 2 {
			return fmt.Errorf("release needs a version, e.g. `changelog release 0.17.0`")
		}
		date := time.Now().Format("2006-01-02")
		if len(args) > 2 {
			date = args[2] // an explicit date keeps the command reproducible in tests
		}
		if err := rewrite(func(s string) (string, error) {
			return changelog.Release(s, frags, args[1], date)
		}); err != nil {
			return err
		}
		return changelog.Consume(frags)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func rewrite(f func(string) (string, error)) error {
	b, err := os.ReadFile(changelogPath)
	if err != nil {
		return err
	}
	out, err := f(string(b))
	if err != nil {
		return err
	}
	return os.WriteFile(changelogPath, []byte(out), 0o644)
}
