package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// CheckMounts narrows what it checks to the services the active profiles
// leave enabled (EnabledServices) — the one path inside the restart
// supervisor's re-exec that actually reads profiles (#1138). A gated
// service's own mount conflict is not this run's business while its profile
// is off, the same way an inactive service's depends_on is not (#1094).
func TestCheckMountsOnlyReadsEnabledServices(t *testing.T) {
	const body = `
services:
  keep:
    image: alpine:3.20
  broken:
    image: alpine:3.20
    profiles: [g]
    volumes: [/x]
    tmpfs: [/x]
`
	p, err := loadProject(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	const want = `services.broken.volumes[0]: target /x already mounted as services.broken.tmpfs[0]`

	rt, _ := fakeShim(t)
	closed := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := closed.CheckMounts(); err != nil {
		t.Errorf("profile closed: want no conflict (broken is not enabled), got %v", err)
	}

	open := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	open.EnableProfiles([]string{"g"})
	if err := open.CheckMounts(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("profile open: want %q, got %v", want, err)
	}
}
