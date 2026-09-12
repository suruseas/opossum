package orchestrator_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The docker oracle's file: web mounts a named volume, an external one and a
// bind directory; db a named one; both mount a third named volume; a fifth is
// declared and never mounted. Only the three named volumes are the project's,
// and the shared one is one volume, not two.
func volumesProject() *compose.Project {
	p := project("demo", map[string]*compose.Service{
		"web": {Volumes: []string{"d1:/d1", "ext:/ext", "./bind:/bind", "shared:/shared"}},
		"db":  {Volumes: []string{"d2:/d2", "shared:/shared"}},
	})
	p.Volumes = map[string]compose.VolumeDecl{
		"d1": {}, "d2": {}, "shared": {}, "unused": {},
		"ext": {External: true, Name: "shared-external"},
	}
	return p
}

// What `container volume ls` shows after both services have started: the
// project's volumes under their runtime names, listed out of name order and
// with different drivers (so the driver is seen to be read, not assumed); the
// external volume; another project's volume; an anonymous one; and two rows
// that carry the project's prefix but are not the file's — `demo_unused` is
// declared and never mounted (the runtime could still hold one from an older
// file), `demo_stale` is one no service mounts any more. Neither is listed:
// the listing is what the file's services mount, not what bears the name.
const volumeTable = "NAME                                  TYPE       DRIVER  OPTIONS\n" +
	"demo_d2                               named      other\n" +
	"demo_stale                            named      local\n" +
	"demo_shared                           named      local\n" +
	"demo_d1                               named      local\n" +
	"demo_unused                           named      local\n" +
	"shared-external                       named      local\n" +
	"other_d1                              named      local\n" +
	"53f55c9f-57c1-40da-bebc-6ba37f66d917  anonymous  local"

func TestProjectVolumesListsWhatExistsOfWhatServicesMount(t *testing.T) {
	for _, tc := range []struct {
		name     string
		table    string
		services []string
		want     []orchestrator.VolumeStatus
	}{
		{"both services started, the shared volume once", volumeTable, nil, []orchestrator.VolumeStatus{
			{Name: "demo_d1", Driver: "local"}, {Name: "demo_d2", Driver: "other"}, {Name: "demo_shared", Driver: "local"},
		}},
		{"one service named", volumeTable, []string{"web"}, []orchestrator.VolumeStatus{
			{Name: "demo_d1", Driver: "local"}, {Name: "demo_shared", Driver: "local"},
		}},
		{"both services named, the shared volume still once", volumeTable, []string{"web", "db"}, []orchestrator.VolumeStatus{
			{Name: "demo_d1", Driver: "local"}, {Name: "demo_d2", Driver: "other"}, {Name: "demo_shared", Driver: "local"},
		}},
		{"before anything started, nothing exists yet", "NAME  TYPE  DRIVER  OPTIONS", nil, []orchestrator.VolumeStatus{}},
		{"only db started so far", "NAME  TYPE  DRIVER  OPTIONS\ndemo_d2  named  local", nil, []orchestrator.VolumeStatus{
			{Name: "demo_d2", Driver: "local"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "VOLUME_LS_TABLE="+tc.table)
			got, err := orchestrator.New(volumesProject(), rt, "opossum", &bytes.Buffer{}).ProjectVolumes(tc.services)
			if err != nil {
				t.Fatalf("ProjectVolumes: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("services %v: got %v, want %v", tc.services, got, tc.want)
			}
		})
	}
	t.Run("a service the project does not define", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "VOLUME_LS_TABLE="+volumeTable)
		_, err := orchestrator.New(volumesProject(), rt, "opossum", &bytes.Buffer{}).ProjectVolumes([]string{"nope"})
		if err == nil || !strings.Contains(err.Error(), `unknown service "nope"`) {
			t.Errorf("want the unknown-service error, got: %v", err)
		}
	})
	t.Run("an anonymous volume is the project's too", func(t *testing.T) {
		// Its runtime name is derived from the service and the target; rather
		// than restate that rule, start the service and let the shim record the
		// volume `up` made, which it then lists as the runtime would.
		rt, _ := fakeShim(t)
		setShimEnv(rt, "VOLUME_LS_TABLE=NAME  TYPE  DRIVER  OPTIONS")
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Volumes: []string{"/cache"}}})
		o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		got, err := o.ProjectVolumes(nil)
		if err != nil {
			t.Fatalf("ProjectVolumes: %v", err)
		}
		if len(got) != 1 || !strings.HasPrefix(got[0].Name, "demo_web_") || got[0].Driver != "local" {
			t.Errorf("want the one anonymous volume up made, named for the project and service, got %v", got)
		}
	})
}

func TestVolumesRendersTheThreeForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts orchestrator.VolumesOptions
		want string
	}{
		{"a DRIVER / VOLUME NAME table", orchestrator.VolumesOptions{Format: "table"},
			"DRIVER  VOLUME NAME\nlocal   demo_d1\nother   demo_d2\nlocal   demo_shared\n"},
		{"names only under -q", orchestrator.VolumesOptions{Quiet: true, Format: "table"}, "demo_d1\ndemo_d2\ndemo_shared\n"},
		{"-q wins over --format json", orchestrator.VolumesOptions{Quiet: true, Format: "json"}, "demo_d1\ndemo_d2\ndemo_shared\n"},
		{"a JSON array under --format json", orchestrator.VolumesOptions{Format: "json"},
			`[{"Name":"demo_d1","Driver":"local"},{"Name":"demo_d2","Driver":"other"},{"Name":"demo_shared","Driver":"local"}]` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "VOLUME_LS_TABLE="+volumeTable)
			var out bytes.Buffer
			if err := orchestrator.New(volumesProject(), rt, "opossum", &out).Volumes(nil, tc.opts); err != nil {
				t.Fatalf("Volumes: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", out.String(), tc.want)
			}
		})
	}
}

func TestVolumesWithNothingToShow(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts orchestrator.VolumesOptions
		want string
	}{
		{"the table keeps its header", orchestrator.VolumesOptions{Format: "table"}, "DRIVER  VOLUME NAME\n"},
		{"-q prints nothing", orchestrator.VolumesOptions{Quiet: true, Format: "table"}, ""},
		{"json is an empty array, not null", orchestrator.VolumesOptions{Format: "json"}, "[]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "VOLUME_LS_TABLE=NAME  TYPE  DRIVER  OPTIONS")
			var out bytes.Buffer
			if err := orchestrator.New(volumesProject(), rt, "opossum", &out).Volumes(nil, tc.opts); err != nil {
				t.Fatalf("Volumes: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("got %q, want %q", out.String(), tc.want)
			}
		})
	}
	t.Run("a stopped runtime is reported, not shown as no volumes", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "VOLUME_LS_TABLE="+volumeTable, "SYSTEM_STOPPED=1")
		var out bytes.Buffer
		err := orchestrator.New(volumesProject(), rt, "opossum", &out).Volumes(nil, orchestrator.VolumesOptions{Format: "table"})
		if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
			t.Errorf("want the runtime-stopped error (OPSM-405), got: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("nothing may be printed when the runtime is down, got %q", out.String())
		}
	})
}
