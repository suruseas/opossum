package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layered writes proj/sub/x.yaml (and what else the row names) and loads it the
// way a file COMPOSE_FILE chose in another directory is loaded: the working
// directory's `.env` first, the file's own directory's under it.
func layered(t *testing.T, files map[string]string, paths ...string) (*Project, error) {
	t.Helper()
	proj := filepath.Join(t.TempDir(), "proj")
	for name, body := range files {
		p := filepath.Join(proj, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if body == "<dir>" {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if body == "<broken link>" {
			if err := os.Symlink("nowhere", p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if body == "<link to /dev/null>" {
			if err := os.Symlink(os.DevNull, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if body == "<link to a dir>" {
			if err := os.Mkdir(p+".d", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(p)+".d", p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	abs := make([]string, len(paths))
	for i, p := range paths {
		abs[i] = filepath.Join(proj, p)
	}
	paths = abs
	for _, v := range []string{"V", "W", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES"} {
		withoutHostVar(t, v)
	}
	return LoadFilesEnvDir(paths, nil, proj)
}

const layeredFile = "services:\n  a:\n    image: \"alpine:${V:-unset}\"\n"

// The second `.env` is read by the load, so what is wrong with it is the load's
// to say — as docker compose says it (measured on v5.5.1: a file it cannot
// open and a line it cannot read are refused, for every command). Before it was
// read at all, such a file was passed over without a word.
func TestASecondDotEnvThatCannotBeReadIsRefused(t *testing.T) {
	t.Run("a line that is not KEY=VALUE", func(t *testing.T) {
		_, err := layered(t, map[string]string{"sub/x.yaml": layeredFile, "sub/.env": "GARBAGE LINE\n"}, "sub/x.yaml")
		if err == nil || !strings.Contains(err.Error(), filepath.Join("sub", ".env")) {
			t.Errorf("want the load refused naming the file, got: %v", err)
		}
	})
	t.Run("a file that cannot be opened", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root opens anything")
		}
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "x.yaml"), []byte(layeredFile), 0o644); err != nil {
			t.Fatal(err)
		}
		env := filepath.Join(dir, "sub", ".env")
		if err := os.WriteFile(env, []byte("V=x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		withoutHostVar(t, "V")
		// Readable, it loads — so the refusal below is the file's doing.
		if _, err := LoadFilesEnvDir([]string{filepath.Join(dir, "sub", "x.yaml")}, nil, dir); err != nil {
			t.Fatalf("readable, this loads: %v", err)
		}
		if err := os.Chmod(env, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(env, 0o644) })
		if _, err := LoadFilesEnvDir([]string{filepath.Join(dir, "sub", "x.yaml")}, nil, dir); err == nil || !strings.Contains(err.Error(), env) {
			t.Errorf("want the load refused naming the file, got: %v", err)
		}
	})
}

// A `.env` that is a directory is no `.env`: docker compose reads on as if it
// were not there, for either of the two (measured), and so through a link. An
// `--env-file` that is a directory is another matter — it was asked for by
// name — and is refused.
func TestADotEnvThatIsADirectoryIsNoDotEnv(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"the working directory's", map[string]string{".env": "<dir>"}, "unset"},
		{"the compose file's directory's", map[string]string{"sub/.env": "<dir>"}, "unset"},
		// No `.env` is not the end of the reading: the other one still counts
		// (docker compose, measured: with the working directory's a directory,
		// the value comes from the compose file's directory's).
		{"the working directory's, and the other one says", map[string]string{".env": "<dir>", "sub/.env": "V=subv\n"}, "subv"},
		{"the compose file's directory's, and the other one says", map[string]string{".env": "V=cwdv\n", "sub/.env": "<dir>"}, "cwdv"},
		// What is a directory through a link is a directory (measured).
		{"the working directory's, through a link", map[string]string{".env": "<link to a dir>", "sub/.env": "V=subv\n"}, "subv"},
		{"the compose file's directory's, through a link", map[string]string{"sub/.env": "<link to a dir>"}, "unset"},
		// …and so is one that leads nowhere, or to nothing: the other file counts
		// (measured; opossum used to take these for an empty `.env` and stop).
		{"the working directory's, a link to nowhere", map[string]string{".env": "<broken link>", "sub/.env": "V=subv\n"}, "subv"},
		{"the working directory's, a link to /dev/null", map[string]string{".env": "<link to /dev/null>", "sub/.env": "V=subv\n"}, "subv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"sub/x.yaml": layeredFile}
			for k, v := range tc.files {
				files[k] = v
			}
			p, err := layered(t, files, "sub/x.yaml")
			if err != nil {
				t.Fatalf("want it loaded as with no .env there, got: %v", err)
			}
			if got := p.Services["a"].Image; got != "alpine:"+tc.want {
				t.Errorf("image = %q, want alpine:%s", got, tc.want)
			}
		})
	}
	t.Run("an --env-file is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "x.yaml"), []byte(layeredFile), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "ef.env"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFiles([]string{filepath.Join(dir, "x.yaml")}, []string{filepath.Join(dir, "ef.env")}); err == nil || !strings.Contains(err.Error(), "ef.env") {
			t.Errorf("an --env-file that is a directory should be refused, naming it; got: %v", err)
		}
	})
}

// With several files, the second `.env` is the first file's directory's — the
// project's directory — and not the last's (docker compose, measured both ways
// round: `sub/x.yaml:other/y.yaml` reads sub's, `other/y.yaml:sub/x.yaml` other's).
func TestTheSecondDotEnvIsTheFirstFilesDirectorys(t *testing.T) {
	files := map[string]string{
		"sub/x.yaml":   layeredFile,
		"other/y.yaml": "services:\n  b:\n    image: \"alpine:${W:-unset}\"\n",
		"sub/.env":     "V=fromsub\nW=fromsub\n",
		"other/.env":   "V=fromother\nW=fromother\n",
	}
	for _, tc := range []struct {
		name  string
		paths []string
		want  string
	}{
		{"sub first", []string{"sub/x.yaml", "other/y.yaml"}, "fromsub"},
		{"other first", []string{"other/y.yaml", "sub/x.yaml"}, "fromother"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := layered(t, files, tc.paths...)
			if err != nil {
				t.Fatal(err)
			}
			if a, b := p.Services["a"].Image, p.Services["b"].Image; a != "alpine:"+tc.want || b != "alpine:"+tc.want {
				t.Errorf("images = %q, %q; want both from %s", a, b, tc.want)
			}
		})
	}
}

// What the second `.env` fills is what the `.env` files and the shell leave
// unset — the built-in OPOSSUM_HOST_GATEWAY ranks below every `.env`, the
// second included, so a project that sets it there has its own value.
func TestTheSecondDotEnvIsOverTheBuiltIn(t *testing.T) {
	stubHostGateway(t, "192.0.2.1")
	p, err := layered(t, map[string]string{
		"sub/x.yaml": "services:\n  a:\n    image: \"alpine:${" + hostGatewayVar + "}\"\n",
		"sub/.env":   hostGatewayVar + "=1.2.3.4\n",
	}, "sub/x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services["a"].Image; got != "alpine:1.2.3.4" {
		t.Errorf("image = %q, want the second .env's 1.2.3.4 over the built-in", got)
	}
}
