package compose

// `extends:` is not read (#415). Until it was refused by name, a service
// that extended another usually failed as "must set either image or build",
// which reads as a typo in the wrong file. docker compose (v5.5.0) applies
// extends in the well-formed shapes below and refuses the empty ones.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAServiceWithExtendsIsRefusedByName(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{
			"long form, same file",
			"services:\n  base:\n    image: alpine\n  web:\n    extends:\n      service: base\n",
			`service "web" uses extends: (service "base")`,
		},
		{
			"long form, another file",
			"services:\n  web:\n    extends:\n      file: common.yml\n      service: base\n",
			`service "web" uses extends: (service "base" in common.yml)`,
		},
		{
			"short form",
			"services:\n  base:\n    image: alpine\n  web:\n    extends: base\n",
			`service "web" uses extends: (service "base")`,
		},
		{
			// An image of its own does not make the inherited settings appear.
			"with an image of its own",
			"services:\n  base:\n    image: alpine\n    command: sleep 1\n  web:\n    image: alpine\n    extends: base\n",
			`service "web" uses extends:`,
		},
		{
			// Neither shape: still the key's meaning, with nothing to name.
			"a value of no known shape",
			"services:\n  web:\n    image: alpine\n    extends: [base]\n",
			`service "web" uses extends:, which`,
		},
		{
			// The second decoding pass keeps the value as a yaml.Node, in
			// which an alias stays an alias; the name must still come out.
			"through an alias",
			"x-ext: &x {service: base, file: common.yml}\nservices:\n  web:\n    image: alpine\n    extends: *x\n",
			`service "web" uses extends: (service "base" in common.yml)`,
		},
		{
			// Only the file: the message still names it.
			"a file without a service",
			"services:\n  web:\n    image: alpine\n    extends:\n      file: common.yml\n",
			`service "web" uses extends: (a service in common.yml)`,
		},
		{
			// Empty values are still the key (docker refuses them too).
			"nothing after the key",
			"services:\n  web:\n    image: alpine\n    extends:\n",
			`service "web" uses extends:, which`,
		},
		{
			"an empty mapping",
			"services:\n  web:\n    image: alpine\n    extends: {}\n",
			`service "web" uses extends:, which`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in the error, got:\n%s", tc.want, got)
			}
			if strings.Contains(got, "must set either image or build") {
				t.Errorf("the refusal must replace the image message, got:\n%s", got)
			}
			if !strings.Contains(got, "remove extends:") {
				t.Errorf("the refusal should say what to do, got:\n%s", got)
			}
		})
	}
}

// A service *called* extends is a service; the key is only a field one
// level down.
func TestAServiceNamedExtendsIsJustAService(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  extends:\n    image: alpine\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Services["extends"] == nil || p.Services["extends"].Extends != nil {
		t.Errorf("a service named extends should load with no Extends: %+v", p.Services["extends"])
	}
}

// Across -f files the key survives the merge, so an override that adds the
// image the base lacks does not make the extended settings appear either.
func TestExtendsInAnEarlierFileIsStillRefusedAfterAnOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    extends:\n      file: common.yml\n      service: base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{base, over}, nil)
	if err == nil || !strings.Contains(err.Error(), `service "web" uses extends: (service "base" in common.yml)`) {
		t.Errorf("want the extends refusal after the merge, got: %v", err)
	}
}

// With two services at fault, the first by name is the one reported — not
// whichever the map hands out first.
func TestTheFirstServiceByNameIsReported(t *testing.T) {
	for i := 0; i < 20; i++ {
		got := loadErr(t, "services:\n  zzz:\n    build: .\n    extends: aaa\n  aaa:\n    extends: zzz\n  mmm:\n    command: x\n")
		if !strings.Contains(got, `service "aaa" uses extends:`) {
			t.Fatalf("run %d: want aaa reported first, got:\n%s", i, got)
		}
	}
}
