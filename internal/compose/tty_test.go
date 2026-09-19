package compose

import (
	"slices"
	"strings"
	"testing"
)

// `tty:` is read the way the other service booleans are (docker compose
// v5.5.1, measured 2026-09-19): the YAML bool, the quoted word, `yes`/`no`
// (docker compose warns and reads them); `1`, `"1"`, `null` and a list are
// refused. `config` prints `tty: true` and nothing when it is false, as
// docker compose prints it. `stdin_open` stays listed among the ignored
// fields: the runtime's `-i` does not keep a shell alive and, with `-t`,
// keeps the container from starting when stdin is not a terminal.
func TestTTYIsReadAsABoolean(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		tty        bool
		err        string
	}{
		{"true", "    tty: true\n", true, ""},
		{"false", "    tty: false\n", false, ""},
		{"absent", "", false, ""},
		{"quoted true", "    tty: \"true\"\n", true, ""},
		{"quoted False", "    tty: \"False\"\n", false, ""},
		{"yes", "    tty: yes\n", true, ""},
		{"no", "    tty: no\n", false, ""},
		{"through an interpolation", "    tty: ${OPOSSUM_TTY_T:-true}\n", true, ""},
		// Beside the other booleans, and not read from them: a service with
		// `init: true` and `read_only: true` alone has no tty, and one with
		// all three has it.
		{"init and read_only without tty", "    init: true\n    read_only: true\n", false, ""},
		{"init and read_only with tty", "    init: true\n    read_only: true\n    tty: true\n", true, ""},
		{"tty false beside init", "    init: true\n    tty: false\n", false, ""},
		{"1 is refused", "    tty: 1\n", false, "tty"},
		{"quoted 1 is refused", "    tty: \"1\"\n", false, "tty"},
		{"null is refused", "    tty: null\n", false, "tty"},
		// In YAML's own words, as `read_only: [true]` is.
		{"a list is refused", "    tty: [true]\n", false, "cannot unmarshal !!seq into bool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  app:\n    image: alpine:3.20\n    stdin_open: true\n"+tc.body))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal naming %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["app"]
			if svc.TTY != tc.tty {
				t.Errorf("tty: got %v, want %v", svc.TTY, tc.tty)
			}
			// Read, so not among the ignored fields; stdin_open still is.
			if slices.Contains(svc.Unsupported, "tty") {
				t.Errorf("tty is read, yet listed as ignored: %v", svc.Unsupported)
			}
			if !slices.Contains(svc.Unsupported, "stdin_open") {
				t.Errorf("stdin_open is not read, so it has to be listed as ignored: %v", svc.Unsupported)
			}
			out, err := RenderConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(out, "tty: true"); got != tc.tty {
				t.Errorf("config prints `tty: true`: %v, want %v\n%s", got, tc.tty, out)
			}
			if strings.Contains(out, "tty: false") {
				t.Errorf("config prints `tty: false`, which docker compose leaves out:\n%s", out)
			}
		})
	}
}
