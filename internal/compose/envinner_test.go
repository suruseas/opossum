package compose

// A variable named `environment` or `labels` inside an env-like mapping (#733).
// The merge rule for a service's `environment:` field used to fire again on a
// key of that name *inside* the field when both files set it, and a scalar read
// as an env mapping is empty — so `environment: prod` overridden by
// `environment: stg` came out as `environment=map[]`. Expected values below are
// what `docker compose config` (v5.5.0) prints for the same pairs.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func loadPair(t *testing.T, base, over string) *Project {
	t.Helper()
	dir := t.TempDir()
	b := filepath.Join(dir, "base.yml")
	o := filepath.Join(dir, "over.yml")
	for p, body := range map[string]string{b: base, o: over} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadFiles([]string{b, o}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

func sortedCopy(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

func TestAVariableNamedEnvironmentInsideEnvironmentMergesAsAScalar(t *testing.T) {
	for _, tc := range []struct {
		name, base, over string
		svc              string
		want             []string // Environment (or build.args) after the merge, sorted
		args             bool
	}{
		{
			name: "environment and labels as variable names, map form",
			base: "services:\n  web:\n    image: alpine\n    environment:\n      environment: prod\n      labels: one\n      OTHER: keep\n",
			over: "services:\n  web:\n    environment:\n      environment: stg\n      labels: two\n",
			svc:  "web", want: []string{"OTHER=keep", "environment=stg", "labels=two"},
		},
		{
			name: "list form on both sides",
			base: "services:\n  web:\n    image: alpine\n    environment:\n      - environment=prod\n",
			over: "services:\n  web:\n    environment:\n      - environment=stg\n",
			svc:  "web", want: []string{"environment=stg"},
		},
		{
			name: "map form over list form",
			base: "services:\n  web:\n    image: alpine\n    environment:\n      - environment=prod\n",
			over: "services:\n  web:\n    environment:\n      environment: stg\n",
			svc:  "web", want: []string{"environment=stg"},
		},
		{
			name: "inside build.args",
			base: "services:\n  web:\n    build:\n      context: .\n      args:\n        environment: prod\n        args: x\n",
			over: "services:\n  web:\n    build:\n      args:\n        environment: stg\n        args: y\n",
			svc:  "web", want: []string{"args=y", "environment=stg"}, args: true,
		},
		{
			name: "a service called environment, with such a variable in its own environment",
			base: "services:\n  environment:\n    image: alpine\n    environment:\n      A: one\n      environment: prod\n",
			over: "services:\n  environment:\n    environment:\n      A: two\n      environment: stg\n",
			svc:  "environment", want: []string{"A=two", "environment=stg"},
		},
		{
			// The same service in list form on both sides: the rule must still
			// be ON for the service's own field (it is the `!collections[parent]`
			// term of insideEnvLike that keeps it on), or the two lists append
			// and the variable is set twice.
			name: "a service called environment, list form on both sides, colliding variable",
			base: "services:\n  environment:\n    image: alpine\n    environment:\n      - A=one\n",
			over: "services:\n  environment:\n    environment:\n      - A=two\n",
			svc:  "environment", want: []string{"A=two"},
		},
		{
			name: "the variable set to nothing in the override is unset, not kept (#732)",
			base: "services:\n  web:\n    image: alpine\n    environment:\n      environment: prod\n",
			over: "services:\n  web:\n    environment:\n      environment:\n",
			svc:  "web", want: []string{"environment"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadPair(t, tc.base, tc.over)
			svc := p.Services[tc.svc]
			if svc == nil {
				t.Fatalf("service %q missing", tc.svc)
			}
			got := []string(svc.Environment)
			if tc.args {
				got = []string(svc.Build.Args)
			}
			if g, w := strings.Join(sortedCopy(got), " "), strings.Join(tc.want, " "); g != w {
				t.Errorf("got %q, want %q", g, w)
			}
		})
	}
}
