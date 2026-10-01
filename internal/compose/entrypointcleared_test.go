package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `entrypoint: []` takes the image's own entrypoint away, and a service that writes none leaves
// it (#1620). Entrypoint is empty in both, so EntrypointCleared is what tells them apart. Every
// shape below was measured against docker compose v5.5.1 (`config`): what it prints for the
// service's `entrypoint`, `[]` or nothing or the words.
func TestAnEntrypointWrittenEmptyIsClearedAndAnyOtherIsNot(t *testing.T) {
	const head = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		cleared    bool
		words      []string
	}{
		{"an empty list", head + "    entrypoint: []\n", true, nil},
		{"an empty flow list with a space in it", head + "    entrypoint: [ ]\n", true, nil},
		{"an empty string", head + "    entrypoint: \"\"\n", true, nil},
		{"a string of one space", head + "    entrypoint: \" \"\n", true, nil},
		{"an alias of an empty list", "x-e: &e []\nservices:\n  web:\n    image: alpine\n    entrypoint: *e\n", true, nil},
		{"an interpolation that gives nothing", head + "    entrypoint: ${E_UNSET_1620:-}\n", true, nil},
		{"an empty string with a tag", head + "    entrypoint: !!str \"\"\n", true, nil},
		{"an empty !!binary, which docker compose reads as empty too", head + "    entrypoint: !!binary \"\"\n", true, nil},
		{"an empty list taken in by a merge key", "x-b: &b {entrypoint: []}\nservices:\n  web:\n    image: alpine\n    <<: *b\n", true, nil},
		{"an empty list taken in by a merge key, then written over with words", "x-b: &b {entrypoint: []}\nservices:\n  web:\n    image: alpine\n    <<: *b\n    entrypoint: [echo, A]\n", false, []string{"echo", "A"}},
		{"words taken in by a merge key, then written over with an empty list", "x-b: &b {entrypoint: [echo, A]}\nservices:\n  web:\n    image: alpine\n    <<: *b\n    entrypoint: []\n", true, nil},
		{"a null taken in by a merge key", "x-b: &b {entrypoint: ~}\nservices:\n  web:\n    image: alpine\n    <<: *b\n", false, nil},
		{"a list of one empty string, which is one word", head + "    entrypoint: [\"\"]\n", false, []string{""}},
		{"a null", head + "    entrypoint: ~\n", false, nil},
		{"a null spelt !!null", head + "    entrypoint: !!null\n", false, nil},
		{"no entrypoint", head, false, nil},
		{"a list of words", head + "    entrypoint: [echo, A]\n", false, []string{"echo", "A"}},
		{"a string of words", head + "    entrypoint: echo A\n", false, []string{"echo", "A"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["web"]
			if svc.EntrypointCleared != tc.cleared {
				t.Errorf("EntrypointCleared = %v, want %v (Entrypoint %q)", svc.EntrypointCleared, tc.cleared, []string(svc.Entrypoint))
			}
			if strings.Join(svc.Entrypoint, "\x00") != strings.Join(tc.words, "\x00") {
				t.Errorf("Entrypoint = %q, want %q", []string(svc.Entrypoint), tc.words)
			}
		})
	}
}

// What the later of two files writes wins, as docker compose reads it: an empty entrypoint over
// words clears them, words over an empty one take it back, and a null removes the key.
func TestTheLaterFilesEntrypointDecidesWhetherItIsCleared(t *testing.T) {
	file := func(entry string) string {
		return "services:\n  web:\n    image: alpine\n" + entry
	}
	for _, tc := range []struct {
		name, first, second string
		cleared             bool
		words               string
	}{
		{"[] over words", "    entrypoint: [echo, A]\n", "    entrypoint: []\n", true, ""},
		{"\"\" over words", "    entrypoint: [echo, A]\n", "    entrypoint: \"\"\n", true, ""},
		{"words over []", "    entrypoint: []\n", "    entrypoint: [echo, A]\n", false, "echo A"},
		{"a null over words", "    entrypoint: [echo, A]\n", "    entrypoint: ~\n", false, ""},
		{"a null over []", "    entrypoint: []\n", "    entrypoint: ~\n", false, ""},
		{"a file that writes none, after []", "    entrypoint: []\n", "    command: [x]\n", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, body := range []string{file(tc.first), file(tc.second)} {
				p := filepath.Join(dir, string(rune('a'+i))+".yaml")
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			p, err := LoadFiles(paths, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["web"]
			if svc.EntrypointCleared != tc.cleared || strings.Join(svc.Entrypoint, " ") != tc.words {
				t.Errorf("EntrypointCleared = %v, Entrypoint %q; want %v, %q", svc.EntrypointCleared, []string(svc.Entrypoint), tc.cleared, tc.words)
			}
		})
	}
}

// A service that extends one takes its entrypoint, empty or not, unless it writes its own.
func TestAnEmptyEntrypointIsTakenThroughExtends(t *testing.T) {
	for _, tc := range []struct {
		name, web string
		cleared   bool
		words     string
	}{
		{"inherited", "", true, ""},
		{"written over with words", "    entrypoint: [echo, B]\n", false, "echo B"},
		{"written over with a null", "    entrypoint: ~\n", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  base:\n    image: alpine\n    entrypoint: []\n  web:\n    extends: base\n" + tc.web
			p, err := Load(writeTemp(t, body))
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["web"]
			if svc.EntrypointCleared != tc.cleared || strings.Join(svc.Entrypoint, " ") != tc.words {
				t.Errorf("EntrypointCleared = %v, Entrypoint %q; want %v, %q", svc.EntrypointCleared, []string(svc.Entrypoint), tc.cleared, tc.words)
			}
		})
	}
	t.Run("[] over the words it extends", func(t *testing.T) {
		body := "services:\n  base:\n    image: alpine\n    entrypoint: [echo, A]\n  web:\n    extends: base\n    entrypoint: []\n"
		p, err := Load(writeTemp(t, body))
		if err != nil {
			t.Fatal(err)
		}
		if svc := p.Services["web"]; !svc.EntrypointCleared || len(svc.Entrypoint) != 0 {
			t.Errorf("EntrypointCleared = %v, Entrypoint %q; want it cleared", svc.EntrypointCleared, []string(svc.Entrypoint))
		}
	})
}

// The rendered config shows `entrypoint: []` for a cleared one, as docker compose config does, so
// that what is printed and read back is the same service.
func TestTheRenderedConfigShowsAnEmptyEntrypoint(t *testing.T) {
	const head = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body, want string
		absent           bool
	}{
		{"[]", head + "    entrypoint: []\n", "entrypoint: []", false},
		{"words", head + "    entrypoint: [echo, A]\n", "entrypoint:\n            - echo\n            - A", false},
		{"a null", head + "    entrypoint: ~\n", "entrypoint", true},
		{"none", head, "entrypoint", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			out, err := RenderConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			if tc.absent && strings.Contains(out, tc.want) {
				t.Errorf("the config should not show an entrypoint:\n%s", out)
			}
			if !tc.absent && !strings.Contains(out, tc.want) {
				t.Errorf("the config should show %q:\n%s", tc.want, out)
			}
		})
	}
}
