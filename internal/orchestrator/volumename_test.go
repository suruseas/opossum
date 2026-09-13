package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/testpair"
)

// loadBody loads a compose file written from body into a fresh directory.
func loadBody(t *testing.T, body string) *compose.Project {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

// A project volume declared with `name:` is created, mounted, seeded, listed
// and removed under that name — as docker compose does — on every path that
// names a project volume. Before this, `up` used `<project>_<key>` and the
// declared name went nowhere, so a volume meant to be found by name (a backup
// script, a second project) silently lived somewhere else; and a fix to `up`
// alone would create `my-custom` and remove `demo_named`.
//
// The named and the bare volume come as a pair in both orders (testpair):
// with one volume a rewrite that named everything, or nothing, would look
// right. The pair beside an external volume checks the removal side keeps
// leaving the user's volume alone.
func TestDeclaredVolumeNameIsUsedOnEveryPath(t *testing.T) {
	body := func(first, second string) string {
		return "name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes: [\"" + first + "\", \"" + second + "\"]\n" +
			"volumes:\n  bare:\n  named: {name: my-custom}\n  ext: {external: true, name: real-outside}\n"
	}
	testpair.Run(t, "named and bare: up, volumes, destroy plan, down -v agree", testpair.Pair[string]{A: "named:/b", B: "bare:/a"}, func(t *testing.T, first, second string) {
		rt, log := fakeShim(t)
		o := orchestrator.New(loadBody(t, body(first, second)), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("up: %v", err)
		}
		joined := strings.Join(log(), "\n")
		// The seed mounts are anchored on `-v ` too: a seed sent to
		// `demo_my-custom` still contains `my-custom:/__opossum_seed__`.
		for _, want := range []string{"-v my-custom:/b", "-v demo_bare:/a", "-v my-custom:/__opossum_seed__", "-v demo_bare:/__opossum_seed__"} {
			if !strings.Contains(joined, want) {
				t.Errorf("up should carry %q, got:\n%s", want, joined)
			}
		}
		if strings.Contains(joined, "demo_named") {
			t.Errorf("the declared name replaces the namespaced one, got:\n%s", joined)
		}
		// And the runtime ends up with exactly these two volumes — not a third
		// one a seed or a mount made under another spelling.
		if made := strings.Join(rt.ListVolumes(), " "); made != "demo_bare my-custom" {
			t.Errorf("up should leave exactly the two volumes it mounts, want %q got %q", "demo_bare my-custom", made)
		}
		vols, err := o.ProjectVolumes(nil)
		if err != nil {
			t.Fatalf("volumes: %v", err)
		}
		var names []string
		for _, v := range vols {
			names = append(names, v.Name)
		}
		if got := strings.Join(names, " "); got != "demo_bare my-custom" {
			t.Errorf("`volumes` lists the runtime's names, want %q got %q", "demo_bare my-custom", got)
		}
		plan, err := o.DestroyPlanFor(false, false, false)
		if err != nil {
			t.Fatalf("destroy plan: %v", err)
		}
		if got := strings.Join(plan.Volumes, " "); got != "demo_bare my-custom" {
			t.Errorf("destroy plans the volumes up created, want %q got %q", "demo_bare my-custom", got)
		}
		if err := o.Down(true, "", false); err != nil {
			t.Fatalf("down -v: %v", err)
		}
		lines := log()
		for _, want := range []string{"volume delete my-custom", "volume delete demo_bare"} {
			if !hasLine(lines, want) {
				t.Errorf("down -v removes what up created, want %q in %v", want, lines)
			}
		}
		if countLines(lines, "volume delete") != 2 || strings.Contains(strings.Join(lines, "\n"), "demo_named") {
			t.Errorf("exactly the two project volumes are removed, under their names, got %v", lines)
		}
	})
	testpair.Run(t, "named beside external: the external one is neither seeded nor removed", testpair.Pair[string]{A: "named:/b", B: "ext:/c"}, func(t *testing.T, first, second string) {
		rt, log := fakeShim(t)
		o := orchestrator.New(loadBody(t, body(first, second)), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("up: %v", err)
		}
		joined := strings.Join(log(), "\n")
		for _, want := range []string{"-v my-custom:/b", "-v real-outside:/c", "-v my-custom:/__opossum_seed__"} {
			if !strings.Contains(joined, want) {
				t.Errorf("up should carry %q, got:\n%s", want, joined)
			}
		}
		if strings.Contains(joined, "real-outside:/__opossum_seed__") {
			t.Errorf("an external volume is never seeded, got:\n%s", joined)
		}
		if err := o.Down(true, "", false); err != nil {
			t.Fatalf("down -v: %v", err)
		}
		if lines := log(); !hasLine(lines, "volume delete my-custom") || countLines(lines, "volume delete") != 1 {
			t.Errorf("down -v removes the named project volume and only it, got %v", lines)
		}
	})
	// One volume here on purpose: this case is about the spelling of a name,
	// not the order or attribution of two, so a pair would tell nothing more.
	t.Run("a name spelled like the project's own prefix is still the declared name", func(t *testing.T) {
		rt, log := fakeShim(t)
		p := loadBody(t, "name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes: [\"named:/b\"]\nvolumes:\n  named: {name: demo_other}\n")
		o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("up: %v", err)
		}
		if err := o.Down(true, "", false); err != nil {
			t.Fatalf("down -v: %v", err)
		}
		lines := log()
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "-v demo_other:/b") || !hasLine(lines, "volume delete demo_other") || strings.Contains(joined, "demo_named") {
			t.Errorf("want the declared name demo_other on the mount and the removal, and no demo_named, got:\n%s", joined)
		}
	})
}
