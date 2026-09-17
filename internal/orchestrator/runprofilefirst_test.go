package orchestrator_test

// Without --no-deps, a one-off whose dependency is behind a profile that is not
// active is refused for it before what `run` refuses about the one-off itself,
// once its environment has been read, as docker compose v5.5.1 refuses it
// first (measured with each of these beside it: a missing external volume, a
// volume name the runtime cannot create, a tmpfs option the engine refuses;
// and a service name and a network key docker compose runs, which opossum
// refuses). The one-off's name held by another project's container has no
// docker compose counterpart and is refused after it too. The control is the
// same file without the dependency: the other refusal is what `run` then
// gives.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

func TestADependencyBehindAnInactiveProfileIsRefusedFirst(t *testing.T) {
	longKey := strings.Repeat("k", 251) // demo_<251> is 256
	longName := strings.Repeat("w", 70)
	longNet := strings.Repeat("n", 70)
	for _, tc := range []struct {
		name    string
		service string
		web     string // the one-off's fields beside its image
		tail    string // top-level declarations
		env     string // a fake knob, if any
		other   string // how the other refusal starts
	}{
		{"a missing external volume", "web", "    volumes: [./work:/work, ext:/v]\n", "volumes:\n  ext:\n    external: true\n    name: no-such-vol\n", "", `[OPSM-210] volume "no-such-vol"`},
		{"a volume name the runtime cannot create", "web", "    volumes: [./work:/work, " + longKey + ":/l]\n", "volumes:\n  " + longKey + ": {}\n", "", `service "web" mounts the volume "demo_` + longKey + `"`},
		{"a tmpfs option the engine refuses", "web", "    volumes: [./work:/work]\n    tmpfs: [\"/t:exec,\"]\n", "", "", `service "web" mounts the tmpfs "/t:exec,"`},
		{"a container name the runtime cannot create", longName, "    volumes: [./work:/work]\n", "", "", "container name "},
		{"a network name the runtime cannot create", "web", "    volumes: [./work:/work]\n    networks: [" + longNet + "]\n", "networks:\n  " + longNet + ": {}\n", "", `network name "demo-` + longNet + `"`},
		{"the one-off's name held by another project's container", "web", "    volumes: [./work:/work]\n", "", "INSPECT_UNLABELED=web-run.demo.opossum", `container "web-run.demo.opossum" already exists`},
	} {
		body := func(withDep bool) string {
			b := "services:\n  " + tc.service + ":\n    image: alpine:3.20\n    working_dir: /work\n" + tc.web
			if withDep {
				b += "    depends_on: [db]\n  db:\n    image: alpine:3.20\n    profiles: [off]\n"
			}
			return b + tc.tail
		}
		for _, withDep := range []bool{true, false} {
			for _, audited := range []bool{false, true} {
				name := tc.name + map[bool]string{true: ", beside the dependency", false: ", the control"}[withDep] + map[bool]string{false: "/run", true: "/run --audit"}[audited]
				t.Run(name, func(t *testing.T) {
					rt, log := fakeShim(t)
					if tc.env != "" {
						setShimEnv(rt, tc.env)
					}
					proj := loadTmpfsProject(t, body(withDep))
					o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
					var err error
					if audited {
						_, err = o.RunAudited(tc.service, []string{"true"}, orchestrator.RunOneOffOptions{})
					} else {
						err = o.RunOneOff(tc.service, []string{"true"}, orchestrator.RunOneOffOptions{})
					}
					want := tc.other
					if withDep {
						want = `service "` + tc.service + `" depends on "db", whose profile is not active`
					}
					if err == nil || !strings.HasPrefix(err.Error(), want) {
						t.Errorf("want %s…, got %v", want, err)
					}
					if l := createdSomething(log()); l != "" {
						t.Errorf("want nothing created before the refusal, got %q", l)
					}
					if _, statErr := os.Stat(filepath.Join(proj.BaseDir, workspace.SnapshotDirName)); statErr == nil {
						t.Error("want no workspace snapshot before the refusal")
					}
				})
			}
		}
	}
}
