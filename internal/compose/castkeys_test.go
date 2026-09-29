package compose

import (
	"strings"
	"testing"
)

// #1366, stage 2 (the cast layer): heldToTheSchema's `number|string` and
// `boolean|string` keys let a string through on the strength of its kind —
// docker compose reads that string into the number or boolean, and a string
// that does not read is refused for a reason of its own, not the kind
// mismatch checkServiceShapes already asks about. Measured against docker
// compose v5.5.1, values written quoted so YAML hands them through as
// strings — a bare `cpu_shares: 7.0` is a number before this ever runs, and
// exercises none of this.
func TestServiceKeyCastKeepsWhatItCanReadAndRefusesWhatItCannot(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		wantErr          bool
	}{
		// bool (stdin_open representative; attach/oom_kill_disable/privileged
		// measured to read identically — see the representative-key subtest
		// below. init/read_only/tty are NOT castKeys — see its doc comment).
		{"bool: true", "stdin_open", "true", false},
		{"bool: yaml 1.1 yes", "stdin_open", "yes", false},
		{"bool: yaml 1.1 y, uppercase", "stdin_open", "Y", false},
		{"bool: yaml 1.1 on", "stdin_open", "on", false},
		{"bool: false", "stdin_open", "false", false},
		{"bool: yaml 1.1 no", "stdin_open", "no", false},
		{"bool: yaml 1.1 n, uppercase", "stdin_open", "N", false},
		{"bool: yaml 1.1 off", "stdin_open", "off", false},
		{"bool: bare 1 does not read", "stdin_open", "1", true},
		{"bool: bare t does not read", "stdin_open", "t", true},
		{"bool: empty does not read", "stdin_open", "", true},
		{"bool: unrelated word does not read", "stdin_open", "abc", true},
		// int (cpu_shares representative).
		{"int: plain", "cpu_shares", "7", false},
		{"int: negative", "cpu_shares", "-7", false},
		{"int: leading zero", "cpu_shares", "007", false},
		{"int: leading plus", "cpu_shares", "+7", false},
		{"int: hex does not read", "cpu_shares", "0x10", true},
		{"int: digit separators do not read", "cpu_shares", "1_000", true},
		{"int: a whole number spelled as a float does not read", "cpu_shares", "7.0", true},
		{"int: a true fraction does not read", "cpu_shares", "1.5", true},
		{"int: scientific notation does not read", "cpu_shares", "1e3", true},
		{"int: surrounding space does not read", "cpu_shares", " 7", true},
		{"int: out of int64 range does not read", "cpu_shares", "9223372036854775808", true},
		{"int: empty does not read", "cpu_shares", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    " + tc.key + ": \"" + tc.value + "\"\n"
			_, err := Load(writeTemp(t, body))
			if tc.wantErr && err == nil {
				t.Errorf("%s: %q should not read as %s, and it loaded", tc.key, tc.value, castKeys[tc.key])
			}
			if !tc.wantErr && err != nil {
				t.Errorf("%s: %q should read as %s, and it was refused: %v", tc.key, tc.value, castKeys[tc.key], err)
			}
		})
	}
}

// countCastKeys is how many entries of castKeys are of the given kind — the
// other half of the drift guard the two "every key" tests below use: their
// hard-coded lists catch a key REMOVED from castKeys without a matching
// removal here (the map-iteration version used to miss this), and this
// catches a key ADDED to castKeys without a matching addition here.
func countCastKeys(kind string) int {
	n := 0
	for _, k := range castKeys {
		if k == kind {
			n++
		}
	}
	return n
}

// The bug this whole file exists to pin: cpu_percent's cast is not plain
// strconv.ParseInt the way the other nine int-cast keys' is (it reads
// through a float — "7.0" is a whole number and docker compose takes it), so
// treating it as an int-cast key over-refused this. cpu_percent is not in
// castKeys today (see its doc comment — the missing piece is a bound check,
// not this), so this loads on the strength of the shape check alone; a
// version that put cpu_percent back in castKeys with kind "int" would refuse
// it, and this is the test that would say so directly rather than through
// the golden corpus's laxer exemption.
func TestCPUPercentDoesNotOverRefuseAWholeNumberSpelledAsAFloat(t *testing.T) {
	for _, v := range []string{"7.0", "7e0", "1e1", "7."} {
		body := "services:\n  web:\n    image: alpine\n    cpu_percent: \"" + v + "\"\n"
		if _, err := Load(writeTemp(t, body)); err != nil {
			t.Errorf("cpu_percent=%q should load (docker compose casts it through a float), and it was refused: %v", v, err)
		}
	}
}

// A native (unquoted) numeric literal never reaches the cast check at all —
// it is accepted on the strength of its kind alone, the way it always was.
// cpu_period: 1.5 (a real number, not a string that merely looks like one)
// is legitimate docker compose input: the runtime truncates it, the same as
// mem_swappiness: "1.5" is measured to (a byte-suffix cast, not this check's
// business). A version of the wiring that ran castOK over every value's
// string form regardless of its actual Go type — fmt.Sprint(1.5) is "1.5",
// which does not read as an integer — would refuse this.
//
// Only the schema's `number`-typed keys are asked (cpu_shares, cpu_quota,
// cpu_period, cpu_rt_period, cpu_rt_runtime, pids_limit): the
// `integer`-typed ones among castKeys (cpu_count, oom_score_adj, scale)
// already refuse a non-whole native number through checkServiceShapes' own
// existing kind check, which is not what this test is about. cpu_percent is
// not in castKeys at all (see its doc comment).
func TestANativeNumberNeverReachesTheCastCheck(t *testing.T) {
	for _, key := range []string{"cpu_shares", "cpu_quota", "cpu_period", "cpu_rt_period", "cpu_rt_runtime", "pids_limit"} {
		t.Run(key, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    " + key + ": 1.5\n"
			if _, err := Load(writeTemp(t, body)); err != nil {
				t.Errorf("%s: 1.5 (a native number) should load, and it was refused: %v", key, err)
			}
		})
	}
}

// The refusal names the key, the value, and what it does not read as —
// distinct from checkServiceShapes' "must be a <kind>" wording (the kind was
// already right; this is about the content).
func TestServiceKeyCastRefusalNamesTheKeyAndWhatItDoesNotReadAs(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{
		{"stdin_open", "abc", `services.web.stdin_open "abc" does not read as a boolean`},
		{"cpu_shares", "abc", `services.web.cpu_shares "abc" does not read as an integer`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    " + tc.key + ": \"" + tc.value + "\"\n"
			_, err := Load(writeTemp(t, body))
			if err == nil {
				t.Fatalf("%s: %q should be refused, and it loaded", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q:\n%v", tc.want, err)
			}
		})
	}
}

// Every bool-cast key reads the same set of strings the same way — measured
// individually (docker compose v5.5.1) rather than assumed from stdin_open,
// after attach's error wording (captured for #1369's golden sweep, "decoding
// failed…" rather than the "failed to cast…" the others give) raised the
// question of whether it actually shares their cast at all. It does: the
// wording differs but which strings are accepted does not.
//
// The key list is written out rather than read from castKeys: a key removed
// from that map (as cpu_percent was, mid-review) silently removes its own
// subtest along with it if the loop reads the map, so a coverage gap for
// that key would go unnoticed rather than failing loudly. countCastKeys
// below catches the other direction — a key added to castKeys without a
// matching addition here.
func TestEveryBoolCastKeyReadsTheSameStringsTheSameWay(t *testing.T) {
	boolKeys := []string{"attach", "oom_kill_disable", "privileged", "stdin_open"}
	if n := countCastKeys("bool"); n != len(boolKeys) {
		t.Errorf("castKeys now has %d bool-cast keys, this test's list has %d; update the list", n, len(boolKeys))
	}
	for _, key := range boolKeys {
		if castKeys[key] != "bool" {
			t.Errorf("%s is no longer a bool-cast key in castKeys; update this test's key list", key)
			continue
		}
		t.Run(key, func(t *testing.T) {
			for _, tc := range []struct {
				value   string
				wantErr bool
			}{
				{"true", false}, {"yes", false}, {"on", false}, {"Y", false},
				{"false", false}, {"no", false}, {"off", false}, {"N", false},
				{"1", true}, {"t", true}, {"abc", true}, {"", true},
			} {
				body := "services:\n  web:\n    image: alpine\n    " + key + ": \"" + tc.value + "\"\n"
				_, err := Load(writeTemp(t, body))
				if tc.wantErr && err == nil {
					t.Errorf("%s=%q should not read as a boolean, and it loaded", key, tc.value)
				}
				if !tc.wantErr && err != nil {
					t.Errorf("%s=%q should read as a boolean, and it was refused: %v", key, tc.value, err)
				}
			}
		})
	}
}

// Every int-cast key reads the same set of strings the same way — the
// representative-key spot check above only pins cpu_shares; the others
// (cpu_count, cpu_quota, cpu_period, cpu_rt_period, cpu_rt_runtime,
// oom_score_adj, pids_limit, scale — NOT cpu_percent, which is not in
// castKeys at all; see its doc comment) are asserted here so a change that
// narrows castOK's "int" case for one of them and not the others is caught
// by name.
//
// The key list is written out rather than read from castKeys, the same
// reason as the bool version of this test above — cpu_shares is asked by
// TestServiceKeyCastKeepsWhatItCanReadAndRefusesWhatItCannot instead, so
// countCastKeys' total here is one short of castKeys' own int count on
// purpose.
func TestEveryIntCastKeyReadsTheSameStringsTheSameWay(t *testing.T) {
	intKeys := []string{"cpu_count", "cpu_quota", "cpu_period", "cpu_rt_period",
		"cpu_rt_runtime", "oom_score_adj", "pids_limit", "scale"}
	if n := countCastKeys("int"); n != len(intKeys)+1 {
		t.Errorf("castKeys now has %d int-cast keys, this test plus cpu_shares' own covers %d; update the list", n, len(intKeys)+1)
	}
	for _, key := range intKeys {
		if castKeys[key] != "int" {
			t.Errorf("%s is no longer an int-cast key in castKeys; update this test's key list", key)
			continue
		}
		t.Run(key, func(t *testing.T) {
			for _, tc := range []struct {
				value   string
				wantErr bool
			}{
				{"7", false}, {"-7", false}, {"007", false},
				{"7.0", true}, {"0x10", true}, {"1_000", true}, {"abc", true}, {"", true},
			} {
				body := "services:\n  web:\n    image: alpine\n    " + key + ": \"" + tc.value + "\"\n"
				_, err := Load(writeTemp(t, body))
				if tc.wantErr && err == nil {
					t.Errorf("%s=%q should not read as an integer, and it loaded", key, tc.value)
				}
				if !tc.wantErr && err != nil {
					t.Errorf("%s=%q should read as an integer, and it was refused: %v", key, tc.value, err)
				}
			}
		})
	}
}
