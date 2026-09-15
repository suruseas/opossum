package compose

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A service's `tmpfs:` and `env_file:` written as a string in one -f file and
// as a string or a list in another merge as lists, as docker compose v5.5.0
// merges them (measured with `config`): the later file's value used to replace
// the earlier one's whenever either side was a string, dropping a mount or a
// file silently.
func TestAStringTmpfsOrEnvFileMergesAsAListAcrossFiles(t *testing.T) {
	for _, tc := range []struct {
		name, base, over string
		tmpfs, env       []string
	}{
		{"tmpfs: string then string", "    tmpfs: /t\n", "    tmpfs: /u\n", []string{"/t", "/u"}, nil},
		{"tmpfs: list then string", "    tmpfs: [/t]\n", "    tmpfs: /u\n", []string{"/t", "/u"}, nil},
		{"tmpfs: string then list", "    tmpfs: /t\n", "    tmpfs: [/u]\n", []string{"/t", "/u"}, nil},
		{"tmpfs: list then list", "    tmpfs: [/t]\n", "    tmpfs: [/u]\n", []string{"/t", "/u"}, nil},
		// Two strings alike up to the first `=` still collapse to the later.
		{"tmpfs: a string's size overridden", "    tmpfs: /t:size=1m\n", "    tmpfs: /t:size=2m\n", []string{"/t:size=2m"}, nil},
		{"env_file: string then string", "    env_file: a.env\n", "    env_file: b.env\n", nil, []string{"A=1", "B=2"}},
		{"env_file: list then string", "    env_file: [a.env]\n", "    env_file: b.env\n", nil, []string{"A=1", "B=2"}},
		{"env_file: string then list", "    env_file: a.env\n", "    env_file: [b.env]\n", nil, []string{"A=1", "B=2"}},
		{"env_file: string then long form", "    env_file: a.env\n", "    env_file: [{path: b.env, required: false}]\n", nil, []string{"A=1", "B=2"}},
		// A string alone in the later file, none in the earlier: the string.
		{"env_file: none then string", "", "    env_file: b.env\n", nil, []string{"B=2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, body := range map[string]string{"a.env": "A=1\n", "b.env": "B=2\n",
				"compose.yaml":  "name: demo\nservices:\n  web:\n    image: alpine\n" + tc.base,
				"override.yaml": "services:\n  web:\n" + tc.over} {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "override.yaml")}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			web := p.Services["web"]
			if tc.tmpfs != nil && !slices.Equal([]string(web.Tmpfs), tc.tmpfs) {
				t.Errorf("want tmpfs %q, got %q", tc.tmpfs, web.Tmpfs)
			}
			if tc.env != nil {
				env, err := web.ResolvedEnv()
				if err != nil {
					t.Fatalf("env: %v", err)
				}
				if !slices.Equal([]string(env), tc.env) {
					t.Errorf("want environment %q, got %q", tc.env, env)
				}
			}
		})
	}
}

// Three files, the string form in the middle: every file's entries are kept,
// in file order.
func TestAStringTmpfsInTheMiddleFileIsKept(t *testing.T) {
	dir := t.TempDir()
	for n, body := range map[string]string{
		"a.yaml": "name: demo\nservices:\n  web:\n    image: alpine\n    tmpfs: [/t]\n",
		"b.yaml": "services:\n  web:\n    tmpfs: /u\n",
		"c.yaml": "services:\n  web:\n    tmpfs: [/v]\n"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadFiles([]string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml"), filepath.Join(dir, "c.yaml")}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{"/t", "/u", "/v"}) {
		t.Errorf("want tmpfs [/t /u /v], got %q", got)
	}
}

// The same when a service extends another: the extending service's string
// joins the base's list (docker compose v5.5.0 merges both, measured), for
// `env_file` too, whose string is a path resolved from the base's file.
func TestAStringTmpfsMergesAsAListThroughExtends(t *testing.T) {
	for _, tc := range []struct{ name, base, over string }{
		{"string then list", "/t", "[/u]"},
		{"list then string", "[/t]", "/u"},
		{"string then string", "/t", "/u"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "name: demo\nservices:\n  base:\n    image: alpine\n    tmpfs: "+tc.base+"\n  web:\n    extends: base\n    tmpfs: "+tc.over+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{"/t", "/u"}) {
				t.Errorf("want tmpfs [/t /u], got %q", got)
			}
		})
	}
	t.Run("env_file: string then list", func(t *testing.T) {
		dir := t.TempDir()
		for n, body := range map[string]string{"a.env": "A=1\n", "b.env": "B=2\n",
			"compose.yaml": "name: demo\nservices:\n  base:\n    image: alpine\n    env_file: a.env\n  web:\n    extends: base\n    env_file: [b.env]\n"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p, err := Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		env, err := p.Services["web"].ResolvedEnv()
		if err != nil {
			t.Fatalf("env: %v", err)
		}
		if !slices.Equal([]string(env), []string{"A=1", "B=2"}) {
			t.Errorf("want environment [A=1 B=2], got %q", env)
		}
	})
}

// `command:` and `entrypoint:` take a string too, but are replaced whole, as
// docker compose replaces them: a string there is a shell line, not a
// one-item list, and the later file's stands alone.
func TestAStringCommandIsReplacedWholeNotJoined(t *testing.T) {
	dir := t.TempDir()
	for n, body := range map[string]string{
		"compose.yaml":  "name: demo\nservices:\n  web:\n    image: alpine\n    command: echo a\n    entrypoint: sh -c\n",
		"override.yaml": "services:\n  web:\n    command: echo b\n    entrypoint: sh -x\n"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "override.yaml")}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if !slices.Equal([]string(web.Command), []string{"echo", "b"}) || !slices.Equal([]string(web.Entrypoint), []string{"sh", "-x"}) {
		t.Errorf("want command [echo b] and entrypoint [sh -x], got %q and %q", web.Command, web.Entrypoint)
	}
}

// A service called `tmpfs`, and a variable called `tmpfs` inside environment,
// are not the field: their values merge as any service's or variable's do.
func TestAServiceOrVariableCalledTmpfsIsNotTheField(t *testing.T) {
	dir := t.TempDir()
	for n, body := range map[string]string{
		"compose.yaml":  "name: demo\nservices:\n  tmpfs:\n    image: alpine\n    environment:\n      tmpfs: one\n",
		"override.yaml": "services:\n  tmpfs:\n    environment:\n      tmpfs: two\n"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "override.yaml")}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := p.Services["tmpfs"]
	if svc == nil || svc.Image != "alpine" || !slices.Equal([]string(svc.Environment), []string{"tmpfs=two"}) {
		t.Errorf("want the service tmpfs with image alpine and environment [tmpfs=two], got %+v", svc)
	}
}
