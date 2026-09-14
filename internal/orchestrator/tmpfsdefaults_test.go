package orchestrator_test

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// A tmpfs mount gets docker's default options, `nosuid,nodev,noexec`, put
// before the options the file writes, so an `exec`, `suid` or `dev` written
// there still wins (the later option wins, on the docker engine and on
// container 1.4.1). Checked on each path that starts a container with the
// mounts: `up`, `run` and `run --audit`.
func TestTmpfsMountsGetDockerDefaultOptions(t *testing.T) {
	paths := []struct {
		name  string
		start func(o *orchestrator.Orchestrator) error
		// runLine picks the line of the container that carries the mounts.
		runLine string
	}{
		{"up", func(o *orchestrator.Orchestrator) error { return o.Up(true) }, "run -d --name web.demo.opossum"},
		{"run", func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
		}, "run -i --name web-run.demo.opossum"},
		{"run --audit", func(o *orchestrator.Orchestrator) error {
			_, err := o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			return err
		}, "run -i --name web-run.demo.opossum"},
	}
	cases := []struct {
		name  string
		tmpfs []string
		want  []string
	}{
		{"no options", []string{"/t"}, []string{"--tmpfs /t:nosuid,nodev,noexec"}},
		{"options written", []string{"/t:exec,mode=700"}, []string{"--tmpfs /t:nosuid,nodev,noexec,exec,mode=700"}},
		{"an empty option list", []string{"/t:"}, []string{"--tmpfs /t:nosuid,nodev,noexec"}},
		{"options only on the second", []string{"/a", "/b:size=1m"}, []string{"--tmpfs /a:nosuid,nodev,noexec", "--tmpfs /b:nosuid,nodev,noexec,size=1m"}},
		{"options only on the first, out of order", []string{"/b:suid,dev", "/a"}, []string{"--tmpfs /b:nosuid,nodev,noexec,suid,dev", "--tmpfs /a:nosuid,nodev,noexec"}},
		// Split at the first `:`: an option may hold one (`mpol=bind:0`, which
		// container 1.4.1 mounts; split at the last, it fails with errno 22).
		{"an option holding a colon", []string{"/t:mpol=bind:0"}, []string{"--tmpfs /t:nosuid,nodev,noexec,mpol=bind:0"}},
		// `defaults` is nothing to the docker engine and fails the mount on
		// container 1.4.1, alone or among other options; `DEFAULTS` is not it.
		{"defaults alone", []string{"/t:defaults"}, []string{"--tmpfs /t:nosuid,nodev,noexec"}},
		{"defaults among options", []string{"/t:exec,defaults,mode=700,defaults"}, []string{"--tmpfs /t:nosuid,nodev,noexec,exec,mode=700"}},
		{"defaults only in the middle", []string{"/t:exec,defaults,mode=700"}, []string{"--tmpfs /t:nosuid,nodev,noexec,exec,mode=700"}},
		{"DEFAULTS", []string{"/t:DEFAULTS"}, []string{"--tmpfs /t:nosuid,nodev,noexec,DEFAULTS"}},
		// Spellings that are not the word, which the engine and the runtime both
		// refuse, are passed as written (one with a space, which the shim's log
		// cannot show apart, is in tmpfsmounts_internal_test.go).
		{"defaults=1", []string{"/t:defaults=1"}, []string{"--tmpfs /t:nosuid,nodev,noexec,defaults=1"}},
		{"nodefaults", []string{"/t:nodefaults"}, []string{"--tmpfs /t:nosuid,nodev,noexec,nodefaults"}},
		{"defaultsx", []string{"/t:defaultsx"}, []string{"--tmpfs /t:nosuid,nodev,noexec,defaultsx"}},
		// The same target twice is passed twice, in the order written.
		{"the same target twice", []string{"/t:exec", "/t"}, []string{"--tmpfs /t:nosuid,nodev,noexec,exec", "--tmpfs /t:nosuid,nodev,noexec"}},
	}
	for _, p := range paths {
		for _, tc := range cases {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				rt, log := fakeShim(t)
				proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Tmpfs: tc.tmpfs}})
				if err := p.start(orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})); err != nil {
					t.Fatalf("want it to start, got %v", err)
				}
				line := ""
				for _, l := range log() {
					if strings.HasPrefix(l, p.runLine) {
						line = l
					}
				}
				if line == "" {
					t.Fatalf("want a %q line, got %v", p.runLine, log())
				}
				if got := regexp.MustCompile(`--tmpfs \S+`).FindAllString(line, -1); strings.Join(got, " | ") != strings.Join(tc.want, " | ") {
					t.Errorf("want %v, got %v in %q", tc.want, got, line)
				}
			})
		}
	}
}

// The defaults are part of what the container is made with, so a container
// with a tmpfs mount made before them is made again on the next `up` (its
// mount changes), and one with no tmpfs mount keeps the hash it had — an
// upgrade does not recreate it. The goldens are what `up` gave the same
// service before the defaults.
func TestTmpfsDefaultsChangeTheConfigHashOnlyWhereThereIsATmpfsMount(t *testing.T) {
	const before = "27c821d56c1f31fb"          // no tmpfs mount, before the defaults
	const beforeWithTmpfs = "2c92a5d386a0d2a0" // `tmpfs: [/t]`, before the defaults
	hashOf := func(t *testing.T, tmpfs []string) string {
		t.Helper()
		rt, _ := fakeShim(t)
		proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Tmpfs: tmpfs}})
		if err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatal(err)
		}
		return rawConfigHash(t, rt)
	}
	for _, tc := range []struct {
		name  string
		tmpfs []string
	}{{"none", nil}, {"empty", []string{}}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hashOf(t, tc.tmpfs); got != before {
				t.Errorf("a service with no tmpfs mount must keep its hash (%s), got %s", before, got)
			}
		})
	}
	t.Run("a tmpfs mount", func(t *testing.T) {
		if got := hashOf(t, []string{"/t"}); got == beforeWithTmpfs || got == before {
			t.Errorf("a tmpfs mount now made with the defaults must hash differently from before (%s), got %s", beforeWithTmpfs, got)
		}
	})
}

// rawConfigHash reads the config hash of the one `run -d` in the shim's log,
// which the fakeShim reader strips.
func rawConfigHash(t *testing.T, rt *runtime.Runtime) string {
	t.Helper()
	var logPath string
	for _, e := range rt.Env {
		if v, ok := strings.CutPrefix(e, "FAKE_LOG="); ok {
			logPath = v
		}
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "run -d") {
			if m := regexp.MustCompile(`config-hash=([0-9a-f]+)`).FindStringSubmatch(l); m != nil {
				hashes = append(hashes, m[1])
			}
		}
	}
	if len(hashes) != 1 {
		t.Fatalf("want one run -d with a config hash, got %v", hashes)
	}
	return hashes[0]
}
