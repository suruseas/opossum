package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `entrypoint: []` takes the image's ENTRYPOINT away (#1620). `container run` cannot say so: on
// 1.5.0 an empty `--entrypoint` leaves the image's in place, and the joined `--entrypoint=` with
// no value takes the image's place on the line (`library/echo/manifests/latest … 401`). What does
// replace it, measured on 1.5.0 against an image whose entrypoint ignores its arguments, is
// `--entrypoint=<word>` — so the command's first word becomes the entrypoint, and with no command
// the first word of the image's CMD.

// clearedShim answers `image inspect` with the given CMD (or fails for an image that is not here
// until `image pull` has run), `image pull` with success, and logs every other call.
type clearedShim struct {
	rt   *Runtime
	read func() []string
}

func newClearedShim(t *testing.T, cmdJSON string, here bool) clearedShim {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	pulled := filepath.Join(dir, "pulled")
	if here {
		if err := os.WriteFile(pulled, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inspect := `[{"variants":[{"config":{"config":{"Cmd":` + cmdJSON + `}}}]}]`
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + logPath + "\n" +
		"if [ \"$1 $2\" = \"image inspect\" ]; then\n" +
		"  if [ -e " + pulled + " ]; then echo '" + inspect + "'; exit 0; fi\n" +
		"  echo 'Error: image not found' >&2; exit 1\n" +
		"fi\n" +
		"if [ \"$1 $2\" = \"image pull\" ]; then : > " + pulled + "; exit 0; fi\n" +
		"exit 0\n"
	shim := filepath.Join(dir, "container")
	writeShimFile(t, shim, script)
	return clearedShim{
		rt: &Runtime{Bin: shim},
		read: func() []string {
			b, err := os.ReadFile(logPath)
			if err != nil {
				return nil
			}
			return splitLines(string(b))
		},
	}
}

func (s clearedShim) runLine() string {
	for _, l := range s.read() {
		if strings.HasPrefix(l, "run ") {
			return l
		}
	}
	return ""
}

func (s clearedShim) called(prefix string) bool {
	for _, l := range s.read() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestAClearedEntrypointRunsTheCommandInItsPlace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cmdJSON    string // the image's CMD
		here       bool   // the image is already pulled
		options    RunOptions
		wantRun    string
		wantInspec bool // the image's config was asked for
		wantPull   bool
		wantErr    string
	}{
		{
			name: "the service's command, whatever the image's CMD", cmdJSON: `["nginx"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", EntrypointCleared: true, Command: []string{"echo", "hi", "there"}},
			wantRun: "run --name w --entrypoint=echo img hi there",
		},
		{
			name: "no command: the image's CMD, one word", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", EntrypointCleared: true},
			wantRun: "run --name w --entrypoint=postgres img", wantInspec: true,
		},
		{
			name: "no command: the image's CMD, several words", cmdJSON: `["nginx","-g","daemon off;"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", EntrypointCleared: true},
			wantRun: "run --name w --entrypoint=nginx img -g daemon off;", wantInspec: true,
		},
		{
			name: "no command, and the image is not here yet: it is pulled to read its CMD", cmdJSON: `["postgres"]`, here: false,
			options: RunOptions{Name: "w", Image: "img", EntrypointCleared: true},
			wantRun: "run --name w --entrypoint=postgres img", wantInspec: true, wantPull: true,
		},
		{
			name: "no command and the image is made by a build here: it is never pulled, and one that cannot be read is told as it is", cmdJSON: `["postgres"]`, here: false,
			options:    RunOptions{Name: "w", Image: "img", EntrypointCleared: true, ImageIsBuilt: true},
			wantInspec: true, wantErr: "has no command that can be read",
		},
		{
			name: "no command and an image with no CMD", cmdJSON: `null`, here: true,
			options:    RunOptions{Name: "w", Image: "img", EntrypointCleared: true},
			wantInspec: true, wantErr: "neither the service nor img names a command",
		},
		{
			name: "an entrypoint the service writes wins over the mark", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", EntrypointCleared: true, Entrypoint: []string{"/app/run", "--serve"}, Command: []string{"-c"}},
			wantRun: "run --name w --entrypoint=/app/run img --serve -c",
		},
		{
			name: "an entrypoint of one empty word is read as cleared: the command in its place", cmdJSON: `["nginx"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{""}, Command: []string{"echo", "hi"}},
			wantRun: "run --name w --entrypoint=echo img hi",
		},
		{
			name: "an entrypoint of one empty word and no command is refused, as docker refuses it, whatever the image CMD", cmdJSON: `["postgres","-c","x"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{""}},
			wantErr: "has no `command:` to run instead",
		},
		{
			name: "an entrypoint of one empty word and a command whose first word is empty is refused", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{""}, Command: []string{"", "x"}},
			wantErr: "that word is empty", // the same refusal `[]` has
		},
		{
			name: "an entrypoint of one word is not cleared", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{"x"}, Command: []string{"y"}},
			wantRun: "run --name w --entrypoint=x img y",
		},
		{
			name: "an entrypoint of one word and no command is run as it is", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{"x"}},
			wantRun: "run --name w --entrypoint=x img",
		},
		{
			name: "an entrypoint of one empty word and a command of one word", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{""}, Command: []string{"echo"}},
			wantRun: "run --name w --entrypoint=echo img",
		},
		{
			name: "an entrypoint of two words whose last is empty is not the one-empty-word form", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{"x", ""}, Command: []string{"y"}},
			wantRun: "run --name w --entrypoint=x img  y",
		},
		{
			name: "an entrypoint of two words whose first is empty is not the one-empty-word form (the line it gives is a known fault, #1651)", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img", Entrypoint: []string{"", "x"}, Command: []string{"y"}},
			wantRun: "run --name w --entrypoint= img x y",
		},
		{
			name: "not cleared: the image is not asked", cmdJSON: `["postgres"]`, here: true,
			options: RunOptions{Name: "w", Image: "img"},
			wantRun: "run --name w img",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newClearedShim(t, tc.cmdJSON, tc.here)
			err := s.rt.Run(tc.options)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Run = %v, want an error naming %q", err, tc.wantErr)
				}
				if s.runLine() != "" {
					t.Errorf("a run was made although there is nothing to run: %q", s.runLine())
				}
			} else if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tc.wantRun != "" && s.runLine() != tc.wantRun {
				t.Errorf("run line\n got: %q\nwant: %q", s.runLine(), tc.wantRun)
			}
			if got := s.called("image inspect"); got != tc.wantInspec {
				t.Errorf("the image's config asked for = %v, want %v", got, tc.wantInspec)
			}
			if got := s.called("image pull"); got != tc.wantPull {
				t.Errorf("the image pulled = %v, want %v", got, tc.wantPull)
			}
		})
	}
}

// An image that cannot be read even after it was pulled is told as it is, and nothing is run:
// starting the image's own ENTRYPOINT is what the service asked not to have.
func TestAClearedEntrypointDoesNotRunWhenTheImageCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	shim := filepath.Join(dir, "container")
	writeShimFile(t, shim, "#!/bin/sh\necho \"$*\" >> "+logPath+"\nif [ \"$1 $2\" = \"image inspect\" ]; then exit 1; fi\nexit 0\n")
	rt := &Runtime{Bin: shim}
	err := rt.Run(RunOptions{Name: "w", Image: "img", EntrypointCleared: true})
	if err == nil || !strings.Contains(err.Error(), "has no command that can be read") {
		t.Fatalf("Run = %v, want an error that the image's command cannot be read", err)
	}
	b, _ := os.ReadFile(logPath)
	for _, l := range splitLines(string(b)) {
		if strings.HasPrefix(l, "run ") {
			t.Errorf("a run was made: %q", l)
		}
	}
}

// ImageCmd reads the CMD the way ImageEnv reads the environment: the first variant that declares
// one answers, an image that declares none answers with nothing and ok, and one that is not here is
// not ok.
func TestImageCmdReadsWhatTheImageDeclares(t *testing.T) {
	if cmd, ok := imageInspectShim(t, "../../testdata/image-inspect/postgres18.json").ImageCmd("postgres"); !ok || strings.Join(cmd, " ") != "postgres" {
		t.Errorf("ImageCmd(postgres18) = %q, %v; want [postgres], true", cmd, ok)
	}
	if cmd, ok := imageInspectShim(t, "").ImageCmd("nope"); ok || cmd != nil {
		t.Errorf("ImageCmd of an image that is not here = %q, %v; want nil, false", cmd, ok)
	}
}

// A plan does not pull, so an image that is not here cannot be asked for its CMD: that is no fault
// of the file, and `up --dry-run` plans the run (with the pull before it) instead of refusing.
func TestADryRunPlansAClearedEntrypointOfAnImageThatIsNotHereYet(t *testing.T) {
	s := newClearedShim(t, `["postgres"]`, false)
	s.rt.DryRun = true
	if err := s.rt.Run(RunOptions{Name: "w", Image: "img", EntrypointCleared: true}); err != nil {
		t.Fatalf("Run in a dry run: %v", err)
	}
	if s.runLine() != "" {
		t.Errorf("a dry run made a run: %q", s.runLine())
	}
	pull, run := -1, -1
	for i, l := range s.rt.Plan {
		if l == "image pull img" {
			pull = i
		}
		if strings.HasPrefix(l, "run ") && strings.Contains(l, "--entrypoint=<the command of img> img") {
			run = i
		}
	}
	if run < 0 {
		t.Errorf("the plan should hold the run, got %q", s.rt.Plan)
	}
	if pull < 0 || pull > run {
		t.Errorf("the plan should pull img before the run (pull at %d, run at %d), got %q", pull, run, s.rt.Plan)
	}
}

// The image of a service with `build:` is made by the build that comes before the run, and no
// registry has it: the plan holds the run with the placeholder and no pull of it (#1634).
func TestADryRunDoesNotPlanAPullOfAnImageABuildMakes(t *testing.T) {
	s := newClearedShim(t, `["postgres"]`, false)
	s.rt.DryRun = true
	if err := s.rt.Run(RunOptions{Name: "w", Image: "img", EntrypointCleared: true, ImageIsBuilt: true}); err != nil {
		t.Fatalf("Run in a dry run: %v", err)
	}
	var run bool
	for _, l := range s.rt.Plan {
		if strings.HasPrefix(l, "image pull") {
			t.Errorf("the plan pulls an image a build makes: %q", l)
		}
		if strings.HasPrefix(l, "run ") && strings.Contains(l, "--entrypoint=<the command of img> img") {
			run = true
		}
	}
	if !run {
		t.Errorf("the plan should hold the run, got %q", s.rt.Plan)
	}
}

// The first word of the command becomes the entrypoint, and `--entrypoint=` with no value takes
// the image's place on the line (container 1.5.0): an empty first word is refused, not passed on.
func TestAClearedEntrypointRefusesACommandWhoseFirstWordIsEmpty(t *testing.T) {
	s := newClearedShim(t, `["postgres"]`, true)
	err := s.rt.Run(RunOptions{Name: "w", Image: "img", EntrypointCleared: true, Command: []string{"", "x"}})
	if err == nil || !strings.Contains(err.Error(), "that word is empty") {
		t.Fatalf("Run = %v, want a refusal of the empty word", err)
	}
	if s.runLine() != "" {
		t.Errorf("a run was made: %q", s.runLine())
	}
}

// An image has one config per platform and an attestation without a CMD between them (what
// `image inspect` answers for a multi-platform image): the first variant that declares a CMD
// answers, not the first variant and not the last.
func TestImageCmdReadsTheFirstVariantThatDeclaresOne(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "inspect.json")
	body := `[{"variants":[
		{"config":{"config":{}}},
		{"config":{"config":{"Cmd":["first"]}}},
		{"config":{"config":{}}},
		{"config":{"config":{"Cmd":["last"]}}}]}]`
	if err := os.WriteFile(fixture, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd, ok := imageInspectShim(t, fixture).ImageCmd("multi")
	if !ok || strings.Join(cmd, " ") != "first" {
		t.Errorf("ImageCmd = %q, %v; want [first], true", cmd, ok)
	}
}
