package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The day a project's name changes, what was made under the old one goes out of
// reach — and COMPOSE_PROJECT_NAME being read is such a day for every directory
// that sets it (#1122). Measured with the version before: `ps` empty, `down`
// stopping nothing while the old container ran on, `up` starting a second
// project over an empty volume beside the one holding the data. What is left is
// found, by what this file accounts for, so it can be pointed at.
func TestWhatIsLeftUnderTheFormerName(t *testing.T) {
	proj := func() *compose.Project {
		p := project("newname", map[string]*compose.Service{
			"web": {Image: "alpine:3"},
			"db":  {Image: "alpine:3"},
		})
		p.Volumes = map[string]compose.VolumeDecl{
			"data":   {},
			"shared": {External: true},
			"fixed":  {Name: "fixedname"},
		}
		return p
	}
	for _, tc := range []struct {
		name           string
		former         string
		env            []string
		wantContainers string
		wantVolumes    string
	}{
		{"a container and a volume under the old name", "dirname",
			[]string{"LS_CONTAINERS=web.dirname.opossum", "LS_PROJECT=dirname", "VOLUME_LS=dirname_data"},
			"web.dirname.opossum", "dirname_data"},
		{"nothing there", "dirname", nil, "", ""},
		// Only what this file accounts for: another service's container under
		// that name is some other project's business.
		{"a container of a service this file does not have", "dirname",
			[]string{"LS_CONTAINERS=cache.dirname.opossum", "LS_PROJECT=dirname"}, "", ""},
		// The name alone is not enough: the label says whose it is.
		{"a container of that name with another project's label", "dirname",
			[]string{"LS_FOREIGN=web.dirname.opossum"}, "", ""},
		{"a container of that name with no label", "dirname",
			[]string{"LS_UNLABELED=web.dirname.opossum"}, "", ""},
		// Called the same whatever the project is: not left anywhere.
		{"an external volume", "dirname", []string{"VOLUME_LS=dirname_shared shared"}, "", ""},
		{"a volume with a name of its own", "dirname", []string{"VOLUME_LS=dirname_fixed fixedname"}, "", ""},
		{"a volume this file does not declare", "dirname",
			[]string{"VOLUME_LS=dirname_other"}, "", ""},
		// Under the current name nothing is "left": it is simply the project.
		{"the current name's own", "dirname",
			[]string{"LS_CONTAINERS=web.newname.opossum", "LS_PROJECT=newname", "VOLUME_LS=newname_data"}, "", ""},
		{"no former name", "", []string{"LS_CONTAINERS=web.dirname.opossum", "LS_PROJECT=dirname"}, "", ""},
		{"the former name is the current one", "newname",
			[]string{"LS_CONTAINERS=web.newname.opossum", "LS_PROJECT=newname", "VOLUME_LS=newname_data"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			o := orchestrator.New(proj(), rt, "opossum", &bytes.Buffer{})
			containers, volumes := o.LeftUnderFormerName(tc.former)
			if got := strings.Join(containers, ","); got != tc.wantContainers {
				t.Errorf("containers = %q, want %q", got, tc.wantContainers)
			}
			if got := strings.Join(volumes, ","); got != tc.wantVolumes {
				t.Errorf("volumes = %q, want %q", got, tc.wantVolumes)
			}
		})
	}
}

// The note names both projects, what is left, and the one way to reach it.
func TestTheFormerNameNote(t *testing.T) {
	if got := orchestrator.FormerNameNote("newname", "dirname", nil, nil, ""); got != "" {
		t.Errorf("nothing left, so nothing to say; got: %q", got)
	}
	got := orchestrator.FormerNameNote("newname", "dirname", []string{"web.dirname.opossum"}, []string{"dirname_data"}, "")
	for _, want := range []string{`"newname"`, `"dirname"`, "COMPOSE_PROJECT_NAME", "web.dirname.opossum", "dirname_data", "`opossum -p dirname down`",
		// What following it to the letter would otherwise leave behind
		// (measured: after `-p <former> down -v`, the image it built).
		"`-v`", "`--rmi local`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the note should carry %s, got: %s", want, got)
		}
	}
	// The command carries the rest of the run's root flags, between the name
	// and the subcommand: a run given `-f sub/x.yaml` is reading a file that
	// `opossum -p dirname down` in the same directory would not (measured: it
	// reads the working directory's file and reaches other names), and
	// `--dns-domain` is in the containers' names.
	withFlags := orchestrator.FormerNameNote("newname", "dirname", []string{"web.dirname.foo"}, nil, " -f sub/x.yaml --dns-domain foo")
	if !strings.Contains(withFlags, "`opossum -p dirname -f sub/x.yaml --dns-domain foo down`") {
		t.Errorf("the command should carry the run's flags, got: %s", withFlags)
	}
	if !strings.Contains(got, "starts \"newname\" with empty ones") {
		t.Errorf("with volumes left, the note should say where the data is; got: %s", got)
	}
	onlyVolumes := orchestrator.FormerNameNote("newname", "dirname", nil, []string{"dirname_data"}, "")
	if strings.Contains(onlyVolumes, "containers (") || !strings.Contains(onlyVolumes, "dirname_data") {
		t.Errorf("only volumes are left, so only volumes are named as left; got: %s", onlyVolumes)
	}
	onlyContainers := orchestrator.FormerNameNote("newname", "dirname", []string{"web.dirname.opossum"}, nil, "")
	if strings.Contains(onlyContainers, "empty ones") || strings.Contains(onlyContainers, "volumes (") {
		t.Errorf("no volume is left, so nothing is said about data; got: %s", onlyContainers)
	}
}
