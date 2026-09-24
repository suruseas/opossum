package orchestrator_test

// Evals for *when* a host port counts as one this project has spoken for.
//
// The sibling file covers what makes two entries the same claim — the host
// port and the protocol, and nothing else. This one covers the other half of
// the question: which entries are asked at all.
//
// They used to be asked in the order they are started, one at a time, so an
// entry was measured against the entries ahead of it and not the ones behind.
// The same two entries then started or did not depending on how they were
// arranged: `ports: ["P", "P:80"]` published P twice and the runtime refused
// the pair, while `["P:80", "P"]` moved the bare one and started. Worse, the
// order is the order services start — which is their names in lexicographic
// order unless `depends_on` says otherwise — so a file that started yesterday
// stops starting when a service is renamed, with nothing in `ports` touched.
// A range published nothing at all into the set, so a bare entry landed
// inside one.
//
// docker compose has no such seam: it never lets a bare entry pick a host
// port, so nothing can be arranged into a conflict (measured on v5.5.1: all
// three arrangements start, with the bare entry on an ephemeral port each
// time). opossum does mirror the container port when it is free, which is
// the difference these rows are about — the mirror has to be free of every
// other entry in the project, not of the ones that happen to come first.

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A claim is the same claim wherever it sits. Each row puts one bare entry —
// the one opossum may move — and one claim somewhere else in the project, and
// asks only where that claim sits: ahead of the bare entry or behind it, in
// the same service or another, written as one port or as a range.
//
// The service names are `a` and `z` so that the start order is the order
// they are written here, lexicographic being what decides it.
func TestAHostPortIsClaimedWhereverTheEntryNamingItSits(t *testing.T) {
	port := freePortWithNeighbours(t)
	for _, tc := range []struct {
		name     string
		a, z     []string // the two services, `a` starting first
		auto     string   // the entry opossum may move
		wantKept bool
	}{
		// Ahead of the bare entry, which is what used to be asked.
		{"a claim earlier in the same service",
			[]string{"%[1]d:80", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", false},
		// Behind it. The same two entries, written the other way round.
		{"a claim later in the same service",
			[]string{"%[1]d:%[1]d", "%[1]d:80"}, nil, "%[1]d:%[1]d", false},
		// In a service that starts earlier.
		{"a claim in a service that starts earlier",
			[]string{"%[1]d:80"}, []string{"%[1]d:%[1]d"}, "%[1]d:%[1]d", false},
		// And in one that starts later. This is the rename: `a` and `z`
		// swapped is the row above, and nothing in `ports` differs.
		{"a claim in a service that starts later",
			[]string{"%[1]d:%[1]d"}, []string{"%[1]d:80"}, "%[1]d:%[1]d", false},
		// A range publishes every port in it, so a bare entry inside one is
		// on a port that is taken — ahead of it or behind it.
		{"a range ahead of it that covers the port",
			[]string{"%[1]d-%[2]d:80-81", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", false},
		{"a range behind it that covers the port",
			[]string{"%[1]d:%[1]d", "%[1]d-%[2]d:80-81"}, nil, "%[1]d:%[1]d", false},
		{"a range in a service that starts later",
			[]string{"%[1]d:%[1]d"}, []string{"%[1]d-%[2]d:80-81"}, "%[1]d:%[1]d", false},
		// The range has to be read as the ports it holds, not as a claim on
		// everything. Both sides of it, because a check that read only one
		// end would pass whichever of these it happened to be given: a range
		// that ends below the port, and one that starts above it.
		{"a range that ends below the port",
			[]string{"%[3]d-%[4]d:80-81", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", true},
		{"a range that starts above the port",
			[]string{"%[5]d-%[6]d:80-81", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", true},
		// Where in a range the port sits. The rows above all put it at the
		// range's first port, so a check that read a range as its first
		// port — or stopped one short of its last — would pass every one of
		// them.
		{"a range whose last port is the one being mirrored",
			[]string{"%[4]d-%[1]d:80-81", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", false},
		{"a range with the port in the middle of it",
			[]string{"%[4]d-%[2]d:80-82", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", false},
		// A bare entry asking for a range is one opossum cannot move, so it
		// takes those ports like any entry the file fixed.
		{"a bare range holds the ports it asks for",
			[]string{"%[4]d-%[1]d:%[4]d-%[1]d", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", false},
		// The same number written with a zero in front of it is the same
		// port. The loader passes it through as written (`config` prints
		// `07310:07310`), so both spellings reach this function.
		{"a claim the mirror writes with a leading zero",
			[]string{"%[1]d:80", "0%[1]d:0%[1]d"}, nil, "0%[1]d:0%[1]d", false},
		// The other way round: an entry naming one port is a claim on that
		// one port, not on everything from there upwards. Both neighbours,
		// because a claim read as an open-ended range would only show on the
		// side the range would have run towards — the row below it.
		{"a claim on the port below",
			[]string{"%[4]d:80", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", true},
		{"a claim on the port above",
			[]string{"%[2]d:80", "%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", true},
		// And it is a claim on its own protocol only, like every other.
		{"a udp range leaves a tcp mirror alone",
			[]string{"%[1]d-%[2]d:80-81/udp", "%[1]d:%[1]d/tcp"}, nil, "%[1]d:%[1]d/tcp", true},
		// The bare entry is not a claim against itself. With nothing else in
		// the project naming the port, the mirror stands — which is the
		// whole point of mirroring, and the row that fails if the set is
		// built without leaving the entries being decided out of it.
		{"the bare entry alone",
			[]string{"%[1]d:%[1]d"}, nil, "%[1]d:%[1]d", true},
		// Nor is another service's bare entry a claim ahead of time: two
		// services asking for the same container port still get one port
		// each, the first keeping the mirror.
		{"another service's bare entry does not take it in advance",
			[]string{"%[1]d:%[1]d"}, []string{"%[1]d:%[1]d"}, "%[1]d:%[1]d", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One port and its neighbours, so that "below" and "above" are
			// what the row says they are. Two ports picked independently
			// land either side of each other at random, and a row named
			// "below" that is sometimes above measures a different thing
			// each run — and kills a different mutation each run.
			fill := func(ss []string) []string {
				if ss == nil {
					return nil
				}
				out := make([]string, len(ss))
				for i, f := range ss {
					out[i] = fmt.Sprintf(f, port, port+1, port-2, port-1, port+2, port+3)
				}
				return out
			}
			aPorts, zPorts := fill(tc.a), fill(tc.z)
			auto := fmt.Sprintf(tc.auto, port, port+1, port-2, port-1, port+2, port+3)
			// Every entry written as a bare container port is one opossum
			// may move, and the loader writes those back with the host side
			// equal to the container side — which is how the rows here say
			// "this one was bare". `auto` names the one the row asks about;
			// a row may hold others (a bare range, or the same spelling in
			// both services), and those are marked too, as the loader marks
			// them.
			services := map[string]*compose.Service{}
			for name, ports := range map[string][]string{"a": aPorts, "z": zPorts} {
				if ports == nil {
					continue
				}
				svc := &compose.Service{Image: "web:latest", Ports: ports, AutoHostPort: map[string]bool{}}
				for _, spec := range ports {
					if bare(spec) {
						svc.AutoHostPort[spec] = true
					}
				}
				services[name] = svc
			}
			rt, log := fakeShim(t)
			var out bytes.Buffer
			if err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			calls := log()
			kept := 0
			for _, c := range calls {
				if strings.Contains(c, "-p "+auto) {
					kept++
				}
			}
			if (kept > 0) != tc.wantKept {
				t.Errorf("the entry opossum may move %s; wanted it %s.\ncalls:\n%s",
					map[bool]string{true: "kept its port", false: "was moved"}[kept > 0],
					map[bool]string{true: "kept", false: "moved"}[tc.wantKept],
					strings.Join(calls, "\n"))
			}
			// However the entries are arranged, no host port is published
			// twice: that is the pair the runtime refuses, and the reason any
			// of this is measured at all. Counted over every container the run
			// started, so a row that moves the wrong one still fails here.
			taken := map[string]string{}
			for _, c := range calls {
				for _, spec := range publishedSpecs(c) {
					network, _, hp, ok := splitPublished(spec)
					if !ok {
						continue
					}
					key := hp + "/" + network
					if prev, dup := taken[key]; dup {
						t.Errorf("host port %s is published twice — by %q and by %q. The runtime "+
							"refuses such a pair (`host ports for different publish port specs may "+
							"not overlap`), so the project does not start at all.\ncalls:\n%s",
							key, prev, spec, strings.Join(calls, "\n"))
					}
					taken[key] = spec
				}
			}
		})
	}
}

// publishedSpecs returns what a run call published: the value after each
// `-p`. Read from the words of the call rather than from the project, so
// that what is checked is what the runtime was told.
func publishedSpecs(call string) []string {
	var out []string
	fields := strings.Fields(call)
	for i, f := range fields {
		if f == "-p" && i+1 < len(fields) {
			out = append(out, fields[i+1])
		}
	}
	return out
}

// splitPublished reads a published spec the way the runtime reads it, in this
// file's own words rather than by calling the code under test — a check that
// asks the implementation what it meant agrees with it whatever it does.
//
// ok is false for a range: this is here to find one host port published
// twice, and a range published beside a single port is two spellings of
// different shapes. No row writes two ranges that overlap; the rows that
// write one alongside a mirrored port are held by what they expect of that
// port instead.
func splitPublished(spec string) (network, address, hostPort string, ok bool) {
	network = "tcp"
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		network, spec = strings.ToLower(spec[i+1:]), spec[:i]
	}
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return "", "", "", false
	}
	hostPort = parts[len(parts)-2]
	if len(parts) > 2 {
		address = strings.Join(parts[:len(parts)-2], ":")
	}
	if hostPort == "" || strings.Contains(hostPort, "-") {
		return "", "", "", false
	}
	return network, address, hostPort, true
}

// freePortWithNeighbours answers with a free port whose two ports below and
// three above are free as well, so that a row saying "below" or "above" is
// about a range on the side it names.
//
// Asking twice for a free port gives two numbers in no particular order, and
// a row built from them is sometimes one shape and sometimes the other. The
// rows would still pass — the answer is the same either way — while the
// mutation they kill changes from run to run: a check that forgets the
// bottom of a range is caught only by a range above, and one that forgets
// the top only by a range below.
func freePortWithNeighbours(t *testing.T) int {
	t.Helper()
	for try := 0; try < 40; try++ {
		p := freePort(t)
		if p < 1024+2 || p > 65535-3 {
			continue
		}
		free := true
		for _, n := range []int{p - 2, p - 1, p + 1, p + 2, p + 3} {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", n))
			if err != nil {
				free = false
				break
			}
			l.Close()
		}
		if free {
			return p
		}
	}
	t.Skip("no run of six free ports on this machine; the rows that say " +
		"\"below\" and \"above\" cannot be built without one")
	return 0
}

// bare reports whether a spec is one the loader wrote back from a bare
// container port: it publishes on the host side what the container side
// asks for, and those are the entries opossum may move.
//
// The protocol comes off first. It sits on the container side of the colon
// (`7310:7310/tcp`), so comparing the two halves as written says a spec
// with a protocol is not bare — which silently unmarks the very rows that
// ask about protocol, and lets a check that ignores protocol pass them.
func bare(spec string) bool {
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		spec = spec[:i]
	}
	host, container, found := strings.Cut(spec, ":")
	return found && host == container
}
