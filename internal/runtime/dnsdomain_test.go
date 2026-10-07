package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// script writes an executable `#!/bin/sh` file and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The DNS domains the runtime lists, the domain not among them, and a listing that failed are three answers, and the third is not the second: an `up` that
// offers to create a domain over a listing that did not come has not checked that one is needed (#1907).
func TestDNSDomainStateTellsAbsentFromUnknown(t *testing.T) {
	listing := func(out string, code int) *Runtime {
		return &Runtime{Bin: script(t, "[ \"$1 $2 $3\" = 'system dns list' ] && { printf '%s' '"+out+"'; exit "+string(rune('0'+code))+"; }\nexit 9\n")}
	}
	for _, tc := range []struct {
		name string
		rt   *Runtime
		want DNSState
	}{
		{"listed", listing("DOMAIN\nopossum\ntest\n", 0), DNSPresent},
		{"listed among others, with blanks round it", listing("DOMAIN\n  opossum  \n", 0), DNSPresent},
		{"not among those listed", listing("DOMAIN\ntest\n", 0), DNSAbsent},
		{"none listed", listing("DOMAIN\n", 0), DNSAbsent},
		{"a longer name is not the domain", listing("DOMAIN\nopossum.local\n", 0), DNSAbsent},
		{"the listing failed", listing("", 1), DNSUnknown},
		{"the listing failed after printing the domain", listing("DOMAIN\nopossum\n", 1), DNSUnknown},
		{"the runtime is not there", &Runtime{Bin: filepath.Join(t.TempDir(), "not-there")}, DNSUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rt.DNSDomainState("opossum"); got != tc.want {
				t.Errorf("DNSDomainState = %v, want %v", got, tc.want)
			}
			if got, want := tc.rt.DNSDomainExists("opossum"), tc.want == DNSPresent; got != want {
				t.Errorf("DNSDomainExists = %v, want %v (only a domain that is listed exists)", got, want)
			}
		})
	}
}

// What sudo is given is the command that is shown, and nothing that is not a name for it: a domain comes from a flag, and one that starts with a hyphen would be an
// option to a program run as root.
func TestCreateDNSDomainRunsTheCommandThatIsShownAndRefusesWhatIsNotAName(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "sudo.log")
	sudo := script(t, "echo \"$*\" >> '"+log+"'\nexit 0\n")
	rt := &Runtime{Bin: "container", SudoBin: sudo}
	if err := rt.CreateDNSDomain("opossum"); err != nil {
		t.Fatalf("CreateDNSDomain: %v", err)
	}
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "container system dns create opossum" {
		t.Errorf("sudo was run with %q, want the arguments `container system dns create opossum`", got)
	}
	if got, want := rt.DNSCreateCommand("opossum"), sudo+" container system dns create opossum"; got != want {
		t.Errorf("the command shown is %q, want %q", got, want)
	}
	if got := (&Runtime{Bin: "container"}).DNSCreateCommand("opossum"); got != "sudo container system dns create opossum" {
		t.Errorf("the command shown by default is %q", got)
	}
	for _, bad := range []string{"", "-x", "--help", "a b", "a;b", "a/b", "a\nb", ".a", "a.", "-", "a$(x)"} {
		_ = os.Remove(log)
		if err := rt.CreateDNSDomain(bad); err == nil {
			t.Errorf("CreateDNSDomain(%q) should refuse a name that is not one for sudo", bad)
		}
		if _, err := os.Stat(log); err == nil {
			t.Errorf("sudo was run for %q", bad)
		}
	}
	for _, good := range []string{"opossum", "test", "my-app.local", "a1", "A"} {
		if !ValidDNSDomainName(good) {
			t.Errorf("%q is a domain name", good)
		}
	}
	// A dry-run runs nothing, sudo included.
	_ = os.Remove(log)
	dry := &Runtime{Bin: "container", SudoBin: sudo, DryRun: true}
	if err := dry.CreateDNSDomain("opossum"); err != nil {
		t.Errorf("a dry-run is not an error: %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("a dry-run ran sudo")
	}
	// A sudo that fails is an error that says what was run.
	failing := &Runtime{Bin: "container", SudoBin: script(t, "exit 1\n")}
	if err := failing.CreateDNSDomain("opossum"); err == nil || !strings.Contains(err.Error(), "container system dns create opossum") {
		t.Errorf("a sudo that fails is an error naming the command, got %v", err)
	}
}

// What sudo prints goes to stderr and not to stdout: `doctor --fix --format json` writes a document to stdout, and a line from a command that ran beside it is a
// document that does not parse (#1907).
func TestCreateDNSDomainKeepsWhatSudoPrintsOffStdout(t *testing.T) {
	sudo := script(t, "echo 'a line from sudo'\necho 'and one on stderr' >&2\nexit 0\n")
	rt := &Runtime{Bin: "container", SudoBin: sudo}
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	runErr := rt.CreateDNSDomain("opossum")
	os.Stdout, os.Stderr = oldOut, oldErr
	outW.Close()
	errW.Close()
	read := func(f *os.File) string {
		b := make([]byte, 4096)
		n, _ := f.Read(b)
		return string(b[:n])
	}
	gotOut, gotErr := read(outR), read(errR)
	if runErr != nil {
		t.Fatalf("CreateDNSDomain: %v", runErr)
	}
	if gotOut != "" {
		t.Errorf("what sudo printed reached stdout: %q", gotOut)
	}
	if !strings.Contains(gotErr, "a line from sudo") || !strings.Contains(gotErr, "and one on stderr") {
		t.Errorf("what sudo printed is on stderr, with the question: %q", gotErr)
	}
	// And the error names the command that was run, with the sudo that it was run with.
	failing := &Runtime{Bin: "container", SudoBin: script(t, "exit 1\n")}
	if err := failing.CreateDNSDomain("opossum"); err == nil || !strings.Contains(err.Error(), failing.SudoBin+" container system dns create opossum") {
		t.Errorf("the error names the command with its sudo, got %v", err)
	}
}
