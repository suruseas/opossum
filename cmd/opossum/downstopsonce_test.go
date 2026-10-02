package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// `down` asks the supervisor to stop before the compose file is read, under the name it can work out
// without the file, and `Down` asks again once the file is loaded. A stop that cannot be confirmed
// keeps the pid file, so a second ask for the same project waits the whole budget again and says
// OPSM-414 twice (#1406). That one is asked once. Whatever else the first ask found is asked again by
// `Down`, which asks under the lock it takes: a supervisor an `up` in another terminal started in
// between is stopped there. A name the file gives, where the first was the directory's, is another
// project's supervisor and is asked as well.
func TestDownAsksTheSameSupervisorToStopOnce(t *testing.T) {
	const plain = "services:\n  web:\n    image: alpine:3.20\n"
	for _, tc := range []struct {
		name               string
		args               []string // before `down`
		body               string
		stopped, attempted bool // what the first ask found
		wantNames          []string
		wantWarns          int
	}{
		{name: "the name given with -p, asked and not confirmed", args: []string{"-p", "once"}, body: plain,
			attempted: true, wantNames: []string{"once"}, wantWarns: 1},
		{name: "the directory's name, asked and not confirmed", body: plain,
			attempted: true, wantNames: []string{"<dir>"}, wantWarns: 1},
		{name: "the name given with -p, stopped by the first ask", args: []string{"-p", "once"}, body: plain,
			stopped: true, attempted: true, wantNames: []string{"once", "once"}, wantWarns: 0},
		{name: "the name given with -p, nothing there to ask", args: []string{"-p", "once"}, body: plain,
			wantNames: []string{"once", "once"}, wantWarns: 0},
		{name: "the name the file gives, which the early stop could not know", body: "name: filenamed\n" + plain,
			attempted: true, wantNames: []string{"<dir>", "filenamed"}, wantWarns: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			var asked []string
			saved := stopSupervisorFn
			stopSupervisorFn = func(project string) (bool, bool) {
				asked = append(asked, project)
				return tc.stopped, tc.attempted
			}
			t.Cleanup(func() { stopSupervisorFn = saved })

			file := writeCompose(t, tc.body)
			args := append([]string{"-f", file}, tc.args...)
			out, err := run(t, append(args, "down")...)
			if err != nil {
				t.Fatalf("down: %v\n%s", err, out)
			}
			// The directory's name is what the project is called where nothing else says: the
			// directory the file is in, as the early stop and the load both read it.
			dir := compose.SanitizeName(filepath.Base(filepath.Dir(file)))
			want := make([]string, len(tc.wantNames))
			for i, n := range tc.wantNames {
				want[i] = strings.ReplaceAll(n, "<dir>", dir)
			}
			if strings.Join(asked, ",") != strings.Join(want, ",") {
				t.Errorf("the supervisors asked to stop = %v, want %v", asked, want)
			}
			if got := strings.Count(out, "OPSM-414"); got != tc.wantWarns {
				t.Errorf("OPSM-414 said %d times, want %d:\n%s", got, tc.wantWarns, out)
			}
		})
	}
}
