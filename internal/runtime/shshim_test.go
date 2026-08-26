package runtime_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shPath is the hand-smoke shim, from this package's directory.
const shPath = "../../testdata/fake-container.sh"

// The shell shim honours the flags that change what a listing contains.
//
// This shim is the one a person points OPOSSUM_CONTAINER_BIN at to try opossum
// without a runtime; the Go tests drive a different one. Until this test it was
// read by nothing at all — the only thing looking at it checked its path, size
// and shebang — so a command added to it could be wrong, or absent, and every
// gate stayed green.
//
// What is checked is that the flags matter: `--format json` or it is a table,
// `-a` or a caller sees less. Not that the content matches the real CLI's — a
// shell script cannot filter JSON, so without `-a` this one answers the empty
// list where the real CLI would answer with the running containers. That
// difference is written down in the shim and is the reason anything needing the
// running half uses the Go shim instead.
func TestTheShellShimHonoursTheListingFlags(t *testing.T) {
	abs, err := filepath.Abs(shPath)
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("sh", append([]string{abs}, args...)...)
		cmd.Env = append(cmd.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// With --format json, something a JSON parser reads.
	var nets []struct {
		Configuration struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	out := run(t, nil, "network", "ls", "--format", "json")
	if err := json.Unmarshal([]byte(out), &nets); err != nil {
		t.Fatalf("network ls --format json: %v\n%s", err, out)
	}
	if len(nets) != 1 || nets[0].Configuration.Name != "default" {
		t.Errorf("the default machine holds only the runtime's own network, got %s", out)
	}
	if _, builtin := nets[0].Configuration.Labels["com.apple.container.resource.role"]; !builtin {
		t.Errorf("and it is marked as the runtime's own, got %s", out)
	}

	// Without it, a table — which is what the real CLI prints, and what makes a
	// caller who forgot the flag fail here the way they would there.
	if out := run(t, nil, "network", "ls"); strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("no --format json, no JSON: %s", out)
	}
	if out := run(t, nil, "ls", "-a"); strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("no --format json, no JSON: %s", out)
	}

	// The inspect document carries both lists called networks, holding different
	// names — so that a parser reading the wrong one produces a visibly wrong
	// answer here rather than the same answer twice.
	var insp []struct {
		Status struct {
			Networks []struct {
				Network     string `json:"network"`
				IPv4Address string `json:"ipv4Address"`
			} `json:"networks"`
		} `json:"status"`
		Configuration struct {
			Networks []struct {
				Network string `json:"network"`
			} `json:"networks"`
		} `json:"configuration"`
	}
	out = run(t, nil, "inspect", "web")
	if err := json.Unmarshal([]byte(out), &insp); err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	if len(insp) != 1 || len(insp[0].Status.Networks) != 1 || len(insp[0].Configuration.Networks) != 1 {
		t.Fatalf("inspect should carry both lists: %s", out)
	}
	if a, b := insp[0].Status.Networks[0].Network, insp[0].Configuration.Networks[0].Network; a == b {
		t.Errorf("the two lists hold the same name (%q), so this fixture cannot show which one a parser read", a)
	}
	if insp[0].Status.Networks[0].IPv4Address == "" {
		t.Errorf("the address is on the running side: %s", out)
	}

	// The container listing, and the -a that decides whether stopped ones appear.
	const doc = `[{"configuration":{"id":"cache.proj.opossum","networks":[{"network":"proj-net"}]},` +
		`"status":{"networks":[],"state":"stopped"}}]`
	if got := strings.TrimSpace(run(t, []string{"CONTAINER_LS=" + doc}, "ls", "-a", "--format", "json")); got != doc {
		t.Errorf("ls -a --format json = %s", got)
	}
	if got := strings.TrimSpace(run(t, []string{"CONTAINER_LS=" + doc}, "ls", "--format", "json")); got != "[]" {
		t.Errorf("without -a the stopped one is not listed, got %s", got)
	}
	// The same request written the other way round. A shim that answers only
	// one argument order is testing the order rather than the flag.
	if got := strings.TrimSpace(run(t, []string{"CONTAINER_LS=" + doc}, "ls", "--format", "json", "-a")); got != doc {
		t.Errorf("ls --format json -a = %s", got)
	}
	// And the two spellings of the network question describe one machine.
	if a, b := run(t, nil, "network", "ls"), run(t, nil, "network", "list"); a != b {
		t.Errorf("`network ls` says\n%s\nand `network list` says\n%s", a, b)
	}
}
