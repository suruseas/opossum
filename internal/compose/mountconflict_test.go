package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Two mounts at one target that docker compose v5.5.0 refuses are recorded
// at load, in its words, for the orchestrator to refuse once it knows the
// profiles; pairs it collapses or reads as two targets are not. Each row is
// a docker compose `config --services` run (measured): the message, or none.
func TestMountConflictsAreRecordedAsDockerComposeRefusesThem(t *testing.T) {
	for _, tc := range []struct {
		name, service string
		want          []string
	}{
		// Refused there.
		{"two tmpfs entries not alike up to the first =", "tmpfs: [/t:mode=700, /t:size=1m]",
			[]string{"services.web.tmpfs[1]: target /t already mounted as services.web.tmpfs[0]"}},
		{"tmpfs then a bind at its target", "tmpfs: [/t]\n    volumes: [\"./bdir:/t\"]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"a bind then tmpfs at its target: reported against the tmpfs side", "volumes: [\"./bdir:/t\"]\n    tmpfs: [/t]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"tmpfs then a named volume at its target", "tmpfs: [/t]\n    volumes: [\"vv:/t\"]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"tmpfs then a long-form tmpfs at its target", "tmpfs: [/t]\n    volumes: [{type: tmpfs, target: /t}]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"tmpfs with options then a bind at its target", "tmpfs: [/t:size=1m]\n    volumes: [\"./bdir:/t\"]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"the volumes index is the one after the collapse", "tmpfs: [/t]\n    volumes: [\"./bdir:/t\", \"./bdir:/u\", \"vv:/t\"]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"a bind at /t/ beside tmpfs /t: the volume's trailing slash is dropped", "tmpfs: [/t]\n    volumes: [\"./bdir:/t/\"]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"}},
		{"a tmpfs pair and a bind at the target: the first pair alone, as docker compose reports one", "tmpfs: [/a:mode=700, /a:size=1m]\n    volumes: [\"./bdir:/a\"]",
			[]string{"services.web.tmpfs[1]: target /a already mounted as services.web.tmpfs[0]"}},
		// Positions, measured: the pair's indices are where the entries sit
		// after the collapse, the tmpfs side's the first at that target.
		{"a bind at volumes[1] after two binds at another target collapse", "tmpfs: [/t]\n    volumes: [\"./bdir:/u\", \"vv:/u\", \"./bdir:/t\"]",
			[]string{"services.web.volumes[1]: target /t already mounted as services.web.tmpfs[0]"}},
		{"the tmpfs side at tmpfs[1]", "tmpfs: [/a, /t]\n    volumes: [\"./bdir:/t\"]",
			[]string{"services.web.volumes[0]: target /t already mounted as services.web.tmpfs[1]"}},
		{"a tmpfs pair two apart", "tmpfs: [/a:mode=700, /b, /a:size=1m]",
			[]string{"services.web.tmpfs[2]: target /a already mounted as services.web.tmpfs[0]"}},
		{"a tmpfs pair after another entry: the earlier one's index", "tmpfs: [/b, /a:mode=700, /a:size=1m]",
			[]string{"services.web.tmpfs[2]: target /a already mounted as services.web.tmpfs[1]"}},
		{"of two pairs in volumes, the first in volumes order", "tmpfs: [/t, /u]\n    volumes: [\"./bdir:/u\", \"./bdir:/t\"]",
			[]string{"services.web.volumes[0]: target /u already mounted as services.web.tmpfs[1]"}},
		{"collapsed before: the bind's index counts the collapsed entries once", "tmpfs: [/t]\n    volumes: [\"./bdir:/u\", \"./bdir:/u\", \"./bdir:/v\", \"./bdir:/t\"]",
			[]string{"services.web.volumes[2]: target /t already mounted as services.web.tmpfs[0]"}},
		// Not refused there.
		{"two tmpfs entries alike up to the first = collapse", "tmpfs: [/t:size=1m, /t:size=2m]", nil},
		{"tmpfs /t and /t/ are two targets there, compared as written", "tmpfs: [/t, /t/]", nil},
		{"tmpfs /t/ beside a bind at /t: not the same target there", "tmpfs: [/t/]\n    volumes: [\"./bdir:/t\"]", nil},
		{"a bind and a long-form tmpfs among volumes collapse", "volumes: [\"./bdir:/t\", {type: tmpfs, target: /t}]", nil},
		{"tmpfs and a bind at another target", "tmpfs: [/t]\n    volumes: [\"./bdir:/u\"]", nil},
		{"a service with no mounts", "command: [sleep, \"1\"]", nil},
		// No target is nothing to compare: an empty tmpfs entry beside a bind
		// written with an empty target (docker compose refuses the bind's
		// spelling itself, `invalid spec: ./a:: empty section between colons`;
		// what opossum says about either is not this check's to decide).
		{"an empty tmpfs target beside a bind with an empty target", "tmpfs: [\"\"]\n    volumes: [\"./a:\"]", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "name: demo\nservices:\n  web:\n    image: alpine\n    " + tc.service + "\nvolumes:\n  vv: {}\n"
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].MountConflicts("web"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MountConflicts = %q, want %q", got, tc.want)
			}
		})
	}
}
