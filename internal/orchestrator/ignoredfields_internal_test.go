package orchestrator

// Unit evals for the ignored-fields note (#274): silent field-dropping let agents
// cargo-cult invalid compose (real case: `dns_search: [name].opossum`), so up/run
// print a one-line pointer to `opossum config`.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

func noteOrch(t *testing.T, p *compose.Project) *Orchestrator {
	t.Helper()
	return New(p, &runtime.Runtime{}, "", &bytes.Buffer{})
}

func TestIgnoredFieldsNote(t *testing.T) {
	svc := func(name string, unsupported ...string) map[string]*compose.Service {
		return map[string]*compose.Service{name: {Name: name, Image: "x", Unsupported: unsupported}}
	}

	t.Run("none is empty", func(t *testing.T) {
		p := &compose.Project{Name: "p", Services: svc("web")}
		if got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, true); got != "" {
			t.Errorf("no ignored fields should give no note, got %q", got)
		}
	})

	t.Run("singular names the field and points to config", func(t *testing.T) {
		p := &compose.Project{Name: "p", Services: svc("web", "dns_search")}
		got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, true)
		want := "note: 1 compose field is ignored (web: dns_search) — run `opossum config` for details\n"
		if got != want {
			t.Errorf("singular note mismatch:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("plural counts all and shows a representative", func(t *testing.T) {
		p := &compose.Project{Name: "p", Services: svc("web", "dns_search", "restart")}
		got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, true)
		if !strings.Contains(got, "2 compose fields are ignored") || !strings.Contains(got, "(e.g. web: dns_search)") {
			t.Errorf("plural note should count 2 and show a representative, got %q", got)
		}
	})

	t.Run("representative favors a service field over top-level", func(t *testing.T) {
		p := &compose.Project{Name: "p", Services: svc("web", "dns_search"), Unsupported: []string{"volumes"}}
		got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, true)
		// 2 total (web:dns_search + top-level:volumes); the service field leads.
		if !strings.Contains(got, "2 compose fields are ignored") || !strings.Contains(got, "(e.g. web: dns_search)") {
			t.Errorf("service-level field should be the representative, got %q", got)
		}
	})

	t.Run("top-level only", func(t *testing.T) {
		p := &compose.Project{Name: "p", Services: svc("web"), Unsupported: []string{"networks"}}
		got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, true)
		if !strings.Contains(got, "1 compose field is ignored (top-level: networks)") {
			t.Errorf("top-level-only note mismatch, got %q", got)
		}
	})

	t.Run("only counts services in scope", func(t *testing.T) {
		p := &compose.Project{Name: "p", Services: map[string]*compose.Service{
			"web": {Name: "web", Image: "x", Unsupported: []string{"dns_search"}},
			"db":  {Name: "db", Image: "x", Unsupported: []string{"restart"}},
		}}
		// Only "web" is being started; db's ignored field must not be counted.
		got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, true)
		if !strings.Contains(got, "1 compose field is ignored (web: dns_search)") {
			t.Errorf("out-of-scope service should not be counted, got %q", got)
		}
	})

	t.Run("includeTopLevel=false omits project-wide fields", func(t *testing.T) {
		// A one-off with deps delegates top-level reporting to the deps' Up, so it
		// passes false — the top-level field must not appear or be counted here.
		p := &compose.Project{Name: "p", Services: svc("web", "dns_search"), Unsupported: []string{"volumes"}}
		got := noteOrch(t, p).ignoredFieldsNote([]string{"web"}, false)
		if !strings.Contains(got, "1 compose field is ignored (web: dns_search)") || strings.Contains(got, "top-level") {
			t.Errorf("top-level should be omitted when includeTopLevel=false, got %q", got)
		}
	})
}

// TestAgentsMdMarksDnsIgnored is a ratchet: AGENTS.md must keep documenting that
// dns/dns_search are ignored and that service discovery is automatic — the guidance
// that stops an agent inventing dns config. #274.
func TestAgentsMdMarksDnsIgnored(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	md := string(data)
	for _, want := range []string{"`dns`", "`dns_search`", "automatic"} {
		if !strings.Contains(md, want) {
			t.Errorf("AGENTS.md should document dns/dns_search as ignored + auto discovery; missing %q", want)
		}
	}
}
