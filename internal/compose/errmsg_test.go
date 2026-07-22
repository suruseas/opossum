package compose

// Error-quality evals (#277): the compose errors a user hits most — a YAML typo,
// an undefined reference, a bad duration/resource value — must say not just WHAT is
// wrong but HOW to fix it (a concrete next step). These golden-substring tests lock
// that in so a future edit can't quietly regress a message back to a raw passthrough.

import (
	"strings"
	"testing"
)

// loadErr loads a compose body expected to fail and returns the error text.
func loadErr(t *testing.T, body string) string {
	t.Helper()
	_, err := Load(writeTemp(t, body))
	if err == nil {
		t.Fatal("expected a load error, got nil")
	}
	return err.Error()
}

func TestErrMsgInvalidYAML(t *testing.T) {
	// A malformed compose file (bad indentation) is the most-hit failure — the
	// message must frame it as invalid YAML and point at the fix, not just dump the
	// raw go-yaml error.
	s := loadErr(t, "services:\n  web:\n  image: web\n   bad: : :\n")
	if !strings.Contains(s, "not valid YAML") || !strings.Contains(s, "indentation") {
		t.Errorf("YAML parse error should name the problem and hint at the fix, got: %s", s)
	}
}

func TestErrMsgUndefinedSecret(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    secrets: [dbpass]
`)
	if !strings.Contains(s, "undefined secret") || !strings.Contains(s, "top-level secrets:") {
		t.Errorf("undefined-secret error should say how to declare it, got: %s", s)
	}
}

func TestErrMsgUnknownDependency(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    depends_on: [db]
`)
	if !strings.Contains(s, "unknown service") || !strings.Contains(s, "depends_on") {
		t.Errorf("unknown-dependency error should say how to fix it, got: %s", s)
	}
}

func TestErrMsgUnsupportedCondition(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_bogus
  db:
    image: postgres
`)
	if !strings.Contains(s, "service_healthy") || !strings.Contains(s, "service_completed_successfully") {
		t.Errorf("unsupported-condition error should list the valid conditions, got: %s", s)
	}
}

func TestErrMsgBadDuration(t *testing.T) {
	// interval: 5 (no unit) is a classic mistake — Go's raw "missing unit" is
	// cryptic; the message must show a unit example.
	s := loadErr(t, `
services:
  web:
    image: web
    healthcheck:
      test: ["CMD", "true"]
      interval: 5
`)
	if !strings.Contains(s, "is not a duration") || !strings.Contains(s, "30s") {
		t.Errorf("bad-duration error should show a unit example, got: %s", s)
	}
	// A semantic error surfaced through the top-level decode must NOT be mislabeled
	// as a YAML syntax problem (that framing is only for real parse errors).
	if strings.Contains(s, "not valid YAML") {
		t.Errorf("a bad duration is a semantic error, not invalid YAML, got: %s", s)
	}
}

func TestErrMsgBadMemory(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    mem_limit: "lots"
`)
	if !strings.Contains(s, "memory") || !strings.Contains(s, "512m") {
		t.Errorf("bad-memory error should show a value example, got: %s", s)
	}
}

func TestErrMsgBadCPUs(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    cpus: "two"
`)
	if !strings.Contains(s, "cpus") || !strings.Contains(s, "1.5") {
		t.Errorf("bad-cpus error should show a value example, got: %s", s)
	}
}
