package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `$$` in a compose file is the escape for one literal `$`, kept for the
// container's shell (`$${PG_USER}` in a healthcheck, `$$HOME` in a command).
// `config` used to print those values with the escape gone, so its output,
// loaded again, expanded them on the host: an empty user in the healthcheck,
// this machine's home in the command. Docker compose config writes `$$`
// back. Each field that carries such a value has its own case here — a fix
// that escaped one field would pass the others — and the literal `$$`
// (written `$$$$`) is checked to come back as `$$$$`, not one step shorter.
func TestConfigWritesDollarsBackEscaped(t *testing.T) {
	t.Setenv("FOO", "host-foo")
	t.Setenv("HOME", "/host/home")
	src := `name: esc
services:
  db:
    image: postgres:16
    environment:
      - POSTGRES_USER=app
      - PROMPT=$$USER@host
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER} -d $${POSTGRES_DB}"]
    command: ["sh", "-c", "echo $$HOME and $${FOO:-x} and $$$$"]
    entrypoint: ["sh", "-c", "$$0"]
    labels:
      price: "$$100"
      $$key: plain
    user: "$$U"
    working_dir: /srv/$$app
    volumes:
      - ./$$src:/app
    build:
      context: .
      args:
        A: "$$1"
  wait:
    image: alpine:3
    environment:
      - QDRANT_URL=http://qdrant:6333
    command:
      - sh
      - -c
      - |
        i=0
        until wget -qO- -T2 "$$QDRANT_URL/readyz" && echo; do
          i=$$((i+1)); [ $$i -ge 15 ] && break; sleep 2
        done
`
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	// Each want is a whole line (or the whole quoted value), so one escape
	// too many (`$$$$0`) is not taken for the right one by a substring match.
	for name, want := range map[string]string{
		"healthcheck.test":                     "- pg_isready -U $${POSTGRES_USER} -d $${POSTGRES_DB}\n",
		"command":                              "- echo $$HOME and $${FOO:-x} and $$$$\n",
		"entrypoint":                           "- $$0\n",
		"environment value":                    "- PROMPT=$$USER@host\n",
		"labels value":                         "price: $$100\n",
		"shell block: var":                     `"$$QDRANT_URL/readyz"`,
		"shell block: arithmetic and loop var": `i=$$((i+1)); [ $$i -ge 15 ]`,
		// Fields beyond the well-known five: the escape is of the whole
		// document, and a field-by-field rewrite would leave these bare.
		"labels key":    "$$key: plain\n",
		"user":          "user: $$U\n",
		"working_dir":   "working_dir: /srv/$$app\n",
		"volume source": "- ./$$src:/app\n",
		"build arg":     "- A=$$1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(out, want) {
				t.Errorf("want %q written back escaped, got:\n%s", want, out)
			}
		})
	}
	// Nothing of the host leaked into the document.
	for _, leaked := range []string{"/host/home", "host-foo", "-U  -d"} {
		if strings.Contains(out, leaked) {
			t.Errorf("the host's environment must not appear in the config, found %q in:\n%s", leaked, out)
		}
	}

	// And the whole thing is a fixed point: the output, loaded and rendered
	// again, is the same document — which is the property the corpus gate now
	// holds for every real-world file too.
	t.Run("the output is a fixed point of config", func(t *testing.T) {
		again := filepath.Join(dir, "again.yaml")
		if err := os.WriteFile(again, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		p2, err := Load(again)
		if err != nil {
			t.Fatalf("the rendered config should load back: %v", err)
		}
		out2, err := RenderConfig(p2)
		if err != nil {
			t.Fatal(err)
		}
		if doc := configDocument(out); !strings.Contains(doc, "services:") {
			t.Fatalf("the compared document is not the config itself: %q", doc)
		}
		if configDocument(out) != configDocument(out2) {
			t.Errorf("config(config(x)) != config(x):\n--- first\n%s\n--- second\n%s", out, out2)
		}
	})
}
