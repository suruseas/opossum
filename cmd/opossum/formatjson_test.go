package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// tableRows splits a column table into its data rows' cells.
func tableRows(out string) [][]string {
	var rows [][]string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		rows = append(rows, strings.Fields(l))
	}
	return rows
}

// `ps`, `images` and `stats --no-stream` print an array of objects under
// --format json, carrying what the table carries for the same project.
func TestFormatJSONCLI(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("IMAGE_ABSENT", "db:latest")
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n  db:\n    image: db:latest\n")

	t.Run("ps", func(t *testing.T) {
		table, err := run(t, "-f", compose, "ps")
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		stdout, _, err := runSplit(t, "-f", compose, "ps", "--format", "json")
		if err != nil {
			t.Fatalf("ps --format json: %v", err)
		}
		// encoding/json writes ">" as \u003e; a JSON reader decodes both the same.
		want := `[{"Service":"db","Container":"db.demo.opossum","Image":"db:latest","IP":"192.168.66.9","Ports":"0.0.0.0:8080-\u003e80/tcp","Status":"running"},` +
			`{"Service":"web","Container":"web.demo.opossum","Image":"web:latest","IP":"192.168.66.9","Ports":"0.0.0.0:8080-\u003e80/tcp","Status":"running"}]` + "\n"
		if stdout != want {
			t.Errorf("ps --format json printed %q, want %q", stdout, want)
		}
		var got []map[string]string
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		rows := tableRows(table)
		if len(rows) != len(got) {
			t.Fatalf("the table has %d rows and the JSON %d:\n%s\n%s", len(rows), len(got), table, stdout)
		}
		for i, r := range rows {
			obj := got[i]
			if cells := []string{obj["Service"], obj["Container"], obj["Image"], obj["IP"], obj["Ports"], obj["Status"]}; strings.Join(cells, " ") != strings.Join(r, " ") {
				t.Errorf("row %d: the table says %q, the JSON %q", i, r, cells)
			}
		}
	})

	t.Run("images", func(t *testing.T) {
		table, err := run(t, "-f", compose, "images")
		if err != nil {
			t.Fatalf("images: %v", err)
		}
		stdout, _, err := runSplit(t, "-f", compose, "images", "--format", "json")
		if err != nil {
			t.Fatalf("images --format json: %v", err)
		}
		want := `[{"Service":"db","Image":"db:latest","Source":"pulled","Present":false},` +
			`{"Service":"web","Image":"web:latest","Source":"pulled","Present":true}]` + "\n"
		if stdout != want {
			t.Errorf("images --format json printed %q, want %q", stdout, want)
		}
		if rows := tableRows(table); len(rows) != 2 || rows[0][0] != "db" || rows[0][3] != "no" || rows[1][0] != "web" || rows[1][3] != "yes" {
			t.Errorf("the table should say the same, got:\n%s", table)
		}
	})

	t.Run("stats", func(t *testing.T) {
		before := len(readLog())
		stdout, _, err := runSplit(t, "-f", compose, "stats", "--no-stream", "--format", "json")
		if err != nil {
			t.Fatalf("stats --no-stream --format json: %v", err)
		}
		row := func(svc string) string {
			return `{"Service":"` + svc + `","Container":"` + svc + `.demo.opossum","CPUUsageUsec":1500000,"MemoryUsageBytes":49283072,"MemoryLimitBytes":1073741824,` +
				`"NetworkRxBytes":2048,"NetworkTxBytes":4096,"BlockReadBytes":8192,"BlockWriteBytes":16384,"NumProcesses":3}`
		}
		if want := "[" + row("db") + "," + row("web") + "]\n"; stdout != want {
			t.Errorf("stats --format json printed %q, want %q", stdout, want)
		}
		// One snapshot, asked for in JSON: a table asked for as well would
		// reach the terminal ahead of the array and break the reader.
		var asked []string
		for _, l := range readLog()[before:] {
			if strings.HasPrefix(l, "stats") {
				asked = append(asked, l)
			}
		}
		if want := "stats --no-stream --format json db.demo.opossum web.demo.opossum"; len(asked) != 1 || asked[0] != want {
			t.Errorf("want the runtime asked once, %q, got %q", want, asked)
		}
	})

	t.Run("stats rows follow the services named, not the runtime's order", func(t *testing.T) {
		stdout, _, err := runSplit(t, "-f", compose, "stats", "--no-stream", "--format", "json", "web", "db")
		if err != nil {
			t.Fatalf("stats --no-stream --format json web db: %v", err)
		}
		if i, j := strings.Index(stdout, `"Service":"web"`), strings.Index(stdout, `"Service":"db"`); i < 0 || j < 0 || i > j {
			t.Errorf("want web's row before db's, got %q", stdout)
		}
	})

	t.Run("stats has no row for a stopped service", func(t *testing.T) {
		t.Setenv("INSPECT_STOPPED", "db.demo.opossum")
		stdout, _, err := runSplit(t, "-f", compose, "stats", "--no-stream", "--format", "json")
		if err != nil {
			t.Fatalf("stats --no-stream --format json: %v", err)
		}
		if want := `[{"Service":"web",`; !strings.HasPrefix(stdout, want) || strings.Count(stdout, `"Service"`) != 1 {
			t.Errorf("want web's row only, got %q", stdout)
		}
	})

	t.Run("stats without --format asks for the runtime's own table", func(t *testing.T) {
		before := len(readLog())
		if _, err := run(t, "-f", compose, "stats", "--no-stream"); err != nil {
			t.Fatalf("stats --no-stream: %v", err)
		}
		var asked []string
		for _, l := range readLog()[before:] {
			if strings.HasPrefix(l, "stats") {
				asked = append(asked, l)
			}
		}
		if want := "stats --no-stream db.demo.opossum web.demo.opossum"; len(asked) != 1 || asked[0] != want {
			t.Errorf("want %q, got %q", want, asked)
		}
	})
}

// A --format these commands cannot print is refused before the runtime is
// asked anything, and so is JSON where only a stream or a table would follow —
// with the runtime stopped, it is neither started nor asked.
func TestFormatJSONRefusalsCLI(t *testing.T) {
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"ps", "--format", "yaml"}, `--format must be table or json, got "yaml"`},
		{[]string{"images", "--format", "yaml"}, `--format must be table or json, got "yaml"`},
		{[]string{"stats", "--no-stream", "--format", "yaml"}, `--format must be table or json, got "yaml"`},
		{[]string{"stats", "--format", "json"}, "--format json requires --no-stream"},
		{[]string{"stats", "--host", "--no-stream", "--format", "json"}, "--format json is not available with --host"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("SYSTEM_STOPPED", "1")
			t.Setenv("APISERVER_DOWN", "1")
			stdout, _, err := runSplit(t, append([]string{"-f", compose}, tc.args...)...)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("want %q, got %v", tc.want, err)
			}
			if stdout != "" {
				t.Errorf("a refusal prints nothing on stdout, got %q", stdout)
			}
			if seen := readLog(); len(seen) != 0 {
				t.Errorf("the runtime should not be asked for anything, it saw %q", seen)
			}
		})
	}
}

// ps and images never start the runtime, so what shows the flag is checked
// before the root's pre-run is the other thing that pre-run does: find the
// container CLI. A flag mistake is the answer even where it is missing.
func TestFormatIsCheckedBeforeTheContainerCLIIsLookedForCLI(t *testing.T) {
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	for _, cmd := range []string{"ps", "images"} {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("OPOSSUM_CONTAINER_BIN", filepath.Join(t.TempDir(), "no-such-container"))
			_, err := run(t, "-f", compose, cmd, "--format", "yaml")
			if err == nil || err.Error() != `--format must be table or json, got "yaml"` {
				t.Errorf("want the format refusal, got %v", err)
			}
		})
	}
}
