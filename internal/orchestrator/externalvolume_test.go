package orchestrator_test

// A volume declared `external: true` that a starting service — or a one-off or
// any of its dependencies — mounts, and that the runtime does not have, is
// refused before anything is created or removed (`[OPSM-210]`): container 1.4.1
// would create an empty volume of that name, where docker compose v5.5.0 refuses
// in `up` and `run` alike, and looks at a one-off's dependencies with --no-deps
// too.

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

func externalVolumeRefusal(name string) string {
	return "[OPSM-210] volume \"" + name + "\" is declared `external: true` but doesn't exist — create it first (`container volume create " + name + "`), or remove `external: true` so opossum creates it for the project"
}

func TestAMissingExternalVolumeIsRefusedBeforeTheServiceStarts(t *testing.T) {
	type row struct {
		name     string
		web      *compose.Service
		volumes  map[string]compose.VolumeDecl
		volumeLs string // the runtime's volumes; "FAIL" when it gives no list
		refusal  string // "" goes ahead
		table    string // the runtime's volumes as the header and rows container 1.4.1 prints
	}
	web := func(vols ...string) *compose.Service {
		return &compose.Service{Image: "alpine:3.20", Volumes: compose.Volumes(vols), DependsOn: compose.DependsOn{{Name: "db"}}}
	}
	ext := map[string]compose.VolumeDecl{"ext": {External: true, Name: "real-ext"}, "short": {}}
	rows := []row{
		{"missing, by its name:", web("ext:/data"), ext, "other", externalVolumeRefusal("real-ext"), ""},
		{"there", web("ext:/data"), ext, "other real-ext", "", ""},
		// A name that holds another as a part is not it.
		{"only a longer name is there", web("ext:/data"), ext, "real-ext-2 xreal-ext", externalVolumeRefusal("real-ext"), ""},
		{"missing, by its key", web("keyonly:/data"), map[string]compose.VolumeDecl{"keyonly": {External: true}}, "", externalVolumeRefusal("keyonly"), ""},
		// The missing one after a bind mount, a project volume and an external
		// volume that is there.
		{"the second external volume missing", web("./src:/src", "short:/s", "there:/t", "ext:/data"),
			map[string]compose.VolumeDecl{"ext": {External: true, Name: "real-ext"}, "short": {}, "there": {External: true}}, "there", externalVolumeRefusal("real-ext"), ""},
		// The missing one first, one that is there after it.
		{"an external volume missing before one that is there", web("ext:/data", "there:/t"),
			map[string]compose.VolumeDecl{"ext": {External: true, Name: "real-ext"}, "there": {External: true}}, "there", externalVolumeRefusal("real-ext"), ""},
		// The name is compared as written, as docker compose compares it.
		{"only another case of the name is there", web("ext:/data"), map[string]compose.VolumeDecl{"ext": {External: true, Name: "Real-Ext"}}, "real-ext", externalVolumeRefusal("Real-Ext"), ""},
		// A volume the project makes is the project's to create.
		{"a project volume that is not there yet", web("short:/s"), ext, "", "", ""},
		// A runtime that gives no volume list is not read as "absent".
		{"no list to look in", web("ext:/data"), ext, "FAIL", "", ""},
		// The list as container 1.4.1 prints it: a NAME / TYPE / DRIVER / OPTIONS
		// header first. A volume named NAME is not the header.
		{"a volume named NAME, with the header only", web("hdr:/data"), map[string]compose.VolumeDecl{"hdr": {External: true, Name: "NAME"}}, "", externalVolumeRefusal("NAME"),
			"NAME  TYPE  DRIVER  OPTIONS"},
		{"a volume named NAME, listed", web("hdr:/data"), map[string]compose.VolumeDecl{"hdr": {External: true, Name: "NAME"}}, "", "",
			"NAME  TYPE  DRIVER  OPTIONS\nNAME  named  local"},
		{"missing, with the header", web("ext:/data"), ext, "", externalVolumeRefusal("real-ext"),
			"NAME  TYPE  DRIVER  OPTIONS\nother  named  local"},
	}
	oneOffs := []string{"run", "run --no-deps", "run --audit", "run --audit --no-deps"}
	for _, tc := range rows {
		for _, path := range append([]string{"up"}, oneOffs...) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				switch {
				case tc.volumeLs == "FAIL":
					setShimEnv(rt, "VOLUME_LS_FAIL=1")
				case tc.table != "":
					setShimEnv(rt, "VOLUME_LS_TABLE="+tc.table)
				default:
					setShimEnv(rt, "VOLUME_LS="+tc.volumeLs)
				}
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

// `up` looks at the services it starts, as docker compose does: one behind a
// profile that is not active, and a declaration no service mounts, do not stop
// it. A one-off looks at its dependencies all the way down, --no-deps or not, as
// docker compose v5.5.0 does, before any of them starts.
func TestAMissingExternalVolumeIsLookedForOnTheServicesThatStart(t *testing.T) {
	ext := map[string]compose.VolumeDecl{"ext": {External: true, Name: "real-ext"}}
	t.Run("a service behind a profile that is not active", func(t *testing.T) {
		rt, log := fakeShim(t)
		proj := project("demo", map[string]*compose.Service{
			"tools": {Image: "alpine:3.20", Profiles: []string{"debug"}, Volumes: compose.Volumes{"ext:/data"}},
			"web":   {Image: "alpine:3.20"},
		})
		proj.Volumes = ext
		if err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil || runLine(log()) < 0 {
			t.Errorf("want web started, got err %v and %v", err, log())
		}
	})
	t.Run("a declaration no service mounts", func(t *testing.T) {
		rt, log := fakeShim(t)
		proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}})
		proj.Volumes = ext
		if err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil || runLine(log()) < 0 {
			t.Errorf("want web started, got err %v and %v", err, log())
		}
	})
	for _, tc := range []struct{ name, long, other string }{
		{"the service mounting it starts second", "zz", "aa"},
		{"the service mounting it starts first", "aa", "zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			proj := project("demo", map[string]*compose.Service{
				tc.long:  {Image: "alpine:3.20", Volumes: compose.Volumes{"ext:/data"}},
				tc.other: {Image: "alpine:3.20"},
			})
			proj.Volumes = ext
			err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil || err.Error() != externalVolumeRefusal("real-ext") {
				t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
			}
			if l := createdSomething(log()); l != "" {
				t.Errorf("want nothing created before the refusal, got %q", l)
			}
		})
	}
	// `up web` (and a watch rebuild, which starts a service by name) looks at the
	// services it starts too.
	t.Run("up with a service named", func(t *testing.T) {
		rt, log := fakeShim(t)
		proj := project("demo", map[string]*compose.Service{
			"web":   {Image: "alpine:3.20", Volumes: compose.Volumes{"ext:/data"}},
			"other": {Image: "alpine:3.20"},
		})
		proj.Volumes = ext
		if err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true, "web"); err == nil || err.Error() != externalVolumeRefusal("real-ext") {
			t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
		}
		if l := createdSomething(log()); l != "" {
			t.Errorf("want nothing created before the refusal, got %q", l)
		}
	})
	t.Run("up --dry-run", func(t *testing.T) {
		rt, log := fakeShim(t)
		proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Volumes: compose.Volumes{"ext:/data"}}})
		proj.Volumes = ext
		o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
		o.SetDryRun(true)
		if err := o.Up(true); err == nil || err.Error() != externalVolumeRefusal("real-ext") {
			t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
		}
		if l := createdSomething(log()); l != "" {
			t.Errorf("want nothing created, got %q", l)
		}
	})
	// web -> db -> cache: the volume one or two dependencies down.
	for _, depth := range []string{"db", "cache"} {
		for _, audited := range []bool{false, true} {
			for _, noDeps := range []bool{false, true} {
				name := "a volume on " + depth + ", " + map[bool]string{false: "run", true: "run --audit"}[audited] + map[bool]string{false: "", true: " --no-deps"}[noDeps]
				t.Run(name, func(t *testing.T) {
					rt, log := fakeShim(t)
					svcs := map[string]*compose.Service{
						"web":   {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
						"db":    {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "cache"}}},
						"cache": {Image: "alpine:3.20"},
					}
					svcs[depth].Volumes = compose.Volumes{"ext:/data"}
					proj := project("demo", svcs)
					proj.Volumes = ext
					o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
					opts := orchestrator.RunOneOffOptions{NoDeps: noDeps}
					var err error
					if audited {
						_, err = o.RunAudited("web", []string{"true"}, opts)
					} else {
						err = o.RunOneOff("web", []string{"true"}, opts)
					}
					if err == nil || err.Error() != externalVolumeRefusal("real-ext") {
						t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
					}
					if l := createdSomething(log()); l != "" {
						t.Errorf("want nothing created before the refusal, got %q in %v", l, log())
					}
				})
			}
		}
	}
	// With --remove-orphans, `up` refuses before an orphan is removed, as
	// docker compose v5.5.0 leaves the orphan running.
	t.Run("orphans", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "LS_CONTAINERS=web.demo.opossum old.demo.opossum", "LS_PROJECT=demo")
		proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Volumes: compose.Volumes{"ext:/data"}}})
		proj.Volumes = ext
		o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
		o.SetUpOptions(false, false, false, true, false) // --remove-orphans
		if err := o.Up(true); err == nil || err.Error() != externalVolumeRefusal("real-ext") {
			t.Fatalf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
		}
		for _, l := range log() {
			if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
				t.Errorf("want no orphan touched before the refusal, got %v", log())
				break
			}
		}
	})
}

// An audited run refuses its one-off's missing external volume before it
// snapshots the workspace; with the volume there it snapshots and runs.
func TestAnAuditedRunRefusesAMissingExternalVolumeBeforeTheSnapshot(t *testing.T) {
	for _, there := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "there"}[there], func(t *testing.T) {
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "work"), 0o755); err != nil {
				t.Fatal(err)
			}
			proj := project("demo", map[string]*compose.Service{
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work", "ext:/data"}},
			})
			proj.Volumes = map[string]compose.VolumeDecl{"ext": {External: true, Name: "real-ext"}}
			proj.BaseDir = base
			rt, log := fakeShim(t)
			if there {
				setShimEnv(rt, "VOLUME_LS=real-ext")
			}
			_, err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			_, statErr := os.Stat(filepath.Join(base, workspace.SnapshotDirName))
			snapshotted := statErr == nil
			if there {
				if err != nil || !snapshotted {
					t.Errorf("want the run to go ahead and snapshot the workspace, got err %v, snapshotted %v", err, snapshotted)
				}
				return
			}
			if err == nil || err.Error() != externalVolumeRefusal("real-ext") {
				t.Errorf("\n got %v\nwant %s", err, externalVolumeRefusal("real-ext"))
			}
			if snapshotted || runLine(log()) >= 0 {
				t.Errorf("want no snapshot and nothing started before the refusal, got snapshotted %v, %v", snapshotted, log())
			}
		})
	}
}

// A missing external network is said before a missing external volume, and
// both before an orphan is removed, as docker compose v5.5.0 refuses them
// (`network … declared as external, but could not be found` first).
func TestAMissingExternalNetworkIsSaidFirstAndBeforeTheOrphans(t *testing.T) {
	proj := func(volumeToo bool) *compose.Project {
		web := &compose.Service{Image: "alpine:3.20", Networks: compose.ServiceNetworks{"proxy"}}
		if volumeToo {
			web.Volumes = compose.Volumes{"ext:/data"}
		}
		p := project("demo", map[string]*compose.Service{"web": web})
		p.Networks = map[string]compose.NetworkDecl{"proxy": {External: true}}
		p.Volumes = map[string]compose.VolumeDecl{"ext": {External: true, Name: "real-ext"}}
		return p
	}
	for _, volumeToo := range []bool{true, false} {
		t.Run(map[bool]string{true: "network and volume missing", false: "network missing"}[volumeToo], func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "NETWORK_ABSENT=proxy", "LS_CONTAINERS=web.demo.opossum old.demo.opossum", "LS_PROJECT=demo")
			o := orchestrator.New(proj(volumeToo), rt, "opossum", &bytes.Buffer{})
			o.SetUpOptions(false, false, false, true, false) // --remove-orphans
			err := o.Up(true)
			if err == nil || !strings.HasPrefix(err.Error(), "[OPSM-205] network \"proxy\"") {
				t.Fatalf("want the missing network said, got %v", err)
			}
			for _, l := range log() {
				if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
					t.Errorf("want no orphan touched before the refusal, got %v", log())
					break
				}
			}
		})
	}
}

// A declared volume named NAME is looked for below the header of the runtime's
// volume list, not in it: a new one is prepared and filled from the image like
// any other new volume, and one that is there is left alone.
func TestAVolumeNamedNAMEIsSeededWhenNew(t *testing.T) {
	for _, tc := range []struct {
		name, table string
		seeded      bool
	}{
		{"new", "NAME  TYPE  DRIVER  OPTIONS", true},
		{"there", "NAME  TYPE  DRIVER  OPTIONS\nNAME  named  local", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "VOLUME_LS_TABLE="+tc.table)
			p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Volumes: compose.Volumes{"d:/x"}}})
			p.Volumes = map[string]compose.VolumeDecl{"d": {Name: "NAME"}}
			if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			if got := indexOf(log(), "-v NAME:/__opossum_seed__") >= 0; got != tc.seeded {
				t.Errorf("want seeded %v, got %v in %v", tc.seeded, got, log())
			}
		})
	}
}
