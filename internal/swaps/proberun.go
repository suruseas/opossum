package swaps

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// This file is the layer between a sweep's report and a probe run: it reads
// survivor rows back into requests, turns requests into mutations the sweep
// machinery can apply, and reads a test transcript back into one of three
// verdicts. The row format it parses is the one name() writes — the two are
// tested against each other, so the writer cannot drift away from this reader.

// ProbeRequest names one surviving pair: a site by position, a pair by the
// 0-based argument indexes Swap and Probe take.
type ProbeRequest struct {
	File      string
	Line, Col int
	A, B      int
}

// survivorRow is the shape name() writes, found anywhere in a line so a row
// pasted from a report — with an outcome column after it, or table pipes
// around it — reads the same as the bare name.
var survivorRow = regexp.MustCompile(`(\S+\.go):(\d+):(\d+) reads arguments (\d+) and (\d+) the other way round`)

// ParseSurvivorRow reads one line of a sweep report. The bool is false for a
// line that carries no survivor row — blank, prose, a table header — which the
// caller counts rather than silently drops.
func ParseSurvivorRow(line string) (ProbeRequest, bool) {
	m := survivorRow.FindStringSubmatch(line)
	if m == nil {
		return ProbeRequest{}, false
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	// The row prints 1-based argument numbers for the reader; Probe counts
	// from 0 like Exchanges does.
	return ProbeRequest{File: m[1], Line: n(m[2]), Col: n(m[3]), A: n(m[4]) - 1, B: n(m[5]) - 1}, true
}

// The reason Probes gives beyond the ones Probe itself can: the row names a
// place the tree no longer has. The sweep that wrote the row and the tree
// being probed are two moments, and anything moved between them lands here
// rather than being matched to whatever now sits nearest.
const ProbeSkipNoSite = "no site at this position — the tree has changed since the sweep that wrote this row"

// Probes matches requests to sites and returns a probe mutation for each pair
// that can carry one, and a skip with its reason for each that cannot. The
// mutation's Name doubles as the probe's marker: it is what the injected code
// prints, so the transcript can be read back per mutation by the same string
// the report shows.
//
// Files are read for the same reason Sweep reads them: the machinery that
// applies a mutation insists its text names one place, and it refuses the
// whole batch on the first that does not. A call written twice is that pair's
// problem, not every row's — so it is a skip here, with Sweep's own reasons,
// rather than a batch-wide error there. An unreadable file still stops
// everything, exactly as it does in Sweep.
func Probes(sites []Site, reqs []ProbeRequest) ([]Mutation, []Skip, error) {
	type pos struct {
		file      string
		line, col int
	}
	at := map[pos]Site{}
	for _, s := range sites {
		at[pos{s.File, s.Line, s.Col}] = s
	}
	text := map[string]string{}
	var muts []Mutation
	var skips []Skip
	for _, q := range reqs {
		s, ok := at[pos{q.File, q.Line, q.Col}]
		if !ok {
			skips = append(skips, Skip{File: q.File, Line: q.Line, A: q.A, B: q.B, Reason: ProbeSkipNoSite})
			continue
		}
		body, ok := text[s.File]
		if !ok {
			b, err := os.ReadFile(s.File)
			if err != nil {
				return nil, nil, err
			}
			body = string(b)
			text[s.File] = body
		}
		marker := probeName(s, [2]int{q.A, q.B})
		from, to, reason := s.Probe(q.A, q.B, marker)
		switch {
		case reason != "":
		case strings.Count(body, from) == 0:
			reason = SkipGone
		case strings.Count(body, from) > 1:
			reason = SkipNotUnique
		}
		if reason != "" {
			skips = append(skips, Skip{File: q.File, Line: q.Line, A: q.A, B: q.B, Reason: reason})
			continue
		}
		muts = append(muts, Mutation{
			Name:     marker,
			File:     s.File,
			From:     from,
			To:       to,
			Packages: []string{pkg(s.File)},
		})
	}
	return muts, skips, nil
}

// probeName is the marker and the report row in one string, built from the
// same position name() uses so the two reports name a pair the same way.
func probeName(s Site, p [2]int) string {
	return "probe " + s.File + ":" + strconv.Itoa(s.Line) + ":" + strconv.Itoa(s.Col) +
		" arguments " + strconv.Itoa(p[0]+1) + " and " + strconv.Itoa(p[1]+1)
}

// The three things a clean probe run can say about a surviving pair. Which one
// applies is read from the transcript by ProbeVerdict, and the words carry the
// work each calls for, because the whole point of probing is that the two
// silences call for opposite work.
const (
	// The suite fed the pair different values, so a test could tell the swap
	// from the original — the survivor means what it appears to mean.
	VerdictDiffers = "the arguments differed under the suite: a test could catch this swap, and none does — write the test"
	// Every evaluation saw equal values: no assertion, however strong, could
	// have caught the swap on these inputs. The fixture is what needs work.
	VerdictNeverDiffered = "reached, and never different: no input the suite has can tell the swap from the original — give the fixture two values it can tell apart"
	// The probe never ran, so the pair was never weighed at all. That is the
	// reach note's finding, confirmed from inside the run.
	VerdictUnreached = "the probe never ran: no test this run executed reaches the call — reach is the problem before the pair is"
)

// ProbeVerdict reads a probe run's transcript for the marker the mutation
// carries, and says which of the three verdicts the run supports.
//
// The counting is substring counting, and parallel tests write stderr
// unsynchronized — a marker line can in principle be torn by an interleaved
// write. A torn line costs one count, which matters only for a call evaluated
// exactly once; there the misreading is "unreached", the verdict that sends
// someone to look at reach and find it fine, not the one that buries a
// defect.
func ProbeVerdict(marker, transcript string) string {
	if strings.Count(transcript, marker+Evaluated) == 0 {
		return VerdictUnreached
	}
	if strings.Count(transcript, marker+Differs) > 0 {
		return VerdictDiffers
	}
	return VerdictNeverDiffered
}
