package orchestrator

import (
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// The two refusals are read from the runtime's own line, which begins the line
// (network-aliases-161.txt); a container's output that repeats one, or the
// line indented, is not the refusal.
func TestTheHeldNameRefusalsAreTheRuntimesLine(t *testing.T) {
	const alias = `Error: alias(es) already exist: ["cache.demo.opossum"]`
	const host = `Error: hostname(s) already exist: ["cache.demo.opossum."]`
	for _, tc := range []struct {
		name, stderr     string
		wantAlias, wantH bool
	}{
		{"the alias refusal", "[0/6] [0s]\n" + alias + "\n", true, false},
		{"the hostname refusal", "[0/6] [0s]\n" + host + "\n", false, true},
		{"the alias refusal repeated by a container", "worker: " + alias, false, false},
		{"the hostname refusal repeated by a container", "worker: " + host, false, false},
		{"the alias refusal indented", " " + alias, false, false},
		{"the hostname refusal indented", " " + host, false, false},
		{"another refusal", `Error: alias "x" is declared on more than one network`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := aliasesHeld(tc.stderr); got != tc.wantAlias {
				t.Errorf("aliasesHeld = %v, want %v", got, tc.wantAlias)
			}
			if got := hostnameHeldAsAlias(tc.stderr); got != tc.wantH {
				t.Errorf("hostnameHeldAsAlias = %v, want %v", got, tc.wantH)
			}
		})
	}
}

// The hostname refusal decodes to OPSM-217, and only the runtime's own line does.
func TestTheHostnameRefusalCarriesItsCode(t *testing.T) {
	h := runErrorHint(&compose.Service{}, "", false, runErr(`Error: hostname(s) already exist: ["cache.demo.opossum."]`))
	if want := "[" + string(codeNameHeldAsAlias) + "]"; !contains(h, want) {
		t.Errorf("want %s in the hint, got %q", want, h)
	}
	if h := runErrorHint(&compose.Service{}, "", false, runErr(`cache: Error: hostname(s) already exist: ["x."]`)); h != "" {
		t.Errorf("a container's line is not the refusal, got %q", h)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
