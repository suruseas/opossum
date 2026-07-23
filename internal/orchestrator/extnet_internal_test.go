package orchestrator

// Evals for #291: `up` pre-flights that a network a service declares
// `external: true` actually exists — opossum uses external networks by name and
// never creates them, so a missing one should fail up front (OPSM-205) with a fix,
// not as a raw per-service "network not found" mid-start.

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

func extNetProject(networks map[string]compose.NetworkDecl, svcNets []string) *compose.Project {
	return &compose.Project{
		Name:     "pj",
		Networks: networks,
		Services: map[string]*compose.Service{
			"web": {Name: "web", Image: "web:latest", Networks: compose.ServiceNetworks(svcNets)},
		},
	}
}

// networkInspectShim: `network inspect` exits `inspectCode` (0 = exists, 1 = missing).
func networkInspectShim(t *testing.T, inspectCode int) *rt.Runtime {
	return scriptShim(t, "  network) if [ \"$2\" = inspect ]; then exit "+strconv.Itoa(inspectCode)+"; fi ;;\n")
}

func TestCheckExternalNetworksMissing(t *testing.T) {
	p := extNetProject(map[string]compose.NetworkDecl{"proxy": {External: true}}, []string{"proxy"})
	o := New(p, networkInspectShim(t, 1), "", &bytes.Buffer{}) // inspect fails -> missing
	err := o.checkExternalNetworks([]string{"web"})
	if err == nil {
		t.Fatal("a missing external network should fail the pre-flight")
	}
	for _, want := range []string{"[OPSM-205]", `"proxy"`, "external: true", "container network create proxy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-external-network error missing %q, got: %s", want, err)
		}
	}
}

func TestCheckExternalNetworksExists(t *testing.T) {
	p := extNetProject(map[string]compose.NetworkDecl{"proxy": {External: true}}, []string{"proxy"})
	o := New(p, networkInspectShim(t, 0), "", &bytes.Buffer{}) // inspect ok -> exists
	if err := o.checkExternalNetworks([]string{"web"}); err != nil {
		t.Errorf("an existing external network should pass, got: %v", err)
	}
}

func TestCheckExternalNetworksIgnoresNonExternal(t *testing.T) {
	// An internal (non-external) network is created by opossum, so it must NOT be
	// pre-flighted — even a shim that would report it missing must be left alone.
	p := extNetProject(map[string]compose.NetworkDecl{"backend": {Internal: true}}, []string{"backend"})
	o := New(p, networkInspectShim(t, 1), "", &bytes.Buffer{})
	if err := o.checkExternalNetworks([]string{"web"}); err != nil {
		t.Errorf("a non-external network must not be checked for existence, got: %v", err)
	}
}

// An external network with an explicit `name:` is checked (and named in the error)
// by that real name, not its compose key.
func TestCheckExternalNetworksUsesRealName(t *testing.T) {
	p := extNetProject(map[string]compose.NetworkDecl{"proxy": {External: true, Name: "real-proxy"}}, []string{"proxy"})
	o := New(p, networkInspectShim(t, 1), "", &bytes.Buffer{})
	err := o.checkExternalNetworks([]string{"web"})
	if err == nil || !strings.Contains(err.Error(), `"real-proxy"`) || strings.Contains(err.Error(), `"proxy"`) {
		t.Errorf("error should name the external network's real name %q, not its key, got: %v", "real-proxy", err)
	}
}

// Two services sharing one external network inspect it once, not per service.
func TestCheckExternalNetworksInspectsOnce(t *testing.T) {
	dir := t.TempDir()
	logf := filepath.Join(dir, "log")
	shim := filepath.Join(dir, "c.sh")
	body := "#!/bin/sh\necho \"$*\" >> " + logf + "\ncase \"$1\" in network) [ \"$2\" = inspect ] && exit 0 ;; esac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: "pj", Networks: map[string]compose.NetworkDecl{"proxy": {External: true}},
		Services: map[string]*compose.Service{
			"web": {Name: "web", Image: "x", Networks: compose.ServiceNetworks{"proxy"}},
			"api": {Name: "api", Image: "x", Networks: compose.ServiceNetworks{"proxy"}},
		}}
	if err := New(p, &rt.Runtime{Bin: shim}, "", &bytes.Buffer{}).checkExternalNetworks([]string{"web", "api"}); err != nil {
		t.Fatalf("both services on an existing external net should pass: %v", err)
	}
	log, _ := os.ReadFile(logf)
	if n := strings.Count(string(log), "network inspect"); n != 1 {
		t.Errorf("a shared external network should be inspected once, got %d:\n%s", n, log)
	}
}
