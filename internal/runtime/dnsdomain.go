package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// DNSState is what is known of a local DNS domain: that the runtime lists it, that it lists others, or that it could not be asked. The last is not the second:
// an `opossum up` that says a domain is not there, and offers to create it, over a listing that failed, offers a thing nobody has checked is needed (#1907).
type DNSState int

const (
	DNSAbsent  DNSState = iota // `container system dns list` answered and the domain is not in it
	DNSPresent                 // it answered and the domain is in it
	DNSUnknown                 // it did not answer (the command failed)
)

// DNSDomainState asks the runtime for its DNS domains and says whether domain is one of them, or that it could not be told.
func (r *Runtime) DNSDomainState(domain string) DNSState {
	out, err := r.capture("system", "dns", "list")
	if err != nil {
		return DNSUnknown
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == domain {
			return DNSPresent
		}
	}
	return DNSAbsent
}

// DNSDomainExists reports whether a local DNS domain has been created (via `sudo container system dns create <domain>`). A listing that failed is not a
// domain: DNSDomainState is the one that tells the two apart.
func (r *Runtime) DNSDomainExists(domain string) bool {
	return r.DNSDomainState(domain) == DNSPresent
}

// dnsDomainName is what a domain given to sudo may be: letters, digits, dots and hyphens, starting and ending with a letter or a digit. The name comes from a
// flag (`--dns-domain`), and a value that starts with a hyphen is read by `container` as an option of its own — to a program run as root.
var dnsDomainName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// ValidDNSDomainName reports whether CreateDNSDomain would take domain.
func ValidDNSDomainName(domain string) bool { return dnsDomainName.MatchString(domain) }

// DNSCreateCommand is the command CreateDNSDomain runs, as it is written for someone to read or to type. What is asked about and what is run are one string
// built in one place, so that the question is never about a command other than the one that follows a yes.
func (r *Runtime) DNSCreateCommand(domain string) string {
	sudo := r.SudoBin
	if sudo == "" {
		sudo = "sudo"
	}
	return sudo + " " + r.Bin + " system dns create " + domain
}

// CreateDNSDomain runs `sudo container system dns create <domain>` — the command opossum prints for someone to type — with the terminal this process has, so
// that sudo asks for the password itself: opossum never reads, passes or keeps it. It is run only where someone has said yes to exactly this command
// (internal/dnsoffer asks, and asks only at a terminal): nothing in this package calls it, and it refuses what is not a domain name. A dry-run runs nothing.
func (r *Runtime) CreateDNSDomain(domain string) error {
	if !ValidDNSDomainName(domain) {
		return fmt.Errorf("%q is not a domain name that opossum will give to sudo", domain)
	}
	if r.DryRun {
		return nil
	}
	sudo := r.SudoBin
	if sudo == "" {
		sudo = "sudo"
	}
	cmd := exec.CommandContext(r.baseCtx(), sudo, r.Bin, "system", "dns", "create", domain)
	// What it prints goes to stderr, with the question that came before it: stdout is a command's output (`doctor --fix --format json` writes a document to it),
	// and a line from sudo on it is a document that does not parse.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	if len(r.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("`%s` did not succeed: %w", r.DNSCreateCommand(domain), err)
	}
	return nil
}
