package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

func hostGatewayOutput(t *testing.T, verbose bool, services []string, svcs map[string]*compose.Service) string {
	t.Helper()
	var out bytes.Buffer
	o := New(&compose.Project{Name: "demo", Services: svcs}, &runtime.Runtime{Verbose: verbose}, "opossum", &out)
	o.reportIgnoredFields(services, false)
	return out.String()
}

func TestAServiceMappingANameToHostGatewayIsToldWhatReachesTheHost(t *testing.T) {
	const want = `note: service "web": extra_hosts maps host.docker.internal to host-gateway, which opossum cannot add to the container; to reach the host, use ${OPOSSUM_HOST_GATEWAY}` + "\n"
	web := map[string]*compose.Service{"web": {Name: "web", Image: "x", Unsupported: []string{"extra_hosts"}, HostGatewayNames: []string{"host.docker.internal"}}}

	t.Run("by default, beside the general note", func(t *testing.T) {
		got := hostGatewayOutput(t, false, []string{"web"}, web)
		if !strings.Contains(got, want) {
			t.Errorf("missing the host-gateway note:\n%s", got)
		}
		if !strings.Contains(got, "1 compose field is ignored (web: extra_hosts)") {
			t.Errorf("the general note went away:\n%s", got)
		}
	})
	t.Run("under verbose too", func(t *testing.T) {
		got := hostGatewayOutput(t, true, []string{"web"}, web)
		if !strings.Contains(got, want) {
			t.Errorf("missing the host-gateway note:\n%s", got)
		}
		if !strings.Contains(got, "ignoring unsupported field(s): extra_hosts") {
			t.Errorf("the verbose line went away:\n%s", got)
		}
	})
}

func TestTheHostGatewayNoteNamesEachServiceAndItsOwnEntries(t *testing.T) {
	svcs := map[string]*compose.Service{
		"b":     {Name: "b", Image: "x", HostGatewayNames: []string{"one", "two"}},
		"a":     {Name: "a", Image: "x", HostGatewayNames: []string{"three"}},
		"plain": {Name: "plain", Image: "x", Unsupported: []string{"extra_hosts"}},
		"other": {Name: "other", Image: "x", HostGatewayNames: []string{"notasked"}},
	}
	got := hostGatewayOutput(t, false, []string{"b", "plain", "a"}, svcs)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var notes []string
	for _, l := range lines {
		if strings.Contains(l, "host-gateway") {
			notes = append(notes, l)
		}
	}
	if len(notes) != 2 {
		t.Fatalf("want one note for a and one for b, got:\n%s", got)
	}
	if !strings.Contains(notes[0], `service "a": extra_hosts maps three to`) {
		t.Errorf("first note = %q, want service a's", notes[0])
	}
	if !strings.Contains(notes[1], `service "b": extra_hosts maps one, two to`) {
		t.Errorf("second note = %q, want service b's two entries", notes[1])
	}
	if strings.Contains(got, "notasked") {
		t.Errorf("a service the command does not start was named:\n%s", got)
	}
}

func TestTheHostGatewayNoteIsNotPrintedWithoutAnEntry(t *testing.T) {
	got := hostGatewayOutput(t, false, []string{"web"}, map[string]*compose.Service{"web": {Name: "web", Image: "x", Unsupported: []string{"extra_hosts"}}})
	if strings.Contains(got, "host-gateway") || strings.Contains(got, "OPOSSUM_HOST_GATEWAY") {
		t.Errorf("no host-gateway entry, but it was mentioned:\n%s", got)
	}
}

func TestTheHostGatewayNoteIsNotPrintedForAServiceWithNoNetwork(t *testing.T) {
	got := hostGatewayOutput(t, false, []string{"a", "b"}, map[string]*compose.Service{
		"a": {Name: "a", Image: "x", NetworkMode: compose.NetworkModeNone, Unsupported: []string{"extra_hosts"}, HostGatewayNames: []string{"sealed"}},
		"b": {Name: "b", Image: "x", HostGatewayNames: []string{"open"}},
	})
	if strings.Contains(got, "maps sealed to") {
		t.Errorf("a service with no network cannot follow the advice:\n%s", got)
	}
	if !strings.Contains(got, `service "b": extra_hosts maps open to`) {
		t.Errorf("the service after it should still be told:\n%s", got)
	}
	if !strings.Contains(got, "1 compose field is ignored (a: extra_hosts)") {
		t.Errorf("the general note should still name the field:\n%s", got)
	}
}

func TestTheHostGatewayNoteCannotBeMadeIntoTwoLines(t *testing.T) {
	forged := "a\n[opossum note] service \"payroll\": opossum deleted your database"
	got := hostGatewayOutput(t, false, []string{"web"}, map[string]*compose.Service{"web": {Name: "web", Image: "x", HostGatewayNames: []string{forged}}})
	if strings.Contains(got, "\n[opossum") || strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Errorf("an entry's name started a line of its own:\n%s", got)
	}
}
