package doctor

import (
	"fmt"
	"strings"

	"github.com/suruseas/opossum/internal/runtime"
)

// networkUse is what is sitting on one network.
type networkUse struct {
	name    string
	running int
	stopped int
}

// checkLeftoverNetworks reports the networks nothing is running on.
//
// Apple `container` keeps a process resident for every network that exists.
// `opossum down` and `opossum destroy` delete the project network, so a run that
// reaches either leaves nothing behind. A run that does not — a session that
// ends, an interrupt, a machine that sleeps — leaves the network, and the
// process with it. Sessions accumulate; so do the processes.
//
// It reports and does not remove, and it does not claim to know whose a network
// is. opossum puts no mark on the ones it creates, so from here a network made
// by `opossum up`, one declared `external: true` (which opossum deliberately
// never removes), and one someone made by hand all look the same. Naming them
// is useful; deciding for the reader which are disposable is not something this
// can honestly do.
func checkLeftoverNetworks(rt Runner) check {
	nets := rt.Networks()
	if nets == nil {
		return check{"leftover-networks", ok, "leftover networks unavailable (`container network ls` didn't run)", ""}
	}
	// Both listings, or neither. A network looks empty when nothing names it,
	// and nothing names it when the container listing did not come back — so
	// half the answer here is not half a report, it is the whole report with
	// every network in the wrong column. This is the same mistake as reading the
	// running attachments, arriving through a different door.
	ctrs := rt.List()
	if ctrs == nil {
		return check{"leftover-networks", ok, "leftover networks unavailable (`container ls` didn't run)", ""}
	}
	use := networkUsage(nets, ctrs)
	var empty, stoppedOnly []string
	for _, u := range use {
		switch {
		case u.running > 0:
		case u.stopped > 0:
			stoppedOnly = append(stoppedOnly, u.name)
		default:
			empty = append(empty, u.name)
		}
	}
	if len(empty) == 0 && len(stoppedOnly) == 0 {
		if len(use) == 0 {
			return check{"leftover-networks", ok, "no networks but the runtime's own", ""}
		}
		return check{"leftover-networks", ok, "every network has something running on it", ""}
	}
	detail := describeLeftovers(empty, stoppedOnly)
	if len(empty) == 0 {
		// Everything found is a project someone may start again, so there is
		// nothing here to warn about. A line that cries wolf is a line the
		// reader stops reading, and this one exists for the day it matters.
		return check{"leftover-networks", ok, detail, ""}
	}
	// One name, because one name is the form of this command that has been seen
	// to work (testdata/real-cli-output.md records it). The error text the
	// runtime gives for a missing one says "one or more networks", which reads
	// like several would be accepted — but a line a reader pastes should not
	// rest on how an error message is worded.
	return check{"leftover-networks", warn, detail,
		"if you are done with one: container network delete " + empty[0] +
			" — or `opossum down`, for a project you still have the compose file for"}
}

// describeLeftovers says what was found, separating the networks nothing is on
// from the ones a stopped container still names. The two need different
// decisions, so they are not added together into one number.
func describeLeftovers(empty, stoppedOnly []string) string {
	var parts []string
	if n := len(empty); n > 0 {
		parts = append(parts, fmt.Sprintf("%d with no containers at all (%s)", n, strings.Join(empty, ", ")))
	}
	if n := len(stoppedOnly); n > 0 {
		parts = append(parts, fmt.Sprintf("%d holding only stopped containers (%s)", n, strings.Join(stoppedOnly, ", ")))
	}
	total := len(empty) + len(stoppedOnly)
	// The process is named rather than its size given. What one of them holds is
	// a number measured on one machine at one moment, and a number a reader
	// cannot check is one they either believe or ignore. The name they can
	// check: `ps | grep container-network-vmnet`, on their machine, now. It is
	// also recorded in the network listing itself, as the `plugin` field (see
	// testdata/real-cli-output.md), so it is not a claim made only here.
	return fmt.Sprintf("%d network%s with nothing running on %s — %s; each keeps a container-network-vmnet process resident",
		total, plural(total), itOrThem(total), strings.Join(parts, ", "))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// networkUsage joins the two listings: every network the runtime did not make
// for itself, with the containers that name it.
//
// The order is the order the listing gave, so two runs of doctor read the same
// way. A network with no name is not reported: the listing has never produced
// one, and a report of "" would offer a `container network delete` with nothing
// after it.
func networkUsage(nets []runtime.NetworkSummary, ctrs []runtime.ContainerSummary) []networkUse {
	byName := map[string]*networkUse{}
	var out []networkUse
	for _, n := range nets {
		if n.Builtin || n.Name == "" {
			continue
		}
		if _, dup := byName[n.Name]; dup {
			// Two entries under one name would be counted twice and could be
			// reported in both columns at once. The runtime does not produce
			// this; the report survives it anyway.
			continue
		}
		out = append(out, networkUse{name: n.Name})
		byName[n.Name] = nil
	}
	for i := range out {
		byName[out[i].name] = &out[i]
	}
	for _, c := range ctrs {
		for _, n := range c.Networks {
			u := byName[n]
			if u == nil {
				continue
			}
			if c.State == "running" {
				u.running++
			} else {
				u.stopped++
			}
		}
	}
	return out
}
