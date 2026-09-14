package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The shared-volume warning from `up` and the overlay's suggestions name the
// volumes by the names written in the file, byte for byte. The names are chosen
// so that this order disagrees both with the loader's keys (`.hid` is carried
// under a key that starts with a NUL byte, which sorts before `-m`) and with the
// order `config` prints (`db9` before `db10`, `_u` before `Zed`), and a volume
// with a `name:` of its own (`ab`, created as `000`) sits by what was written,
// not by what the runtime calls it — so the test says which order the output
// follows.
func TestSharedVolumesAreListedByTheirNamesByteForByte(t *testing.T) {
	names := []string{"aaa", ".hid", "db10", "-m", "ab", "Zed", "db9", "_u"}
	var mounts, decls strings.Builder
	for i, n := range names {
		mounts.WriteString("      - {type: volume, source: " + n + ", target: /v" + string(rune('a'+i)) + "}\n")
		if n == "ab" {
			decls.WriteString("  ab: {name: \"000\"}\n")
			continue
		}
		decls.WriteString("  " + n + ": {}\n")
	}
	src := "name: demo\nservices:\n  one:\n    image: alpine\n    volumes:\n" + mounts.String() +
		"  two:\n    image: alpine\n    volumes:\n" + mounts.String() + "volumes:\n" + decls.String()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Byte order of the written names; the key order would put `.hid` first,
	// and config's would put `_u` before `Zed` and `db9` before `db10`.
	want := []string{"-m", ".hid", "Zed", "_u", "aaa", "ab", "db10", "db9"}
	rendered, err := compose.RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if strings.Index(rendered, "\n    _u:") > strings.Index(rendered, "\n    Zed:") {
		t.Fatalf("config should print _u before Zed, or the fixture no longer tells the orders apart:\n%s", rendered)
	}

	rt, _ := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}

	named := regexp.MustCompile(`share(?:s)? named volume "([^"]+)"`)
	firsts := func(s string) []string {
		var got []string
		for _, m := range named.FindAllStringSubmatch(s, -1) {
			if !slices.Contains(got, m[1]) {
				got = append(got, m[1])
			}
		}
		return got
	}
	if got := firsts(out.String()); !slices.Equal(got, want) {
		t.Errorf("up's warnings name %q, want %q", got, want)
	}
	_, changes := o.PlanOverlay()
	var summaries strings.Builder
	for _, c := range changes {
		summaries.WriteString(c.Summary + "\n")
	}
	if got := firsts(summaries.String()); !slices.Equal(got, want) {
		t.Errorf("the overlay's suggestions name %q, want %q", got, want)
	}
}
