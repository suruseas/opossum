package orchestrator_test

// A `tmpfs:` entry whose options hold an empty one (`/t:exec,,size=1m`) is
// refused by `up` and `run` before anything is created, as the docker engine
// 29.7.2 refuses the container docker compose v5.5.0 creates for it (`invalid
// tmpfs option ""`); container 1.4.1 would mount it. The file is loaded from
// YAML, so the entries are the ones the loader keeps after the collapse.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

func tmpfsOptionRefusal(service, entry string) string {
	return `service "` + service + `" mounts the tmpfs "` + entry + `", whose options hold an empty one, which the docker engine 29.7.2 refuses (` + "`invalid tmpfs option \"\"`" + `) — drop the extra comma`
}

// loadTmpfsProject loads body as the compose file of a project named demo, in
// a directory holding the `work` directory the audited run snapshots.
func loadTmpfsProject(t *testing.T, body string) *compose.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("name: demo\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

// tmpfsArgs is every `--tmpfs` value the runs in lines pass, in order.
func tmpfsArgs(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "run ") {
			continue
		}
		f := strings.Fields(l)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "--tmpfs" {
				out = append(out, f[i+1])
			}
		}
	}
	return out
}

func TestATmpfsEntryWithAnEmptyOptionIsRefusedBeforeCreating(t *testing.T) {
	web := func(tmpfs string) string {
		return "services:\n  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    tmpfs:" + tmpfs + "\n"
	}
	const d = "nosuid,nodev,noexec"
	type row struct {
		name, body string
		refused    string   // the entry named; "" goes ahead
		passed     []string // what goes ahead passes as --tmpfs
	}
	rows := []row{
		{"two commas together", web(` ["/t:exec,,size=1m"]`), "/t:exec,,size=1m", nil},
		{"only a comma", web(` ["/t:,"]`), "/t:,", nil},
		{"a comma after defaults", web(` ["/t:defaults,"]`), "/t:defaults,", nil},
		{"a comma at the start", web(` ["/t:,exec"]`), "/t:,exec", nil},
		{"a comma at the end", web(` ["/t:exec,"]`), "/t:exec,", nil},
		{"written as one string", web(" /t:exec,,size=1m"), "/t:exec,,size=1m", nil},
		{"the second entry", web(` [/a, "/t:exec,"]`), "/t:exec,", nil},
		{"the first entry", web(` ["/t:exec,", /a]`), "/t:exec,", nil},
		// The later of two entries alike up to the first `=` is the one kept.
		{"kept over an earlier entry at its target", web(` [/t:mode=700, "/t:mode=700,"]`), "/t:mode=700,", nil},
		{"no options after the colon", web(` ["/t:"]`), "", []string{"/t:" + d}},
		{"no colon", web(" [/t]"), "", []string{"/t:" + d}},
		{"options without an empty one", web(` ["/t:exec,size=1m"]`), "", []string{"/t:" + d + ",exec,size=1m"}},
		{"replaced by a later entry at its target", web(` ["/t:mode=700,", /t:mode=755]`), "", []string{"/t:" + d + ",mode=755"}},
		{"a long-form tmpfs mount", "services:\n  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work, {type: tmpfs, target: /t, tmpfs: {size: 1024}}]\n", "",
			[]string{"/t:" + d + ",size=1024"}},
	}
	for _, tc := range rows {
		for _, path := range []string{"up", "run", "run --no-deps", "run --audit", "run --audit --no-deps"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				proj := loadTmpfsProject(t, tc.body)
				o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
				opts := orchestrator.RunOneOffOptions{NoDeps: strings.HasSuffix(path, "--no-deps")}
				var err error
				switch {
				case path == "up":
					err = o.Up(true)
				case strings.HasPrefix(path, "run --audit"):
					_, err = o.RunAudited("web", []string{"true"}, opts)
				default:
					err = o.RunOneOff("web", []string{"true"}, opts)
				}
				_, statErr := os.Stat(filepath.Join(proj.BaseDir, workspace.SnapshotDirName))
				if tc.refused == "" {
					if err != nil || !slices.Equal(tmpfsArgs(log()), tc.passed) {
						t.Errorf("want it run with --tmpfs %q, got err %v and %v", tc.passed, err, log())
					}
					if strings.HasPrefix(path, "run --audit") && statErr != nil {
						t.Errorf("want the audited run to snapshot the workspace, got %v", statErr)
					}
					return
				}
				if want := tmpfsOptionRefusal("web", tc.refused); err == nil || err.Error() != want {
					t.Errorf("\n got %v\nwant %s", err, want)
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created or started before the refusal, got %q in %v", l, log())
				}
				if statErr == nil {
					t.Error("want no workspace snapshot before the refusal")
				}
			})
		}
	}
}

// `up` looks at every service it starts, in startup order, and not at one
// behind a profile that is not active; it refuses before an orphan is
// removed. A one-off refuses its dependency's entry when the `up` it runs for
// the dependency gets to it; with --no-deps the dependency is not looked at.
func TestATmpfsEntryWithAnEmptyOptionIsLookedAtOnTheServicesThatStart(t *testing.T) {
	for _, tc := range []struct{ name, body, refused string }{
		// zdb sorts after web by name and starts before it.
		{"the dependency starts first", "services:\n  web:\n    image: alpine:3.20\n    tmpfs: [\"/w:exec,\"]\n    depends_on: [zdb]\n  zdb:\n    image: alpine:3.20\n    tmpfs: [\"/d:exec,\"]\n", "zdb"},
		{"the dependent one is the only one", "services:\n  web:\n    image: alpine:3.20\n    tmpfs: [\"/w:exec,\"]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n", "web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			err := orchestrator.New(loadTmpfsProject(t, tc.body), rt, "opossum", &bytes.Buffer{}).Up(true)
			entry := map[string]string{"web": "/w:exec,", "zdb": "/d:exec,"}[tc.refused]
			if want := tmpfsOptionRefusal(tc.refused, entry); err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
	t.Run("a service behind a profile that is not active", func(t *testing.T) {
		rt, log := fakeShim(t)
		body := "services:\n  tools:\n    image: alpine:3.20\n    profiles: [debug]\n    tmpfs: [\"/t:exec,\"]\n  web:\n    image: alpine:3.20\n"
		if err := orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil || runLine(log()) < 0 {
			t.Errorf("want web started, got err %v and %v", err, log())
		}
	})
	oneOff := func(o *orchestrator.Orchestrator, path string, noDeps bool) error {
		opts := orchestrator.RunOneOffOptions{NoDeps: noDeps}
		if path == "run --audit" {
			_, err := o.RunAudited("web", []string{"true"}, opts)
			return err
		}
		return o.RunOneOff("web", []string{"true"}, opts)
	}
	const oneOffWeb = "services:\n  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n"
	for _, path := range []string{"run", "run --audit"} {
		for _, noDeps := range []bool{false, true} {
			t.Run(path+", a dependency's entry, --no-deps "+map[bool]string{false: "off", true: "on"}[noDeps], func(t *testing.T) {
				rt, log := fakeShim(t)
				body := oneOffWeb + "  db:\n    image: alpine:3.20\n    tmpfs: [\"/d:exec,\"]\n"
				err := oneOff(orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{}), path, noDeps)
				if noDeps {
					if err != nil || runLine(log()) < 0 {
						t.Errorf("want the one-off run, got err %v and %v", err, log())
					}
					return
				}
				if want := "starting dependencies: " + tmpfsOptionRefusal("db", "/d:exec,"); err == nil || err.Error() != want {
					t.Errorf("\n got %v\nwant %s", err, want)
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created before the refusal, got %q", l)
				}
			})
		}
		// The one-off's own entry is refused before its dependency starts; the
		// entry without the comma is the control, where the dependency does start.
		for _, entry := range []string{"/w:exec,", "/w:exec"} {
			t.Run(path+", its own entry "+entry+" beside a dependency", func(t *testing.T) {
				rt, log := fakeShim(t)
				body := oneOffWeb + "    tmpfs: [\"" + entry + "\"]\n  db:\n    image: alpine:3.20\n"
				err := oneOff(orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{}), path, false)
				dbStarted := slices.ContainsFunc(log(), func(l string) bool {
					return strings.HasPrefix(l, "run ") && strings.Contains(l, "db.demo.opossum")
				})
				if entry == "/w:exec" {
					if err != nil || !dbStarted {
						t.Errorf("want the dependency started and the one-off run, got err %v and %v", err, log())
					}
					return
				}
				if err == nil || err.Error() != tmpfsOptionRefusal("web", entry) {
					t.Errorf("\n got %v\nwant %s", err, tmpfsOptionRefusal("web", entry))
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created before the refusal, got %q in %v", l, log())
				}
			})
		}
	}
	// The entry without the comma is the control: there the orphan is removed,
	// so the refusal is what keeps it.
	for _, entry := range []string{"/t:exec,", "/t:exec"} {
		t.Run("orphans "+entry, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "LS_CONTAINERS=web.demo.opossum old.demo.opossum", "LS_PROJECT=demo")
			o := orchestrator.New(loadTmpfsProject(t, "services:\n  web:\n    image: alpine:3.20\n    tmpfs: [\""+entry+"\"]\n"), rt, "opossum", &bytes.Buffer{})
			o.SetUpOptions(false, false, false, true, false) // --remove-orphans
			err := o.Up(true)
			touched := slices.ContainsFunc(log(), func(l string) bool {
				return (strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ")) && strings.Contains(l, "old.demo.opossum")
			})
			if entry == "/t:exec" {
				if err != nil || !touched {
					t.Errorf("want the orphan removed, got err %v and %v", err, log())
				}
				return
			}
			if err == nil || err.Error() != tmpfsOptionRefusal("web", entry) {
				t.Fatalf("want the entry refused, got %v", err)
			}
			if touched {
				t.Errorf("want no orphan touched before the refusal, got %v", log())
			}
		})
	}
}

// docker compose v5.5.1 refuses a missing external volume before the engine
// refuses a container for its empty tmpfs option, so the volume is named
// first here too; with the volume there, the entry is what is refused. Under
// `up --dry-run` the entry is not refused, as docker compose's dry run
// creates no container for the engine to refuse (measured, rc 0).
func TestATmpfsEntryWithAnEmptyOptionIsRefusedAfterTheExternalVolumes(t *testing.T) {
	const body = "services:\n  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work, ext:/data]\n    tmpfs: [\"/t:exec,\"]\nvolumes:\n  ext:\n    external: true\n    name: real-ext\n"
	for _, path := range []string{"up", "run", "run --audit"} {
		for _, present := range []bool{false, true} {
			t.Run(path+map[bool]string{false: ", the volume missing", true: ", the volume there"}[present], func(t *testing.T) {
				rt, log := fakeShim(t)
				if present {
					setShimEnv(rt, "VOLUME_LS=real-ext")
				}
				o := orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{})
				var err error
				switch path {
				case "up":
					err = o.Up(true)
				case "run":
					err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
				default:
					_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
				}
				want := externalVolumeRefusal("real-ext")
				if present {
					want = tmpfsOptionRefusal("web", "/t:exec,")
				}
				if err == nil || err.Error() != want {
					t.Errorf("\n got %v\nwant %s", err, want)
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created before the refusal, got %q", l)
				}
			})
		}
	}
	// The missing volume one level down: a dependency's, which `run` looks up
	// for the one-off and its dependencies whether or not it starts them, as
	// docker compose v5.5.1 refuses it first there too (measured, with and
	// without --no-deps); and `up`'s, on the service that starts first or last.
	const depVolume = "services:\n  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    tmpfs: [\"/t:exec,\"]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    volumes: [ext:/data]\nvolumes:\n  ext:\n    external: true\n    name: real-ext\n"
	for _, path := range []string{"run", "run --audit"} {
		for _, noDeps := range []bool{false, true} {
			t.Run(path+", a dependency's volume missing, --no-deps "+map[bool]string{false: "off", true: "on"}[noDeps], func(t *testing.T) {
				rt, log := fakeShim(t)
				o := orchestrator.New(loadTmpfsProject(t, depVolume), rt, "opossum", &bytes.Buffer{})
				opts := orchestrator.RunOneOffOptions{NoDeps: noDeps}
				var err error
				if path == "run" {
					err = o.RunOneOff("web", []string{"true"}, opts)
				} else {
					_, err = o.RunAudited("web", []string{"true"}, opts)
				}
				if err == nil || err.Error() != externalVolumeRefusal("real-ext") {
					t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created before the refusal, got %q", l)
				}
			})
		}
	}
	// With no dependency between them, adb starts before web and zdb after it.
	for _, other := range []string{"adb", "zdb"} {
		for _, detach := range []bool{true, false} {
			t.Run("up, the volume missing on "+other+map[bool]string{true: "", false: ", in the foreground"}[detach], func(t *testing.T) {
				rt, log := fakeShim(t)
				body := "services:\n  web:\n    image: alpine:3.20\n    tmpfs: [\"/t:exec,\"]\n  " + other + ":\n    image: alpine:3.20\n    volumes: [ext:/data]\nvolumes:\n  ext:\n    external: true\n    name: real-ext\n"
				err := orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{}).Up(detach)
				if err == nil || err.Error() != externalVolumeRefusal("real-ext") {
					t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created before the refusal, got %q", l)
				}
			})
		}
	}
	// A foreground `up` refuses the entry as a detached one does (docker
	// compose v5.5.1 `up` without -d refuses it too).
	t.Run("up in the foreground", func(t *testing.T) {
		rt, log := fakeShim(t)
		err := orchestrator.New(loadTmpfsProject(t, "services:\n  web:\n    image: alpine:3.20\n    tmpfs: [\"/t:exec,\"]\n"), rt, "opossum", &bytes.Buffer{}).Up(false)
		if err == nil || err.Error() != tmpfsOptionRefusal("web", "/t:exec,") {
			t.Errorf("want the entry refused, got %v", err)
		}
		if l := createdSomething(log()); l != "" {
			t.Errorf("want nothing created before the refusal, got %q", l)
		}
	})
	// The dry run goes ahead and still prints the entry it would pass; the
	// real `up` of the same file is the control.
	for _, dry := range []bool{true, false} {
		t.Run(map[bool]string{true: "up --dry-run", false: "up"}[dry], func(t *testing.T) {
			rt, log := fakeShim(t)
			var out bytes.Buffer
			o := orchestrator.New(loadTmpfsProject(t, "services:\n  web:\n    image: alpine:3.20\n    tmpfs: [\"/t:exec,\"]\n"), rt, "opossum", &out)
			o.SetDryRun(dry)
			err := o.Up(true)
			if !dry {
				if err == nil || err.Error() != tmpfsOptionRefusal("web", "/t:exec,") {
					t.Errorf("want the entry refused, got %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(out.String(), "--tmpfs /t:nosuid,nodev,noexec,exec,") {
				t.Errorf("want the plan printed with the entry, got err %v\n%s", err, out.String())
			}
			// Nothing is said about the entry either, as docker compose's dry run
			// says nothing.
			if strings.Contains(out.String(), "whose options hold an empty one") {
				t.Errorf("want the dry run silent about the entry, got\n%s", out.String())
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created by a dry run, got %q", l)
			}
		})
	}
}
