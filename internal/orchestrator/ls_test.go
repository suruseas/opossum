package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// Three projects and two containers that are not opossum's. shop has a
// running, a stopped and a stopping container, so the default view and --all
// disagree about it and a state that is neither running nor stopped is seen
// to count as not running; old has only stopped ones, so the default view
// hides it; blog's second container is named without a project or domain
// (a service run under an empty --dns-domain), so only the label can place
// it, and it sits between shop's containers, as a service added to a project
// later does on a real machine (the listing is in creation order), so that
// a fold which only joins adjacent rows is seen to split shop in two.
// Projects and states are listed out of order (shop, old, blog; stopping,
// running, stopped) so that an unsorted answer is visibly wrong, and shop's
// last container repeats a state seen two rows earlier, so that a fold which
// only merges a state into its immediate predecessor is seen to count it
// twice. buildkit
// carries no project label and the last one a label of someone else's.
const lsDoc = `[` +
	`{"configuration":{"id":"cache.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"stopping"}},` +
	`{"configuration":{"id":"web.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"running"}},` +
	`{"configuration":{"id":"api","labels":{"opossum.project":"blog"}},"status":{"state":"running"}},` +
	`{"configuration":{"id":"db.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"stopped"}},` +
	`{"configuration":{"id":"a.old.opossum","labels":{"opossum.project":"old"}},"status":{"state":"stopped"}},` +
	`{"configuration":{"id":"b.old.opossum","labels":{"opossum.project":"old"}},"status":{"state":"stopped"}},` +
	`{"configuration":{"id":"web.blog.opossum","labels":{"opossum.project":"blog"}},"status":{"state":"running"}},` +
	`{"configuration":{"id":"buildkit","labels":{"com.apple.container.plugin":"builder"}},"status":{"state":"running"}},` +
	`{"configuration":{"id":"stray","labels":{"other.project":"shop"}},"status":{"state":"running"}},` +
	`{"configuration":{"id":"worker.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"running"}}` +
	`]`

func TestListProjectsFoldsContainersByProjectLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		all  bool
		want []orchestrator.ProjectStatus
	}{
		{"running containers only, a project with none hidden", false, []orchestrator.ProjectStatus{
			{Name: "blog", Status: "running(2)"},
			{Name: "shop", Status: "running(2)"},
		}},
		{"--all counts every state, side by side", true, []orchestrator.ProjectStatus{
			{Name: "blog", Status: "running(2)"},
			{Name: "old", Status: "stopped(2)"},
			{Name: "shop", Status: "running(2), stopped(1), stopping(1)"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "CONTAINER_LS="+lsDoc)
			got, err := orchestrator.ListProjects(rt, tc.all)
			if err != nil {
				t.Fatalf("ListProjects: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("all=%v: got %v, want %v", tc.all, got, tc.want)
			}
		})
	}
}

func TestLsRendersTheThreeForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts orchestrator.LsOptions
		want string
	}{
		{"a NAME / STATUS table", orchestrator.LsOptions{All: true, Format: "table"},
			"NAME  STATUS\nblog  running(2)\nold   stopped(2)\nshop  running(2), stopped(1), stopping(1)\n"},
		{"names only under -q", orchestrator.LsOptions{All: true, Quiet: true, Format: "table"},
			"blog\nold\nshop\n"},
		{"-q wins over --format json, as under docker compose", orchestrator.LsOptions{All: true, Quiet: true, Format: "json"},
			"blog\nold\nshop\n"},
		{"a JSON array under --format json", orchestrator.LsOptions{All: true, Format: "json"},
			`[{"Name":"blog","Status":"running(2)"},{"Name":"old","Status":"stopped(2)"},{"Name":"shop","Status":"running(2), stopped(1), stopping(1)"}]` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "CONTAINER_LS="+lsDoc)
			var out bytes.Buffer
			if err := orchestrator.Ls(rt, &out, tc.opts); err != nil {
				t.Fatalf("Ls: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", out.String(), tc.want)
			}
		})
	}
	// The JSON form must parse back to what ListProjects returned — the
	// reader of `--format json` is a program.
	t.Run("the JSON form round-trips", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "CONTAINER_LS="+lsDoc)
		var out bytes.Buffer
		if err := orchestrator.Ls(rt, &out, orchestrator.LsOptions{Format: "json"}); err != nil {
			t.Fatalf("Ls: %v", err)
		}
		var got []orchestrator.ProjectStatus
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out.String())
		}
		want := []orchestrator.ProjectStatus{{Name: "blog", Status: "running(2)"}, {Name: "shop", Status: "running(2)"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func TestLsWithNothingToShow(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts orchestrator.LsOptions
		want string
	}{
		{"the table keeps its header", orchestrator.LsOptions{Format: "table"}, "NAME  STATUS\n"},
		{"-q prints nothing", orchestrator.LsOptions{Quiet: true, Format: "table"}, ""},
		{"json is an empty array, not null", orchestrator.LsOptions{Format: "json"}, "[]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "CONTAINER_LS=[]")
			var out bytes.Buffer
			if err := orchestrator.Ls(rt, &out, tc.opts); err != nil {
				t.Fatalf("Ls: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("got %q, want %q", out.String(), tc.want)
			}
		})
	}
	t.Run("a stopped runtime is reported, not shown as no projects", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "CONTAINER_LS="+lsDoc, "SYSTEM_STOPPED=1")
		var out bytes.Buffer
		err := orchestrator.Ls(rt, &out, orchestrator.LsOptions{Format: "table"})
		if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
			t.Errorf("want the runtime-stopped error (OPSM-405), got: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("nothing may be printed when the runtime is down, got %q", out.String())
		}
	})
}
