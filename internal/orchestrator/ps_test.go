package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The JSON form of `ps` must carry the same columns as the table (SERVICE,
// CONTAINER, IMAGE, IP, PORTS, STATUS) and round-trip through
// encoding/json — the reader of `--format json` is a program.
func TestPsRendersJSON(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Ps(orchestrator.PsOptions{Format: "json"}); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	want := []orchestrator.ServiceStatus{
		{Service: "db", Container: "db.demo.opossum", Image: "postgres:16", IP: "192.168.64.10", Ports: "0.0.0.0:8080->8080/tcp", Status: "running"},
	}
	var got []orchestrator.ServiceStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A service whose container does not exist gets no row in JSON either — an
// empty ps must serialize as `[]`, not `null`, so a caller doesn't have to
// special-case a nil slice.
func TestPsJSONIsEmptyArrayNotNull(t *testing.T) {
	rt := fakeShimInspect(t, "Error: container not found", 1)
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Ps(orchestrator.PsOptions{Format: "json"}); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	if out.String() != "[]\n" {
		t.Errorf("got %q, want %q", out.String(), "[]\n")
	}
}
