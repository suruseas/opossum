package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// Each row of the stats snapshot is joined to the service whose container it
// names, not by position and not by reading the service out of the id: the
// runtime here answers in the reverse of the services' order, with different
// counters per container, for a service whose name has a dot in it, and with
// a reading for a container nobody asked about.
func TestStatsJSONJoinsEachRowToItsOwnService(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  inspect) echo '[{"status":{"state":"running","networks":[]},"configuration":{"id":"x","labels":{"opossum.project":"demo"}}}]' ;;
  stats) echo '[{"id":"db.demo.opossum","cpuUsageUsec":21,"memoryUsageBytes":22,"memoryLimitBytes":23,"networkRxBytes":24,"networkTxBytes":25,"blockReadBytes":26,"blockWriteBytes":27,"numProcesses":28},{"id":"other.demo.opossum","cpuUsageUsec":31,"memoryUsageBytes":32,"memoryLimitBytes":33,"networkRxBytes":34,"networkTxBytes":35,"blockReadBytes":36,"blockWriteBytes":37,"numProcesses":38},{"id":"api.v1.demo.opossum","cpuUsageUsec":11,"memoryUsageBytes":12,"memoryLimitBytes":13,"networkRxBytes":14,"networkTxBytes":15,"blockReadBytes":16,"blockWriteBytes":17,"numProcesses":18}]' ;;
  system) echo 'status running' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "c.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &runtime.Runtime{Bin: filepath.Join(dir, "c.sh")}
	p := project("demo", map[string]*compose.Service{
		"api.v1": {Image: "web:latest"},
		"db":     {Image: "postgres:16"},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: "json"}); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	want := `[{"Service":"api.v1","Container":"api.v1.demo.opossum","CPUUsageUsec":11,"MemoryUsageBytes":12,"MemoryLimitBytes":13,"NetworkRxBytes":14,"NetworkTxBytes":15,"BlockReadBytes":16,"BlockWriteBytes":17,"NumProcesses":18},` +
		`{"Service":"db","Container":"db.demo.opossum","CPUUsageUsec":21,"MemoryUsageBytes":22,"MemoryLimitBytes":23,"NetworkRxBytes":24,"NetworkTxBytes":25,"BlockReadBytes":26,"BlockWriteBytes":27,"NumProcesses":28}]` + "\n"
	if out.String() != want {
		t.Errorf("got  %s\nwant %s", out.String(), want)
	}
}

// The runtime prints no reading for a stopped container, so a stopped service
// has no row — and when every service is stopped the answer is an empty
// array, not null and not an error.
func TestStatsJSONHasNoRowForAStoppedService(t *testing.T) {
	p := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {Image: "web:latest"},
			"db":  {Image: "postgres:16"},
		})
	}
	for _, tc := range []struct {
		name, env, want string
	}{
		{"one of two stopped", "INSPECT_STOPPED=db.demo.opossum", `[{"Service":"web",`},
		{"every one stopped", "INSPECT_STATE=stopped", "[]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env)
			var out bytes.Buffer
			if err := orchestrator.New(p(), rt, "opossum", &out).Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: "json"}); err != nil {
				t.Fatalf("Stats: %v", err)
			}
			got := out.String()
			if tc.want == "[]\n" {
				if got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
				return
			}
			if !strings.HasPrefix(got, tc.want) || strings.Count(got, `"Service"`) != 1 {
				t.Errorf("want only web's row, got %s", got)
			}
		})
	}
}

// What the runtime does not report stays empty in JSON: the table's "-" is
// for a reader's eye, and a program would read it as an address or a port.
func TestPsJSONLeavesWhatIsNotThereEmpty(t *testing.T) {
	rt := fakeShimInspect(t, `[{"status":{"state":"stopped"},"configuration":{"labels":{"opossum.project":"demo"}}}]`, 0)
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Ps(orchestrator.PsOptions{Format: "json"}); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	want := `[{"Service":"db","Container":"db.demo.opossum","Image":"postgres:16","IP":"","Ports":"","Status":"stopped"}]` + "\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// A service named twice is one service: one row.
func TestStatsJSONNamesAServiceOnce(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Stats([]string{"web", "web"}, orchestrator.StatsOptions{NoStream: true, Format: "json"}); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if n := strings.Count(out.String(), `"Service":"web"`); n != 1 {
		t.Errorf("want one row for web, got %d: %s", n, out.String())
	}
}

// Rows follow the services: the order `up` starts them in when none is named
// (here z before a, though the runtime answers a first), and the order they
// were named in otherwise.
func TestStatsJSONRowsFollowTheServices(t *testing.T) {
	p := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"a": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "z"}}},
			"z": {Image: "postgres:16"},
		})
	}
	for _, tc := range []struct {
		name  string
		named []string
		want  []string
	}{
		{"none named: startup order", nil, []string{"z", "a"}},
		{"named: the order given", []string{"a", "z"}, []string{"a", "z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			var out bytes.Buffer
			if err := orchestrator.New(p(), rt, "opossum", &out).Stats(tc.named, orchestrator.StatsOptions{NoStream: true, Format: "json"}); err != nil {
				t.Fatalf("Stats: %v", err)
			}
			var rows []orchestrator.ServiceStat
			if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, out.String())
			}
			var got []string
			for _, r := range rows {
				got = append(got, r.Service)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("rows %q, want %q", got, tc.want)
			}
		})
	}
}

// With no container for any service, the JSON form says so the way the table
// does, and asks the runtime for nothing: `container stats` with no names
// would report every container on the machine.
func TestStatsJSONWithNoContainersSaysSo(t *testing.T) {
	rt, log := shimWhereSomeContainersDoNotExist(t, ".demo.")
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
		"db":  {Image: "postgres:16"},
	})
	var out bytes.Buffer
	err := orchestrator.New(p, rt, "opossum", &out).Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: "json"})
	if err == nil || !strings.Contains(err.Error(), "opossum up") {
		t.Fatalf("want an error pointing at `opossum up`, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be printed, got %q", out.String())
	}
	for _, l := range log() {
		if strings.HasPrefix(l, "stats") {
			t.Errorf("the runtime should not be asked for stats, got %q", l)
		}
	}
}
