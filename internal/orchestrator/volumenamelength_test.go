package orchestrator_test

// A volume a service mounts whose name in the runtime is longer than the 255
// characters container 1.4.1 creates (`invalid volume name`) is refused before
// anything is created or started. The name is the user's — the volume's key
// under the project name, its `name:`, an external volume's real name — so it
// is not cut; an anonymous volume's path part is (anonvolume_internal_test.go),
// and only a project and service that leave it no room are refused.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

func volumeNameRefusal(service, name, fix string) string {
	return `service "` + service + `" mounts the volume "` + name + `", ` + itoa(len(name)) + " characters, and the container runtime (1.4.1) creates at most 255 — " + fix
}

func TestAVolumeNameLongerThanTheRuntimeTakesIsRefusedBeforeCreating(t *testing.T) {
	r := strings.Repeat
	keyFix := func(key string) string {
		return `shorten the volume key "` + key + "\" or the project name (`-p`, or `name:` in the compose file)"
	}
	nameFix := func(key string) string { return "shorten the `name:` of the volume \"" + key + `"` }
	type row struct {
		name    string
		web     *compose.Service
		volumes map[string]compose.VolumeDecl
		refusal string // "" goes ahead
	}
	web := func(vols ...string) *compose.Service {
		return &compose.Service{Image: "alpine:3.20", Volumes: compose.Volumes(vols), DependsOn: compose.DependsOn{{Name: "db"}}}
	}
	// demo_<250> is 255, demo_<251> is 256.
	rows := []row{
		{"a key at 255", web(r("k", 250) + ":/data"), map[string]compose.VolumeDecl{r("k", 250): {}}, ""},
		{"a key at 256", web(r("k", 251) + ":/data"), map[string]compose.VolumeDecl{r("k", 251): {}},
			volumeNameRefusal("web", "demo_"+r("k", 251), keyFix(r("k", 251)))},
		{"a name: at 255", web("data:/data"), map[string]compose.VolumeDecl{"data": {Name: r("n", 255)}}, ""},
		{"a name: at 256", web("data:/data"), map[string]compose.VolumeDecl{"data": {Name: r("n", 256)}},
			volumeNameRefusal("web", r("n", 256), nameFix("data"))},
		{"an external name: at 255", web("ext:/data"), map[string]compose.VolumeDecl{"ext": {External: true, Name: r("e", 255)}}, ""},
		// An external volume the runtime does not have is refused for being
		// absent before its name is weighed, as docker compose v5.5.1 refuses
		// it (measured 2026-09-16: a 256-character external name gives
		// `external volume "eee…" not found`, not a word about its length).
		{"an external name: at 256", web("ext:/data"), map[string]compose.VolumeDecl{"ext": {External: true, Name: r("e", 256)}},
			externalVolumeRefusal(r("e", 256))},
		{"an external key at 256", web(r("x", 256) + ":/data"), map[string]compose.VolumeDecl{r("x", 256): {External: true}},
			externalVolumeRefusal(r("x", 256))},
		// The long one second among the service's mounts, after a bind mount and
		// a short named volume.
		{"the second named volume at 256", web("./src:/src", "short:/s", r("k", 251)+":/data"),
			map[string]compose.VolumeDecl{"short": {}, r("k", 251): {}},
			volumeNameRefusal("web", "demo_"+r("k", 251), keyFix(r("k", 251)))},
		// A bind mount's path is not a volume name, however long.
		{"a bind mount of 300", web("/" + r("b", 300) + ":/data"), nil, ""},
	}
	oneOffs := []string{"run", "run --no-deps", "run --audit", "run --audit --no-deps"}
	for _, tc := range rows {
		for _, path := range append([]string{"up"}, oneOffs...) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				// The external volumes this file declares exist, as a user who declares one has made it.
				setShimEnv(rt, "VOLUME_LS=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
				proj := project("demo", map[string]*compose.Service{"db": {Image: "alpine:3.20"}, "web": tc.web})
				proj.Volumes = tc.volumes
				proj.BaseDir = t.TempDir()
				if err := os.Mkdir(filepath.Join(proj.BaseDir, "src"), 0o755); err != nil {
					t.Fatal(err)
				}
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
				if tc.refusal == "" {
					if err != nil || runLine(log()) < 0 {
						t.Errorf("want it run, got err %v and %v", err, log())
					}
					return
				}
				if err == nil || err.Error() != tc.refusal {
					t.Errorf("\n got %v\nwant %s", err, tc.refusal)
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created or started before the refusal, got %q in %v", l, log())
				}
			})
		}
	}
}

// createdSomething returns the first line that creates or starts something: a
// network, a container (a service's or a seed's) or a volume.
func createdSomething(lines []string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, "network create") || strings.HasPrefix(l, "run ") || strings.HasPrefix(l, "create ") || strings.HasPrefix(l, "volume create") {
			return l
		}
	}
	return ""
}

// `up` looks at every service it starts, the long one first or last in the
// startup order, and not at one behind a profile that is not active. A one-off
// refuses its dependency's long volume when the `up` it runs for the dependency
// gets to it, before the dependency starts; with --no-deps the dependency is not
// started and its volume is not looked at.
func TestAVolumeNameIsLookedAtOnTheServicesThatStart(t *testing.T) {
	long := strings.Repeat("k", 251) // demo_<251> is 256
	decl := map[string]compose.VolumeDecl{long: {}}
	refusal := func(service string) string {
		return volumeNameRefusal(service, "demo_"+long, `shorten the volume key "`+long+"\" or the project name (`-p`, or `name:` in the compose file)")
	}
	for _, tc := range []struct{ name, long, other string }{
		{"the long one starts second", "zz", "aa"},
		{"the long one starts first", "aa", "zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			proj := project("demo", map[string]*compose.Service{
				tc.long:  {Image: "alpine:3.20", Volumes: compose.Volumes{long + ":/data"}},
				tc.other: {Image: "alpine:3.20"},
			})
			proj.Volumes = decl
			err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil || err.Error() != refusal(tc.long) {
				t.Errorf("\n got %v\nwant %s", err, refusal(tc.long))
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
	t.Run("a service behind a profile that is not active", func(t *testing.T) {
		rt, log := fakeShim(t)
		proj := project("demo", map[string]*compose.Service{
			"tools": {Image: "alpine:3.20", Profiles: []string{"debug"}, Volumes: compose.Volumes{long + ":/data"}},
			"web":   {Image: "alpine:3.20"},
		})
		proj.Volumes = decl
		if err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil || runLine(log()) < 0 {
			t.Errorf("want web started, got err %v and %v", err, log())
		}
	})
	for _, noDeps := range []bool{false, true} {
		t.Run("a dependency's volume, --no-deps "+map[bool]string{false: "off", true: "on"}[noDeps], func(t *testing.T) {
			rt, log := fakeShim(t)
			proj := project("demo", map[string]*compose.Service{
				"db":  {Image: "alpine:3.20", Volumes: compose.Volumes{long + ":/data"}},
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
			})
			proj.Volumes = decl
			err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: noDeps})
			// Either way: the volume a dependency names is created for it even
			// under --no-deps, which does not start its container, so docker
			// compose v5.5.1 refuses the name in both (measured, #1072). The
			// run looks for itself, so nothing wraps the words.
			if want := refusal("db"); err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
}

// An audited run refuses its one-off's long volume name before it snapshots
// the workspace; one that goes ahead does snapshot it. With --remove-orphans,
// `up` refuses before an orphan is removed.
func TestALongVolumeNameIsRefusedBeforeTheSnapshotAndTheOrphans(t *testing.T) {
	for _, n := range []int{251, 250} { // demo_<n>: 256, 255
		t.Run("audit "+itoa(n), func(t *testing.T) {
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "work"), 0o755); err != nil {
				t.Fatal(err)
			}
			key := strings.Repeat("k", n)
			proj := project("demo", map[string]*compose.Service{
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work", key + ":/data"}},
			})
			proj.Volumes = map[string]compose.VolumeDecl{key: {}}
			proj.BaseDir = base
			rt, log := fakeShim(t)
			_, err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			_, statErr := os.Stat(filepath.Join(base, workspace.SnapshotDirName))
			snapshotted := statErr == nil
			if n == 250 {
				if err != nil || !snapshotted {
					t.Errorf("want the run to go ahead and snapshot the workspace, got err %v, snapshotted %v", err, snapshotted)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), `service "web" mounts the volume "demo_`+key+`", 256 characters`) {
				t.Errorf("want the long volume name refused, got %v", err)
			}
			if snapshotted || runLine(log()) >= 0 {
				t.Errorf("want no snapshot and nothing started before the refusal, got snapshotted %v, %v", snapshotted, log())
			}
		})
	}
	t.Run("orphans", func(t *testing.T) {
		rt, log := fakeShim(t)
		key := strings.Repeat("k", 251)
		setShimEnv(rt, "LS_CONTAINERS=web.demo.opossum old.demo.opossum", "LS_PROJECT=demo")
		proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Volumes: compose.Volumes{key + ":/data"}}})
		proj.Volumes = map[string]compose.VolumeDecl{key: {}}
		o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
		o.SetUpOptions(false, false, false, true, false) // --remove-orphans
		if err := o.Up(true); err == nil || !strings.HasPrefix(err.Error(), `service "web" mounts the volume "demo_`+key+`", 256 characters`) {
			t.Fatalf("want the long volume name refused, got %v", err)
		}
		for _, l := range log() {
			if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
				t.Errorf("want no orphan touched before the refusal, got %v", log())
				break
			}
		}
	})
}

// An anonymous volume's path part is cut to fit, so its name is too long only
// when the project and service leave no room — reachable with no network to
// name (`network_mode: none`) and no DNS domain in the container's name. The
// advice then offers the project and service names.
func TestAnAnonymousVolumeWithNoRoomLeftIsRefused(t *testing.T) {
	long := strings.Repeat("p", 242) // p…_web_ + _<hash> is 256 with the path part empty
	for _, path := range []string{"up", "run"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "INSPECT_PROJECT="+long) // names without a DNS domain: the fake cannot read the project from them
			proj := project(long, map[string]*compose.Service{"web": {Image: "alpine:3.20", NetworkMode: compose.NetworkModeNone, Volumes: compose.Volumes{"/data"}}})
			o := orchestrator.New(proj, rt, "", &bytes.Buffer{})
			var err error
			if path == "up" {
				err = o.Up(true)
			} else {
				err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			name := long + "_web__49f653fe"
			want := volumeNameRefusal("web", name, "the path part is already cut, so shorten the service name \"web\" or the project name (`-p`, or `name:` in the compose file)")
			if err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
	t.Run("one character shorter goes ahead", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT="+long[1:]) // names without a DNS domain: the fake cannot read the project from them
		proj := project(long[1:], map[string]*compose.Service{"web": {Image: "alpine:3.20", NetworkMode: compose.NetworkModeNone, Volumes: compose.Volumes{"/data"}}})
		if err := orchestrator.New(proj, rt, "", &bytes.Buffer{}).Up(true); err != nil || runLine(log()) < 0 {
			t.Errorf("want it run, got err %v and %v", err, log())
		}
	})
}

// An audited one-off looks at the volumes of the services it would create the
// volumes of — its own and its dependencies' — before the snapshot, with or
// without --no-deps: docker compose creates a dependency's named volume either
// way (measured, #1072).
func TestAnAuditedRunLooksAtItsDependenciesVolumes(t *testing.T) {
	long := strings.Repeat("k", 251) // demo_<251> is 256
	for _, noDeps := range []bool{false, true} {
		t.Run("--no-deps "+map[bool]string{false: "off", true: "on"}[noDeps], func(t *testing.T) {
			rt, log := fakeShim(t)
			proj := project("demo", map[string]*compose.Service{
				"db":  {Image: "alpine:3.20", Volumes: compose.Volumes{long + ":/data"}},
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
			})
			proj.Volumes = map[string]compose.VolumeDecl{long: {}}
			_, err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: noDeps})
			want := volumeNameRefusal("db", "demo_"+long, `shorten the volume key "`+long+"\" or the project name (`-p`, or `name:` in the compose file)")
			if err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if runLine(log()) >= 0 {
				t.Errorf("want nothing started, got %v", log())
			}
		})
	}
}

// A volume key starting with `.` (a long-form `type: volume` source) is named in
// the advice as written, not in the loader's own spelling of it.
func TestAVolumeNameRefusalShowsADotKeyAsWritten(t *testing.T) {
	key := "." + strings.Repeat("k", 250) // demo_.<250> is 256
	nm := strings.Repeat("n", 256)
	for _, tc := range []struct {
		name, decl, source, want string
	}{
		{"a key", key + ": {}", key, volumeNameRefusal("web", "demo_"+key, `shorten the volume key "`+key+"\" or the project name (`-p`, or `name:` in the compose file)")},
		{"a name:", ".hn: {name: " + nm + "}", ".hn", volumeNameRefusal("web", nm, "shorten the `name:` of the volume \".hn\"")},
		// An external volume that is not there is refused for being absent
		// before its name is weighed, as docker compose refuses it (#1072).
		{"an external name:", ".he: {external: true, name: " + nm + "}", ".he", externalVolumeRefusal(nm)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "name: demo\nservices:\n  web:\n    image: alpine:3.20\n    volumes:\n      - {type: volume, source: " + tc.source + ", target: /data}\nvolumes:\n  " + tc.decl + "\n"
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := compose.Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			rt, log := fakeShim(t)
			err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil || err.Error() != tc.want {
				t.Errorf("\n got %v\nwant %s", err, tc.want)
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
}

// A volume declared `external: true` whose name the runtime cannot create is
// still refused for its length when the runtime cannot say whether it is there
// — `volume ls` failing is not "the volume is missing", so the check that
// looks for it lets the project through and this one names the length, with
// the way out an external volume has (a shorter `name:`, since opossum never
// creates it).
func TestAnExternalVolumeNameIsRefusedWhenTheRuntimeCannotListVolumes(t *testing.T) {
	long := strings.Repeat("e", 256)
	for _, path := range []string{"up", "run", "run --audit"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "VOLUME_LS_FAIL=1")
			proj := project("demo", map[string]*compose.Service{
				"web": {Image: "alpine:3.20", Volumes: compose.Volumes{"ext:/data"}},
			})
			proj.Volumes = map[string]compose.VolumeDecl{"ext": {External: true, Name: long}}
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			var err error
			switch path {
			case "up":
				err = o.Up(true)
			case "run --audit":
				_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			default:
				err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			want := volumeNameRefusal("web", long, `the runtime cannot mount a volume by that name, so use an external volume with a shorter name for "ext"`)
			if err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
}
