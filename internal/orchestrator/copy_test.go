package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// cp resolves a `<service>:path` argument to the service's container name and
// delegates to `container cp`, in either direction; a non-service prefix passes
// through.
func TestCopyResolvesServiceRefs(t *testing.T) {
	rt, calls := fakeShim(t)
	p := project("pj", map[string]*compose.Service{"web": {Image: "x"}, "db": {Image: "y"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	cases := []struct{ src, dst, want string }{
		{"./local.conf", "web:/etc/x", "cp ./local.conf web.pj.opossum:/etc/x"},         // host -> container
		{"db:/var/dump.sql", "./dump.sql", "cp db.pj.opossum:/var/dump.sql ./dump.sql"}, // container -> host
		{"nope:/a", "./b", "cp nope:/a ./b"},                                            // unknown prefix passes through
	}
	for _, c := range cases {
		if err := o.Copy(c.src, c.dst); err != nil {
			t.Fatalf("Copy(%q,%q): %v", c.src, c.dst, err)
		}
	}
	got := strings.Join(calls(), "\n")
	for _, c := range cases {
		if !strings.Contains(got, c.want) {
			t.Errorf("expected %q in calls:\n%s", c.want, got)
		}
	}
}
