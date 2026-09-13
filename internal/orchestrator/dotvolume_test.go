package orchestrator_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/testpair"
)

// A long-form `type: volume` whose source starts with `.` names a volume, as
// docker compose and the runtime both take it (`<project>_.hidden` is created
// and mounted). Its short spelling would read as a host path, so the loader
// carries the type in a spelling of its own; that spelling must never reach
// anything a person or the runtime reads. Every row runs `up`, `down -v`,
// `config`, and `up` again from the rendered config, and looks in all of it
// for the word the loader's spelling carries (compose.ShowsLoaderSpelling).
func TestAVolumeWhoseNameStartsWithADotRunsAsThatVolume(t *testing.T) {
	const decls = "volumes:\n  .hid: {}\n  ..x: {}\n  \".\": {}\n  \"..\": {}\n  .hext: {external: true}\n  .hnamed: {name: realvol}\n  .pg: {}\n"
	for _, tc := range []struct {
		name, image, item string
		source            string // the volume's name as written
		run               string // what the service's run line mounts
		deleted           string // what `down -v` removes, empty when nothing
	}{
		{"a declared volume", "alpine", "{type: volume, source: .hid, target: /y}", ".hid", "-v demo_.hid:/y", "volume delete demo_.hid"},
		{"a declared volume starting with two dots", "alpine", "{type: volume, source: ..x, target: /y}", "..x", "-v demo_..x:/y", "volume delete demo_..x"},
		{"read-only", "alpine", "{type: volume, source: .hid, target: /y, read_only: true}", ".hid", "-v demo_.hid:/y:ro", "volume delete demo_.hid"},
		{"a declared external volume", "alpine", "{type: volume, source: .hext, target: /y}", ".hext", "-v .hext:/y", ""},
		{"a declared volume with a name", "alpine", "{type: volume, source: .hnamed, target: /y}", ".hnamed", "-v realvol:/y", "volume delete realvol"},
		// `.` and `..` alone are names too (docker compose passes both as
		// volumes); as paths they would be the project directory and its parent.
		{"a declared volume named a single dot", "alpine", "{type: volume, source: \".\", target: /y}", ".", "-v demo_.:/y", "volume delete demo_."},
		{"a declared volume named two dots", "alpine", "{type: volume, source: \"..\", target: /y}", "..", "-v demo_..:/y", "volume delete demo_.."},
		{"nocopy", "alpine", "{type: volume, source: .hid, target: /y, volume: {nocopy: true}}", ".hid", "-v demo_.hid:/y", "volume delete demo_.hid"},
		// config pairs nocopy with its mount by target, which docker compose
		// prints without the trailing `/`.
		{"nocopy on a target written with a trailing slash", "alpine", "{type: volume, source: .hid, target: /z/, volume: {nocopy: true}}", ".hid", "-v demo_.hid:/z", "volume delete demo_.hid"},
		// The database adaptations read a data directory's mount by its source;
		// they have to see a volume here, and their notes a name.
		{"a postgres data directory", "postgres:16", "{type: volume, source: .pg, target: /var/lib/postgresql/data}", ".pg", "-v demo_.pg:/var/lib/postgresql/data", "volume delete demo_.pg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "name: demo\nservices:\n  web:\n    image: " + tc.image + "\n    volumes:\n      - " + tc.item + "\n" + decls
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := compose.Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			rt, log := fakeShim(t)
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			if err := o.Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			runs := runLinesOf(log(), "")
			if len(runs) == 0 {
				t.Fatalf("up ran nothing:\n%s", strings.Join(log(), "\n"))
			}
			if !strings.Contains(strings.Join(runs, "\n"), tc.run) {
				t.Errorf("should run with %q, got:\n%s", tc.run, strings.Join(runs, "\n"))
			}
			if tc.deleted == "" && indexOf(log(), "volume create .hext") >= 0 {
				t.Errorf("an external volume is the user's and is not created, got:\n%s", strings.Join(log(), "\n"))
			}
			if err := o.Down(true, "", false); err != nil {
				t.Fatalf("down -v: %v", err)
			}
			if tc.deleted != "" && !hasLine(log(), tc.deleted) {
				t.Errorf("down -v should run %q, got:\n%s", tc.deleted, strings.Join(log(), "\n"))
			}
			if tc.deleted == "" && indexOf(log(), "volume delete .hext") >= 0 {
				t.Errorf("down -v never removes an external volume, got:\n%s", strings.Join(log(), "\n"))
			}
			rendered, err := compose.RenderConfig(p)
			if err != nil {
				t.Fatalf("config: %v", err)
			}
			for what, text := range map[string]string{"the runtime": strings.Join(log(), "\n"), "the output": out.String(), "config": rendered} {
				if compose.ShowsLoaderSpelling(text) {
					t.Errorf("the loader's spelling reached %s: %q", what, text)
				}
			}
			// config prints what was written: the long form, and the declaration
			// under its own name.
			for _, want := range []string{"type: volume", "source: " + tc.source, "\n    " + tc.source + ":"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("config should print %q, got:\n%s", want, rendered)
				}
			}
			if strings.Contains(tc.item, "nocopy: true") && !strings.Contains(rendered, "nocopy: true") {
				t.Errorf("config should keep nocopy, got:\n%s", rendered)
			}
			// The rendered config is a compose file of its own: it runs the same.
			if err := os.WriteFile(filepath.Join(dir, "rendered.yaml"), []byte(rendered), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			again, err := compose.Load(filepath.Join(dir, "rendered.yaml"))
			if err != nil {
				t.Fatalf("the rendered config does not load: %v\n%s", err, rendered)
			}
			rt2, log2 := fakeShim(t)
			if err := orchestrator.New(again, rt2, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatalf("up from the rendered config: %v", err)
			}
			if a, b := strings.Join(runs, "\n"), strings.Join(runLinesOf(log2(), ""), "\n"); a != b {
				t.Errorf("the rendered config runs differently:\n--- input\n%s\n--- rendered\n%s\n--- config\n%s", a, b, rendered)
			}
		})
	}
}

// The same source under the two types is two different mounts: `type: bind`
// is the directory beside the file, `type: volume` the declared volume. The
// short spelling is a path, as docker reads it, even with a declaration of
// that name present. Both orders of the pair, so a reading that took the type
// of one entry for both shows on one of them.
func TestADotSourceIsWhatItsTypeSays(t *testing.T) {
	testpair.Run(t, "bind and volume of one dot name", testpair.Pair[string]{A: "bind", B: "volume"}, func(t *testing.T, first, second string) {
		dir := t.TempDir()
		body := "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: " + first + ", source: .hid, target: /a}\n      - {type: " + second + ", source: .hid, target: /b}\n      - .hid:/c\nvolumes:\n  .hid: {}\n"
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
		joined := strings.Join(runLinesOf(log(), "web.demo.opossum"), "\n")
		bind := "-v " + filepath.Join(dir, ".hid") + ":"
		want := map[string]string{"bind": bind, "volume": "-v demo_.hid:"}
		for typ, target := range map[string]string{first: "/a", second: "/b"} {
			if !strings.Contains(joined, want[typ]+target) {
				t.Errorf("type: %s at %s should run as %q, got:\n%s", typ, target, want[typ]+target, joined)
			}
		}
		if !strings.Contains(joined, bind+"/c") {
			t.Errorf("the short spelling is a path even with a declaration of that name, got:\n%s", joined)
		}
	})
}

// section is the text of a transcript's `== name` section.
func section(transcript, name string) string {
	_, rest, ok := strings.Cut(transcript, "== "+name+"\n")
	if !ok {
		return ""
	}
	text, _, _ := strings.Cut(rest, "\n== ")
	return text
}

func runLinesOf(lines []string, container string) []string {
	var runs []string
	for _, l := range lines {
		if strings.HasPrefix(l, "run ") && strings.Contains(l, container) {
			runs = append(runs, l)
		}
	}
	return runs
}

// A volume whose name starts with `.` behaves as a volume with a plain name
// does, everywhere this can look: the same file with the name swapped for a
// plain one of the same length (`.hid` for `zhid`) must produce the same
// transcript — `up`'s runtime calls and output, `ps`, `volumes` as a table
// and as JSON, a `run --rm` of each service, the overlay `up
// --from-docker-compose` would write, the destroy plan and `down -v` — once
// the name is swapped back. The transcript must name the volume at all
// (`volumes` lists it, unless it is external), so two empty sections never
// agree by saying nothing. (`config` is the one place the two differ on
// purpose: the dot name has no short spelling and is printed in the long
// form. It is looked through for the loader's spelling all the same, and the
// test above runs it again.) That catches the loader's
// spelling however a printer shows it (as bytes, escaped, flattened to spaces,
// or folded into a sanitized name) and a place that treats the dot name
// differently, without listing the places. It cannot see an order: each row
// has one volume, and with two the swap itself would reorder them.
func TestADotNamedVolumeBehavesAsAPlainNamedVolume(t *testing.T) {
	for _, tc := range []struct{ name, dot, plain, body string }{
		{"a declared volume", ".hid", "zhid", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /y}\nvolumes:\n  NAME: {}\n"},
		{"two dots", "..x", "zzx", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /y}\nvolumes:\n  NAME: {}\n"},
		{"read-only and nocopy", ".hid", "zhid", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /y, read_only: true}\n      - {type: volume, source: NAME, target: /z, volume: {nocopy: true}}\nvolumes:\n  NAME: {}\n"},
		{"external", ".hext", "zhext", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /y}\nvolumes:\n  NAME: {external: true}\n"},
		{"a declaration with a name", ".hnamed", "zhnamed", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /y}\nvolumes:\n  NAME: {name: realvol}\n"},
		{"two services share it", ".hid", "zhid", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /y}\n  side:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /z}\nvolumes:\n  NAME: {}\n"},
		{"a postgres data directory", ".pg", "zpg", "services:\n  db:\n    image: postgres:16\n    volumes:\n      - {type: volume, source: NAME, target: /var/lib/postgresql/data}\nvolumes:\n  NAME: {}\n"},
		// A warning that quotes the whole mount rather than the volume's name.
		{"at the Docker socket's path", ".dsock", "zdsock", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: NAME, target: /var/run/docker.sock}\nvolumes:\n  NAME: {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dot, dotConfig := dotTranscript(t, strings.ReplaceAll(tc.body, "NAME", tc.dot))
			plain, _ := dotTranscript(t, strings.ReplaceAll(tc.body, "NAME", tc.plain))
			if compose.ShowsLoaderSpelling(dot + dotConfig) {
				t.Errorf("the loader's spelling of %s reached a person or the runtime:\n%s\n%s", tc.dot, dot, dotConfig)
			}
			// What `volumes` lists: the runtime name, which is the declaration's
			// `name:` when it gives one, and nothing for an external volume.
			listed := "demo_" + tc.plain
			switch {
			case strings.Contains(tc.body, "external"):
				listed = ""
			case strings.Contains(tc.body, "name: realvol"):
				listed = "realvol"
			}
			if listed != "" && !strings.Contains(section(plain, "volumes"), listed) {
				t.Errorf("volumes should list %s, so the comparison has something to compare:\n%s", listed, plain)
			}
			// A host directory the overlay suggests is named after the volume
			// sanitized, which drops the leading dot: that is the one place the
			// swapped name is spelled differently. It is matched exactly, the
			// same number of times on both sides, so a suggestion that stopped
			// sanitizing does not pass as the swap.
			dotDir, plainDir := "./"+compose.SanitizeName(tc.dot)+":", "./"+compose.SanitizeName(tc.plain)+":"
			if a, b := strings.Count(dot, dotDir), strings.Count(plain, plainDir); a != b {
				t.Errorf("the overlay suggests %q %d times for %s but %q %d times for %s", dotDir, a, tc.dot, plainDir, b, tc.plain)
			}
			swapped := strings.ReplaceAll(dot, dotDir, plainDir)
			if swapped = strings.ReplaceAll(swapped, tc.dot, tc.plain); swapped != plain {
				t.Errorf("%s behaves differently from %s:\n--- %s (name swapped)\n%s\n--- %s\n%s", tc.dot, tc.plain, tc.dot, swapped, tc.plain, plain)
			}
		})
	}
}

// dotTranscript loads body as project demo in a fresh directory and records
// what a person or the runtime sees from up, the overlay plan, the destroy
// plan and down -v, with the directory written as {dir}, and config apart.
func dotTranscript(t *testing.T, body string) (transcript, config string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("name: demo\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, log := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	var b strings.Builder
	if err := o.Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	b.WriteString("== up\n" + out.String() + strings.Join(log(), "\n") + "\n")
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"ps", o.Ps},
		{"volumes", func() error { return o.Volumes(nil, orchestrator.VolumesOptions{}) }},
		{"volumes json", func() error { return o.Volumes(nil, orchestrator.VolumesOptions{Format: "json"}) }},
	} {
		out.Reset()
		err := step.run()
		fmt.Fprintf(&b, "== %s\n%s%v\n", step.name, out.String(), err)
	}
	services := make([]string, 0, len(p.Services))
	for name := range p.Services {
		services = append(services, name)
	}
	sort.Strings(services)
	for _, name := range services {
		out.Reset()
		before := len(log())
		err := o.RunOneOff(name, []string{"true"}, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true})
		fmt.Fprintf(&b, "== run %s\n%s%s\n%v\n", name, out.String(), strings.Join(log()[before:], "\n"), err)
	}
	text, changes, gated := o.PlanOverlayFor(nil)
	fmt.Fprintf(&b, "== overlay\n%s\n%+v\n%v\n", text, changes, gated)
	plan, err := o.DestroyPlanFor(false, false, false)
	fmt.Fprintf(&b, "== destroy plan\n%+v %v\n", plan, err)
	out.Reset()
	before := len(log())
	if err := o.Down(true, "", false); err != nil {
		t.Fatalf("down -v: %v", err)
	}
	b.WriteString("== down -v\n" + out.String() + strings.Join(log()[before:], "\n") + "\n")
	rendered, err := compose.RenderConfig(p)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return strings.ReplaceAll(b.String(), dir, "{dir}"), rendered
}
