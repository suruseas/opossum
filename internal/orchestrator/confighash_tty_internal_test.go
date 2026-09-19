package orchestrator

import (
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// `tty` is part of the fingerprint only when set: toggling it recreates the
// container, and a service without it keeps the hash its container was made
// with before `tty` was read — held to the value main produced for this
// service (2026-09-19), so an upgrade does not recreate every service.
func TestConfigHashSeesTTYOnlyWhenSet(t *testing.T) {
	base := runtime.RunOptions{Name: "web", Image: "alpine:3.20", Networks: []string{"demo-net"}}
	withTTY := base
	withTTY.TTY = true
	if configHash(base) == configHash(withTTY) {
		t.Errorf("tty: true must change the hash")
	}
	// Its own word in the fingerprint, not another boolean's: a service made
	// read-only and one given a tty must not share a hash, or a change from
	// the one to the other would leave the old container running as "up to
	// date".
	withReadOnly := base
	withReadOnly.ReadOnly = true
	if configHash(withReadOnly) == configHash(withTTY) {
		t.Errorf("read_only: true and tty: true must not hash alike")
	}
	if got := configHash(base); got != "3142b07b376b9ed9" {
		t.Errorf("with no tty the hash must be what existing containers carry: got %s, want 3142b07b376b9ed9", got)
	}
}
