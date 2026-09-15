package orchestrator_test

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// `logs` and `stats` read this project's containers: a container of a
// service's name that is another project's, carries no project label, or
// that the runtime gives no readable answer about is not read — its logs are
// someone else's — and is named on stderr, while the other services are read
// as before. docker compose v5.5.0 shows nothing for such a container,
// silently (measured: `logs`, `stats --no-stream`, `top` all print nothing,
// exit 0). The third kind also makes the exit non-zero once the rest are
// read, as `down` exits over the containers it left: a runtime that would
// not answer is not a project with nothing to show.
func TestLogsAndStatsReadOnlyThisProjectsContainers(t *testing.T) {
	kinds := []struct {
		name, env, said string
		left            string // "" when the exit is zero; else what the error says
	}{
		{"no project label", "INSPECT_UNLABELED=web.demo.opossum", "web: container web.demo.opossum carries no opossum.project label, so it was not made by this project", ""},
		{"another project's", "INSPECT_OWNER=web.demo.opossum=otherproj", `web: container web.demo.opossum belongs to project "otherproj"`, ""},
		{"no readable answer", "INSPECT_FAIL=web.demo.opossum", "web: container web.demo.opossum could not be asked: the runtime gave no readable answer about which project owns it",
			"the runtime gave no readable answer about which project owns 1 container(s), so they were left: web.demo.opossum"},
	}
	newP := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"db":  {Image: "postgres:16"},
			"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
		})
	}
	// exitOK checks the error against the kind: nil, or the unanswered
	// containers named with the command to run again.
	exitOK := func(t *testing.T, err error, left, command string) {
		t.Helper()
		if left == "" {
			if err != nil {
				t.Errorf("want a zero exit, got %v", err)
			}
			return
		}
		if err == nil || !strings.Contains(err.Error(), left) || !strings.Contains(err.Error(), "run `"+command+"` again") {
			t.Errorf("want %q and `%s` to run again, got %v", left, command, err)
		}
	}
	for _, k := range kinds {
		t.Run("logs/"+k.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, k.env)
			var out bytes.Buffer
			var err error
			stderr := stderrOf(t, func() { err = orchestrator.New(newP(), rt, "opossum", &out).Logs(nil, runtime.LogsOptions{}) })
			exitOK(t, err, k.left, "opossum logs")
			lines := log()
			if !slices.Contains(lines, "logs db.demo.opossum") {
				t.Errorf("db's logs should be read, got %v", lines)
			}
			if slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "logs web.demo.opossum") }) {
				t.Errorf("web's container is not this project's; its logs must not be read, got %v", lines)
			}
			if !strings.Contains(stderr, k.said+" — not shown") {
				t.Errorf("want %q said on stderr, got:\n%s", k.said+" — not shown", stderr)
			}
			if strings.Contains(out.String(), "not shown") {
				t.Errorf("the note belongs on stderr, got stdout:\n%s", out.String())
			}
		})
		// Three services with one left out: the two that remain are followed
		// multiplexed, and the one left out is neither followed nor in the
		// prefix width (one more than "cache-1", not "web-1").
		t.Run("logs --follow/"+k.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, k.env)
			p := newP()
			p.Services["cache"] = &compose.Service{Image: "cache:latest"}
			var out bytes.Buffer
			var err error
			stderr := stderrOf(t, func() { err = orchestrator.New(p, rt, "opossum", &out).Logs(nil, runtime.LogsOptions{Follow: true}) })
			exitOK(t, err, k.left, "opossum logs")
			if slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "logs") && strings.Contains(l, "web.demo.opossum") }) {
				t.Errorf("web's container is not this project's; its logs must not be followed, got %v", log())
			}
			for _, want := range []string{"db-1     | log-line db.demo.opossum\n", "cache-1  | log-line cache.demo.opossum\n"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("want %q multiplexed with the prefix width of the services followed, got:\n%s", want, out.String())
				}
			}
			if !strings.Contains(stderr, k.said+" — not shown") {
				t.Errorf("want %q said on stderr, got:\n%s", k.said, stderr)
			}
		})
		t.Run("stats/"+k.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, k.env)
			var out bytes.Buffer
			var err error
			stderr := stderrOf(t, func() {
				err = orchestrator.New(newP(), rt, "opossum", &out).Stats(nil, orchestrator.StatsOptions{NoStream: true})
			})
			exitOK(t, err, k.left, "opossum stats")
			stats := slices.IndexFunc(log(), func(l string) bool { return strings.HasPrefix(l, "stats") })
			if stats < 0 || !strings.Contains(log()[stats], "db.demo.opossum") || strings.Contains(log()[stats], "web.demo.opossum") {
				t.Errorf("stats should be asked for db alone, got %v", log())
			}
			if !strings.Contains(stderr, k.said+" — not measured") {
				t.Errorf("want %q said on stderr, got:\n%s", k.said+" — not measured", stderr)
			}
		})
		t.Run("stats --format json/"+k.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, k.env)
			var out bytes.Buffer
			var err error
			_ = stderrOf(t, func() {
				err = orchestrator.New(newP(), rt, "opossum", &out).Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: "json"})
			})
			exitOK(t, err, k.left, "opossum stats")
			if !strings.Contains(out.String(), `"Service":"db"`) || strings.Contains(out.String(), `"Service":"web"`) {
				t.Errorf("want db's row and no row for web, got %s", out.String())
			}
		})
		// `stats --host` renders a row per service without failing, so leaving
		// a container out means no row: neither its guest usage nor its host
		// footprint is this project's to show.
		t.Run("stats --host/"+k.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, k.env)
			var out bytes.Buffer
			var err error
			stderr := stderrOf(t, func() {
				o := orchestrator.New(newP(), rt, "opossum", &out)
				o.HostFP = fakeFootprinter{"web.demo.opossum": 305 * 1024 * 1024, "db.demo.opossum": 340 * 1024 * 1024}
				err = o.StatsHost(nil)
			})
			exitOK(t, err, k.left, "opossum stats --host")
			stats := slices.IndexFunc(log(), func(l string) bool { return strings.HasPrefix(l, "stats") })
			if stats < 0 || !strings.Contains(log()[stats], "db.demo.opossum") || strings.Contains(log()[stats], "web.demo.opossum") {
				t.Errorf("stats should be asked for db alone, got %v", log())
			}
			lines := nonEmptyLines(out.String())
			if len(lines) != 4 || columns(lines[1])[0] != "db" || !reflect.DeepEqual(columns(lines[2]), []string{"total", "340MiB"}) {
				t.Errorf("want a header, db's row, db's total and the footnote — no row for web, got:\n%s", out.String())
			}
			if !strings.Contains(stderr, k.said+" — not measured") {
				t.Errorf("want %q said on stderr, got:\n%s", k.said+" — not measured", stderr)
			}
		})
	}
	// Every container asked for someone else's: nothing to read or measure,
	// nothing printed, and the exit is zero, as docker compose's — not "no
	// container found", which would offer `opossum up`, and `up` refuses a
	// container that is there and someone else's. Every container one the
	// runtime gave no readable answer about: nothing printed either, and the
	// exit is non-zero, naming both.
	commands := []struct {
		name string
		run  func(o *orchestrator.Orchestrator) error
	}{
		{"opossum logs", func(o *orchestrator.Orchestrator) error { return o.Logs(nil, runtime.LogsOptions{}) }},
		{"opossum stats", func(o *orchestrator.Orchestrator) error {
			return o.Stats(nil, orchestrator.StatsOptions{NoStream: true})
		}},
		{"opossum stats --host", func(o *orchestrator.Orchestrator) error {
			o.HostFP = fakeFootprinter{"web.demo.opossum": 305 * 1024 * 1024, "db.demo.opossum": 340 * 1024 * 1024}
			return o.StatsHost(nil)
		}},
	}
	for _, c := range commands {
		t.Run(c.name+"/every container someone else's", func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "INSPECT_UNLABELED=web.demo.opossum", "INSPECT_OWNER=db.demo.opossum=otherproj")
			var out bytes.Buffer
			var err error
			stderr := stderrOf(t, func() { err = c.run(orchestrator.New(newP(), rt, "opossum", &out)) })
			if err != nil {
				t.Errorf("want a zero exit with nothing of this project's to show, got %v", err)
			}
			if out.String() != "" {
				t.Errorf("want nothing on stdout, got:\n%s", out.String())
			}
			if slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "logs") || strings.HasPrefix(l, "stats") }) {
				t.Errorf("nothing of this project's to read or measure, got %v", log())
			}
			if strings.Count(stderr, ": container ") != 2 {
				t.Errorf("want both containers named on stderr, got:\n%s", stderr)
			}
		})
		t.Run(c.name+"/every container unanswered", func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum db.demo.opossum")
			var out bytes.Buffer
			var err error
			_ = stderrOf(t, func() { err = c.run(orchestrator.New(newP(), rt, "opossum", &out)) })
			want := "the runtime gave no readable answer about which project owns 2 container(s), so they were left: db.demo.opossum, web.demo.opossum"
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "run `"+c.name+"` again") {
				t.Errorf("want %q and `%s` to run again, got %v", want, c.name, err)
			}
			if out.String() != "" {
				t.Errorf("want nothing on stdout, got:\n%s", out.String())
			}
			if slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "logs") || strings.HasPrefix(l, "stats") }) {
				t.Errorf("nothing of this project's to read or measure, got %v", log())
			}
		})
	}
	// `ps` with every container one the runtime gave no readable answer
	// about: the table is its header alone, and the exit is non-zero.
	t.Run("opossum ps/every container unanswered", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum db.demo.opossum")
		var out bytes.Buffer
		var err error
		_ = stderrOf(t, func() { err = orchestrator.New(newP(), rt, "opossum", &out).Ps(orchestrator.PsOptions{}) })
		want := "the runtime gave no readable answer about which project owns 2 container(s), so they were left: db.demo.opossum, web.demo.opossum"
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "run `opossum ps` again") {
			t.Errorf("want %q and `opossum ps` to run again, got %v", want, err)
		}
		if lines := nonEmptyLines(out.String()); len(lines) != 1 || !strings.HasPrefix(lines[0], "SERVICE") {
			t.Errorf("want the header alone, got:\n%s", out.String())
		}
	})
	// `stats --format json` with every container someone else's prints
	// nothing — not even `[]` — as docker compose v5.5.0 prints nothing.
	t.Run("opossum stats --format json/every container someone else's", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "INSPECT_UNLABELED=web.demo.opossum", "INSPECT_OWNER=db.demo.opossum=otherproj")
		var out bytes.Buffer
		var err error
		_ = stderrOf(t, func() {
			err = orchestrator.New(newP(), rt, "opossum", &out).Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: "json"})
		})
		if err != nil || out.String() != "" || slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "stats") }) {
			t.Errorf("want nothing printed, a zero exit and no stats call, got err %v, stdout %q, %v", err, out.String(), log())
		}
	})
	// A failure to read or measure the containers that are this project's
	// still names the ones the runtime gave no readable answer about, as
	// `start` names them beside its failures (both in one error).
	unansweredWeb := "so they were left: web.demo.opossum"
	t.Run("logs: a failing read beside an unanswered container", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum", "LOGS_FAIL=db.demo.opossum")
		var err error
		_ = stderrOf(t, func() {
			err = orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{}).Logs(nil, runtime.LogsOptions{})
		})
		for _, want := range []string{`logs for service "db"`, unansweredWeb} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want %q, got %v", want, err)
			}
		}
	})
	t.Run("logs --follow: every stream failing beside an unanswered container", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum", "LOGS_FAIL=db.demo.opossum cache.demo.opossum")
		p := newP()
		p.Services["cache"] = &compose.Service{Image: "cache:latest"}
		var err error
		_ = stderrOf(t, func() {
			err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Logs(nil, runtime.LogsOptions{Follow: true})
		})
		for _, want := range []string{"could not follow logs for any of the 2 service(s)", unansweredWeb} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want %q, got %v", want, err)
			}
		}
	})
	for _, format := range []string{"", "json"} {
		t.Run("stats --format "+format+": a failing snapshot beside an unanswered container", func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum", "STATS_FAIL=1")
			var err error
			_ = stderrOf(t, func() {
				err = orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{}).Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: format})
			})
			// The runtime's stderr goes to the terminal; the error carries the
			// exit status, and the unanswered container beside it.
			for _, want := range []string{"exit status 1", unansweredWeb} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("want %q, got %v", want, err)
				}
			}
		})
	}
	// A service named twice is one service: read once, counted once, named
	// once (docker compose v5.5.0 prints `logs web web` once).
	t.Run("logs db db web web: each once", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum")
		var err error
		_ = stderrOf(t, func() {
			err = orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{}).Logs([]string{"db", "db", "web", "web"}, runtime.LogsOptions{})
		})
		if n := slices.IndexFunc(log(), func(l string) bool { return l == "logs db.demo.opossum" }); n < 0 || strings.Count(strings.Join(log(), "\n"), "logs db.demo.opossum") != 1 {
			t.Errorf("want db read once, got %v", log())
		}
		want := "which project owns 1 container(s), so they were left: web.demo.opossum —"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q (once, counted once), got %v", want, err)
		}
	})
	// A service named twice keeps the position it was first named in: the
	// order services are read in is the order they were named.
	t.Run("logs web db web: web first, each once", func(t *testing.T) {
		rt, log := fakeShim(t)
		var err error
		_ = stderrOf(t, func() {
			err = orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{}).Logs([]string{"web", "db", "web"}, runtime.LogsOptions{})
		})
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		var reads []string
		for _, l := range log() {
			if strings.HasPrefix(l, "logs ") {
				reads = append(reads, l)
			}
		}
		if want := []string{"logs web.demo.opossum", "logs db.demo.opossum"}; !reflect.DeepEqual(reads, want) {
			t.Errorf("want %v (web where it was first named, each once), got %v", want, reads)
		}
	})
	// This project's own service with no container yet, beside one the
	// runtime gave no readable answer about: `stats` still says there is no
	// container to measure (and offers `opossum up` for that one), and names
	// the unanswered one.
	t.Run("stats: own service never started, the other unanswered", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_ABSENT=db.demo.opossum", "INSPECT_FAIL=web.demo.opossum")
		var err error
		_ = stderrOf(t, func() {
			err = orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{}).Stats(nil, orchestrator.StatsOptions{NoStream: true})
		})
		for _, want := range []string{"no container found for any of the 1 service(s)", "so they were left: web.demo.opossum"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want %q, got %v", want, err)
			}
		}
	})
	// Bare names (--dns-domain ""): the same reading, by the bare name.
	t.Run("logs with bare names", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo", "INSPECT_UNLABELED=db")
		var out bytes.Buffer
		var err error
		stderr := stderrOf(t, func() { err = orchestrator.New(newP(), rt, "", &out).Logs(nil, runtime.LogsOptions{}) })
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		if !slices.Contains(log(), "logs web") || slices.Contains(log(), "logs db") {
			t.Errorf("web read and db left out, got %v", log())
		}
		if !strings.Contains(stderr, "db: container db carries no opossum.project label, so it was not made by this project — not shown") {
			t.Errorf("want db named on stderr, got:\n%s", stderr)
		}
	})
	// This project's own: read as before, nothing said.
	t.Run("this project's own", func(t *testing.T) {
		rt, log := fakeShim(t)
		var out bytes.Buffer
		var err error
		stderr := stderrOf(t, func() { err = orchestrator.New(newP(), rt, "opossum", &out).Logs(nil, runtime.LogsOptions{}) })
		if err != nil || !slices.Contains(log(), "logs web.demo.opossum") || strings.Contains(stderr, "not shown") {
			t.Errorf("own containers are read with nothing said, got %v, %v and stderr %q", err, log(), stderr)
		}
	})
}
