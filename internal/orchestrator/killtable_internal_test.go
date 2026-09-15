package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// killShim is a runtime for `kill` over two services, api and web (killed in
// that reverse startup order: web first, then api). refuser names the one
// whose signal is refused ("" for none, "both"); after is what a refused
// container answers once its kill was sent (running, stopped, absent,
// unknown), and afterAPI the same for api when both refuse; apiOwner is who
// owns api before any kill (demo, otherproj, unanswered). Every call is
// logged, and an inspect after a refused kill logs `inspect-after <name>`.
func killShim(t *testing.T, refuser, after, afterAPI, apiOwner string) (*runtime.Runtime, func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	answer := func(state string) string {
		return map[string]string{
			"running": `echo '[{"status":{"state":"running"},"configuration":{"labels":{"opossum.project":"demo"}}}]'`,
			// container 1.4.1's own words for these (measured).
			"stopped": `echo '[{"status":{"state":"stopped"},"configuration":{"labels":{"opossum.project":"demo"}}}]'`,
			"absent":  `echo 'Error: notFound: "container not found"' >&2; exit 1`,
			"unknown": `echo 'Error: XPC connection error: Connection invalid' >&2; exit 1`,
		}[state]
	}
	refuses := func(name string) string {
		if refuser == name || refuser == "both" {
			return `echo 'Error: internalError: "failed to kill container" (cause: "unknown: "invalid signal: BOGUS"")' >&2; exit 1`
		}
		return "exit 0"
	}
	before := map[string]string{
		"demo":       answer("running"),
		"otherproj":  `echo '[{"status":{"state":"running"},"configuration":{"labels":{"opossum.project":"otherproj"}}}]'`,
		"unanswered": answer("unknown"),
	}[apiOwner]
	if afterAPI == "" {
		afterAPI = after
	}
	body := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]s
last=""; for a in "$@"; do last="$a"; done
case "$1" in
  system) echo 'status running' ;;
  kill)
    case "$last" in
      web.demo.opossum) touch %[2]s/killed-web; %[3]s ;;
      api.demo.opossum) touch %[2]s/killed-api; %[4]s ;;
    esac ;;
  inspect)
    case "$last" in
      web.demo.opossum)
        if [ -e %[2]s/killed-web ]; then echo "inspect-after web" >> %[1]s; %[5]s; exit 0; fi
        %[7]s ;;
      api.demo.opossum)
        if [ -e %[2]s/killed-api ]; then echo "inspect-after api" >> %[1]s; %[6]s; exit 0; fi
        %[8]s ;;
    esac ;;
esac
exit 0
`, log, dir, refuses("web"), refuses("api"), answer(after), answer(afterAPI), answer("running"), before)
	shim := filepath.Join(dir, "c.sh")
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}, func() string { b, _ := os.ReadFile(log); return string(b) }
}

// `kill`, over the product of what can happen to one signal: the runtime
// takes it or refuses it; after a refusal the container is found running,
// stopped, gone, or the runtime gives no readable answer about it; the
// refused service had a record of a stop or not; it is the only service
// killed, or one of two — killed first or killed after the other, whose
// signal is taken. The oracle is docker compose v5.5.0 (measured): a refused
// signal on a running container exits 1 (`invalid signal`), a container not
// running is not killed and exits 0, a daemon that does not answer exits 1;
// a container `kill` stopped is not restarted, and a refused kill leaves it
// as it was. Each cell first proves it reached the calls it is about.
func TestKillOverWhatTheRuntimeAnswers(t *testing.T) {
	type cell struct {
		refused bool
		after   string
		prior   bool
		// "web" alone, or two services with the refused one killed "first"
		// (web) or "later" (api).
		shape string
	}
	var cells []cell
	for _, refused := range []bool{false, true} {
		afters := []string{"running"}
		if refused {
			afters = []string{"running", "stopped", "absent", "unknown"}
		}
		for _, after := range afters {
			for _, prior := range []bool{false, true} {
				for _, shape := range []string{"alone", "first", "later"} {
					cells = append(cells, cell{refused, after, prior, shape})
				}
			}
		}
	}
	for _, c := range cells {
		t.Run(fmt.Sprintf("refused=%v after=%s prior=%v shape=%s", c.refused, c.after, c.prior, c.shape), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			subject, other := "web", "api"
			if c.shape == "later" {
				subject, other = "api", "web"
			}
			refuser := ""
			if c.refused {
				refuser = subject
			}
			rt, calls := killShim(t, refuser, c.after, "", "demo")
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"web": {Name: "web", Image: "w"}}}
			if c.shape != "alone" {
				p.Services["api"] = &compose.Service{Name: "api", Image: "a"}
			}
			o := New(p, rt, "opossum", &bytes.Buffer{})
			if c.prior {
				o.MarkStopped(subject)
			}
			err := o.Kill(nil, "BOGUS")

			// Reached: the kill was sent to the subject, a refusal asked about
			// it afterwards, and the other service got its kill too.
			if !strings.Contains(calls(), "kill -s BOGUS "+subject+".demo.opossum") {
				t.Fatalf("the cell never sent %s its kill, calls:\n%s", subject, calls())
			}
			if c.refused && !strings.Contains(calls(), "inspect-after "+subject) {
				t.Fatalf("a refused kill must ask about %s afterwards, calls:\n%s", subject, calls())
			}
			if c.shape != "alone" && !strings.Contains(calls(), "kill -s BOGUS "+other+".demo.opossum") {
				t.Fatalf("%s must be signalled whatever %s's kill did, calls:\n%s", other, subject, calls())
			}

			wantErr := c.refused && (c.after == "running" || c.after == "unknown")
			if (err != nil) != wantErr {
				t.Errorf("error = %v, want an error: %v", err, wantErr)
			}
			if wantErr {
				if !strings.Contains(err.Error(), fmt.Sprintf("killing service %q", subject)) || strings.Contains(err.Error(), fmt.Sprintf("killing service %q", other)) {
					t.Errorf("want %s named and %s not, got %v", subject, other, err)
				}
				if c.after == "unknown" && !strings.Contains(err.Error(), "no readable answer") {
					t.Errorf("want the missing answer said, got %v", err)
				}
			}
			wantRecord := !c.refused || c.prior
			if got := o.wasStoppedByUs(subject); got != wantRecord {
				t.Errorf("%s recorded as stopped = %v, want %v", subject, got, wantRecord)
			}
			if c.shape != "alone" && !o.wasStoppedByUs(other) {
				t.Errorf("%s took its signal and should be recorded as stopped", other)
			}
		})
	}

	// Both refuse: one entry each, under a heading that says only that the
	// kill failed — an entry the runtime gave no answer about may have taken
	// the signal. Each keeps the record it had.
	for _, tc := range []struct{ web, api string }{{"running", "running"}, {"unknown", "running"}, {"unknown", "unknown"}} {
		t.Run("both refused: web "+tc.web+", api "+tc.api, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, calls := killShim(t, "both", tc.web, tc.api, "demo")
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"web": {Name: "web", Image: "w"}, "api": {Name: "api", Image: "a"}}}
			o := New(p, rt, "opossum", &bytes.Buffer{})
			o.MarkStopped("api")
			err := o.Kill(nil, "BOGUS")
			if !strings.Contains(calls(), "inspect-after web") || !strings.Contains(calls(), "inspect-after api") {
				t.Fatalf("both refusals must be asked about, calls:\n%s", calls())
			}
			if err == nil || !strings.HasPrefix(err.Error(), "the kill failed for 2 services:\n- killing service \"web\"") || !strings.Contains(err.Error(), "\n- killing service \"api\"") {
				t.Errorf("want two entries, web then api, under the heading, got:\n%v", err)
			}
			if o.wasStoppedByUs("web") || !o.wasStoppedByUs("api") {
				t.Errorf("each keeps the record it had: web none, api one; got web %v, api %v", o.wasStoppedByUs("web"), o.wasStoppedByUs("api"))
			}
		})
	}

	// A refusal beside a container `kill` leaves alone: api is another
	// project's, or the runtime will not say whose it is. api is not
	// signalled and gets no record; the refusal is said first, and the
	// unanswered owner after it, a blank line apart.
	for _, owner := range []string{"otherproj", "unanswered"} {
		t.Run("web refused beside api "+owner, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, calls := killShim(t, "web", "running", "", owner)
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"web": {Name: "web", Image: "w"}, "api": {Name: "api", Image: "a"}}}
			o := New(p, rt, "opossum", &bytes.Buffer{})
			err := o.Kill(nil, "BOGUS")
			if !strings.Contains(calls(), "inspect-after web") {
				t.Fatalf("web's refusal must be asked about, calls:\n%s", calls())
			}
			if strings.Contains(calls(), "kill -s BOGUS api.demo.opossum") || o.wasStoppedByUs("api") {
				t.Errorf("api is not this project's to kill or record, calls:\n%s", calls())
			}
			if err == nil || !strings.HasPrefix(err.Error(), `killing service "web"`) {
				t.Fatalf("want web's refusal first, got %v", err)
			}
			const owners = "the runtime gave no readable answer about which project owns 1 container(s), so they were left: api.demo.opossum"
			if owner == "unanswered" && !strings.Contains(err.Error(), "\n\n"+owners) {
				t.Errorf("want api's unanswered owner after the refusal, a blank line apart, got:\n%v", err)
			}
			if owner == "otherproj" && strings.Contains(err.Error(), "left:") {
				t.Errorf("another project's container is no error, got:\n%v", err)
			}
		})
	}
}
