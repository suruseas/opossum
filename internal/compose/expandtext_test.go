package compose

// What a `${VAR}` expands to is text (#824). docker compose (v5.5.0) reads
// the expansion as the string it is, whatever it looks like: `TAG=42` under
// `image: alpine:${TAG}` is the tag "42", `V=[1]` under `environment` is
// the value "[1]", `F=1.50` is "1.50" as written, and a boolean field takes
// `RO=true` as the word. opossum expanded references in the file's text
// before reading it, so `${NN}` with `NN=42` was read as the number 42
// and refused where a string belongs (`image must be a string, got a
// number`), `V=[1]` became a list, and `F=1.50` the number 1.5. Now every
// expanded value rides through parsing held aside, as a multi-line value
// already did, and comes back as a string.

import (
	"strings"
	"testing"
)

func TestWhatAVariableExpandsToIsText(t *testing.T) {
	t.Setenv("OPOSSUM_T_TAG", "42")
	t.Setenv("OPOSSUM_T_LIST", "[1]")
	t.Setenv("OPOSSUM_T_FLOAT", "1.50")
	t.Setenv("OPOSSUM_T_BOOL", "true")
	t.Setenv("OPOSSUM_T_CMD", `["sh", "-c", "echo hi"]`)
	t.Setenv("OPOSSUM_T_CPUS", "0.5")
	body := "services:\n  web:\n    image: alpine:${OPOSSUM_T_TAG}\n    command: ${OPOSSUM_T_CMD}\n    read_only: ${OPOSSUM_T_BOOL}\n    cpus: ${OPOSSUM_T_CPUS}\n    user: ${OPOSSUM_T_TAG}\n    environment:\n      A: ${OPOSSUM_T_LIST}\n      B: ${OPOSSUM_T_TAG}\n      F: ${OPOSSUM_T_FLOAT}\n    networks: [back]\nnetworks:\n  back:\n    name: ${OPOSSUM_T_TAG}\n"
	p, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	for _, tc := range []struct{ name, got, want string }{
		{"a number under image", web.Image, "alpine:42"},
		{"a number where a string belongs", web.User, "42"},
		{"a number as a declaration's name", p.Networks["back"].Name, "42"},
		{"a list under environment", strings.Join(web.Environment, ","), "A=[1],B=42,F=1.50"},
		{"a flow list under command, split as text", strings.Join(web.Command, "|"), `[sh,|-c,|echo hi]`},
		{"a number under cpus", string(web.CPUs), "0.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
	t.Run("a boolean field takes the word", func(t *testing.T) {
		if !web.ReadOnly {
			t.Error("read_only: ${RO} with RO=true should be read-only")
		}
	})
	// A value that would have broken the document is text too, and a value
	// next to a reference that expands to nothing (`${REGISTRY}${IMAGE}`
	// with REGISTRY unset — the everyday shape) is read whole.
	t.Setenv("OPOSSUM_T_BROKEN", "x: [")
	unsetHostVars(t, "OPOSSUM_T_NOPE")
	p, err = Load(writeTemp(t, "services:\n  web:\n    image: ${OPOSSUM_T_NOPE}alpine:${OPOSSUM_T_TAG}\n    environment:\n      A: ${OPOSSUM_T_BROKEN}\n      B: ${OPOSSUM_T_NOPE}${OPOSSUM_T_TAG}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Environment, ","); got != "A=x: [,B=42" {
		t.Errorf("environment = %q, want the values as text", got)
	}
	if got := p.Services["web"].Image; got != "alpine:42" {
		t.Errorf("image = %q, want the empty reference dropped and the rest whole", got)
	}
}
