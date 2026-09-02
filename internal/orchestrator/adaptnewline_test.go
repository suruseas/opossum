package orchestrator

import (
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// newlineProject is the compose body these tests share: the bind source carries
// a REAL newline (the YAML `\n` escape inside a double-quoted scalar — a raw
// line break in the file would be folded to a space by YAML itself and reach
// nobody). newlineVictim asserts the newline survived loading, because the
// first version of this test wrote a raw line break and went green against an
// input that had already lost it.
const newlineProject = `
name: p
services:
  db:
    image: postgres:16
    volumes:
      - "./pg\nFORGED opossum says something:/var/lib/postgresql/data"
`

func newlineVictim(t *testing.T) (string, []Adaptation) {
	t.Helper()
	p := loadProject(t, newlineProject)
	if v := p.Services["db"].Volumes[0]; !strings.Contains(v, "\n") {
		t.Fatalf("the fixture's newline never reached the project (YAML folded it?): %q", v)
	}
	o := New(p, nil, "opossum", io.Discard)
	return o.PlanOverlay()
}

// A newline in a compose value used to cost the whole overlay: the applied
// entry's comment carried it verbatim, the comment ended a line early, and the
// self-check refused the file — every fix of that run gone because one value
// had a line break in it (#513). The note class was fixed first (#509); these
// pin the applied class, whose builder was one of the two still carrying raw
// lines.
func TestAnOverlayStaysYAMLWhenAValueCarriesANewline(t *testing.T) {
	overlay, changes := newlineVictim(t)
	if len(changes) == 0 || overlay == "" {
		t.Fatalf("this project needs adapting, got %d change(s)", len(changes))
	}
	// The overlay must survive its own parse — the self-check that used to drop
	// it does exactly this.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(overlay), &doc); err != nil {
		t.Fatalf("the overlay is not valid YAML: %v\n%s", err, overlay)
	}
	// And no line of it begins with the second half of the value: the newline is
	// flattened, not carried.
	for _, line := range strings.Split(overlay, "\n") {
		if strings.HasPrefix(line, "FORGED") {
			t.Errorf("a compose value got a line of its own in the overlay:\n%s", overlay)
		}
	}
	// The flattened value is still there to read — sanitizing must not drop it.
	if !strings.Contains(overlay, "./pg FORGED") {
		t.Errorf("the value should survive flattened (newline -> space), got:\n%s", overlay)
	}
}

// The builders themselves, fed a hostile heading directly. Today's reachable
// headings happen to be safe (%q-formatted or value-free), so without this the
// heading's flattening is armor that can be removed green — measurable only
// the day a producer writes a raw %s heading, which is the wrong day to learn.
func TestEveryCommentBlockLineIsAComment(t *testing.T) {
	// Both list positions carry a line break: the first line and the
	// continuation lines are different branches of the writer, and a hostile
	// value only in the first position leaves the other branch unmeasured.
	// A carriage return rides along — YAML treats it as a line break too, and
	// on a terminal it rewinds the line, so it must flatten the same way.
	hostile := []string{"x\nFORGED", "y\rFORGED2"}
	for name, block := range map[string]string{
		"comment":    commentBlock("h\nFORGED", hostile, hostile, hostile),
		"suggestion": suggestionBlock("h\nFORGED", hostile, hostile),
		"note":       noteBlock("h\nFORGED", hostile, hostile),
	} {
		for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
			if !strings.HasPrefix(line, "#") {
				t.Errorf("%s block let a line escape the comment: %q", name, line)
			}
			// A carriage return survives the split above (it is not "\n") and
			// still rewinds a terminal line — no control character may remain.
			if i := strings.IndexFunc(line, func(r rune) bool { return r < ' ' || r == 0x7f }); i >= 0 {
				t.Errorf("%s block kept a control character (0x%02x): %q", name, line[i], line)
			}
		}
	}
}
