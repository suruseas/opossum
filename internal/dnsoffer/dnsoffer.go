// Package dnsoffer asks whether to create the local DNS domain, and creates it on a yes (#1907). It is the one place that decides whether `sudo` is run for
// that, and it decides by the rules a person gave for it: sudo is never run without being asked about first; it is asked only at a terminal, on both the
// input and the output the question is written to; the answer is a yes only for `y` or `yes` — an empty line, end of input, `n`, and anything else are a no; and
// there is no setting, in the environment or anywhere else, that turns the question into a yes.
package dnsoffer

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Outcome is what an Offer came to.
type Outcome int

const (
	// NotAsked: nothing was asked and nothing was run — not at a terminal, or a name that is not one to give to sudo. The caller says what it said before.
	NotAsked Outcome = iota
	// Declined: it was asked and the answer was not a yes. Nothing was run.
	Declined
	// Failed: it was asked, the answer was yes, and the command did not succeed. The error was written to Out.
	Failed
	// Created: it was asked, the answer was yes, and the command succeeded.
	Created
)

// Offer is what is needed to ask and, on a yes, to do. Every field is set by the caller; a zero Offer asks nothing and runs nothing.
type Offer struct {
	// Interactive reports whether this is a terminal in the sense that matters: both the input the answer is read from and the output the question is written
	// to. It is asked each time, and where it is nil the answer is no.
	Interactive func() bool
	// In is where the answer is read from, one line; Out is where the question is written.
	In  io.Reader
	Out io.Writer
	// Valid reports whether a domain is a name that may be given to sudo; where it is nil no name is.
	Valid func(domain string) bool
	// Command is the command that Create runs, as it is shown in the question; Create runs it.
	Command func(domain string) string
	Create  func(domain string) error
}

// Ask asks whether to create the domain and, only if the answer is yes, creates it.
func (o *Offer) Ask(domain string) Outcome {
	if o == nil || o.Interactive == nil || !o.Interactive() || o.Valid == nil || !o.Valid(domain) ||
		o.In == nil || o.Out == nil || o.Command == nil || o.Create == nil {
		return NotAsked
	}
	fmt.Fprintf(o.Out, "DNS domain %q not found — services won't resolve each other by name.\n", domain)
	fmt.Fprintf(o.Out, "Create it now? This runs: %s [y/N] ", o.Command(domain))
	line, _ := bufio.NewReader(o.In).ReadString('\n')
	if !strings.HasSuffix(line, "\n") { // the input ended where the question stopped
		fmt.Fprintln(o.Out)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
	default:
		return Declined
	}
	if err := o.Create(domain); err != nil {
		fmt.Fprintf(o.Out, "%v\n", err)
		return Failed
	}
	fmt.Fprintf(o.Out, "DNS domain %q created.\n", domain)
	return Created
}
