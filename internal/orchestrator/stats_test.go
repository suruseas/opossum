package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `--format json --no-stream` joins the service name onto `container stats`'s
// own guest-view snapshot, so a reader doesn't have to separately fetch `ps`
// and match containers back to services itself.
func TestStatsRendersJSON(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Stats(nil, orchestrator.StatsOptions{NoStream: true, Format: "json"}); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	want := []orchestrator.ServiceStat{
		{
			Service:          "web",
			Container:        "web.demo.opossum",
			CPUUsageUsec:     1500000,
			MemoryUsageBytes: 49283072,
			MemoryLimitBytes: 1073741824,
			NetworkRxBytes:   2048,
			NetworkTxBytes:   4096,
			BlockReadBytes:   8192,
			BlockWriteBytes:  16384,
			NumProcesses:     3,
		},
	}
	var got []orchestrator.ServiceStat
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A streaming JSON array isn't well-formed line by line, so `--format json`
// without `--no-stream` is refused rather than silently doing something
// unparseable.
func TestStatsJSONRequiresNoStream(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Stats(nil, orchestrator.StatsOptions{NoStream: false, Format: "json"})
	if err == nil || !strings.Contains(err.Error(), "--no-stream") {
		t.Errorf("want an error requiring --no-stream, got %v", err)
	}
}
