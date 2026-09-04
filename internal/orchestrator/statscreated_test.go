package orchestrator_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// shimWhereSomeContainersDoNotExist writes a `container` stand-in whose
// `inspect` says "container not found" (exit 1) for any name containing
// `absent`, and running for the rest, and logs every invocation — so a test can
// read exactly which names `stats` was handed.
func shimWhereSomeContainersDoNotExist(t *testing.T, absent string) (*runtime.Runtime, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> "%s"
case "$1" in
  inspect)
    case "$2" in
      *%s*) echo "Error: container not found: $2" >&2; exit 1 ;;
    esac
    echo '[{"status":{"state":"running","networks":[]},"configuration":{"id":"x","labels":{}}}]' ;;
  stats) ;;
  system) echo 'status running' ;;
esac
exit 0
`, logPath, absent)
	if err := os.WriteFile(filepath.Join(dir, "c.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func() []string {
		b, _ := os.ReadFile(logPath)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return &runtime.Runtime{Bin: filepath.Join(dir, "c.sh")}, read
}

// `container stats` 1.3.1 refuses the whole call when any name it is handed
// does not exist ("no such container", exit 1) — measured 2026-09-04, where
// 1.2.2 skipped the missing name. A project with a service that was never
// started must still show stats for the ones that were: only the containers
// that exist are asked for.
func TestStatsAsksOnlyForContainersThatExist(t *testing.T) {
	rt, log := shimWhereSomeContainersDoNotExist(t, "db")
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
		"db":  {Image: "postgres:16"},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Stats(nil, true); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var stats []string
	for _, l := range log() {
		if strings.HasPrefix(l, "stats") {
			stats = append(stats, l)
		}
	}
	if len(stats) != 1 {
		t.Fatalf("want exactly one stats call, got %v", stats)
	}
	if !strings.Contains(stats[0], "web.demo.opossum") {
		t.Errorf("the container that exists should be asked for, got %q", stats[0])
	}
	if strings.Contains(stats[0], "db.demo.opossum") {
		t.Errorf("a container that does not exist must not be handed to stats (1.3.1 fails the whole call on it), got %q", stats[0])
	}
}

// When no service has a container, there is nothing to hand to `stats` — and
// handing it nothing would show every running container on the machine, other
// projects included. Say so instead.
func TestStatsWithNoContainersSaysSoInsteadOfAskingForEverything(t *testing.T) {
	rt, log := shimWhereSomeContainersDoNotExist(t, ".demo.")
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
		"db":  {Image: "postgres:16"},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Stats(nil, true)
	if err == nil || !strings.Contains(err.Error(), "opossum up") {
		t.Fatalf("want an error pointing at `opossum up`, got %v", err)
	}
	for _, l := range log() {
		if strings.HasPrefix(l, "stats") {
			t.Errorf("stats must not be called with no names (that would list every container on the machine), got %q", l)
		}
	}
}

// StatsHost takes the same road: the guest-view snapshot asks only for the
// containers that exist, so one never-started service does not blank the
// guest column for the others.
func TestStatsHostSnapshotAsksOnlyForContainersThatExist(t *testing.T) {
	rt, log := shimWhereSomeContainersDoNotExist(t, "db")
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
		"db":  {Image: "postgres:16"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.HostFP = fakeFootprinter{"web.demo.opossum": 1024 * 1024}
	if err := o.StatsHost(nil); err != nil {
		t.Fatalf("StatsHost: %v", err)
	}
	for _, l := range log() {
		if strings.HasPrefix(l, "stats") && strings.Contains(l, "db.demo.opossum") {
			t.Errorf("the snapshot must not name a container that does not exist, got %q", l)
		}
	}
	// Narrowing what is asked for must not narrow the table: the service with
	// no container keeps its row, reading "—" in both columns — one row per
	// service is the table's contract, and a filter applied to the rows would
	// have quietly dropped it.
	var dbRow string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "db") {
			dbRow = l
		}
	}
	if dbRow == "" {
		t.Fatalf("the service without a container should still have a row, got:\n%s", out.String())
	}
	if strings.Count(dbRow, "—") != 2 {
		t.Errorf("a service without a container reads — in both columns, got %q", dbRow)
	}
}

// A container that exists but is stopped is not the same as one that does not
// exist: both versions of `container stats` skip a stopped one quietly, so it
// stays in the list — narrowing to "running" would drop a service the user
// asked about for no reason. (Both sides of the branch: absent is dropped
// above, stopped is kept here.)
func TestStatsKeepsStoppedContainersThatExist(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> "%s"
case "$1" in
  inspect)
    case "$2" in
      *db*) echo '[{"status":{"state":"stopped","networks":[]},"configuration":{"id":"x","labels":{}}}]' ;;
      *) echo '[{"status":{"state":"running","networks":[]},"configuration":{"id":"x","labels":{}}}]' ;;
    esac ;;
  stats) ;;
  system) echo 'status running' ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(dir, "c.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &runtime.Runtime{Bin: filepath.Join(dir, "c.sh")}
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
		"db":  {Image: "postgres:16"},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Stats(nil, true); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	b, _ := os.ReadFile(logPath)
	var stats string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "stats") {
			stats = l
		}
	}
	if !strings.Contains(stats, "db.demo.opossum") || !strings.Contains(stats, "web.demo.opossum") {
		t.Errorf("a stopped container exists and must still be asked for, got %q", stats)
	}
}
