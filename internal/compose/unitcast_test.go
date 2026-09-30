package compose

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/unit-forms.json is what docker compose v5.5.1 (Docker 29.8.0) said to
// each of 2998 strings written for a byte-size key (`shm_size`) and 350 for a
// duration key (`stop_grace_period`), accepted or refused
// (testdata/tools/capture-unit-forms.py makes it). bytesOK and durationOK are held
// to every row, in both directions: a string docker compose reads must load here,
// and one it refuses must be refused (#1366).
func TestTheUnitCastsAgreeWithDockerCompose(t *testing.T) {
	raw, err := os.ReadFile("testdata/unit-forms.json")
	if err != nil {
		t.Fatal(err)
	}
	var forms struct {
		Bytes    [][]any `json:"bytes"`
		Duration [][]any `json:"duration"`
	}
	if err := json.Unmarshal(raw, &forms); err != nil {
		t.Fatal(err)
	}
	if len(forms.Bytes) != 2998 || len(forms.Duration) != 350 {
		t.Fatalf("the capture has %d byte forms and %d duration forms, want 2998 and 350", len(forms.Bytes), len(forms.Duration))
	}
	// Refused here and read by docker compose, on purpose: docker compose reads each
	// component of a duration through float64 and floors its fraction, and durationOK
	// adds them exactly, so a value within a nanosecond of the int64 limit that carries
	// a fraction is refused here where docker compose reads it (a duration of 292
	// years). They are pinned: a change that moves one is seen and the list is written
	// again. The forms docker compose refuses by the same arithmetic are in the
	// capture too and are refused here as well.
	stricter := map[string]bool{}
	for _, v := range []string{
		"9223372036854775807.5ns", "9223372036854775807.9ns", "-9223372036854775807.5ns", "-9223372036854775807.9ns",
		"2562047h47m16.8547758075s", "2562047h47m16.8547758079999999s", "9223372036.8547758079s",
		"4611686018427387903.6ns4611686018427387904.6ns", "9223372036854775806.5ns0.6ns", "0.9ns9223372036854775807ns",
		"153722867m16.8547758079s", "106751d23h47m16.8547758075s",
	} {
		stricter["duration/"+v] = true
	}
	seenStricter := map[string]bool{}
	for _, c := range []struct {
		kind string
		rows [][]any
		ok   func(string) bool
	}{{"bytes", forms.Bytes, bytesOK}, {"duration", forms.Duration, durationOK}} {
		wrong := 0
		for _, r := range c.rows {
			v, docker := r[0].(string), r[1].(bool)
			if got := c.ok(v); got != docker {
				if id := c.kind + "/" + v; stricter[id] && docker && !got {
					seenStricter[id] = true
					continue
				}
				wrong++
				if wrong <= 25 {
					t.Errorf("%s %q: docker compose %s it, and it is %s here", c.kind, v, map[bool]string{true: "reads", false: "refuses"}[docker], map[bool]string{true: "read", false: "refused"}[got])
				}
			}
		}
		if wrong > 0 {
			t.Errorf("%s: %d of %d forms differ from docker compose", c.kind, wrong, len(c.rows))
		}
	}
	for id := range stricter {
		if !seenStricter[id] {
			t.Errorf("%s is listed as refused here and read by docker compose, and it no longer differs: take it off the list", id)
		}
	}
}
