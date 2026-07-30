package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRestart(t *testing.T) {
	cases := []struct {
		in       string
		mode     string
		maxRetry int
		wants    bool
		wantErr  bool
	}{
		{"", "", 0, false, false},
		{"no", RestartNo, 0, false, false},
		{"always", RestartAlways, 0, true, false},
		{"unless-stopped", RestartUnlessStopped, 0, true, false},
		{"on-failure", RestartOnFailure, 0, true, false},
		{"on-failure:5", RestartOnFailure, 5, true, false},
		{" always ", RestartAlways, 0, true, false},
		{"on-failure:", "", 0, false, true},
		{"on-failure:-1", "", 0, false, true},
		{"on-failure:x", "", 0, false, true},
		{"sometimes", "", 0, false, true},
	}
	for _, c := range cases {
		got, err := ParseRestart(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseRestart(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if got.Mode != c.mode || got.MaxRetry != c.maxRetry || got.Wants() != c.wants {
			t.Errorf("ParseRestart(%q) = %+v (wants=%v), want mode=%q max=%d wants=%v",
				c.in, got, got.Wants(), c.mode, c.maxRetry, c.wants)
		}
	}
}

// `no` and an unset policy must not put a service under supervision — otherwise
// every project would grow a watcher process.
func TestRestartNoDoesNotWantSupervision(t *testing.T) {
	for _, v := range []string{"", "no"} {
		p, err := ParseRestart(v)
		if err != nil {
			t.Fatal(err)
		}
		if p.Wants() {
			t.Errorf("restart: %q should not ask for supervision", v)
		}
	}
}

// A misspelled policy must fail the load rather than being accepted and then
// silently never supervised — the field left the "ignored" list, so a typo has no
// other way of surfacing.
func TestLoadRejectsAnUnreadableRestartPolicy(t *testing.T) {
	for _, bad := range []string{"Always", "on-failure:abc", "sometimes"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(p, []byte("name: demo\nservices:\n  web:\n    image: w\n    restart: "+bad+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p)
		if err == nil {
			t.Errorf("restart: %q should fail the load", bad)
			continue
		}
		if !strings.Contains(err.Error(), "web") {
			t.Errorf("the error should name the service, got: %v", err)
		}
	}
}

// …and the valid ones still load.
func TestLoadAcceptsEveryValidRestartPolicy(t *testing.T) {
	for _, ok := range []string{"no", "always", "unless-stopped", "on-failure", "on-failure:5"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(p, []byte("name: demo\nservices:\n  web:\n    image: w\n    restart: "+ok+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err != nil {
			t.Errorf("restart: %q should load, got %v", ok, err)
		}
	}
}
