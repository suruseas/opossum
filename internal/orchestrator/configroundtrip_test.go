package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/testpair"
)

// `config`'s output, run, must mean what the input meant. The textual fixed
// point (the corpus gate) cannot tell: a declaration the output drops is
// dropped again on the second pass, and the diff is empty. So the comparison
// here is of what `up` asks the runtime to do — every `run` line, the seeds
// included — from the input and from its rendered config, written into the
// same directory so relative paths point where they did (the shim's log reader
// already strips the config-hash label, which follows the file's bytes).
//
// The volumes come as a pair in both orders (testpair): with one volume a
// rewrite that namespaced everything, or seeded everything, would look right.
func TestConfigOutputRunsAsTheInputDid(t *testing.T) {
	runLines := func(t *testing.T, dir, file string) []string {
		t.Helper()
		rt, log := fakeShim(t)
		// The external volumes this file declares exist, as a user who declares one has made it.
		setShimEnv(rt, "VOLUME_LS=real-name-outside")
		p, err := compose.Load(filepath.Join(dir, file))
		if err != nil {
			t.Fatalf("load %s: %v", file, err)
		}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("up from %s: %v", file, err)
		}
		var runs []string
		for _, l := range log() {
			if strings.HasPrefix(l, "run ") {
				runs = append(runs, l)
			}
		}
		return runs
	}
	// A shipped example, the one whose `$$` the round trip used to break (#935):
	// a fixed point textually, and now the same runs. Copied into a temp dir so
	// the rendered file lands beside it without touching the repository.
	t.Run("examples/local-ai-stack runs the same from its rendered config", func(t *testing.T) {
		src, err := os.ReadFile(filepath.Join("..", "..", "examples", "local-ai-stack", "compose.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), src, 0o644); err != nil {
			t.Fatal(err)
		}
		original := runLines(t, dir, "compose.yaml")
		if len(original) == 0 {
			t.Fatal("the example started nothing, so this compares nothing")
		}
		p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := compose.RenderConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rendered.yaml"), []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
		// The input is gone before the second run, so a run that read it again
		// (the same lines from a second place) fails to load instead of passing.
		if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
			t.Fatal(err)
		}
		if again := runLines(t, dir, "rendered.yaml"); strings.Join(original, "\n") != strings.Join(again, "\n") {
			t.Errorf("the rendered example runs differently:\n--- input\n%s\n--- rendered\n%s", strings.Join(original, "\n"), strings.Join(again, "\n"))
		}
	})

	// A project volume's `name:` is read by `up` and written by `config` from
	// the same change (#937): a drop of the name from the output now shows here,
	// on the run, where before it could only show on the rendered text.
	testpair.Run(t, "named and bare project volumes", testpair.Pair[string]{A: "named:/b", B: "bare:/a"}, func(t *testing.T, first, second string) {
		dir := t.TempDir()
		compose1 := "name: decl\nservices:\n  db:\n    image: postgres:16\n    volumes: [\"" + first + "\", \"" + second + "\"]\nvolumes:\n  bare:\n  named: {name: my-custom}\n"
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose1), 0o644); err != nil {
			t.Fatal(err)
		}
		original := runLines(t, dir, "compose.yaml")
		p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := compose.RenderConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rendered.yaml"), []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
			t.Fatal(err)
		}
		again := runLines(t, dir, "rendered.yaml")
		if strings.Join(original, "\n") != strings.Join(again, "\n") {
			t.Errorf("the rendered config runs differently from the input:\n--- input\n%s\n--- rendered\n%s\n--- config\n%s",
				strings.Join(original, "\n"), strings.Join(again, "\n"), rendered)
		}
		joined := strings.Join(again, "\n")
		for _, want := range []string{"-v my-custom:/b", "-v decl_bare:/a"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the rendered config's run should carry %q, got:\n%s", want, joined)
			}
		}
		if strings.Contains(joined, "decl_named") {
			t.Errorf("the declared name must survive the round trip, got:\n%s", joined)
		}
	})

	// A long-form tmpfs mount's `read_only`, `tmpfs.size` and `tmpfs.mode` are
	// written by `config` as the short form's options, and run the same.
	testpair.Run(t, "long-form tmpfs mounts with options", testpair.Pair[string]{
		A: "{type: tmpfs, target: /a, read_only: true, tmpfs: {mode: 0700}}",
		B: "{type: tmpfs, target: /b, tmpfs: {size: 2m}}",
	}, func(t *testing.T, first, second string) {
		dir := t.TempDir()
		body := "name: decl\nservices:\n  web:\n    image: alpine:3.20\n    volumes:\n      - " + first + "\n      - " + second + "\n"
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		original := runLines(t, dir, "compose.yaml")
		p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := compose.RenderConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rendered.yaml"), []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
			t.Fatal(err)
		}
		again := runLines(t, dir, "rendered.yaml")
		if strings.Join(original, "\n") != strings.Join(again, "\n") {
			t.Errorf("the rendered config runs differently from the input:\n--- input\n%s\n--- rendered\n%s\n--- config\n%s",
				strings.Join(original, "\n"), strings.Join(again, "\n"), rendered)
		}
		joined := strings.Join(again, "\n")
		for _, want := range []string{"--tmpfs /a:nosuid,nodev,noexec,ro,mode=700", "--tmpfs /b:nosuid,nodev,noexec,size=2097152"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the rendered config's run should carry %q, got:\n%s", want, joined)
			}
		}
	})

	testpair.Run(t, "external and bare volumes", testpair.Pair[string]{A: "bare:/a", B: "ext:/c"}, func(t *testing.T, first, second string) {
		dir := t.TempDir()
		for name, body := range map[string]string{
			"pg_password.txt": "secret\n",
			"api_key.txt":     "key\n",
			"cf.txt":          "cf\n",
			".env":            "PROBE_CF=value-from-env\n",
			"compose.yaml": "name: decl\nservices:\n  db:\n    image: postgres:16\n    volumes: [\"" + first + "\", \"" + second + "\"]\n" +
				"    secrets:\n      - source: pg_password\n        target: pw\n      - api_key\n" +
				"    configs:\n      - source: app_cf\n        target: /etc/app/cf.txt\n      - app_cf\n      - source: app_cf\n        target: /x/app_cf\n      - inline\n      - fromenv\n" +
				"volumes:\n  bare:\n  ext: {external: true, name: real-name-outside}\n" +
				"secrets:\n  pg_password: {file: ./pg_password.txt}\n  api_key: {file: ./api_key.txt}\n" +
				"configs:\n  app_cf: {file: ./cf.txt}\n  inline: {content: \"user=$$HOME\\n\"}\n  fromenv: {environment: PROBE_CF}\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		original := runLines(t, dir, "compose.yaml")
		p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := compose.RenderConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rendered.yaml"), []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
		// Same guard as above: the second run cannot be the input read twice.
		if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
			t.Fatal(err)
		}
		again := runLines(t, dir, "rendered.yaml")
		if strings.Join(original, "\n") != strings.Join(again, "\n") {
			t.Errorf("the rendered config runs differently from the input:\n--- input\n%s\n--- rendered\n%s\n--- config\n%s",
				strings.Join(original, "\n"), strings.Join(again, "\n"), rendered)
		}
		// The three meanings the declarations carry, named so a wrong run reads
		// as what it is rather than a diff: the external volume is mounted by its
		// real name and never seeded; the secret and the config are mounted.
		joined := strings.Join(again, "\n")
		// The external volume is mounted by the name its declaration gives — the
		// one `up` already reads, so the output has to carry it.
		for _, want := range []string{"-v real-name-outside:/c", "-v decl_bare:/a", ":/run/secrets/pw:ro", ":/run/secrets/api_key:ro", ":/app_cf:ro", ":/etc/app/cf.txt:ro", ":/x/app_cf:ro", ":/inline:ro", ":/fromenv:ro"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the rendered config's run should carry %q, got:\n%s", want, joined)
			}
		}
		if strings.Contains(joined, "real-name-outside:/__opossum_seed__") || strings.Contains(joined, "decl_ext") || strings.Contains(joined, "-v ext:/c") {
			t.Errorf("the external volume must stay external — not namespaced, not seeded — got:\n%s", joined)
		}
	})
}
