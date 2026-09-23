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

// The forms the loader lets through without a declaration run as what the
// loader took them for: a host path in every spelling mounts as a bind (an
// absolute path on the host side), a bare target as an anonymous volume named
// for the service, tmpfs as `--tmpfs`, and a declared volume by its runtime
// name. The loader and the orchestrator share one judgement of an entry
// (compose.ClassifyMount); this pins the run side of that judgement with the
// same inputs the loader's own table uses, so a drift between the two shows
// as a mount running as something other than what loaded.
//
// One entry per file on purpose: each row is a membership question (does this
// spelling run as this kind), with no second element whose order or
// attribution could hide anything.
//
// The boundaries the judgement answers, one row each (docker compose's rule:
// a source starting with `/`, `.` or `~` is a path, the rest a volume name):
// `/…` · `./…` · `../…` · `.` · `..` · `.hidden` · `.hidden/sub` · `..hidden`
// · `~` · `~/…` · empty source · bare target · long-form bind · long-form
// volume · tmpfs · a declared name with a mode / nocopy · external. (`~name`
// never reaches a run: the loader refuses it.)
func TestAMountRunsAsWhatItLoadedAs(t *testing.T) {
	// `mkdir` is the host directory a bind's `up` has to have created (the
	// runtime refuses a missing one: `path '…' does not exist`); empty where
	// there is nothing to create (a volume, the compose dir itself, home
	// itself — which exists already).
	//
	// A source under home is created like any other missing one, so the rows
	// that use `~` name what they make. They used to leave the column empty,
	// which read as "nothing is created here" and was how this test came to
	// make a directory in the home of whoever ran it: `go test ./...` left an
	// empty directory behind, under a one-letter name that said nothing about
	// what had made it. Each row now runs with a home of its own, names what
	// it makes after this test, and the run is held to making nothing outside
	// it.
	for _, tc := range []struct{ name, item, want, mkdir string }{
		{"a relative host path", "./x:/y", "-v {dir}/x:/y", "{dir}/x"},
		{"a parent-relative host path", "../x:/y", "-v {parent}/x:/y", "{parent}/x"},
		{"the directory itself", ".:/y", "-v {dir}:/y", ""},
		{"an absolute host path", "{dir}/abs:/y", "-v {dir}/abs:/y", "{dir}/abs"},
		{"a home-relative host path", "~/opossum-test-mountkinds:/y", "-v {home}/opossum-test-mountkinds:/y", "{home}/opossum-test-mountkinds"},
		{"the home directory itself", "~:/y", "-v {home}:/y", ""},
		{"a hidden name, a path as docker reads it", ".hidden:/y", "-v {dir}/.hidden:/y", "{dir}/.hidden"},
		{"a path under a hidden name", ".hidden/sub:/y", "-v {dir}/.hidden/sub:/y", "{dir}/.hidden/sub"},
		{"a name starting with two dots", "..hidden:/y", "-v {dir}/..hidden:/y", "{dir}/..hidden"},
		{"a long-form bind", "{type: bind, source: ./lb, target: /lb}", "-v {dir}/lb:/lb", "{dir}/lb"},
		{"an anonymous volume", "/anon", "-v demo_web_anon_", ""},
		{"an anonymous volume written with an empty source", ":/es", "-v demo_web_es_", ""},
		{"a long-form anonymous volume", "{type: volume, target: /la}", "-v demo_web_la_", ""},
		{"a tmpfs", "{type: tmpfs, target: /t}", "--tmpfs /t", ""},
		{"a declared volume with a mode", "good:/g:ro", "-v demo_good:/g:ro", ""},
		{"a declared volume with nocopy", "good:/n:nocopy", "-v demo_good:/n", ""},
		{"a declared external volume", "ext:/e", "-v ext:/e", ""},
		{"a declared volume in the long form", "{type: volume, source: good, target: /lg}", "-v demo_good:/lg", ""},
		{"a long-form bind whose source is a bare name", "{type: bind, source: data, target: /y}", "-v {dir}/data:/y", "{dir}/data"},
		{"a long-form bind whose source is home-relative", "{type: bind, source: ~/opossum-test-mountkinds, target: /y}", "-v {home}/opossum-test-mountkinds:/y", "{home}/opossum-test-mountkinds"},
		{"a long-form bind whose source is a hidden name", "{type: bind, source: .lhid, target: /y}", "-v {dir}/.lhid:/y", "{dir}/.lhid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// The home the run expands `~` against is one of this test's
			// own, so a row that creates something under it creates it
			// there. `real` is the home the suite was started with, kept to
			// hold the run to that: with the line below gone, a row naming
			// `~` would make its directory in it.
			real := os.Getenv("HOME")
			// What the run would make in that home if it reached it. The
			// rows that use `~` name a directory no home is expected to
			// hold, so that its appearing is this run's doing and not
			// something the machine already had: an earlier version of this
			// test used a one-letter name under the home directory, left one
			// behind, and a check asking whether that name was there would
			// then have read its own leavings as normal. The name says which
			// test makes it, so that the next person to find one in their
			// home knows where to look — and so that looking the old name up
			// in this repository finds nothing, rather than finding this
			// sentence and reading it as something that still makes one.
			// Asked before as well as after all the same, since the answer
			// wanted is what this run did.
			const probe = "opossum-test-mountkinds"
			hadProbe := false
			if real != "" {
				if _, err := os.Stat(filepath.Join(real, probe)); err == nil {
					hadProbe = true
				}
			}
			t.Setenv("HOME", t.TempDir())
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			fill := strings.NewReplacer("{dir}", dir, "{parent}", filepath.Dir(dir), "{home}", home)
			body := "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - " + fill.Replace(tc.item) + "\nvolumes:\n  good: {}\n  ext: {external: true}\n"
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := compose.Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			rt, log := fakeShim(t)
			// The external volumes this file declares exist, as a user who declares one has made it.
			setShimEnv(rt, "VOLUME_LS=ext")
			if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			if real != "" && !hadProbe {
				if _, err := os.Stat(filepath.Join(real, probe)); err == nil {
					t.Errorf("%s made %s in the home this suite was started with (%s)", tc.name, probe, real)
				}
			}
			want := fill.Replace(tc.want)
			var runs []string
			for _, l := range log() {
				if strings.HasPrefix(l, "run ") && strings.Contains(l, "web.demo.opossum") {
					runs = append(runs, l)
				}
			}
			if joined := strings.Join(runs, "\n"); !strings.Contains(joined, want) {
				t.Errorf("%s should run with %q, got:\n%s", tc.name, want, joined)
			}
			if tc.mkdir != "" {
				made := fill.Replace(tc.mkdir)
				if st, err := os.Stat(made); err != nil || !st.IsDir() {
					t.Errorf("%s should have had its host directory %s created before the run, got: %v", tc.name, made, err)
				}
				// Where it was made, not only that it was: a row may make
				// something in the directory its file sits in, in that
				// directory's parent (what `../x:/y` names), or in the home
				// this row was given — and nowhere else. Held against those
				// three rather than against the home the suite was started
				// with: `t.TempDir()` sits under `TMPDIR`, and a `TMPDIR`
				// inside that home (the self-hosted runner had one) would
				// make every row read as writing there.
				under := false
				for _, root := range []string{dir, filepath.Dir(dir), home} {
					if rel, err := filepath.Rel(root, made); err == nil && !strings.HasPrefix(rel, "..") {
						under = true
						break
					}
				}
				if !under {
					t.Errorf("%s made %s, which is none of the places this row may write: %s, %s or %s", tc.name, made, dir, filepath.Dir(dir), home)
				}
			}
		})
	}
}

// In the long form the type decides what a source is, not its spelling: the
// same bare name `good` is the directory beside the file under `type: bind`
// and the declared volume under `type: volume`. The two types come as a pair
// in both orders so a reading that ignored the type — or read it from the
// first entry for both — shows on one of them.
func TestTheLongFormTypeDecidesWhatASourceIs(t *testing.T) {
	testpair.Run(t, "bind and volume of one name", testpair.Pair[string]{A: "bind", B: "volume"}, func(t *testing.T, first, second string) {
		dir := t.TempDir()
		body := "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: " + first + ", source: good, target: /a}\n      - {type: " + second + ", source: good, target: /b}\nvolumes:\n  good: {}\n"
		path := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := compose.Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		rt, log := fakeShim(t)
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("up: %v", err)
		}
		joined := strings.Join(log(), "\n")
		want := map[string]string{"bind": "-v " + filepath.Join(dir, "good") + ":", "volume": "-v demo_good:"}
		for typ, target := range map[string]string{first: "/a", second: "/b"} {
			if w := want[typ] + target; !strings.Contains(joined, w) {
				t.Errorf("type: %s at %s should run as %q, got:\n%s", typ, target, w, joined)
			}
		}
	})
}
