package orchestrator_test

// Evals for which services' entries count as host ports this project has
// spoken for.
//
// The sibling files cover what makes two entries the same claim, and where
// in the file an entry may sit. This one covers a third question the first
// two hid: whose entries are asked at all. They were the ones this command
// starts — so `up a` mirrored a port that `z` names, and the `up` after it
// could not start; and a service a profile keeps out of this run was not
// asked either, so enabling that profile later broke a project that had
// been starting.
//
// docker compose has neither failure, and for the same reason it has none
// of the others: it never lets a bare entry pick a host port, so nothing
// can be arranged into a collision (measured on v5.5.1 — `up a` then `up`,
// and the profile pair, both start).

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A host port the file fixed is taken whether or not this command starts the
// service that fixed it. The file is the project; which services a command
// happens to start is the command's business.
func TestAHostPortTheFileFixedIsTakenEvenWhenThatServiceIsNotStarted(t *testing.T) {
	port := freePort(t)
	mirror := fmt.Sprintf("%[1]d:%[1]d", port)
	explicit := fmt.Sprintf("%d:80", port)

	// A range wide enough to hold the port, written as a bare entry: the
	// host side of a range is never opossum's to move, so it fixes those
	// ports whoever wrote it.
	rng := fmt.Sprintf("%[1]d-%[2]d:%[1]d-%[2]d", port, port+2)

	for _, tc := range []struct {
		name     string
		zPort    string   // z's single entry
		zAuto    bool     // whether that entry left the host port to opossum
		only     []string // the services this run starts; nil means all of them
		profiles []string // profiles the file gives z; nil means none
		wantKept bool
	}{
		// The whole project: z is started, and its entry has always counted.
		{"both services start", explicit, false, nil, nil, false},
		// Only a. z is not started by this command, but the file still
		// names the port, and the `up` after this one starts z on it.
		{"only the service with the bare entry starts", explicit, false, []string{"a"}, nil, false},
		// z is gated behind a profile this run does not enable. Same shape:
		// the file names the port, and enabling the profile later starts z
		// on it.
		{"the other service is behind a profile", explicit, false, nil, []string{"extra"}, false},
		// The other value of the same question, and the one that says how
		// far this reaches: z's entry is bare too, so the file fixes
		// nothing — when z starts, opossum moves z's entry if it has to.
		// Counting it would move a off a port nobody asked for.
		{"the other service's entry is bare, and it does not start", mirror, true, []string{"a"}, nil, true},
		// A bare RANGE is not the same as a bare single port: opossum has
		// no way to move a range, so every port in it is fixed, and a
		// mirror inside it collides as soon as z starts.
		{"the other service's entry is a bare range, and it does not start", rng, true, []string{"a"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			z := &compose.Service{Image: "web:latest", Ports: []string{tc.zPort}, Profiles: tc.profiles}
			if tc.zAuto {
				z.AutoHostPort = map[string]bool{tc.zPort: true}
			}
			services := map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{mirror},
					AutoHostPort: map[string]bool{mirror: true}},
				"z": z,
			}
			rt, log := fakeShim(t)
			var out bytes.Buffer
			o := orchestrator.New(project("demo", services), rt, "opossum", &out)
			if err := o.Up(true, tc.only...); err != nil {
				t.Fatalf("up: %v", err)
			}
			calls := strings.Join(log(), "\n")
			if kept := strings.Contains(calls, "-p "+mirror); kept != tc.wantKept {
				t.Errorf("the bare entry %s %s; wanted it %s. z's entry is %q, so host port %d "+
					"%s.\ncalls:\n%s", mirror,
					map[bool]string{true: "kept its port", false: "was moved"}[kept],
					map[bool]string{true: "kept", false: "moved"}[tc.wantKept],
					tc.zPort, port,
					map[bool]string{
						true:  "is nobody's until z starts and asks for it",
						false: "is z's as soon as z starts, and publishing it twice fails",
					}[tc.wantKept], calls)
			}
		})
	}
}

// The set is built by walking a map, whose order differs from run to run.
// Building a set is order-free — but only as long as nothing in the walk
// depends on the order, and this walk sits next to one that does (the pass
// that decides the bare entries, which follows the start order). Reading
// the same file many times is how that would show.
//
// The file has to hold a claim for the reading to have an answer to get
// wrong: with nothing claiming the port, a walk that stopped at its first
// service would keep the mirror on every reading and look steady. So z
// fixes the port, and the mirror must move on all twenty.
func TestTheSetIsTheSameWhateverOrderTheServicesAreWalkedIn(t *testing.T) {
	port := freePort(t)
	mirror := fmt.Sprintf("%[1]d:%[1]d", port)
	for i := 0; i < 20; i++ {
		services := map[string]*compose.Service{
			"a": {Image: "web:latest", Ports: []string{mirror},
				AutoHostPort: map[string]bool{mirror: true}},
			// The claim. It is one service among several, so a walk that
			// reads fewer than all of them misses it some of the time.
			"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
		}
		// Enough other services that a map walk has somewhere to differ.
		for _, name := range []string{"m", "q", "w"} {
			services[name] = &compose.Service{Image: "web:latest",
				Ports: []string{fmt.Sprintf("%d:80", freePort(t))}}
		}
		rt, log := fakeShim(t)
		var out bytes.Buffer
		if err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true); err != nil {
			t.Fatalf("reading %d: up: %v", i, err)
		}
		calls := strings.Join(log(), "\n")
		if strings.Contains(calls, "-p "+mirror) {
			t.Fatalf("on reading %d the bare entry kept host port %d, which z's entry fixes. "+
				"The services are walked in a map's order, so something in the walk is "+
				"taking that order for an answer.\ncalls:\n%s", i, port, calls)
		}
	}
}

// What the notice says about WHY the mirrored port was not available.
//
// Once the set holds ports no running container is on — a port another
// service's line fixes, a port this run has just handed out — "that host
// port is taken" stops being true of every move. A reader who is told a
// port is taken goes to `lsof`, finds nothing there, and concludes opossum
// is wrong about their machine; the fix they need (name a host port in the
// file, or look at the other service's line) is the one the notice was
// meant to hand them.
//
// A second reading is just as wrong in the other direction: an entry that
// names a range of CONTAINER ports claims host ports without naming one, so
// sending the reader to that line for a host port number leaves them looking
// for something that is not written there.
//
// These rows are about the sentence, not about where the port went. The
// replacement port is asked of the operating system, which knows nothing of
// the claims, so on the range rows it usually lands inside the very range
// that caused the move — a project the runtime would still refuse. That is a
// separate defect, tracked on its own; a row here that asserted the move
// landed clear would be asserting something opossum does not yet do.

// The six things the notice can say, as patterns that tell them apart. Each
// row below claims one and must not say any of the other five.
var remapReasons = map[string]*regexp.Regexp{
	"in use":       regexp.MustCompile(`host port \d+ is in use`),
	"gone":         regexp.MustCompile(`has already gone to another entry in this project`),
	"own line":     regexp.MustCompile(`another entry for this service already asks for host port`),
	"own mirror":   regexp.MustCompile(`another entry for this service publishes container ports`),
	"other line":   regexp.MustCompile(`service "[^"]+" asks for host port`),
	"other mirror": regexp.MustCompile(`service "[^"]+" publishes container ports`),
}

func TestTheRemapNoticeSaysWhyTheMirroredPortWasNotAvailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		// build returns the services, and holds any listener for the run.
		// `moved` is the service whose bare entry the notice is about.
		build  func(t *testing.T, port int) map[string]*compose.Service
		moved  string
		reason string                // the key in remapReasons this row claims
		want   func(port int) string // the sentence itself
	}{
		// Something is listening. This is the one reason `lsof` can show,
		// and the only one the notice used to be able to say.
		{"something is listening on it", func(t *testing.T, port int) map[string]*compose.Service {
			l, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
			if err != nil {
				t.Fatalf("could not hold host port %d, so this row cannot be run: %v", port, err)
			}
			t.Cleanup(func() { l.Close() })
			return map[string]*compose.Service{"a": bareOn(port)}
		}, "a", "in use", func(p int) string { return fmt.Sprintf("host port %d is in use", p) }},

		// Two bare entries. Neither line names the port: the first one
		// walked took it, and there is no line to send the reader to.
		{"this run has already handed it out", func(t *testing.T, port int) map[string]*compose.Service {
			return map[string]*compose.Service{"a": bareOn(port), "b": bareOn(port)}
		}, "", "gone", func(p int) string {
			return fmt.Sprintf("host port %d has already gone to another entry in this project", p)
		}},

		// Another service's line names the host port.
		{"another service's line names it", func(t *testing.T, port int) map[string]*compose.Service {
			return map[string]*compose.Service{
				"a": bareOn(port),
				"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
			}
		}, "a", "other line", func(p int) string {
			return fmt.Sprintf("service %q asks for host port %d in the compose file", "z", p)
		}},

		// The same service's own other line. The service holding both
		// entries is NOT the first one started, so a check that compared
		// against the first service in the order instead of against this
		// one fails here.
		{"another line of the same service names it", func(t *testing.T, port int) map[string]*compose.Service {
			mirror := fmt.Sprintf("%[1]d:%[1]d", port)
			return map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", freePort(t))}},
				"b": {Image: "web:latest",
					Ports:        []string{fmt.Sprintf("%d:80", port), mirror},
					AutoHostPort: map[string]bool{mirror: true}},
			}
		}, "b", "own line", func(p int) string {
			return fmt.Sprintf("another entry for this service already asks for host port %d", p)
		}},

		// Another service's line names a range of CONTAINER ports, which
		// opossum mirrors. The file does not name the host port anywhere,
		// so the notice must not say it does.
		//
		// The port being moved sits INSIDE the range rather than at its
		// start, and the range is five ports wide: a sentence assembled
		// from the port asked about, or from a width taken for granted,
		// reads the same as this one when the port is the first of three.
		{"another service mirrors a container port range", func(t *testing.T, port int) map[string]*compose.Service {
			rng := fmt.Sprintf("%[1]d-%[2]d:%[1]d-%[2]d", port-1, port+3)
			return map[string]*compose.Service{
				"a": bareOn(port),
				"z": {Image: "web:latest", Ports: []string{rng},
					AutoHostPort: map[string]bool{rng: true}},
			}
		}, "a", "other mirror", func(p int) string {
			return fmt.Sprintf("service %q publishes container ports %d-%d, which opossum mirrors "+
				"onto the same host ports", "z", p-1, p+3)
		}},

		// The same service's own mirrored range. A third position and a
		// third width, and the service holding both entries is again not
		// the first one started.
		{"the same service mirrors a container port range", func(t *testing.T, port int) map[string]*compose.Service {
			rng := fmt.Sprintf("%[1]d-%[2]d:%[1]d-%[2]d", port-2, port+1)
			mirror := fmt.Sprintf("%[1]d:%[1]d", port)
			return map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", freePort(t))}},
				"b": {Image: "web:latest", Ports: []string{rng, mirror},
					AutoHostPort: map[string]bool{rng: true, mirror: true}},
			}
		}, "b", "own mirror", func(p int) string {
			return fmt.Sprintf("another entry for this service publishes container ports %d-%d, "+
				"which opossum mirrors onto the same host ports", p-2, p+1)
		}},

		// A range whose HOST side the file wrote. The number is written
		// down — inside the range rather than at either end, so a reader
		// sent to that line does find it — which is what makes the mirrored
		// rows above say something different.
		{"another service's line names a host port range", func(t *testing.T, port int) map[string]*compose.Service {
			return map[string]*compose.Service{
				"a": bareOn(port),
				"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d-%d:80-82", port-1, port+1)}},
			}
		}, "a", "other line", func(p int) string {
			return fmt.Sprintf("service %q asks for host port %d in the compose file", "z", p)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := freePortWithNeighbours(t)
			services := tc.build(t, port)
			rt, _ := fakeShim(t)
			var out bytes.Buffer
			if err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			said := noticeLine(t, out.String())
			if tc.moved != "" && !strings.Contains(said, fmt.Sprintf("service %q publishes container port %d", tc.moved, port)) {
				t.Errorf("the notice is about some other entry than %s's bare one:\n%s", tc.moved, said)
			}
			want := tc.want(port)
			if !strings.Contains(said, want) {
				t.Errorf("the notice does not say %q:\n%s", want, said)
			}
			// Each reason belongs to its own row. A notice that reaches for
			// a second one — or for the wrong one — fails here, which is
			// what makes these rows one guard each rather than one shared
			// "some reason was printed".
			for key, pat := range remapReasons {
				if hit := pat.MatchString(said); hit != (key == tc.reason) {
					t.Errorf("the reason %q %s, and this row is about %q:\n%s", key,
						map[bool]string{true: "was printed", false: "was not printed"}[hit],
						tc.reason, said)
				}
			}
		})
	}
}

// bareOn is a service whose one entry names only a container port, so
// opossum mirrors it onto the same host port and may move it.
func bareOn(port int) *compose.Service {
	mirror := fmt.Sprintf("%[1]d:%[1]d", port)
	return &compose.Service{Image: "web:latest", Ports: []string{mirror},
		AutoHostPort: map[string]bool{mirror: true}}
}

// noticeLine returns the one [OPSM-206] notice, failing if there is not
// exactly one — two moves in a run would otherwise let a row read another
// entry's sentence as its own.
func noticeLine(t *testing.T, out string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[OPSM-206]") {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("wanted exactly one [OPSM-206] notice, got %d:\n%s", len(found), out)
	}
	return found[0]
}

// Two entries may speak for one host port, and the notice then has two lines
// it could send the reader to. Which one it names must not depend on the order
// the services were walked in, which is a map's.
//
// Such a file may or may not go on to start: the pre-flight turns away a pair
// on one address, and reads no ports at all out of a range. Either way the
// notice comes out first, so the question this asks is the same.
func TestTheNoticeNamesTheSameLineWhateverOrderTheServicesAreWalkedIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, port int) map[string]*compose.Service
		want  func(port int) string
		// refused says the file is one the pre-flight turns away once the
		// notice has been printed. Two lines naming one host port is such a
		// file; two ranges overlapping is not, because a range is not read
		// as the ports it holds there.
		refused bool
	}{
		// Two lines naming the host port. These are kept by number, so the
		// choice is made where the number is written down.
		{"two lines name the host port", func(t *testing.T, port int) map[string]*compose.Service {
			return map[string]*compose.Service{
				"c": bareOn(port),
				"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
				"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:81", port)}},
			}
		}, func(p int) string {
			return fmt.Sprintf("service %q asks for host port %d in the compose file", "a", p)
		}, true},
		// Two mirrored ranges covering it. These are kept as they were
		// written and asked one by one, so it is a second place the walk
		// order could decide the answer — and the range in the sentence
		// tells the two apart, not only the name.
		{"two mirrored ranges cover the host port", func(t *testing.T, port int) map[string]*compose.Service {
			a := fmt.Sprintf("%[1]d-%[2]d:%[1]d-%[2]d", port-1, port+3)
			z := fmt.Sprintf("%[1]d-%[2]d:%[1]d-%[2]d", port-2, port+1)
			return map[string]*compose.Service{
				"c": bareOn(port),
				"a": {Image: "web:latest", Ports: []string{a}, AutoHostPort: map[string]bool{a: true}},
				"z": {Image: "web:latest", Ports: []string{z}, AutoHostPort: map[string]bool{z: true}},
			}
		}, func(p int) string {
			return fmt.Sprintf("service %q publishes container ports %d-%d, which opossum mirrors "+
				"onto the same host ports", "a", p-1, p+3)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := freePortWithNeighbours(t)
			for i := 0; i < 20; i++ {
				rt, _ := fakeShim(t)
				var out bytes.Buffer
				err := orchestrator.New(project("demo", tc.build(t, port)), rt, "opossum", &out).Up(true)
				if refused := err != nil; refused != tc.refused {
					t.Fatalf("reading %d: up err = %v, wanted it %s", i, err,
						map[bool]string{true: "refused", false: "accepted"}[tc.refused])
				}
				if tc.refused && !strings.Contains(err.Error(), "[OPSM-213]") {
					t.Fatalf("reading %d: refused for some other reason than the two lines "+
						"naming one host port: %v", i, err)
				}
				// The notice comes out before the refusal does, and it is the
				// notice this test is about: which of two lines gets named
				// must not depend on the walk, whether or not the file then
				// turns out to be one opossum will not start.
				want := tc.want(port)
				if said := noticeLine(t, out.String()); !strings.Contains(said, want) {
					t.Fatalf("on reading %d the notice does not say %q. Both a and z speak for host "+
						"port %d, and which one is read back is being decided by the order the "+
						"services happened to be walked in.\n%s", i, want, port, said)
				}
			}
		})
	}
}
