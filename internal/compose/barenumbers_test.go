package compose

import (
	"fmt"
	"strings"
	"testing"
)

// A whole number written bare is the number YAML reads, in the fields opossum reads one from, as docker compose
// v5.5.1 reads it (measured with `config`): `ulimits` (a limit, and its soft and hard), and the `target` and
// `published` of a long-form port. `010` is eight, `0x10` sixteen, `0o10` eight, `1_0` ten: the digits were read as
// text, so `ulimits: {nofile: 010}` was ten limits and `published: 010` a host port the runtime was given as `010`, and
// `0x10`, `0o10`, `1_0` were refused. A number written as a string is read as it was, and `08` (which YAML reads as
// a float, and docker compose refuses as a limit and as a port) is refused as a limit and as a published port. A
// `target` of `+08` is refused as it was (docker compose gives 8), and a `target` of `08` or `"010"` is passed on as written (docker compose gives 8 and 10, the same numbers): not a row here. A limit is given as `soft:hard`, a
// port as `published:target`.
func TestBareWholeNumbersAreTheNumberYAMLReads(t *testing.T) {
	for _, tc := range []struct{ shape, yaml, want string }{
		{"ulimits scalar", "010", "8:8"},
		{"ulimits scalar", "0x10", "16:16"},
		{"ulimits scalar", "0o10", "8:8"},
		{"ulimits scalar", "1_0", "10:10"},
		{"ulimits scalar", "+5", "5:5"},
		{"ulimits scalar", "5", "5:5"},
		{"ulimits scalar", "65536", "65536:65536"},
		{"ulimits scalar", "0x400", "1024:1024"},
		{"ulimits scalar", "08", "REFUSE"},
		{"ulimits scalar", "\"5\"", "5:5"},
		{"ulimits scalar", "\"010\"", "10:10"},
		{"ulimits scalar", "\"0x10\"", "REFUSE"},
		{"ulimits scalar", "0777", "511:511"},
		{"ulimits soft/hard", "010", "8:8"},
		{"ulimits soft/hard", "0x10", "16:16"},
		{"ulimits soft/hard", "0o10", "8:8"},
		{"ulimits soft/hard", "1_0", "10:10"},
		{"ulimits soft/hard", "+5", "5:5"},
		{"ulimits soft/hard", "5", "5:5"},
		{"ulimits soft/hard", "65536", "65536:65536"},
		{"ulimits soft/hard", "0x400", "1024:1024"},
		{"ulimits soft/hard", "08", "REFUSE"},
		{"ulimits soft/hard", "\"5\"", "5:5"},
		{"ulimits soft/hard", "\"010\"", "10:10"},
		{"ulimits soft/hard", "\"0x10\"", "REFUSE"},
		{"ulimits soft/hard", "0777", "511:511"},
		{"port target", "010", "9000:8"},
		{"port target", "0x10", "9000:16"},
		{"port target", "0o10", "9000:8"},
		{"port target", "1_0", "9000:10"},
		{"port target", "+5", "9000:5"},
		{"port target", "5", "9000:5"},
		{"port target", "0x400", "9000:1024"},
		{"port target", "\"5\"", "9000:5"},
		{"port target", "\"0x10\"", "REFUSE"},
		{"port target", "0777", "9000:511"},
		{"port published", "010", "8:80"},
		{"port published", "0x10", "16:80"},
		{"port published", "0o10", "8:80"},
		{"port published", "1_0", "10:80"},
		{"port published", "+5", "5:80"},
		{"port published", "5", "5:80"},
		{"port published", "0x400", "1024:80"},
		{"port published", "08", "REFUSE"},
		{"port published", "\"5\"", "5:80"},
		{"port published", "\"010\"", "010:80"},
		{"port published", "0777", "511:80"},
		{"ulimits scalar", "-0", "REFUSE"},
		{"ulimits soft/hard", "-0", "REFUSE"},
		{"ulimits scalar", "!!float 3", "REFUSE"},
		{"ulimits soft/hard", "!!float 3", "REFUSE"},
		{"ulimits scalar", "!!float \"3\"", "REFUSE"},
		{"ulimits soft/hard", "!!float \"3\"", "REFUSE"},
		{"ulimits scalar", "+08", "REFUSE"},
		{"ulimits soft/hard", "+08", "REFUSE"},
		{"ulimits scalar", "0x7fffffffffffffff", "9223372036854775807:9223372036854775807"},
		{"ulimits soft/hard", "0x7fffffffffffffff", "9223372036854775807:9223372036854775807"},
		{"port published", "-0", "REFUSE"},
		{"port published", "-00", "0:80"},
		{"port published", "+08", "REFUSE"},
		{"port published", "!!float 3", "REFUSE"},
		{"port target", "!!float 3", "9000:3"},
		{"ulimits mixed", "{soft: 08, hard: 8}", "REFUSE"},
		{"ulimits mixed", "{soft: 8, hard: 08}", "REFUSE"},
		{"ulimits mixed", "{soft: 010, hard: 08}", "REFUSE"},
	} {
		t.Run(tc.shape+"/"+tc.yaml, func(t *testing.T) {
			var body string
			switch tc.shape {
			case "ulimits scalar":
				body = "    ulimits:\n      nofile: " + tc.yaml + "\n"
			case "ulimits soft/hard":
				body = "    ulimits:\n      nofile: {soft: " + tc.yaml + ", hard: " + tc.yaml + "}\n"
			case "ulimits mixed":
				body = "    ulimits:\n      nofile: " + tc.yaml + "\n"
			case "port target":
				body = "    ports:\n      - target: " + tc.yaml + "\n        published: \"9000\"\n"
			case "port published":
				body = "    ports:\n      - target: 80\n        published: " + tc.yaml + "\n"
			}
			p, err := Load(writeSizeFile(t, "services:\n  a:\n    image: x\n"+body))
			got := "REFUSE"
			if err == nil {
				if strings.HasPrefix(tc.shape, "ulimits") {
					u := p.Services["a"].Ulimits["nofile"]
					got = fmt.Sprintf("%d:%d", u.Soft, u.Hard)
				} else {
					got = p.Services["a"].Ports[0]
				}
			}
			if got != tc.want {
				t.Errorf("%s: %s: %q, want %q (err %v)", tc.shape, tc.yaml, got, tc.want, err)
			}
		})
	}
}

// A port written in the short form as a bare integer is the number YAML reads, as docker compose v5.5.1 reads it
// (measured with `config`; a single port is the container's, and opossum publishes it on the same host port):
// `010` is 8 and `0777` is 511 (the digits were read as text, so `- 010` was `010:010`, a port the runtime was given as
// `010`), and `0x10`, `0o10`, `1_0`, `+80`, `0b101` were refused. What YAML reads as a float (`080`, `80.0`, `1e2`) and `-0`
// are refused, as they are there. A string (`"010"`) is read as it was: docker compose gives 10, the same number as the
// text, so there is no row for it.
func TestAShortFormPortWrittenBareIsTheNumberYAMLReads(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{"010", "8:8"},
		{"0x10", "16:16"},
		{"0o10", "8:8"},
		{"1_0", "10:10"},
		{"+80", "80:80"},
		{"0777", "511:511"},
		{"0b101", "5:5"},
		{"8080", "8080:8080"},
		{`!!int "010"`, "8:8"},
		{"080", "REFUSE"},
		{"08", "REFUSE"},
		{"-0", "REFUSE"}, // the port 0 once read as an integer: refused as one
		{"0", "REFUSE"},
		{"00", "REFUSE"},
		{"80.0", "REFUSE"},
		{"80.5", "REFUSE"}, // a fraction: what keeps the integer reading to `!!int` is another test's row (a container port that is a fraction)
		{"1e2", "REFUSE"},
		{`"0x10"`, "REFUSE"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			p, err := Load(writeSizeFile(t, "services:\n  a:\n    image: x\n    ports:\n      - "+tc.yaml+"\n"))
			got := "REFUSE"
			if err == nil {
				got = p.Services["a"].Ports[0]
			}
			if got != tc.want {
				t.Errorf("ports: - %s: %q, want %q (err %v)", tc.yaml, got, tc.want, err)
			}
		})
	}
}

// The number is read from the node the alias stands for: a port written as an alias to a bare integer is the number
// YAML reads it as (docker compose v5.5.1: `010` is 8 and `0x10` is 16 there as well).
func TestAShortFormPortThatIsAnAliasToABareIntegerIsTheNumberYAMLReads(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"an anchor in an extension":    {"x-p: &p 010\nservices:\n  a:\n    image: x\n    ports: [*p]\n", "8:8"},
		"an anchor in another service": {"services:\n  b:\n    image: x\n    ports: [&p 0x10]\n  a:\n    image: x\n    ports: [*p]\n", "16:16"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Load(writeSizeFile(t, tc.doc))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["a"].Ports[0]; got != tc.want {
				t.Errorf("ports: [*p]: %q, want %q", got, tc.want)
			}
		})
	}
}
