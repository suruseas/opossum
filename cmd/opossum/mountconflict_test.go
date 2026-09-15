package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two mounts at one target that docker compose v5.5.0 refuses are refused by
// the commands that read the project, in its words, for the services the
// active profiles leave enabled — and not for a gated service enabled by
// naming it, which starts as written (docker compose, measured: `config`,
// `ps`, `down`, `logs`, `stop`, `up web` and `run --no-deps web` all refuse
// a project whose enabled service has such a pair; a service behind an
// inactive profile is not checked; `up extra` and `run extra` naming it are
// not refused). The commands that stop or remove what is running — `down`,
// `destroy`, `stop`, `kill` — name the pair on stderr and go on: docker
// compose never started such a project, but an earlier opossum passed the
// pair on and did, and that project must still come down.
func TestCommandsRefuseMountsDockerComposeRefuses(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	want := "services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]"
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n    tmpfs: [/t]\n    volumes: [\"vv:/t\"]\n  db:\n    image: db\nvolumes:\n  vv: {}\n")
	for _, args := range [][]string{
		{"config"}, {"config", "--services"}, {"ps"}, {"logs"}, {"start"}, {"restart"}, {"up", "db"}, {"run", "--no-deps", "db", "true"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := run(t, append([]string{"-f", compose}, args...)...)
			if err == nil || !strings.HasPrefix(err.Error(), want+"\n") {
				t.Errorf("want %q refused, its words first, got err %v, out:\n%s", want, err, out)
			}
			// The way out: the compose file is what to change, and what an
			// earlier opossum started from it still comes down.
			if err != nil && !strings.Contains(err.Error(), "`opossum down` (with the same -f, -p and --env-file) still takes down what an earlier opossum started from it") {
				t.Errorf("want the way out named, got %v", err)
			}
		})
	}
	// Taking down: named on stderr, and the command goes on to act.
	for _, tc := range []struct {
		args []string
		acts string // a line the fake runtime logs when the command acted
	}{
		{[]string{"down"}, "delete"},
		{[]string{"down", "-v"}, "delete"},
		{[]string{"destroy", "--force"}, "delete"},
		{[]string{"stop"}, "stop"},
		{[]string{"kill"}, "kill"},
	} {
		t.Run("takes down: "+strings.Join(tc.args, " "), func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			stdout, stderr, err := runSplit(t, append([]string{"-f", compose}, tc.args...)...)
			if err != nil {
				t.Fatalf("want the command to go on, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if !strings.Contains(stderr, "opossum: "+want+" — `up` refuses this compose file; going on, as an earlier opossum may have started it") {
				t.Errorf("want the pair named on stderr, got:\n%s", stderr)
			}
			if strings.Contains(stdout, "already mounted") {
				t.Errorf("the note belongs on stderr, got stdout:\n%s", stdout)
			}
			acted := false
			for _, l := range readLog() {
				if strings.HasPrefix(l, tc.acts+" ") && strings.Contains(l, "web.demo.opossum") {
					acted = true
				}
			}
			if !acted {
				t.Errorf("want %q sent for web.demo.opossum, got log:\n%s", tc.acts, strings.Join(readLog(), "\n"))
			}
		})
	}
	// Two services with a pair each: one line per service, in startup order —
	// `aa` depends on `zz`, so `zz` comes first where the names sort the
	// other way.
	both := writeCompose(t, "name: demo\nservices:\n  aa:\n    image: web\n    tmpfs: [/t:mode=700, /t:size=1m]\n    depends_on: [zz]\n  zz:\n    image: db\n    tmpfs: [/u]\n    volumes: [\"./d:/u\"]\n")
	if _, err := run(t, "-f", both, "config"); err == nil || !strings.HasPrefix(err.Error(), "services.zz.volumes[0]: target /u already mounted as services.zz.tmpfs[0]\nservices.aa.tmpfs[1]: target /t already mounted as services.aa.tmpfs[0]\n  change the compose file") {
		t.Errorf("want both services' lines in startup order, got %v", err)
	}
	// Taking down names every pair, a line each, in startup order too.
	for _, args := range [][]string{{"down"}, {"stop"}} {
		t.Run("takes down, two pairs: "+strings.Join(args, " "), func(t *testing.T) {
			fakeShim(t)
			_, stderr, err := runSplit(t, append([]string{"-f", both}, args...)...)
			if err != nil {
				t.Fatalf("want the command to go on, got %v", err)
			}
			zz := strings.Index(stderr, "opossum: services.zz.volumes[0]: target /u already mounted as services.zz.tmpfs[0] — ")
			aa := strings.Index(stderr, "opossum: services.aa.tmpfs[1]: target /t already mounted as services.aa.tmpfs[0] — ")
			if zz < 0 || aa < 0 || zz > aa {
				t.Errorf("want zz's line then aa's on stderr, got:\n%s", stderr)
			}
		})
	}
	// With a cycle, the services are read by name: four pairs, named in name
	// order (several services, so an order a map happened to give would show).
	var cyc strings.Builder
	cyc.WriteString("name: demo\nservices:\n")
	for _, n := range []string{"dd", "bb", "cc", "aa"} {
		next := map[string]string{"dd": "bb", "bb": "cc", "cc": "aa", "aa": "dd"}[n]
		cyc.WriteString("  " + n + ":\n    image: x\n    depends_on: [" + next + "]\n    tmpfs: [/t]\n    volumes: [\"./v:/t\"]\n")
	}
	cycleFour := writeCompose(t, cyc.String())
	for round := 0; round < 5; round++ {
		_, err := run(t, "-f", cycleFour, "config")
		var got []string
		if err != nil {
			for _, l := range strings.Split(err.Error(), "\n") {
				if strings.HasPrefix(l, "services.") {
					got = append(got, strings.SplitN(l, ".", 3)[1])
				}
			}
		}
		if strings.Join(got, " ") != "aa bb cc dd" {
			t.Fatalf("round %d: want the pairs named in name order beside a cycle, got %v (err %v)", round, got, err)
		}
	}
	// A dependency cycle among services an inactive profile gates is not
	// looked at by docker compose, and is not a reason to refuse here: `run
	// --no-deps web` goes on (as on main). With a pair beside the cycle, the
	// pair is still named, the services read by name.
	cycle := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n  aa:\n    image: a\n    profiles: [x]\n    depends_on: [bb]\n  bb:\n    image: b\n    profiles: [x]\n    depends_on: [aa]\n")
	if out, err := run(t, "-f", cycle, "run", "--rm", "--no-deps", "web", "true"); err != nil {
		t.Errorf("a cycle among gated services is not refused by run --no-deps web, got %v, out:\n%s", err, out)
	}
	cyclePair := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n    tmpfs: [/t]\n    volumes: [\"./w:/t\"]\n  aa:\n    image: a\n    profiles: [x]\n    depends_on: [bb]\n  bb:\n    image: b\n    profiles: [x]\n    depends_on: [aa]\n")
	if _, err := run(t, "-f", cyclePair, "ps"); err == nil || !strings.HasPrefix(err.Error(), "services.web.volumes[0]: target /t already mounted as services.web.tmpfs[0]\n") {
		t.Errorf("want the pair named beside a gated cycle, got %v", err)
	}
	// `doctor` reads the project for its memory estimate, which such a pair
	// does not change: the estimate is still there.
	t.Run("doctor still estimates memory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("name: demo\nservices:\n  web:\n    image: web\n    tmpfs: [/t]\n    volumes: [\"./w:/t\"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		out, _ := run(t, "doctor")
		if !strings.Contains(out, "1 services ≈") {
			t.Errorf("want the memory estimate for the project, got:\n%s", out)
		}
	})
	// Gated: not checked until its profile is active; named, it starts as written.
	gated := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n  extra:\n    image: extra\n    profiles: [x]\n    tmpfs: [/t]\n    volumes: [\"./e:/t\"]\n")
	for _, args := range [][]string{{"config", "--services"}, {"ps"}, {"up"}, {"up", "extra"}, {"run", "--no-deps", "extra", "true"}} {
		t.Run("gated: "+strings.Join(args, " "), func(t *testing.T) {
			if out, err := run(t, append([]string{"-f", gated}, args...)...); err != nil {
				t.Errorf("a gated pair is not refused here, got %v, out:\n%s", err, out)
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{"config --profile x", []string{"config", "--services", "--profile", "x"}, ""},
		{"up --profile x", []string{"up", "--profile", "x"}, ""},
		{"COMPOSE_PROFILES=x up", []string{"up"}, "x"},
		{"run --profile x web", []string{"run", "--no-deps", "--profile", "x", "web", "true"}, ""},
	} {
		t.Run("gated, profile active: "+tc.name, func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", tc.env)
			out, err := run(t, append([]string{"-f", gated}, tc.args...)...)
			if want := "services.extra.volumes[0]: target /t already mounted as services.extra.tmpfs[0]\n"; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("want %q refused once the profile is active, its words first, got err %v, out:\n%s", want, err, out)
			}
			// The way out, on this path too.
			if err != nil && !strings.Contains(err.Error(), "`opossum down` (with the same -f, -p and --env-file) still takes down what an earlier opossum started from it") {
				t.Errorf("want the way out named, got %v", err)
			}
		})
	}
}
