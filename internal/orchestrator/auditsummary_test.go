package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/workspace"
)

// The audit summary quotes what the audited container did — file paths it
// wrote, hosts it dialed — and those are the container's words, not opossum's.
// A line break inside one used to end the summary's line and start the rest at
// column zero, where the report's own sentences start, so a process being
// audited could write a file whose NAME reads as a clean finding ("no changes
// under /workspace", say). The report about a suspect must not let the suspect
// write any line of it (#573's family; the tables went through row(), errors
// through quoted(), and this writer had neither).
func TestAnAuditedProcessCannotWriteItsOwnSummary(t *testing.T) {
	forged := "FORGED egress: no outbound connections"
	r := &AuditReport{
		Service: "job\n" + forged,
		Command: []string{"sh", "-c", "x\n" + forged},
		Files: AuditFiles{
			Observed:  true,
			Workspace: "/w\n" + forged,
			Changes: []workspace.FileChange{
				{Kind: workspace.Added, Path: "evil\n" + forged},
			},
		},
		Egress: AuditEgress{
			Observed:     true,
			Via:          "proxy\n" + forged,
			Destinations: []string{"1.2.3.4:443\n" + forged},
		},
		Resources: AuditSection{Reason: "r\n" + forged},
	}
	// A second report walks the branches the first cannot: unobserved reasons,
	// the no-changes workspace, the no-outbound via. Each printf site is its
	// own decision to flatten, and a fixture that reaches 7 of 11 leaves 4
	// free to regress (this PR's independent review counted them, one survived
	// mutation per uncovered slot).
	quiet := &AuditReport{
		Service: "job\n" + forged,
		Files: AuditFiles{
			Observed:  true,
			Workspace: "/w\n" + forged,
		},
		Egress: AuditEgress{
			Observed: true,
			Via:      "proxy\n" + forged,
		},
		Resources: AuditSection{Reason: "r\n" + forged},
	}
	unobserved := &AuditReport{
		Service:   "job\n" + forged,
		Files:     AuditFiles{Reason: "f\n" + forged},
		Egress:    AuditEgress{Reason: "e\n" + forged},
		Resources: AuditSection{Reason: "r\n" + forged},
	}
	for name, report := range map[string]*AuditReport{
		"observed with findings": r,
		"observed but quiet":     quiet,
		"unobserved":             unobserved,
	} {
		var out bytes.Buffer
		report.WriteSummary(&out)
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "FORGED") {
				t.Errorf("%s: the audited process wrote a line of its own report:\n%s", name, out.String())
			}
		}
	}
}
