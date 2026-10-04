package orchestrator

import (
	"testing"
	"time"
)

// What decides that a followed container has ended (exitWatch) takes the time of each look and what
// the look saw, and nothing else: the rows below give both, so each boundary is kept as a boundary —
// a look one nanosecond short of the settle, one at it — and not by a row that waits for a clock a
// slow `inspect` outruns (#1717: a settle 1.5 times too long was invisible to the rows that wait,
// once they were made to survive a slow `inspect`, #1713). The rows that wait stay as the check that
// the end reaches the stream.

func withSettle(t *testing.T, d time.Duration) {
	t.Helper()
	saved := LogsExitSettle
	LogsExitSettle = d
	t.Cleanup(func() { LogsExitSettle = saved })
}

// step is one look: how long after the first one it was made, and what it saw.
type step struct {
	at time.Duration
	l  look
}

func runWatch(steps []step) (w exitWatch, settledAfter []bool) {
	t0 := time.Unix(1_000_000, 0)
	for _, st := range steps {
		w.see(t0.Add(st.at), st.l)
		settledAfter = append(settledAfter, w.settled())
	}
	return w, settledAfter
}

func TestAContainerHasEndedOnlyOnceItHasStayedStoppedForTheWholeSettle(t *testing.T) {
	const settle = 300 * time.Millisecond
	withSettle(t, settle)
	// The first look that saw it stopped is what the count runs from — not the stop, which was
	// somewhere between that look and the one before it.
	const stopped = 50 * time.Millisecond
	for _, tc := range []struct {
		name  string
		steps []step
		want  bool // settled after the last look
	}{
		{"a look one nanosecond short of the settle", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle - 1, lookNotRunning}}, false},
		{"a look exactly the settle after the first that saw it stopped", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookNotRunning}}, true},
		{"a look one nanosecond past the settle", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle + 1, lookNotRunning}}, true},
		{"the count is not from the last look that saw it running", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {settle, lookNotRunning}}, false},
		{"the first look that saw it stopped starts the count and ends nothing", []step{
			{0, lookRunning}, {stopped, lookNotRunning}}, false},
		{"a container never seen running is never the end", []step{
			{0, lookNotRunning}, {settle * 10, lookNotRunning}, {settle * 100, lookNotRunning}}, false},
		{"a look the runtime does not answer changes nothing: the count goes on through it", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookUnknown}, {stopped + settle + 1, lookNotRunning}}, true},
		{"a look the runtime does not answer is not the end either", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookUnknown}}, false},
		{"a running look starts the count again", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + 100*time.Millisecond, lookRunning},
			{stopped + 200*time.Millisecond, lookNotRunning}, {stopped + settle + 1*time.Millisecond, lookNotRunning}}, false},
		{"…and counts from the look after it that saw it stopped", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + 100*time.Millisecond, lookRunning},
			{stopped + 200*time.Millisecond, lookNotRunning}, {stopped + 200*time.Millisecond + settle, lookNotRunning}}, true},
		{"a gap while restart is under way is not counted, however long", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle*10, lookRestarting},
			{stopped + settle*10 + 1, lookNotRunning}}, false},
		{"…and the count then starts from the first look after it", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle*10, lookRestarting},
			{stopped + settle*10 + 1, lookNotRunning}, {stopped + settle*10 + 1 + settle, lookNotRunning}}, true},
		{"restart under way takes back an end already decided", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookNotRunning}, {stopped + settle + 1, lookRestarting}}, false},
		{"a running look takes back an end already decided", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookNotRunning}, {stopped + settle + 1, lookRunning}}, false},
		{"looks under the settle in between do not move the count", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + 100*time.Millisecond, lookNotRunning},
			{stopped + 200*time.Millisecond, lookNotRunning}, {stopped + settle, lookNotRunning}}, true},
		{"a look the runtime does not answer keeps an end already decided", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookNotRunning}, {stopped + settle + 1, lookUnknown}}, true},
		{"a look the runtime does not answer is not seeing it running", []step{
			{0, lookUnknown}, {stopped, lookNotRunning}, {stopped + settle*10, lookNotRunning}}, false},
		{"restart under way is not seeing it running", []step{
			{0, lookRestarting}, {stopped, lookNotRunning}, {stopped + settle*10, lookNotRunning}}, false},
		{"an end once decided stays through the looks after it", []step{
			{0, lookRunning}, {stopped, lookNotRunning}, {stopped + settle, lookNotRunning}, {stopped + settle + 1, lookNotRunning},
			{stopped + settle*5, lookUnknown}, {stopped + settle*9, lookNotRunning}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, settled := runWatch(tc.steps)
			if got := settled[len(settled)-1]; got != tc.want {
				t.Errorf("settled after the last look = %v, want %v (settle %v, settled after each look: %v)", got, tc.want, settle, settled)
			}
		})
	}
}

// The same rows with another settle: the boundary is the settle's, not 300 ms.
func TestTheBoundaryIsTheSettlesWhateverItIs(t *testing.T) {
	for _, settle := range []time.Duration{time.Second, 3 * time.Second} {
		withSettle(t, settle)
		short, _ := runWatch([]step{{0, lookRunning}, {10, lookNotRunning}, {10 + settle - 1, lookNotRunning}})
		if short.settled() {
			t.Errorf("settle %v: settled one nanosecond short of it", settle)
		}
		at, _ := runWatch([]step{{0, lookRunning}, {10, lookNotRunning}, {10 + settle, lookNotRunning}})
		if !at.settled() {
			t.Errorf("settle %v: not settled exactly at it", settle)
		}
	}
}

// The stream is ended once the container has settled and nothing has been written for the drain,
// a line being written to a slow reader counting as one.
func TestAWriterIsIdleOnlyOnceNothingHasBeenWrittenForTheWholeSpanAndNothingIsBeing(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	const span = 150 * time.Millisecond
	for _, tc := range []struct {
		name    string
		since   time.Duration
		writing int32
		want    bool
	}{
		{"one nanosecond short of the span", span - 1, 0, false},
		{"exactly the span", span, 0, true},
		{"past the span", span + 1, 0, true},
		{"a write under way, long after the last", span * 100, 1, false},
		{"two writes under way", span * 100, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lw := &lastWrite{}
			lw.at.Store(t0.UnixNano())
			lw.writing.Store(tc.writing)
			if got := lw.idle(t0.Add(tc.since), span); got != tc.want {
				t.Errorf("idle(%v after the last write, %d under way) = %v, want %v", tc.since, tc.writing, got, tc.want)
			}
		})
	}
}
