package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// `up` offers to create the DNS domain only where it is not there, and says what it said before where it is not offered, where the answer is not a yes, and where
// the command did not work. A listing that failed is not a domain that is not there: nothing is offered over it, and nothing is said to create it (#1907).
func TestUpOffersTheDNSDomainOnlyWhereItIsNotThere(t *testing.T) {
	const warning = `DNS domain "opossum" not found`
	const typeIt = "sudo container system dns create opossum"
	for _, tc := range []struct {
		name     string
		env      []string
		offer    func(asked *[]string) func(string) bool // nil = no offer is set
		dry      bool
		wantAsk  int
		wantWarn bool
		wantNote bool
	}{
		{"not there, and the offer creates it", nil, func(a *[]string) func(string) bool {
			return func(d string) bool { *a = append(*a, d); return true }
		}, false, 1, false, false},
		{"not there, and the answer is not a yes", nil, func(a *[]string) func(string) bool {
			return func(d string) bool { *a = append(*a, d); return false }
		}, false, 1, true, false},
		{"not there, and no offer is set", nil, nil, false, 0, true, false},
		{"there", []string{"DNS_DOMAINS=opossum"}, func(a *[]string) func(string) bool {
			return func(d string) bool { *a = append(*a, d); return true }
		}, false, 0, false, false},
		{"there among others", []string{"DNS_DOMAINS=test opossum"}, func(a *[]string) func(string) bool {
			return func(d string) bool { *a = append(*a, d); return true }
		}, false, 0, false, false},
		{"the listing failed", []string{"DNS_LIST_FAIL=1"}, func(a *[]string) func(string) bool {
			return func(d string) bool { *a = append(*a, d); return true }
		}, false, 0, false, true},
		{"a dry-run asks nothing", nil, func(a *[]string) func(string) bool {
			return func(d string) bool { *a = append(*a, d); return true }
		}, true, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			p := project("pj", map[string]*compose.Service{"web": {Image: "nginx:alpine"}})
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			var asked []string
			if tc.offer != nil {
				o.OfferDNSDomain = tc.offer(&asked)
			}
			if tc.dry {
				o.SetDryRun(true)
			}
			if err := o.Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			s := out.String()
			if len(asked) != tc.wantAsk || (tc.wantAsk > 0 && asked[0] != "opossum") {
				t.Errorf("the offer was asked %v, want %d time(s) with the domain", asked, tc.wantAsk)
			}
			if got := strings.Contains(s, warning); got != tc.wantWarn {
				t.Errorf("the warning that the domain is not found: %v, want %v; out: %s", got, tc.wantWarn, s)
			}
			if tc.wantWarn && !strings.Contains(s, typeIt) {
				t.Errorf("the warning says the command to type (%q) as it did; out: %s", typeIt, s)
			}
			if got := strings.Contains(s, "could not list the DNS domains"); got != tc.wantNote {
				t.Errorf("the note that the listing failed: %v, want %v; out: %s", got, tc.wantNote, s)
			}
			if tc.wantNote && strings.Contains(s, "Create it once with") {
				t.Errorf("a listing that failed is not a reason to create the domain; out: %s", s)
			}
			if !strings.Contains(s, "web") {
				t.Errorf("up goes on; out: %s", s)
			}
		})
	}
}

// Where there is no DNS domain to speak of, nothing is checked or offered.
func TestUpOffersNothingWhereThereIsNoDNSDomain(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("pj", map[string]*compose.Service{"web": {Image: "nginx:alpine"}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "", &out)
	asked := 0
	o.OfferDNSDomain = func(string) bool { asked++; return true }
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if asked != 0 || strings.Contains(out.String(), "DNS domain") {
		t.Errorf("no domain, no question: asked %d, out: %s", asked, out.String())
	}
	_ = runtime.DNSAbsent
}
