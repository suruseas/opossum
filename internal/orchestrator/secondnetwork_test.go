package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// namedPairs reads the pairs out of the warnings, as `"answers"->"asks"`, in
// the order they were written. A one-off is written two ways — named where a
// reader has to type the name (the side that answers) and unnamed where the
// name is only there to say who cannot reach (the side that asks) — so both
// come back here as the name its container carries.
func namedPairs(out string) []string {
	oneOff := func(s string) (string, bool) {
		_, rest, ok := strings.Cut(s, `the one-off this run starts for service "`)
		if !ok {
			return "", false
		}
		svc, _, _ := strings.Cut(rest, `"`)
		return `"` + svc + `-run"`, true
	}
	var pairs []string
	for _, line := range strings.Split(out, "\n") {
		_, rest, ok := strings.Cut(line, "[OPSM-211] ")
		if !ok {
			continue
		}
		to, _, _ := strings.Cut(rest, " answers")
		if named, isOneOff := strings.CutPrefix(to, "the one-off "); isOneOff {
			to, _, _ = strings.Cut(named, ",")
		} else {
			to = strings.TrimPrefix(to, "service ")
		}
		from := line[strings.LastIndex(line, "so ")+len("so "):]
		from, _, _ = strings.Cut(from, " —")
		if name, isOneOff := oneOff(from); isOneOff {
			from = name
		} else {
			from = strings.TrimPrefix(from, "service ")
		}
		pairs = append(pairs, to+"->"+from)
	}
	return pairs
}

// container 1.4.1 registers only a container's first attachment in DNS, so a
// service on two networks answers by name with the address on the network it
// is attached to first. A service that shares one of the later networks with it, but
// not the first, does not reach it by that name — the address it gets back is
// on a network it is not on (measured 2026-09-18: `wget http://api:8080/`
// times out from such a peer, and the address on the shared network answers).
// docker compose answers with an address the asking service can reach
// (measured on v5.5.1: one address, on a network the two share), so `up` says
// which pairs are in that position, naming both services and the network they
// do share.
func TestUpSaysWhichPeersCannotReachAServiceByName(t *testing.T) {
	const twoNetworks = "services:\n" +
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
		"  frontend:\n    image: alpine:3.20\n    networks: [front]\n" +
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n" +
		"networks:\n  front: {}\n  back: {}\n"
	for _, tc := range []struct {
		name, body string
		want       []string // lines that must be in the warning
		absent     []string // services the warning must not name
	}{
		{
			"a peer on the second network only",
			twoNetworks,
			[]string{`[OPSM-211] service "api" answers by name with its address on network "front"`, `service "worker" —`,
				`which shares "back" with it but not "front"`, `Attach "back" to "api"`, `have "worker" use the address`},
			[]string{`"frontend" —`},
		},
		{
			// The peer shares the network api is attached to first, so the
			// address it gets back is one it is on.
			"a peer on the first network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
				"  frontend:\n    image: alpine:3.20\n    networks: [front]\n" +
				"networks:\n  front: {}\n  back: {}\n",
			nil, []string{"answers by name"},
		},
		{
			// One network each: there is only one address to answer with.
			"services on one network each",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front]\n" +
				"  frontend:\n    image: alpine:3.20\n    networks: [front]\n" +
				"networks:\n  front: {}\n",
			nil, []string{"answers by name"},
		},
		{
			// The peer is on both too, so it shares the first network.
			"a peer on both networks",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
				"  worker:\n    image: alpine:3.20\n    networks: [back, front]\n" +
				"networks:\n  front: {}\n  back: {}\n",
			nil, []string{"answers by name"},
		},
		{
			// No network in common: the peer does not reach it at all, which
			// is what the file asks for.
			"a peer on neither network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
				"  other:\n    image: alpine:3.20\n    networks: [side]\n" +
				"networks:\n  front: {}\n  back: {}\n  side: {}\n",
			nil, []string{"answers by name"},
		},
		{
			// Two keys declared `external: true` under one name are one
			// network: the peer is on the network api answers on, spelled
			// another way, so it reaches it by name.
			"two external keys naming one network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [aext, back]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [bext, back]\n" +
				"networks:\n  aext: {external: true, name: shared}\n  bext: {external: true, name: shared}\n  back: {}\n",
			nil, []string{"answers by name"},
		},
		{
			// Of two networks the peer shares, the refusal names one, and
			// the same one every time.
			"a peer sharing two later networks",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, zback, aside]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [zback, aside]\n" +
				"networks:\n  front: {}\n  zback: {}\n  aside: {}\n",
			[]string{`which shares "aside" with it but not "front"`, `Attach "aside" to "api"`},
			[]string{`shares "zback"`},
		},
		{
			// Of two shared networks, the one named is the one that is not
			// internal — whichever way the two are spelled, since the choice
			// is not the name order.
			"an internal network shared beside a normal one, internal first by name",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, aside, zback]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [zback, aside]\n" +
				"networks:\n  front: {}\n  aside: {internal: true}\n  zback: {}\n",
			[]string{`which shares "zback" with it but not "front"`, `Attach "zback" to "api"`},
			[]string{`shares "aside"`},
		},
		{
			"an internal network shared beside a normal one, normal first by name",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, zside, aback]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [aback, zside]\n" +
				"networks:\n  front: {}\n  zside: {internal: true}\n  aback: {}\n",
			[]string{`which shares "aback" with it but not "front"`, `Attach "aback" to "api"`},
			[]string{`shares "zside"`},
		},
		{
			// Three shared networks, and the one that is not internal is
			// neither first nor last by name: the choice is not the order.
			"the only network that is not internal is in the middle",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, bnet, cnet, dnet]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [cnet, bnet, dnet]\n" +
				"networks:\n  front: {}\n  bnet: {internal: true}\n  cnet: {}\n  dnet: {internal: true}\n",
			[]string{`which shares "cnet" with it but not "front"`, `Attach "cnet" to "api"`},
			[]string{`shares "bnet"`, `shares "dnet"`},
		},
		{
			// The one that cannot resolve is not told it cannot reach the
			// other: `peer` is attached first to an internal network of its
			// own — one `api` is not even on — so it looks up nothing, and
			// the line about `api` is left out. (The line the other way
			// round, about `peer` answering on that network, still stands:
			// `api` does resolve.)
			"the asking side is attached first to an internal network of its own",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [pint, back]\n" +
				"networks:\n  front: {}\n  back: {}\n  pint: {internal: true}\n",
			[]string{`service "peer" answers by name`},
			[]string{`service "api" answers by name`},
		},
		{
			// The peer's own first network is internal, so its resolver — the
			// gateway of that network — answers nothing at all: it looks up
			// no name, and reordering api's networks does not change that
			// (measured 2026-09-21). That is [OPSM-203]'s subject.
			"the peer is attached to an internal network first",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, aside, zback]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [aside, zback]\n" +
				"networks:\n  front: {}\n  aside: {internal: true}\n  zback: {}\n",
			[]string{"[OPSM-203] network"}, []string{"answers by name"},
		},
		{
			// The peer resolves (its own first network is not internal), and
			// everything the two share is internal. Attaching one of those
			// first has not been measured, so there is nothing to advise
			// here and the message about internal networks is what is left.
			"every shared network is internal",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, aside, zside]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [pown, aside, zside]\n" +
				"networks:\n  front: {}\n  pown: {}\n  aside: {internal: true}\n  zside: {internal: true}\n",
			// The message about internal networks is what says why, and it
			// is the one this leaves the pair to.
			[]string{"[OPSM-203] network"}, []string{"answers by name"},
		},
		{
			// The shared network answers no name at all, so there is nothing
			// to advise: [OPSM-203] says so for everyone on it.
			"the shared network is internal",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
				"  worker:\n    image: alpine:3.20\n    networks: [back]\n" +
				"networks:\n  front: {}\n  back: {internal: true}\n",
			nil, []string{"answers by name"},
		},
		{
			// Of two keys the service itself writes for one network, the
			// message names the first it is attached to under that name.
			"the service writes two keys for one network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [back, aext, bext]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [cext]\n" +
				"networks:\n  back: {}\n  aext: {external: true, name: shared}\n" +
				"  bext: {external: true, name: shared}\n  cext: {external: true, name: shared}\n",
			[]string{`which shares "aext" with it but not "back"`, `Attach "aext" to "api"`},
			[]string{`shares "bext"`, `shares "cext"`},
		},
		{
			// The key the message names is the one the service that answers
			// writes, which is the one the advice tells the reader to move.
			"the peer writes another key for the shared network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, aext]\n" +
				"  peer:\n    image: alpine:3.20\n    networks: [bext]\n" +
				"networks:\n  front: {}\n  aext: {external: true, name: shared}\n  bext: {external: true, name: shared}\n",
			[]string{`which shares "aext" with it but not "front"`, `Attach "aext" to "api"`},
			[]string{`shares "bext"`},
		},
		{
			// `network_mode: none` has no network at all.
			"a peer with no network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
				"  worker:\n    image: alpine:3.20\n    network_mode: none\n" +
				"networks:\n  front: {}\n  back: {}\n",
			nil, []string{"answers by name"},
		},
		{
			// Both peers are on a later network only: each is named, with the
			// network it shares.
			"two peers, each on its own later network",
			"services:\n" +
				"  api:\n    image: alpine:3.20\n    networks: [front, back, side]\n" +
				"  worker:\n    image: alpine:3.20\n    networks: [back]\n" +
				"  logger:\n    image: alpine:3.20\n    networks: [side]\n" +
				"networks:\n  front: {}\n  back: {}\n  side: {}\n",
			[]string{`service "logger" —`, `which shares "side" with it but not "front"`, `service "worker" —`, `which shares "back" with it but not "front"`},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("want %q in:\n%s", want, out.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out.String(), absent) {
					t.Errorf("did not want %q in:\n%s", absent, out.String())
				}
			}
		})
	}
}

// The warning reads the services the command starts: a service left out is
// neither named as the one that answers nor as the peer.
func TestThePairsAreTheOnesThisCommandStarts(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  frontend:\n    image: alpine:3.20\n    networks: [front]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, started := range [][]string{
		{"api", "frontend"},    // the peer that cannot reach it is left out
		{"frontend", "worker"}, // the service that answers is left out
	} {
		t.Run(strings.Join(started, "+"), func(t *testing.T) {
			var out bytes.Buffer
			if err := orchestrator.New(p, rt, "opossum", &out).Up(true, started...); err != nil {
				t.Fatalf("up: %v", err)
			}
			if strings.Contains(out.String(), "answers by name") {
				t.Errorf("a service left out should not be named:\n%s", out.String())
			}
		})
	}
}

// Without a DNS domain there are no bare names to fail: nothing is said.
func TestNothingIsSaidWithoutADNSDomain(t *testing.T) {
	rt, _ := fakeShim(t)
	// Without a domain the fake cannot read the project from a container's
	// name, so it is told which project these are.
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	p, err := loadProject(t, "services:\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "", &out).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	if strings.Contains(out.String(), "answers by name") {
		t.Errorf("no DNS domain, so nothing to say:\n%s", out.String())
	}
}

// A `run` of a service with no dependencies starts one container, so there is
// no pair to name: the one-off has nobody to be in a pair with. The service
// here is one that answers on two networks, so the only reason there is no
// pair is that nothing else is running.
func TestAOneOffThatStartsNothingElseNamesNoPair(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.RunOneOff("api", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := o.RunAudited("api", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run --audit: %v", err)
	}
	if strings.Contains(out.String(), "answers by name") {
		t.Errorf("a one-off should name no pair:\n%s", out.String())
	}
}

// The pairs come out in a settled order — by the service that answers, then
// by the peer — and not in the order the services start in (here the file
// makes the startup order the other way round).
func TestThePairsComeOutInASettledOrder(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  zapi:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  aapi:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [zapi]\n"+
		"  zworker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"  aworker:\n    image: alpine:3.20\n    networks: [back]\n    depends_on: [zworker]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	want := []string{`"aapi"->"aworker"`, `"aapi"->"zworker"`, `"zapi"->"aworker"`, `"zapi"->"zworker"`}
	if got := namedPairs(out.String()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pairs = %v, want %v\n%s", got, want, out.String())
	}
}

// A one-off whose own container answers on one network is in no pair as the
// one that answers, and is still told about the ones that do: the pairs named
// here are the dependency's, with the one-off among the peers — called what it
// is rather than named, since the name to use from there is the other one.
func TestAOneOffNamesThePairsAmongItsDependencies(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    networks: [back]\n    depends_on: [api, worker]\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The pairs among the dependencies come first, from the `up` that starts
	// them; the run then adds the ones the one-off is in.
	want := []string{`"api"->"worker"`, `"api"->"tool-run"`}
	if got := namedPairs(out.String()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pairs = %v, want %v\n%s", got, want, out.String())
	}
}

// The advice is written on the service the file names. A one-off's container
// is not in the file, so its pair points at the service it was made from and
// says why moving a network there moves it for the one-off.
func TestTheAdviceNamesTheServiceTheFileSpells(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [worker]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	const want = `Attach "back" to "tool"` + "\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("want the advice on the service the file spells (%s):\n%s", want, out.String())
	}
	if strings.Contains(out.String(), `Attach "back" to "tool-run"`) {
		t.Errorf(`the file has no service "tool-run" to attach a network to:`+"\n%s", out.String())
	}
}

// The clause that explains a one-off is written only where a one-off is in the
// pair. Between two services of the file the reader edits the very service
// that is named, and a sentence about joining somebody's networks would be
// about nobody.
func TestAPairOfTwoServicesIsAdvisedWithoutTheOneOffClause(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out.String(), `Attach "back" to "api"`+"\n") {
		t.Errorf("want the advice to end at the service it names:\n%s", out.String())
	}
	if strings.Contains(out.String(), "joins that service's networks in the same order") {
		t.Errorf("no one-off is in this pair, so nothing joins another's networks:\n%s", out.String())
	}
	if strings.Contains(out.String(), "one-off") {
		t.Errorf("no one-off is in this pair at all:\n%s", out.String())
	}
	// Both sides are named as services, the way an `up` has always written
	// them: a pair of two services of the file reads as it did before.
	want := `service "api" answers by name with its address on network "front", the one it is attached to first, so service "worker" —`
	if !strings.Contains(out.String(), want) {
		t.Errorf("want %s:\n%s", want, out.String())
	}
}

// The one-off is named by the name its container answers to, and each pair is
// said once: the pairs among the dependencies come from the `up` that starts
// them, the ones the one-off is in from the run itself.
func TestAOneOffIsNamedByItsRunName(t *testing.T) {
	const body = "services:\n" +
		"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [worker]\n" +
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n" +
		"networks:\n  front: {}\n  back: {}\n"
	for _, cmd := range []string{"run", "run --audit"} {
		t.Run(cmd, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			if cmd == "run" {
				err = o.RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
			} else {
				_, err = o.RunAudited("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			if want := `the one-off "tool-run", the container this run starts for service "tool", answers by name with its address on network "front"`; !strings.Contains(out.String(), want) {
				t.Errorf("want %q in:\n%s", want, out.String())
			}
			// The service's own name is not what answers here.
			if strings.Contains(out.String(), `service "tool" answers by name`) {
				t.Errorf("the one-off answers to `tool-run`, not `tool`:\n%s", out.String())
			}
			if n := strings.Count(out.String(), "[OPSM-211]"); n != 1 {
				t.Errorf("said %d times, want once:\n%s", n, out.String())
			}
		})
	}
}

// The dependency is the one that answers and the one-off is the peer that
// cannot reach it, called the one-off this run starts for its service and not
// named — on both commands. The dependency may
// be one the run reaches through another service: the Up starts those too.
func TestADependencyThatAnswersCallsTheOneOffByWhatItIs(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"a direct dependency", "services:\n" +
			"  tool:\n    image: alpine:3.20\n    networks: [back]\n    depends_on: [api]\n" +
			"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
			"networks:\n  front: {}\n  back: {}\n"},
		{"a dependency of a dependency", "services:\n" +
			"  tool:\n    image: alpine:3.20\n    networks: [back]\n    depends_on: [mid]\n" +
			"  mid:\n    image: alpine:3.20\n    networks: [front]\n    depends_on: [api]\n" +
			"  api:\n    image: alpine:3.20\n    networks: [front, back]\n" +
			"networks:\n  front: {}\n  back: {}\n"},
	} {
		for _, cmd := range []string{"run", "run --audit"} {
			t.Run(tc.name+", "+cmd, func(t *testing.T) {
				rt, _ := fakeShim(t)
				p, err := loadProject(t, tc.body)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				o := orchestrator.New(p, rt, "opossum", &out)
				if cmd == "run" {
					err = o.RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
				} else {
					_, err = o.RunAudited("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
				}
				if err != nil {
					t.Fatalf("%s: %v", cmd, err)
				}
				if want := `service "api" answers by name with its address on network "front", the one it is attached to first, so the one-off this run starts for service "tool"`; !strings.Contains(out.String(), want) {
					t.Errorf("want %q in:\n%s", want, out.String())
				}
				if n := strings.Count(out.String(), "[OPSM-211]"); n != 1 {
					t.Errorf("said %d times, want once:\n%s", n, out.String())
				}
			})
		}
	}
}

// A service of the project called `<service>-run` carries the name a one-off
// of `<service>` would answer to. No line here says which of the two a peer
// reaches — the run is refused before it starts, with the name as its reason
// (oneoffnametaken_test.go), so these warnings never speak under a name two
// containers could hold.
func TestAServiceNamedLikeTheOneOffRefusesTheRun(t *testing.T) {
	for _, cmd := range []string{"run", "run --audit"} {
		t.Run(cmd, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, "services:\n"+
				"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [tool-run]\n"+
				"  tool-run:\n    image: alpine:3.20\n    networks: [back]\n"+
				"networks:\n  front: {}\n  back: {}\n")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			if cmd == "run" {
				err = o.RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
			} else {
				_, err = o.RunAudited("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			if err == nil || err.Error() != oneOffNameTakenRefusal("tool") {
				t.Errorf("want the run refused for the name\n got %v\nwant %s", err, oneOffNameTakenRefusal("tool"))
			}
			if got := namedPairs(out.String()); len(got) != 0 {
				t.Errorf("nothing started, so no pair: %v\n%s", got, out.String())
			}
		})
	}
}

// Where the reader has to type the one-off's name — the side that answers, so
// a peer looks it up — the name comes with what it is, at its first mention.
// The advice names the service the file spells and says why moving a network
// there moves it for the one-off.
func TestTheAnsweringOneOffIsNamedAndSaidWhatItIs(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [worker]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The whole line, so that what is said about the peer — a service of the
	// file, named as one — and where the clause about the one-off sits are
	// both held, not only the pieces about the one-off itself. Every line of
	// this kind is written this way, not only the first (see
	// TestEveryLineOfARoleIsWrittenTheSameWay).
	want := `warning: [OPSM-211] the one-off "tool-run", the container this run starts for service "tool", answers by name with its address on network "front", the one it is attached to first, so service "worker" —` + "\n" +
		`         which shares "back" with it but not "front" — cannot reach it by that name (the address on "back" does answer). Attach "back" to "tool"` + "\n" +
		`         first — the one-off joins that service's networks in the same order, a list of networks in one file attaches in the order written, a mapping and networks merged from several files` + "\n" +
		`         in name order — or have "worker" use the address.` + "\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("want the line\n%s\ngot\n%s", want, out.String())
	}
	// The name is not introduced as a service: the file has no such service,
	// and one carrying that name is refused before this runs.
	if strings.Contains(out.String(), `service "tool-run"`) {
		t.Errorf("the one-off is a container of this run, not a service:\n%s", out.String())
	}
}

// The same role can fill two lines of one run: two peers that share only a
// later network with the one-off, or two services the one-off shares only a
// later network with. The writing is the line's, not the run's — every line
// where a peer would look the name up carries it, and none of the others do.
func TestEveryLineOfARoleIsWrittenTheSameWay(t *testing.T) {
	for _, tc := range []struct{ name, body, want, absent string }{{
		name: "the one-off answers to two peers",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [wa, wb]\n" +
			"  wa:\n    image: alpine:3.20\n    networks: [back]\n" +
			"  wb:\n    image: alpine:3.20\n    networks: [back]\n" +
			"networks:\n  front: {}\n  back: {}\n",
		want:   `the one-off "tool-run", the container this run starts for service "tool", answers by name`,
		absent: `service "tool-run"`,
	}, {
		name: "the one-off cannot reach two services",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n    networks: [back]\n    depends_on: [apia, apib]\n" +
			"  apia:\n    image: alpine:3.20\n    networks: [front, back]\n" +
			"  apib:\n    image: alpine:3.20\n    networks: [front, back]\n" +
			"networks:\n  front: {}\n  back: {}\n",
		want:   `so the one-off this run starts for service "tool" —`,
		absent: "tool-run",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if n := strings.Count(out.String(), "[OPSM-211]"); n != 2 {
				t.Fatalf("want two lines, got %d:\n%s", n, out.String())
			}
			if n := strings.Count(out.String(), tc.want); n != 2 {
				t.Errorf("want both lines written %s (%d of 2):\n%s", tc.want, n, out.String())
			}
			if strings.Contains(out.String(), tc.absent) {
				t.Errorf("want no line written with %s:\n%s", tc.absent, out.String())
			}
		})
	}
}

// Where the reader types nothing — the side that asks, whose name is only
// there to say who cannot reach — the one-off is not named at all. What the
// reader types from there is the other name, which the line already carries.
func TestTheAskingOneOffIsNotNamed(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    networks: [back]\n    depends_on: [api]\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{
		`so the one-off this run starts for service "tool" —`,
		`or have the one-off use the address`,
		`Attach "back" to "api"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("want %s:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "tool-run") {
		t.Errorf("a name the reader does not type is not spelled out:\n%s", out.String())
	}
	// The advice is the service's own, so nothing is said about joining
	// another's networks.
	if strings.Contains(out.String(), "joins that service's networks") {
		t.Errorf("no one-off is on the side the advice names:\n%s", out.String())
	}
}

// One run can write two lines: the one-off answers for one pair and asks in
// the other. They come out by the name that answers, like the ones an `up`
// writes — not by the service the advice names, which for a one-off is the
// service it was made from and sorts elsewhere.
func TestTwoLinesOfOneRunComeOutByTheNameThatAnswers(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  zeta:\n    image: alpine:3.20\n    networks: [one, shared]\n    depends_on: [zeta-a]\n"+
		"  zeta-a:\n    image: alpine:3.20\n    networks: [two, shared]\n"+
		"networks:\n  one: {}\n  two: {}\n  shared: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("zeta", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// By the service the advice names it would be `zeta` (for the one-off's
	// pair) before `zeta-a`, the other way round.
	want := []string{`"zeta-a"->"zeta-run"`, `"zeta-run"->"zeta-a"`}
	if got := namedPairs(out.String()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pairs = %v, want %v\n%s", got, want, out.String())
	}
}

// `--no-deps` starts the one-off alone: there is no peer to be in a pair
// with, so nothing is said.
func TestAOneOffWithNoDepsNamesNoPair(t *testing.T) {
	for _, cmd := range []string{"run", "run --audit"} {
		t.Run(cmd, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, "services:\n"+
				"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [worker]\n"+
				"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
				"networks:\n  front: {}\n  back: {}\n")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			if cmd == "run" {
				err = o.RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
			} else {
				_, err = o.RunAudited("tool", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
			}
			if err != nil {
				t.Fatalf("%s --no-deps: %v", cmd, err)
			}
			if strings.Contains(out.String(), "[OPSM-211]") {
				t.Errorf("nothing else is started, so there is no pair:\n%s", out.String())
			}
		})
	}
}

// With two dependencies, the pair among them is said once — by the `up` that
// starts them — and the pair the one-off is in is said once by the run. The
// run does not repeat the dependencies' pair (measured on the real runtime,
// 2026-09-21: an earlier build said it twice).
func TestARunWithTwoDependenciesSaysEachPairOnce(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    networks: [front, back]\n    depends_on: [api, worker]\n"+
		"  api:\n    image: alpine:3.20\n    networks: [front, back]\n"+
		"  worker:\n    image: alpine:3.20\n    networks: [back]\n"+
		"networks:\n  front: {}\n  back: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{`"api"->"worker"`, `"tool-run"->"worker"`}
	if got := namedPairs(out.String()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pairs = %v, want %v\n%s", got, want, out.String())
	}
}
