package orchestrator

import (
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// Network aliases are part of the fingerprint only when there are some: adding
// or changing one recreates the container, and a service without them keeps the
// hash its container was made with before aliases were passed (the value the
// tty test holds for the same options).
func TestConfigHashSeesAliasesOnlyWhenSet(t *testing.T) {
	base := runtime.RunOptions{Name: "web", Image: "alpine:3.20", Networks: []string{"demo-net"}}
	if got := configHash(base); got != "3142b07b376b9ed9" {
		t.Errorf("with no aliases the hash must be what existing containers carry: got %s", got)
	}
	with := func(m map[string][]string) runtime.RunOptions {
		o := base
		o.NetworkAliases = m
		return o
	}
	a := with(map[string][]string{"demo-net": {"cache.demo.opossum"}})
	if configHash(a) == configHash(base) {
		t.Errorf("an alias must change the hash")
	}
	if configHash(a) == configHash(with(map[string][]string{"demo-net": {"kv.demo.opossum"}})) {
		t.Errorf("another alias must change the hash")
	}
	if configHash(a) == configHash(with(map[string][]string{"back": {"cache.demo.opossum"}})) {
		t.Errorf("the alias on another network must change the hash")
	}
	two := with(map[string][]string{"a": {"x"}, "b": {"y"}})
	for i := 0; i < 20; i++ {
		if configHash(two) != configHash(with(map[string][]string{"b": {"y"}, "a": {"x"}})) {
			t.Fatalf("the hash must not depend on the order of the networks in the map")
		}
	}
}
