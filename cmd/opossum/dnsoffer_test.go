package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// offering sets what the process has for the question about the DNS domain — the terminal or not on stdin and on stderr, what is typed, where the question
// goes — and what a yes runs, which here is counted and never sudo. It returns what a yes was asked to create and what was written for the question.
func offering(t *testing.T, stdinTTY, stderrTTY bool, typed string) (created *[]string, asked *strings.Builder) {
	t.Helper()
	// Taken before the question's output is replaced, as it is the one the process has: the question is written to stderr — the terminal it is asked at, which
	// stdin and stderr being terminals is what lets it be asked at — and not to stdout, which a command's output goes to.
	if dnsOfferOut != io.Writer(os.Stderr) {
		t.Fatalf("the question is written to %v, and not to stderr", dnsOfferOut)
	}
	oldIn, oldErr, oldTyped, oldOut, oldCreate := stdinIsTerminal, stderrIsTerminal, dnsOfferIn, dnsOfferOut, createDNSDomain
	var made []string
	var question strings.Builder
	stdinIsTerminal = func() bool { return stdinTTY }
	stderrIsTerminal = func() bool { return stderrTTY }
	dnsOfferIn = strings.NewReader(typed)
	dnsOfferOut = &question
	createDNSDomain = func(*runtime.Runtime) func(string) error {
		return func(d string) error {
			made = append(made, d)
			// What the runtime lists from now on: the domain beside the one that was there, as a created one is.
			t.Setenv("DNS_DOMAINS", "opossum "+d)
			return nil
		}
	}
	t.Cleanup(func() {
		stdinIsTerminal, stderrIsTerminal, dnsOfferIn, dnsOfferOut, createDNSDomain = oldIn, oldErr, oldTyped, oldOut, oldCreate
	})
	return &made, &question
}

// The question about the DNS domain is asked by `up` and by `doctor --fix`, at a terminal on stdin and on stderr, where the domain is not there, and only a yes
// runs the command (#1907). It is asked by nothing else: not by `doctor` without --fix, not by `ps`.
func TestTheDNSDomainIsOfferedWhereItIsNotThereAtATerminalAndOnlyAYesCreatesIt(t *testing.T) {
	const domain = "neko-absent"
	compose := "name: demo\nservices:\n  web:\n    image: nginx:alpine\n"
	for _, tc := range []struct {
		name               string
		args               func(f string) []string
		stdinTTY, errTTY   bool
		typed              string
		wantCreated, asked bool
	}{
		{"up, at a terminal, yes", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "up"} }, true, true, "y\n", true, true},
		{"up, at a terminal, the default", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "up"} }, true, true, "\n", false, true},
		{"up, at a terminal, no", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "up"} }, true, true, "n\n", false, true},
		{"up, stdin is not a terminal, though it says yes", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "up"} }, false, true, "y\n", false, false},
		{"up, stderr is not a terminal, though it says yes", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "up"} }, true, false, "y\n", false, false},
		{"up, neither is a terminal", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "up"} }, false, false, "y\n", false, false},
		{"up, the domain is there", func(f string) []string { return []string{"-f", f, "up"} }, true, true, "y\n", false, false},
		{"ps asks nothing", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "ps"} }, true, true, "y\n", false, false},
		{"doctor --fix, at a terminal, yes", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "doctor", "--fix"} }, true, true, "y\n", true, true},
		{"doctor --fix, at a terminal, json, yes", func(f string) []string {
			return []string{"-f", f, "--dns-domain", domain, "doctor", "--fix", "--format", "json"}
		}, true, true, "yes\n", true, true},
		{"doctor --fix, the default", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "doctor", "--fix"} }, true, true, "\n", false, true},
		{"doctor --fix, no", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "doctor", "--fix"} }, true, true, "no\n", false, true},
		{"doctor --fix, not at a terminal, though it says yes", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "doctor", "--fix"} }, false, true, "y\n", false, false},
		{"doctor --fix, the domain is there", func(f string) []string { return []string{"-f", f, "doctor", "--fix"} }, true, true, "y\n", false, false},
		{"up, a name that starts with a hyphen is not asked about", func(f string) []string { return []string{"-f", f, "--dns-domain=-x", "up"} }, true, true, "y\n", false, false},
		{"doctor --fix, a name that starts with a hyphen is not asked about", func(f string) []string { return []string{"-f", f, "--dns-domain=-x", "doctor", "--fix"} }, true, true, "y\n", false, false},
		{"doctor --fix with a format that is not one asks nothing, with a yes waiting", func(f string) []string {
			return []string{"-f", f, "--dns-domain", domain, "doctor", "--fix", "--format", "bogus"}
		}, true, true, "y\n", false, false},
		{"up, a name with a space is not asked about", func(f string) []string { return []string{"-f", f, "--dns-domain", "a b", "up"} }, true, true, "y\n", false, false},
		{"doctor without --fix never asks, at a terminal, with a yes waiting", func(f string) []string { return []string{"-f", f, "--dns-domain", domain, "doctor"} }, true, true, "y\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			created, question := offering(t, tc.stdinTTY, tc.errTTY, tc.typed)
			f := writeCompose(t, compose)
			out, _ := run(t, tc.args(f)...)
			if got := len(*created) > 0; got != tc.wantCreated {
				t.Errorf("the command was run: %v (%v), want %v; out: %s", got, *created, tc.wantCreated, out)
			}
			if tc.wantCreated && (len(*created) != 1 || (*created)[0] != domain) {
				t.Errorf("a yes creates the domain, once: %v", *created)
			}
			if got := question.Len() > 0; got != tc.asked {
				t.Errorf("the question was written: %v, want %v: %q", got, tc.asked, question.String())
			}
			if tc.asked && !strings.Contains(question.String(), "system dns create "+domain+" [y/N] ") {
				t.Errorf("the question names the command that is run, and its default: %q", question.String())
			}
			// What was said before is what is said where nothing was created, for `up`: the domain is not found, and the command to type (the domain the run
			// was given, which is `opossum` where none was).
			args := tc.args(f)
			isUp := false
			given := "opossum"
			for i, a := range args {
				if a == "up" {
					isUp = true
				}
				if a == "--dns-domain" && i+1 < len(args) {
					given = args[i+1]
				}
				if strings.HasPrefix(a, "--dns-domain=") {
					given = strings.TrimPrefix(a, "--dns-domain=")
				}
			}
			if isUp {
				thereAlready := given == "opossum"
				switch {
				case tc.wantCreated || thereAlready:
					if strings.Contains(out, "not found") {
						t.Errorf("up says the domain is not found where it was created or is there; out: %s", out)
					}
				case given == "a b":
					// A name with a space is refused earlier, as a container name the runtime does not create: no question, no warning, and no sudo.
				case !strings.Contains(out, `DNS domain "`+given+`" not found`):
					t.Errorf("up says what it said before where nothing was created; out: %s", out)
				}
			}
		})
	}
	_ = io.Discard
}

// After a yes the report is of what is there then: `doctor --fix` creates the domain before it makes the report, so that a domain that was created is reported as
// registered, and one that was not as it was (the fake lists the domain from the moment it is created).
func TestDoctorFixReportsTheDomainAfterItWasCreated(t *testing.T) {
	const domain = "neko-absent"
	for _, tc := range []struct {
		name, typed, want, notWant string
	}{
		{"yes", "y\n", "✅ dns", "❌ dns"},
		{"no", "n\n", "❌ dns", "✅ dns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			created, _ := offering(t, true, true, tc.typed)
			f := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx:alpine\n")
			out, _ := run(t, "-f", f, "--dns-domain", domain, "doctor", "--fix")
			if !strings.Contains(out, tc.want) || strings.Contains(out, tc.notWant) {
				t.Errorf("the report after %q says %q, and not %q (created: %v); out:\n%s", tc.typed, tc.want, tc.notWant, *created, out)
			}
		})
	}
}

// `watch` does not ask: a rebuild runs `up` again, and a question that comes with each is one that Ctrl-C does not end. The offer is set on every command's
// orchestrator, and taken off the one `watch` loads; `run`, which does `up` for its dependencies, keeps it.
func TestWatchDoesNotOfferTheDNSDomainAndOtherCommandsDo(t *testing.T) {
	fakeShim(t)
	f := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx:alpine\n")
	t.Chdir(filepath.Dir(f))
	// -f is a package variable, which the commands an earlier test ran went through cobra to set.
	oldFiles := composeFiles
	composeFiles = []string{f}
	t.Cleanup(func() { composeFiles = oldFiles })
	other, err := loadOrchestratorChecked(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if other.OfferDNSDomain == nil {
		t.Error("a command's orchestrator has the offer, which `up` and `run` ask")
	}
	watching, err := loadWatchOrchestrator(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if watching.OfferDNSDomain != nil {
		t.Error("`watch` asks nothing: its rebuilds would each ask, in a command that is left running")
	}
}
