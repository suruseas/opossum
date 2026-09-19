package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `group_add` reaches `container run` as `--gid <n>` when the runtime can
// take it as written: one group, numeric, on a service that writes no
// `user:` (container 1.4.1, measured 2026-09-19: `--gid 1000` adds the
// group, `--gid 1000 --gid 2000` keeps only 2000, `--gid wheel` is refused,
// and beside `-u` — `1000:1000` or `1000` alone — `--gid` does nothing).
// Every other shape is refused before anything is created — by `up`, `run`
// and `run --audit` alike — rather than started without the group it asked
// for.
func TestGroupAddReachesTheRuntimeOrIsRefusedFirst(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []string
		user   string
		gid    string // what --gid carries; "" when the service is refused
		err    string
	}{
		{"one numeric group", []string{"2000"}, "", "2000", ""},
		{"no group", nil, "1000:1000", "", ""},
		{"beside a user without a gid", []string{"2000"}, "1000", "", `service "app" adds the group 2000 (group_add) beside user: "1000"; container 1.4.1's --gid does nothing next to --user`},
		{"beside a user by name", []string{"2000"}, "app", "", `beside user: "app"`},
		{"the largest gid the engine takes", []string{"2147483647"}, "", "2147483647", ""},
		{"two groups", []string{"2000", "3000"}, "", "", `service "app" adds 2 groups (group_add: "2000", "3000"); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"two groups, one empty", []string{"", "3000"}, "", "", `adds 2 groups (group_add: "", "3000")`},
		{"a group by name", []string{"wheel"}, "", "", `service "app" adds the group "wheel" (group_add); container 1.4.1's --gid takes a number, where docker compose resolves a name in the image — write the group's number`},
		{"an empty group", []string{""}, "", "", `service "app" adds an empty group (group_add: [""]); the docker engine refuses it too (` + "`unable to find group`" + `) — write the group's number, or drop the entry`},
		{"a negative number", []string{"-1"}, "", "", `service "app" adds the group -1 (group_add); a gid is not negative — the docker engine refuses it too`},
		{"a number out of the engine's range", []string{"2147483648"}, "", "", `service "app" adds the group 2147483648 (group_add), which is past 2147483647, the largest gid the docker engine takes`},
		// The quoted forms a number would have been read to: not digits, so
		// not a gid as --gid takes one.
		// `+2000` and `0002000` are 2000 to the runtime (container 1.4.1,
		// measured), so they pass as written.
		{"a signed number", []string{"+2000"}, "", "+2000", ""},
		{"a zero-padded number", []string{"0002000"}, "", "0002000", ""},
		{"two plus signs", []string{"++2000"}, "", "", `adds the group "++2000" (group_add), which is not the digits of a gid`},
		{"a hex number as written", []string{"0x10"}, "", "", `service "app" adds the group "0x10" (group_add), which is not the digits of a gid — write the number alone, as in ` + "`- 2000`"},
		{"a string with a space before", []string{" 2000"}, "", "", `adds the group " 2000" (group_add), which is not the digits of a gid`},
		{"a string with a space after", []string{"2000 "}, "", "", `adds the group "2000 " (group_add), which is not the digits of a gid`},
		{"a group by name with a digit in it", []string{"group1"}, "", "", `adds the group "group1" (group_add), which is not the digits of a gid`},
		{"beside a user with a gid", []string{"2000"}, "1000:1000", "", `service "app" adds the group 2000 (group_add) beside user: "1000:1000"; container 1.4.1's --gid does nothing next to --user — drop group_add (the process then runs without the group, and a socket or device that needs it refuses it), or drop user: (the image's own user then runs with the group)`},
		{"beside a user with a gid by name", []string{"2000"}, "app:app", "", `beside user: "app:app"`},
	} {
		for _, cmd := range []string{"up", "run", "run --audit"} {
			t.Run(cmd+", "+tc.name, func(t *testing.T) {
				rt, log := fakeShim(t)
				// Beside another service that sorts first, with its own
				// group, so that the service named in a refusal is the one
				// at fault and every service is looked at, not the first.
				p := project("demo", map[string]*compose.Service{
					"aaa": {Image: "alpine:3.20", GroupAdd: compose.GroupAdd{"4000"}},
					"app": {Image: "alpine:3.20", GroupAdd: compose.GroupAdd(tc.groups), User: tc.user},
				})
				o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
				var err error
				switch cmd {
				case "up":
					err = o.Up(true)
				case "run":
					err = o.RunOneOff("app", []string{"true"}, orchestrator.RunOneOffOptions{})
				default:
					_, err = o.RunAudited("app", []string{"true"}, orchestrator.RunOneOffOptions{})
				}
				if tc.err != "" {
					if err == nil || !strings.Contains(err.Error(), tc.err) {
						t.Fatalf("want a refusal saying %q, got %v", tc.err, err)
					}
					// Before anything was made: no run, no network, no volume.
					for _, made := range []string{"run ", "network create", "volume create"} {
						if i := indexOf(log(), made); i >= 0 {
							t.Errorf("refused, yet %q was done:\n%s", log()[i], strings.Join(log(), "\n"))
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				i := indexOf(log(), "--name app")
				if i < 0 {
					t.Fatalf("no run:\n%s", strings.Join(log(), "\n"))
				}
				run := " " + log()[i] + " "
				if got := strings.Contains(run, " --gid "+tc.gid+" "); tc.gid != "" && !got {
					t.Errorf("want --gid %s in the run:\n%s", tc.gid, run)
				}
				if tc.gid == "" && strings.Contains(run, " --gid ") {
					t.Errorf("no group asked for, yet --gid passed:\n%s", run)
				}
				if cmd == "up" {
					if j := indexOf(log(), "--name aaa"); j < 0 || !strings.Contains(" "+log()[j]+" ", " --gid 4000 ") {
						t.Errorf("the other service's own group should reach its run:\n%s", strings.Join(log(), "\n"))
					}
				}
			})
		}
	}
}

// `up --dry-run` plans what `up` would run, and `up` refuses these shapes:
// so does the plan, with the same words. Known difference: docker compose's
// `--dry-run` goes through (v5.5.1, measured 2026-09-19: `--dry-run up`
// with `group_add: [wheel]` prints the plan and exits 0), as docker takes
// every shape. A plan that goes through carries the --gid `up` would pass,
// and never one beside --user.
func TestGroupAddUnderDryRunIsRefusedOrPlannedAsUpWould(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []string
		user   string
		err    string
		plan   string // a substring the planned run must carry
	}{
		{"one numeric group", []string{"2000"}, "", "", " --gid 2000 "},
		{"a group by name", []string{"wheel"}, "", `adds the group "wheel"`, ""},
		{"two groups", []string{"2000", "3000"}, "", "adds 2 groups", ""},
		{"beside a user", []string{"2000"}, "1000:1000", `beside user: "1000:1000"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20", GroupAdd: compose.GroupAdd(tc.groups), User: tc.user}})
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			o.SetDryRun(true)
			err := o.Up(true)
			if i := indexOf(log(), "run "); i >= 0 {
				t.Errorf("a dry run ran something: %s", log()[i])
			}
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want the plan refused saying %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.plan) {
				t.Errorf("the plan should carry %q:\n%s", tc.plan, out.String())
			}
			if strings.Contains(out.String(), "--user") {
				t.Errorf("no user was written, yet the plan carries one:\n%s", out.String())
			}
		})
	}
}

// The check reaches the services a command starts, and those alone: `up app`
// leaves a sibling's group_add unread, as docker compose's `up app` does;
// `run needsdep` reads its dependency's, and `run --no-deps` does not.
func TestGroupAddIsCheckedOnTheServicesACommandStarts(t *testing.T) {
	p := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"app":      {Image: "alpine:3.20"},
			"other":    {Image: "alpine:3.20", GroupAdd: compose.GroupAdd{"wheel"}},
			"needsdep": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "other", Condition: compose.ConditionStarted}}},
		})
	}
	t.Run("up of a sibling goes ahead", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(p(), rt, "opossum", &bytes.Buffer{}).Up(true, "app"); err != nil {
			t.Fatalf("up app: %v", err)
		}
		if indexOf(log(), "--name app.demo.opossum") < 0 {
			t.Errorf("app did not start:\n%s", strings.Join(log(), "\n"))
		}
	})
	t.Run("up of everything is refused", func(t *testing.T) {
		rt, _ := fakeShim(t)
		if err := orchestrator.New(p(), rt, "opossum", &bytes.Buffer{}).Up(true); err == nil || !strings.Contains(err.Error(), `service "other" adds the group "wheel"`) {
			t.Fatalf("want other refused, got %v", err)
		}
	})
	t.Run("run reads its dependency's", func(t *testing.T) {
		rt, _ := fakeShim(t)
		if err := orchestrator.New(p(), rt, "opossum", &bytes.Buffer{}).RunOneOff("needsdep", []string{"true"}, orchestrator.RunOneOffOptions{}); err == nil || !strings.Contains(err.Error(), `service "other" adds the group "wheel"`) {
			t.Fatalf("want the dependency refused, got %v", err)
		}
	})
	t.Run("run --no-deps does not", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(p(), rt, "opossum", &bytes.Buffer{}).RunOneOff("needsdep", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
			t.Fatalf("run --no-deps: %v", err)
		}
		if indexOf(log(), "--name needsdep-run.demo.opossum") < 0 {
			t.Errorf("the one-off did not run:\n%s", strings.Join(log(), "\n"))
		}
	})
}

// A changed group recreates the container, as any change to what `run` is
// given does; the same file again is up to date.
func TestChangingGroupAddRecreatesTheContainer(t *testing.T) {
	rt, log := fakeShim(t)
	up := func(groups ...string) string {
		var out bytes.Buffer
		p := project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20", GroupAdd: compose.GroupAdd(groups)}})
		if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	runs := func() (n int) {
		for _, l := range log() {
			if strings.HasPrefix(l, "run ") && strings.Contains(l, "--name app.demo.opossum") {
				n++
			}
		}
		return n
	}
	up()
	if out := up("2000"); strings.Contains(out, "up to date") || runs() != 2 {
		t.Errorf("adding a group should recreate the container: %d runs\n%s", runs(), out)
	}
	if out := up("3000"); strings.Contains(out, "up to date") || runs() != 3 {
		t.Errorf("changing the group should recreate the container: %d runs\n%s", runs(), out)
	}
	if out := up("3000"); !strings.Contains(out, "up to date") || runs() != 3 {
		t.Errorf("the same group again should be up to date: %d runs\n%s", runs(), out)
	}
	if out := up(); strings.Contains(out, "up to date") || runs() != 4 {
		t.Errorf("dropping the group should recreate the container: %d runs\n%s", runs(), out)
	}
}
