package main

import (
	"strings"
	"testing"
)

// A range as wide as one port is that port: docker compose reads `47100-47100:80` as `published: "47100"` (v5.5.1 `config`), so two entries
// that name the one port in either spelling are the pair `up` refuses before it starts anything, where they reached the runtime and the
// second bind failed (#1772). The file is read by the loader, as the file is; an entry built by hand would not be.
func TestAPortWrittenAsARangeOfOneIsThePortForTheDuplicateCheck(t *testing.T) {
	for _, tc := range []struct {
		name         string
		a, z         string
		wantRefusing bool
	}{
		{"a host range of one and the plain port", "47100-47100:80", "47100:81", true},
		{"the plain port and a host range of one", "47100:80", "47100-47100:81", true},
		{"two host ranges of one", "47100-47100:80", "47100-47100:81", true},
		{"a container range of one, beside the plain port", "47100:80-80", "47100:81", true},
		{"a range of one on both sides", "47100-47100:80-80", "47100:81", true},
		{"a range of one with the address of the other", "127.0.0.1:47100-47100:80", "127.0.0.1:47100:81", true},
		{"a host range of one with a leading zero", "047100-47100:80", "47100:81", true},
		{"a range of one beside another port", "47100-47100:80", "47101:81", false},
		{"a range of one beside a range that does not reach it", "47100-47100:80", "47101-47102:81-82", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			body := "name: demo\nservices:\n  a:\n    image: alpine:3.20\n    ports: [\"" + tc.a + "\"]\n  z:\n    image: alpine:3.20\n    ports: [\"" + tc.z + "\"]\n"
			out, err := run(t, "-f", writeCompose(t, body), "up", "--dry-run")
			refused := err != nil && strings.Contains(err.Error(), "OPSM-213")
			if refused != tc.wantRefusing {
				t.Errorf("want the duplicate refused: %v, got err %v\n%s", tc.wantRefusing, err, out)
			}
		})
	}
}
