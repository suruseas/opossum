package orchestrator

import (
	"reflect"
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
			if _, got := aliasesHeld(tc.stderr); got != tc.wantAlias {
				t.Errorf("aliasesHeld = %v, want %v", got, tc.wantAlias)
			}
			if got := hostnameHeldAsAlias(tc.stderr); got != tc.wantH {
				t.Errorf("hostnameHeldAsAlias = %v, want %v", got, tc.wantH)
			}
		})
	}
}

// The alias refusal names the aliases it met: those are what the run made again
// leaves out. A list that does not read as one names none.
func TestTheAliasRefusalNamesWhatIsHeld(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       []string
	}{
		{"one", `Error: alias(es) already exist: ["cache.demo.opossum"]`, []string{"cache.demo.opossum"}},
		{"two", `Error: alias(es) already exist: ["cache.demo.opossum", "kv.demo.opossum"]`, []string{"cache.demo.opossum", "kv.demo.opossum"}},
		{"not a list", `Error: alias(es) already exist: [cache.demo.opossum]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := aliasesHeld("[0/6] [0s]\n" + tc.line + "\n")
			if !ok || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("aliasesHeld = %q, %v; want %q, true", got, ok, tc.want)
			}
		})
	}
}

// Only the held aliases are taken out, on whichever network passes them, without
// regard to case or a trailing dot; the rest are passed as they were.
func TestWithoutAliasesTakesOutOnlyTheHeld(t *testing.T) {
	byNet := map[string][]string{
		"demo-back":  {"cache.demo.opossum", "kv.demo.opossum"},
		"demo-front": {"Www.demo.opossum"},
	}
	for _, tc := range []struct {
		name        string
		held        []string
		wantKept    map[string][]string
		wantDropped []string
	}{
		{"one on the first network", []string{"cache.demo.opossum"},
			map[string][]string{"demo-back": {"kv.demo.opossum"}, "demo-front": {"Www.demo.opossum"}}, []string{"cache.demo.opossum"}},
		{"one on the second network, in other capitals with a dot", []string{"www.demo.opossum."},
			map[string][]string{"demo-back": {"cache.demo.opossum", "kv.demo.opossum"}}, []string{"Www.demo.opossum"}},
		// Matched without regard to case, whichever capitals either side has.
		{"one named in capitals", []string{"KV.DEMO.OPOSSUM"},
			map[string][]string{"demo-back": {"cache.demo.opossum"}, "demo-front": {"Www.demo.opossum"}}, []string{"kv.demo.opossum"}},
		{"every one", []string{"cache.demo.opossum", "kv.demo.opossum", "www.demo.opossum"}, nil,
			[]string{"cache.demo.opossum", "kv.demo.opossum", "Www.demo.opossum"}},
		{"none the run passed", []string{"other.demo.opossum"}, byNet, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, dropped := withoutAliases(byNet, tc.held)
			if !reflect.DeepEqual(kept, tc.wantKept) || !reflect.DeepEqual(dropped, tc.wantDropped) {
				t.Errorf("withoutAliases = %q, %q; want %q, %q", kept, dropped, tc.wantKept, tc.wantDropped)
			}
		})
	}
}

// The hostname refusal decodes to OPSM-217, and only the runtime's own line does.
func TestTheHostnameRefusalCarriesItsCode(t *testing.T) {
	h := runErrorHint(&compose.Service{}, "", false, runErr(`Error: hostname(s) already exist: ["cache.demo.opossum."]`))
	// The whole advice, in order: recreating the holder starts only the holder
	// (network-aliases-161.txt, part c), so the `opossum up` after it is a step
	// of its own; `--remove-orphans` is for a holder the project no longer has
	// (`down` alone leaves one).
	want := "\n  → [" + string(codeNameHeldAsAlias) + "] another container holds this service's name as a network alias — " +
		"one an earlier `opossum up` made, before a service took that name. Recreate the service that holds the alias " +
		"(`opossum up --force-recreate <service>`), which no longer passes it, then `opossum up`; " +
		"if the holder is a container this project no longer has, `opossum up --remove-orphans`"
	if h != want {
		t.Errorf("hint:\n got %q\nwant %q", h, want)
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
