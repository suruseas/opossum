package compose

import (
	"strconv"
	"strings"
	"testing"
)

// The network names container 1.4.1 creates, measured with `container network
// create` one name at a time: lower case, digits, `.`, `_` and `-`, a letter
// or digit at both ends, at most 63 characters.
func TestValidRuntimeNetworkNameIsWhatTheRuntimeCreates(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"n1003..x", true},
		{"n1003-0", true},
		{"a", true},
		{"a_b.c-d", true},
		{"n1003-" + strings.Repeat("k", 57), true},  // 63
		{"n1003-" + strings.Repeat("k", 58), false}, // 64
		{"n1003-x-", false},
		{"n1003-x.", false},
		{"n1003-x_", false},
		{"n1003-Xy", false},
		{"n1003-x+y", false},
		{"n1003-x y", false},
		{"-x", false},
		{".x", false},
		{"", false},
	} {
		t.Run(strconv.Quote(tc.name), func(t *testing.T) {
			if got := ValidRuntimeNetworkName(tc.name); got != tc.want {
				t.Errorf("ValidRuntimeNetworkName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A declared key folds to what the runtime takes after `<project>-`: a key it
// already takes is unchanged (so is the network a running project has), and
// every other one ends as a name the runtime creates — or as nothing.
func TestNetworkRuntimeKeyFoldsAKeyTheRuntimeRefuses(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"backend", "backend"},
		{"a..b", "a..b"},
		{"a_b.c-d", "a_b.c-d"},
		{"0", "0"},
		{"-a", "-a"},
		{"backEnd", "backend"},
		{"BACKEND", "backend"},
		{"a+b", "a-b"},
		{"a b", "a-b"},
		{"a/b", "a-b"},
		{"a++b", "a--b"},
		{"aéb", "a-b"},
		{"x-", "x"},
		{"x.", "x"},
		{"x_", "x"},
		{"x+", "x"},
		{"x-._", "x"},
		{"a.b-", "a.b"},
		{"+", ""},
		{"_", ""},
	} {
		t.Run(strconv.Quote(tc.key), func(t *testing.T) {
			got := NetworkRuntimeKey(tc.key)
			if got != tc.want {
				t.Errorf("NetworkRuntimeKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
			if got != "" && !ValidRuntimeNetworkName("demo-"+got) {
				t.Errorf("demo-%s is not a name the runtime creates", got)
			}
		})
	}
}

// What reaches the runtime from a network a service joins: an external one by
// its real name as written, and a key that folds to nothing names no network.
func TestANetworkNameThatReachesTheRuntimeIsOneItCanCreate(t *testing.T) {
	long := strings.Repeat("k", 64)
	for _, tc := range []struct {
		name, key, decl, refusal string
	}{
		{"an external key the runtime creates", "proxy", "{external: true}", ""},
		{"an external key with a capital", "Proxy", "{external: true}", `external network "Proxy" is used by that name, which ` + runtimeNetworkNameRule + "; set `name:` to the network's real name"},
		{"an external key ending in a dash", "p-", "{external: true}", `external network "p-" is used by that name`},
		{"an external key of 64 characters", long, "{external: true}", `external network "` + long + `" is used by that name`},
		{"an external key of 63 characters", long[1:], "{external: true}", ""},
		{"an external key with a name of its own", "Proxy", "{external: true, name: proxy}", ""},
		{"an external name with a capital", "proxy", "{external: true, name: Proxy}", `external network "proxy": name "Proxy" ` + runtimeNetworkNameRule},
		{"an external name ending in an underscore", "proxy", "{external: true, name: p_}", `external network "proxy": name "p_" `},
		{"a key with a capital", "backEnd", "{}", ""},
		{"a key ending in a plus", "a+", "{}", ""},
		{"a key of 64 characters (the project name settles its length)", long, "{}", ""},
		{"a key that folds to nothing", "+", "{}", `network "+" keeps no character a network name can hold once folded to what the container runtime (1.4.1) takes (lower-case a-z, 0-9, and ` + "`.`, `_` or `-` before the end) — rename the key"},
		{"an internal key that folds to nothing", "_", "{internal: true}", `network "_" keeps no character`},
		{"a key of a letter outside a-z", "é", "{}", `network "é" keeps no character`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := strconv.Quote(tc.key)
			body := "services:\n  web:\n    image: alpine\n    networks: [" + key + "]\nnetworks:\n  " + key + ": " + tc.decl + "\n"
			_, err := Load(writeTemp(t, body))
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), `service "web": `+tc.refusal)):
				t.Errorf("\n got %v\nwant it to contain %q", err, `service "web": `+tc.refusal)
			}
		})
	}
}

// A declaration no service joins reaches no runtime, as with volumes.
func TestAnUnusedNetworkDeclarationIsNotHeldToTheRuntimeRule(t *testing.T) {
	for _, decl := range []string{`Proxy: {external: true}`, `proxy: {external: true, name: "p-"}`, `"+": {}`, `backEnd: {}`} {
		t.Run(decl, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    networks: [backend]\nnetworks:\n  backend: {}\n  " + decl + "\n"
			if _, err := Load(writeTemp(t, body)); err != nil {
				t.Errorf("an unused declaration should not stop the file loading, got %v", err)
			}
		})
	}
}

// Two networks the services join that fold to one runtime network would be one
// network — docker compose gives each its own — so CheckNetworkKeys refuses
// them, naming both keys in the same order whichever service joins which. The
// file still loads: a key `net` beside the default network ran before, and
// `down` has to read it.
func TestNetworksThatFoldToOneRuntimeNetworkAreRefused(t *testing.T) {
	const pair = "networks \"backEnd\" and \"backend\" both become the runtime network `<project>-backend`"
	const onDefault = "network %q becomes the runtime network `<project>-net`, which is the network services without `networks:` join — rename the key"
	for _, tc := range []struct {
		name, services, networks, refusal string
	}{
		{"three services, three spellings", "  api:\n    image: a\n    networks: [backEnd]\n  db:\n    image: a\n    networks: [BACKEND]\n  web:\n    image: a\n    networks: [backend]\n", "  backEnd: {}\n  BACKEND: {}\n  backend: {}\n", "networks \"BACKEND\" and \"backEnd\" both become the runtime network `<project>-backend`"},
		{"two services, the capital first", "  api:\n    image: a\n    networks: [backEnd]\n  web:\n    image: a\n    networks: [backend]\n", "  backEnd: {}\n  backend: {}\n", pair},
		{"two services, the capital second", "  api:\n    image: a\n    networks: [backend]\n  web:\n    image: a\n    networks: [backEnd]\n", "  backEnd: {}\n  backend: {}\n", pair},
		{"one service joining both", "  web:\n    image: a\n    networks: [backend, backEnd]\n", "  backEnd: {}\n  backend: {}\n", pair},
		{"a trailing character dropped", "  web:\n    image: a\n    networks: [back, back_]\n", "  back: {}\n  back_: {}\n", "networks \"back\" and \"back_\" both become the runtime network `<project>-back`"},
		{"a key folding to the default network, the defaulted service first", "  api:\n    image: a\n  web:\n    image: a\n    networks: [NET]\n", "  NET: {}\n", strings.Replace(onDefault, "%q", `"NET"`, 1)},
		{"a key folding to the default network, the defaulted service second", "  api:\n    image: a\n    networks: [NET]\n  web:\n    image: a\n", "  NET: {}\n", strings.Replace(onDefault, "%q", `"NET"`, 1)},
		{"the key net itself beside the default network", "  api:\n    image: a\n    networks: [net]\n  web:\n    image: a\n", "  net: {}\n", strings.Replace(onDefault, "%q", `"net"`, 1)},
		{"two services on one key", "  api:\n    image: a\n    networks: [backEnd]\n  web:\n    image: a\n    networks: [backEnd]\n", "  backEnd: {}\n", ""},
		{"two services on the default network", "  api:\n    image: a\n  web:\n    image: a\n", "  unused: {}\n", ""},
		{"a folded key nobody else takes", "  web:\n    image: a\n    networks: [backEnd, front]\n", "  backEnd: {}\n  front: {}\n", ""},
		{"a colliding key no service joins", "  web:\n    image: a\n    networks: [backend]\n", "  backEnd: {}\n  backend: {}\n", ""},
		{"a key folding to net while no service is on the default network", "  web:\n    image: a\n    networks: [NET]\n", "  NET: {}\n", ""},
		{"a key folding to net beside an isolated service", "  api:\n    image: a\n    network_mode: none\n  web:\n    image: a\n    networks: [NET]\n", "  NET: {}\n", ""},
		{"an external network first on a service, the collision after it", "  api:\n    image: a\n    networks: [ext, backEnd]\n  web:\n    image: a\n    networks: [backend]\n", "  ext: {external: true, name: proxy}\n  backEnd: {}\n  backend: {}\n", pair},
		{"an external network beside the key it is spelled like", "  web:\n    image: a\n    networks: [backend, BackEnd]\n", "  backend: {}\n  BackEnd: {external: true, name: backend}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n"+tc.services+"networks:\n"+tc.networks))
			if err != nil {
				t.Fatalf("want the file loaded whatever its keys fold to, got %v", err)
			}
			err = p.CheckNetworkKeys()
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("\n got %v\nwant it to contain %q", err, tc.refusal)
			}
		})
	}
}

// Which pair is reported does not depend on map order: the services are read
// in name order, so the same file names the same two keys on every run.
func TestTheRefusedPairIsTheSameOnEveryRun(t *testing.T) {
	path := writeTemp(t, "services:\n  api:\n    image: a\n    networks: [backEnd]\n  db:\n    image: a\n    networks: [BACKEND]\n  web:\n    image: a\n    networks: [backend]\n  zed:\n    image: a\n    networks: [Backend]\nnetworks:\n  backEnd: {}\n  BACKEND: {}\n  backend: {}\n  Backend: {}\n")
	const want = "networks \"BACKEND\" and \"backEnd\" both become"
	for i := 0; i < 30; i++ {
		p, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.CheckNetworkKeys(); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("run %d: got %v, want it to start %q", i, err, want)
		}
	}
}
