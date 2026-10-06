package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An infinity or a NaN in a service that another file's service extends is refused where it stays in what the project
// takes, and not where it is written over or reset: docker compose cannot write it into the model it checks, and the
// model it checks is the one after the merge (measured, v5.5.1, `config -q`; the answers are docker compose's). The base
// file is only extended from, so a value of it that the extending service replaces is never in the model — and a service
// of that file that nothing extends is not either. It was asked of the extended service before the merge, so `x-a: 1`
// over `x-a: .inf` was refused (#1515). Through two files the value goes as far as the last service that does not
// replace it. (A list written over keeps the infinity: `x-a: [1]` beside `x-a: [.inf]` is refused, and `!override [1]` is read. Not here: a `.inf` in the `labels` and some other keys of the extended service that the service over it writes over or resets — docker compose reads it, and opossum refuses it in an older check of those keys: a known difference, in the compatibility notes.)
func TestAnInfinityInAnExtendedServiceIsRefusedWhereItStaysInTheMerge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		main  string
		want  string // docker compose's answer: "read" or "REFUSE"
	}{
		{"base x-a .inf; web overrides x-a: 1", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: 1\n", "read"},
		{"base x-a .inf; web !reset null", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: !reset null\n", "read"},
		{"base x-a .inf; web !override 2", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: !override 2\n", "read"},
		{"base x-a .inf; web keeps it", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n", "REFUSE"},
		{"base cpus .inf; web overrides cpus: 1", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    cpus: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    cpus: 1\n", "read"},
		{"base x-a [.inf]; web appends x-a: [1] (stays)", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: [.inf]\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: [1]\n", "REFUSE"},
		{"base x-a [.inf]; web !override [1]", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: [.inf]\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: !override [1]\n", "read"},
		{"base x-a .nan; web overrides x-a: 1", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: .nan\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: 1\n", "read"},
		{"base x-a -.inf; web overrides x-a: 1", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: -.inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: 1\n", "read"},
		{"2 hops: C b x-a .inf; B b extends C, B overrides x-a: 1; web extends B", map[string]string{"base.yaml": "services:\n  b:\n    extends: {file: c.yaml, service: b}\n    x-a: 1\n", "c.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n", "read"},
		{"2 hops: C b x-a .inf; B b extends C (keeps); web overrides x-a: 1", map[string]string{"base.yaml": "services:\n  b:\n    extends: {file: c.yaml, service: b}\n", "c.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    x-a: 1\n", "read"},
		{"2 hops: C b x-a .inf; B b extends C (keeps); web keeps", map[string]string{"base.yaml": "services:\n  b:\n    extends: {file: c.yaml, service: b}\n", "c.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n", "REFUSE"},
		{"other service in base.yaml has .inf (not extended)", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n  other:\n    image: y\n    x-a: .inf\n"}, "services:\n  web:\n    extends: {file: base.yaml, service: b}\n", "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte(tc.main), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{main}, nil)
			got := "read"
			if err != nil {
				got = "REFUSE"
			}
			if got != tc.want {
				t.Errorf("%s: %s, want %s (err %v)", tc.name, got, tc.want, err)
			}
		})
	}
}

// What a refusal names. The file is the one the service was extended from; when that service extends another in turn,
// the value may be written in the file beyond, which is not known after the merge, so the file named says so. A refusal that
// named a file the value is not in (the one of the middle, for a value written two files away) was the answer of a version
// that read the file beyond one at a time (#1515; the rows above assert only the refusal).
func TestTheRefusalOfAnInfinityInAnExtendedServiceNamesTheFileItWasExtendedFrom(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		wantIn  string
		wantNot string
	}{
		{"one hop", map[string]string{"base.yaml": "services:\n  b:\n    image: x\n    x-a: .inf\n"}, "base.yaml", "or a file it extends"},
		{"two hops: the service of the middle extends another file", map[string]string{
			"base.yaml": "services:\n  b:\n    extends: {file: c.yaml, service: deep}\n",
			"c.yaml":    "services:\n  deep:\n    image: x\n    x-a: .inf\n"}, "or a file it extends", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte("services:\n  web:\n    extends: {file: base.yaml, service: b}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{main}, nil)
			if err == nil {
				t.Fatal("an infinity that stays was read")
			}
			if !strings.Contains(err.Error(), "base.yaml") || !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the refusal is %q, want it to name base.yaml and say %q", err, tc.wantIn)
			}
			if tc.wantNot != "" && strings.Contains(err.Error(), tc.wantNot) {
				t.Errorf("the refusal is %q, which says %q of a value written in the file it names", err, tc.wantNot)
			}
		})
	}
}
