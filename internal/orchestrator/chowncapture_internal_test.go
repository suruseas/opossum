package orchestrator

// The captures the image wordings cite are also what the chown-path reader is
// held to: for each, the path it pulls out of the real log is the one the
// image chowned — so an image changing its spelling (clickhouse did in 2026)
// shows up here the day the capture is retaken, not in a corpus run months
// later (#480).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheChownPathReaderReadsEachCapturedSpelling(t *testing.T) {
	for _, tc := range []struct{ capture, want string }{
		{"mongo-chown-131.txt", "/data/db"},
		{"clickhouse-chown-131.txt", "/var/lib/clickhouse/"},
		{"pg17-bind-old-datadir-131.txt", "/var/lib/postgresql/data"},
		// redis names only its working directory; that is "no path" here, and
		// saysWorkingDirectory is what reads it.
		{"redis-chown-131.txt", ""},
	} {
		t.Run(tc.capture, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "error-wordings", tc.capture))
			if err != nil {
				t.Fatal(err)
			}
			if got := chownedPath(string(body)); got != tc.want {
				t.Errorf("chownedPath(%s) = %q, want %q", tc.capture, got, tc.want)
			}
			if tc.want == "" && !saysWorkingDirectory(string(body)) {
				t.Errorf("%s names the working directory, and saysWorkingDirectory should say so", tc.capture)
			}
		})
	}
}
