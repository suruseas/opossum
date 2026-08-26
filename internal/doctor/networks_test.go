package doctor

import (
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// The machine this was found on, as internal/runtime hands it over: five
// networks, one of them the runtime's own, and three containers of which two are
// stopped. The stopped ones matter — a fixture of running containers cannot tell
// "reads the configured networks" from "reads the running attachments", because
// for a running container the two agree.
func realMachine() ([]runtime.NetworkSummary, []runtime.ContainerSummary) {
	nets := []runtime.NetworkSummary{
		{Name: "agent-sandbox-net"},
		{Name: "default", Builtin: true},
		{Name: "mongo4-net"},
		{Name: "opossum-sprint-v0200b-net"},
		{Name: "proj-net"},
	}
	ctrs := []runtime.ContainerSummary{
		{Name: "cache.proj.opossum", State: "stopped", Networks: []string{"proj-net"}},
		{Name: "db.mongo4.opossum", State: "stopped", Networks: []string{"mongo4-net"}},
		{Name: "buildkit", State: "running", Networks: []string{"default"}},
	}
	// Named rather than returned inline: gofmt indents a multi-value return of
	// two composite literals differently in Go 1.26 and 1.27, and this
	// repository's gate runs both. Neither formatting is wrong; the construct
	// is just one the two versions disagree about, so it is not used here.
	return nets, ctrs
}

// A network holding no containers and a network holding stopped ones are
// different findings, and the report keeps them apart.
//
// Adding them up would be the easier sentence and the wrong one: the first is
// nothing anybody is using, and the second is a project somebody may start.
func TestTheTwoKindsOfLeftoverAreCountedApart(t *testing.T) {
	nets, ctrs := realMachine()
	got := checkLeftoverNetworks(mock{nets: nets, ctrs: ctrs})
	if got.status != warn {
		t.Errorf("two networks have nothing on them at all; that is worth a warning, got %v", got.status)
	}
	for _, want := range []string{
		"2 with no containers at all",
		"agent-sandbox-net",
		"opossum-sprint-v0200b-net",
		"2 holding only stopped containers",
		"mongo4-net",
		"proj-net",
	} {
		if !strings.Contains(got.detail, want) {
			t.Errorf("detail is missing %q:\n%s", want, got.detail)
		}
	}
	// Plural, because there are four. Every other fixture here has one, and a
	// version that says "it" for all of them reads correctly in all of those.
	if !strings.Contains(got.detail, "4 networks with nothing running on them") {
		t.Errorf("four of them, and the sentence knows it:\n%s", got.detail)
	}
	// The builtin one is the runtime's own and is never a leftover.
	if strings.Contains(got.detail, "default") {
		t.Errorf("`default` belongs to the runtime, not to a project:\n%s", got.detail)
	}
	// The offer names one of the ones with nothing on them, in the form the
	// recorded real output has been seen to take: one name.
	//
	// Compared whole. This fixture has two empty networks on purpose, because a
	// check for "contains the first name" is satisfied by a line that lists
	// both — which is how the earlier multi-name form survived being replaced.
	// A fixture with one empty network cannot tell the two forms apart at all.
	wantFix := "if you are done with one: container network delete agent-sandbox-net" +
		" — or `opossum down`, for a project you still have the compose file for"
	if got.fix != wantFix {
		t.Errorf("fix =\n%s\nwant\n%s", got.fix, wantFix)
	}
}

// A stopped container still holds its network.
//
// This is the whole of the classification: the runtime reports a stopped
// container with no attachments and the network still in its configuration, so a
// reading taken from the running side would call every stopped project's network
// abandoned — and the reclaim this report is a first step towards would delete a
// network something is waiting for. internal/runtime holds the reading itself;
// this holds what the classification does with it.
func TestAStoppedContainerStillHoldsItsNetwork(t *testing.T) {
	nets, ctrs := realMachine()
	by := map[string]networkUse{}
	for _, u := range networkUsage(nets, ctrs) {
		by[u.name] = u
	}
	if u := by["proj-net"]; u.stopped != 1 || u.running != 0 {
		t.Errorf("proj-net holds one stopped container, got %+v", u)
	}
	if _, present := by["default"]; present {
		t.Errorf("the runtime's own network is not in the list: %+v", by)
	}
	if len(by) != 4 {
		t.Errorf("five networks, one of them the runtime's own, got %d: %+v", len(by), by)
	}
}

// Nothing left over says so, and says which kind of nothing it is.
//
// Two machines pass this check and they are not the same machine: one where
// every network has something on it, and one that has no networks but the
// runtime's own. A check that reads only the status cannot tell them apart —
// and the line it prints is read by a person, who can.
func TestACleanMachineSaysWhichKindOfCleanItIs(t *testing.T) {
	onlyTheRuntimes := checkLeftoverNetworks(mock{
		nets: []runtime.NetworkSummary{{Name: "default", Builtin: true}},
		ctrs: []runtime.ContainerSummary{},
	})
	if onlyTheRuntimes.status != ok || onlyTheRuntimes.fix != "" {
		t.Errorf("nothing to reclaim and nothing to do, got %+v", onlyTheRuntimes)
	}
	if want := "no networks but the runtime's own"; onlyTheRuntimes.detail != want {
		t.Errorf("detail = %q, want %q — there is nothing here for `every network` to be about",
			onlyTheRuntimes.detail, want)
	}

	busy := checkLeftoverNetworks(mock{
		nets: []runtime.NetworkSummary{{Name: "default", Builtin: true}, {Name: "live-net"}},
		ctrs: []runtime.ContainerSummary{{Name: "web", State: "running", Networks: []string{"live-net"}}},
	})
	if busy.status != ok || busy.fix != "" {
		t.Errorf("everything is in use, got %+v", busy)
	}
	if want := "every network has something running on it"; busy.detail != want {
		t.Errorf("detail = %q, want %q", busy.detail, want)
	}
}

// Only stopped containers and no empty networks is not a warning.
func TestOnlyStoppedProjectsIsNotAWarning(t *testing.T) {
	_, ctrs := realMachine()
	got := checkLeftoverNetworks(mock{nets: []runtime.NetworkSummary{{Name: "proj-net"}}, ctrs: ctrs})
	if got.status != ok {
		t.Errorf("nothing here is unused, got %v: %s", got.status, got.detail)
	}
	if !strings.Contains(got.detail, "1 holding only stopped containers") {
		t.Errorf("but it is still reported:\n%s", got.detail)
	}
	if got.fix != "" {
		t.Errorf("and there is nothing to offer removing: %q", got.fix)
	}
}

// A running container is not a leftover at all — and neither is the network it
// shares with a stopped one.
//
// A project with one service stopped is an ordinary state, and the network it is
// on has both kinds of container hanging off it. Anything running is enough to
// make the network in use; classified by what is stopped, a live project gets
// reported as "holding only stopped containers". Every other fixture here has
// one kind per network, so none of them can tell the two rules apart.
func TestARunningProjectIsNotReported(t *testing.T) {
	got := checkLeftoverNetworks(mock{
		nets: []runtime.NetworkSummary{{Name: "live-net"}},
		ctrs: []runtime.ContainerSummary{{Name: "web.live.opossum", State: "running", Networks: []string{"live-net"}}},
	})
	if got.status != ok || strings.Contains(got.detail, "live-net") {
		t.Errorf("a running project is in use, got %+v", got)
	}

	mixed := checkLeftoverNetworks(mock{
		nets: []runtime.NetworkSummary{{Name: "half-net"}},
		ctrs: []runtime.ContainerSummary{
			{Name: "web.half.opossum", State: "running", Networks: []string{"half-net"}},
			{Name: "worker.half.opossum", State: "stopped", Networks: []string{"half-net"}},
		},
	})
	if mixed.status != ok {
		t.Errorf("something is running on it, so it is in use: %+v", mixed)
	}
	if strings.Contains(mixed.detail, "half-net") {
		t.Errorf("and it is not reported at all:\n%s", mixed.detail)
	}
}

// A listing that did not come back is not a machine with nothing left over.
//
// internal/runtime answers nil for both "the command failed" and "the output
// would not parse", so those are one case here — and it is the case where this
// check must not report a clean machine that nobody measured.
func TestAListingThatWouldNotComeBackSaysSo(t *testing.T) {
	nets, ctrs := realMachine()
	for _, c := range []struct {
		name string
		m    mock
	}{
		// Both missing, and then each one alone. The half-missing cases are the
		// ones worth writing down: with both nil the first guard answers and the
		// second is never reached, so a fixture of "nothing came back" tests one
		// guard twice. The container listing alone is the dangerous half — every
		// network then looks like nothing is on it, and the offer names all of
		// them.
		{"neither listing", mock{}},
		{"no networks", mock{ctrs: ctrs}},
		{"no containers", mock{nets: nets}},
	} {
		got := checkLeftoverNetworks(c.m)
		if !strings.Contains(got.detail, "unavailable") {
			t.Errorf("%s: detail = %q, want it to say the listing did not come back", c.name, got.detail)
		}
		if strings.Contains(got.detail, "with nothing running on") {
			t.Errorf("%s: a probe that did not run has found nothing: %q", c.name, got.detail)
		}
		if got.fix != "" {
			t.Errorf("%s: and nothing to offer: %q", c.name, got.fix)
		}
		// Not a warning. A probe that did not run has found nothing wrong, and
		// a line that warns about its own silence is a line the reader learns
		// to skip. The runtime being down is the runtime check's to report, and
		// it says so first.
		if got.status != ok {
			t.Errorf("%s: status = %v, want ok — nothing was measured, so nothing is wrong", c.name, got.status)
		}
	}
}

// A network with no name is not reported, because the offer would end in a
// `container network delete` with nothing after it.
func TestANamelessNetworkIsNotOffered(t *testing.T) {
	got := checkLeftoverNetworks(mock{
		nets: []runtime.NetworkSummary{{Name: ""}, {Name: "real-net"}},
		ctrs: []runtime.ContainerSummary{},
	})
	if !strings.Contains(got.detail, "container-network-vmnet") {
		t.Errorf("the reader is told what to look for on their own machine:\n%s", got.detail)
	}
	if !strings.Contains(got.detail, "1 network with nothing running") {
		t.Errorf("one network to report, not two:\n%s", got.detail)
	}
	if !strings.Contains(got.fix, "delete real-net ") {
		t.Errorf("the offer names the one network there is:\n%s", got.fix)
	}
}

// The report is a table: every detail starts in the same column, and so does
// every fix.
//
// The width was a constant until a check arrived whose name was longer than it,
// and that check's detail sat one column right of everyone else's while its fix
// line stayed where the old width put it. A fixture of short names cannot see
// this — the constant and the measurement agree until a name outgrows it — so
// the names below include one that does.
//
// Measured after the icon, not from the start of the line. The icons are not
// the same number of runes — a check mark is one, a warning sign is two plus a
// space it carries to make up the difference — so counting runes from column
// zero compares two different things. This test got that wrong first.
//
// What is left unchecked is the icons themselves: whether each one occupies two
// columns is the terminal's answer, not this program's, and a warning sign is
// one of the characters terminals disagree about. The trailing space in the
// warning icon is a choice made for the terminals that render it narrow. Nothing
// here can verify that choice, so nothing here pretends to — this is about the
// arithmetic after the icon, which is the part the program decides.
func TestTheColumnsAfterTheIconLineUp(t *testing.T) {
	var b strings.Builder
	Run(&b, mock{
		status:  "status running\n",
		builder: "buildkit img running 2 2048 MB\n",
		probe:   "DNS-OK\nIP-OK\n",
		df:      "IMAGES  1 GB  500 MB (50%)",
		nets:    []runtime.NetworkSummary{{Name: "agent-sandbox-net"}},
		ctrs:    []runtime.ContainerSummary{},
		dns:     true,
	}, "opossum", nil, 16384)

	// The widest name in this report, which is what the column should be.
	const width = len("leftover-networks")

	// Each icon is swapped for two plain characters, so that what follows can be
	// counted in runes. What an icon costs on screen is the terminal's business;
	// what follows it is this program's.
	flat := b.String()
	for _, st := range []status{ok, warn, fail} {
		flat = strings.ReplaceAll(flat, st.icon(), "..")
	}

	var checks, fixes int
	for _, line := range strings.Split(strings.TrimRight(flat, "\n"), "\n") {
		r := []rune(line)
		if i := strings.IndexRune(line, '↳'); i >= 0 {
			fixes++
			if got := len([]rune(line[:i])); got != width+4 {
				t.Errorf("the fix arrow is %d columns in, want %d:\n%s", got, width+4, line)
			}
			continue
		}
		checks++
		// Two for the icon, one space, the name field, one space.
		if len(r) <= width+4 || r[width+3] != ' ' || r[width+4] == ' ' {
			t.Errorf("the detail should begin at column %d:\n%s", width+4, line)
		}
	}
	if checks < 2 || fixes < 1 {
		t.Fatalf("this fixture should produce several checks and at least one fix:\n%s", b.String())
	}
}

// Two entries under one name are counted once, and not reported in both columns.
//
// The runtime does not produce this. The guard is there because the report is
// built from a map keyed by name, and a duplicate would make one network appear
// as both "nothing is on it" and "a stopped container names it" — a row that
// contradicts itself, which is worse than a row that is missing.
func TestANameThatArrivesTwiceIsCountedOnce(t *testing.T) {
	got := checkLeftoverNetworks(mock{
		nets: []runtime.NetworkSummary{{Name: "twice"}, {Name: "twice"}},
		ctrs: []runtime.ContainerSummary{{Name: "db", State: "stopped", Networks: []string{"twice"}}},
	})
	if !strings.Contains(got.detail, "1 network with nothing running on it") {
		t.Errorf("one name, one network:\n%s", got.detail)
	}
	if strings.Contains(got.detail, "no containers at all") {
		t.Errorf("and it is in one column, not both:\n%s", got.detail)
	}
}
