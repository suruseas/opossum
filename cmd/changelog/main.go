// Command changelog assembles CHANGELOG.md from the fragments in changelog.d/.
//
// It is a repository tool, not part of the opossum binary (goreleaser builds only
// ./cmd/opossum).
//
//	changelog preview        print what `## [Unreleased]` should contain
//	changelog sync           write that into CHANGELOG.md
//	changelog release X.Y.Z  fold the fragments into a released section and delete them
//	changelog check FILE...  report Markdown in the files that GitHub renders differently from how it reads
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suruseas/opossum/internal/changelog"
)

const (
	fragmentDir   = "changelog.d"
	changelogPath = "CHANGELOG.md"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "check" {
		os.Exit(check(os.Args[2:], os.Stderr))
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "changelog: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: changelog preview|sync|release <version>|check <file>...")
	}
	frags, err := changelog.Load(fragmentDir)
	if err != nil {
		return err
	}
	// Checked here, for fragments, and not inside Load: Load also reads published
	// sections back when a release is rebuilt, and those are never rewritten. The
	// file as written, not the trimmed body, so a line number is the file's.
	for _, f := range frags {
		if p, err := changelog.FileMarkupProblem(f.Path); err != nil || p != "" {
			if err != nil {
				return err
			}
			return fmt.Errorf("%s: %s", f.Path, p)
		}
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

// check reports, for each file, the first piece of Markdown that renders
// differently from how it reads — the same check fragments get — and returns the
// exit code: 0 when every file is clean, 1 when one is not or cannot be read, 2
// when no file was named. It reads any Markdown file, not only fragments: the
// release notes assembled around a changelog section are published the same way.
// Raw HTML is refused wherever it appears, including tags GitHub does render:
// the check cannot tell which tags a page keeps. Each file's first problem is
// reported. Run through `go run`, every non-zero exit reaches the shell as 1.
func check(files []string, stderr io.Writer) int {
	if len(files) == 0 {
		fmt.Fprintln(stderr, "usage: changelog check <file>... (reports each file's first problem; exit 1 if a file has one or cannot be read, 2 when no file is named)")
		return 2
	}
	code := 0
	for _, f := range files {
		p, err := changelog.FileMarkupProblem(f)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", f, err)
			code = 1
			continue
		}
		if p != "" {
			fmt.Fprintf(stderr, "%s: %s\n", f, p)
			code = 1
		}
	}
	return code
}
