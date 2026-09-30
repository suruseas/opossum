package compose

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
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
// castKeys today (see its doc comment — its cast is asked through
// cpuPercentMismatch instead, for the bound and whole-number check that kind
// alone does not cover), so this loads on the strength of the shape check
// and cpuPercentMismatch together; a version that put cpu_percent back in
// castKeys with kind "int" would refuse it, and this is the test that would
// say so directly rather than through the golden corpus's laxer exemption.
func TestCPUPercentDoesNotOverRefuseAWholeNumberSpelledAsAFloat(t *testing.T) {
	for _, v := range []string{"7.0", "7e0", "1e1", "7.", "5_0", "1_00"} {
		body := "services:\n  web:\n    image: alpine\n    cpu_percent: \"" + v + "\"\n"
		if _, err := Load(writeTemp(t, body)); err != nil {
			t.Errorf("cpu_percent=%q should load (docker compose casts it through a float), and it was refused: %v", v, err)
		}
	}
}

// cpu_percent's own cast/bound check (#1366, stage 3): ParseFloat, then a
// whole-number requirement and the schema's own 0–100 bound, both of which
// checkServiceShapes' kind check already applies to a *native* cpu_percent
// value (7.5 or 150 unquoted) but never reached a string before this.
// Measured against docker compose v5.5.1: "abc" is a cast failure of its
// own wording; "7.5", "150", "-1" and "1_000" (which ParseFloat reads as
// 1000, the same as "1_00" reads as 100 — see the doc comment on castKeys)
// all parse fine and are refused by the bound/whole-number check instead.
func TestCPUPercentCastAndBoundCheck(t *testing.T) {
	for _, tc := range []struct {
		name, value, wantErr string
	}{
		{"in bounds, whole", "50", ""},
		{"lower bound, inclusive", "0", ""},
		{"upper bound, inclusive", "100", ""},
		{"does not read as a number at all", "abc", `cpu_percent "abc" does not read as a number`},
		{"a true fraction", "7.5", `cpu_percent 7.5 is not a whole number`},
		{"below the lower bound", "-1", `cpu_percent -1 is below the least, 0`},
		{"above the upper bound", "101", `cpu_percent 101 is above the most, 100`},
		{"digit separators read past the bound", "1_000", `cpu_percent 1_000 is above the most, 100`},
		// Pins ParseFloat at 64 bits, not 32: at float32 precision this value
		// rounds to exactly 100.0 and would wrongly load. Measured on docker
		// compose v5.5.1: refused.
		{"just past the bound, below float32 precision", "100.000001", `cpu_percent 100.000001 is not a whole number`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    cpu_percent: \"" + tc.value + "\"\n"
			_, err := Load(writeTemp(t, body))
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("cpu_percent=%q should load, and it was refused: %v", tc.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("cpu_percent=%q should be refused, and it loaded", tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal does not say %q:\n%v", tc.wantErr, err)
			}
		})
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
			// cpu_count and scale are the keys here with a lower bound (0), so "-7"
			// reads as an integer and is then refused for the bound (#1451, #1462):
			// docker compose v5.5.1 refuses cpu_count: "-1" and scale: "-1" alike.
			negativeRefused := key == "cpu_count" || key == "scale"
			for _, tc := range []struct {
				value   string
				wantErr bool
			}{
				{"7", false}, {"-7", negativeRefused}, {"007", false},
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

// The bounds of the integer a string reads into (#1451): the schema gives
// oom_score_adj −1000 to 1000 and cpu_count at least 0, and docker compose
// refuses a string outside them (measured, v5.5.1, `docker compose config`:
// oom_score_adj "1001", "-1001", "2000" and cpu_count "-1" rc 1; the edges
// "-1000", "1000", "0", and the spellings "+5" and "-0" rc 0). Only the keys
// the schema bounds are checked: cpu_shares, cpu_quota, cpu_period and
// pids_limit take "-1" in docker compose as well as here, so a check applied to
// every int key rather than read off each key's own schema would over-refuse.
func TestAnIntStringOutsideTheSchemasBoundsIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, wantErr string
	}{
		{"oom_score_adj at the upper edge", "oom_score_adj", "1000", ""},
		{"oom_score_adj at the lower edge", "oom_score_adj", "-1000", ""},
		{"oom_score_adj a plus sign", "oom_score_adj", "+5", ""},
		{"oom_score_adj minus zero", "oom_score_adj", "-0", ""},
		{"oom_score_adj one past the upper edge", "oom_score_adj", "1001", `oom_score_adj 1001 is above the most, 1000`},
		{"oom_score_adj one past the lower edge", "oom_score_adj", "-1001", `oom_score_adj -1001 is below the least, -1000`},
		{"oom_score_adj far above", "oom_score_adj", "2000", `oom_score_adj 2000 is above the most, 1000`},
		// The refusing side of the spellings the loading side above accepts (docker
		// compose v5.5.1 refuses each: rc 1): a sign, leading zeros, and a value
		// that only lands in range when it is cut to 32 bits.
		{"oom_score_adj a plus sign past the edge", "oom_score_adj", "+1001", `oom_score_adj +1001 is above the most, 1000`},
		{"oom_score_adj leading zeros past the edge", "oom_score_adj", "0001001", `oom_score_adj 0001001 is above the most, 1000`},
		{"oom_score_adj negative with leading zeros", "oom_score_adj", "-0001001", `oom_score_adj -0001001 is below the least, -1000`},
		{"oom_score_adj past what 32 bits hold", "oom_score_adj", "4294968296", `oom_score_adj 4294968296 is above the most, 1000`},
		{"cpu_count leading zeros below its least", "cpu_count", "-0001", `cpu_count -0001 is below the least, 0`},
		{"cpu_count at its least", "cpu_count", "0", ""},
		{"cpu_count above its least", "cpu_count", "2", ""},
		{"cpu_count a plus sign", "cpu_count", "+2", ""},
		{"cpu_count below its least", "cpu_count", "-1", `cpu_count -1 is below the least, 0`},
		{"cpu_shares has no bound", "cpu_shares", "-1", ""},
		{"cpu_quota has no bound", "cpu_quota", "-1", ""},
		{"cpu_period has no bound", "cpu_period", "-1", ""},
		{"pids_limit has no bound", "pids_limit", "-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    " + tc.key + ": \"" + tc.value + "\"\n"
			_, err := Load(writeTemp(t, body))
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("%s=%q should load, and it was refused: %v", tc.key, tc.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s=%q should be refused, and it loaded", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal does not say %q:\n%v", tc.wantErr, err)
			}
		})
	}
}

// The bound is asked of every service and every key of it, not only of a
// service named web with nothing else in it (#1451 review): here the offending
// service is the second in name order and carries other keys, and the one before
// it is in range.
func TestABoundIsCheckedOnAnyServiceAndAmongOtherKeys(t *testing.T) {
	body := "services:\n" +
		"  api:\n    image: alpine\n    oom_score_adj: \"5\"\n    command: [\"true\"]\n" +
		"  worker:\n    image: alpine\n    command: [\"true\"]\n    environment:\n      A: b\n    cpu_count: \"-1\"\n"
	_, err := Load(writeTemp(t, body))
	if err == nil {
		t.Fatal("services.worker.cpu_count \"-1\" should be refused, and the file loaded")
	}
	if !strings.Contains(err.Error(), `services.worker.cpu_count -1 is below the least, 0`) {
		t.Errorf("the refusal does not name services.worker.cpu_count:\n%v", err)
	}
}

// scale and deploy.replicas below zero (#1462). docker compose v5.5.1 refuses
// each, native or as a string, with `must be greater than or equal to 0` — not
// through the schema, which gives them no bound, so the loader had nothing that
// asked. Measured (`docker compose config`, rc): scale -1, "-1", -1.0, -1e0 and
// deploy.replicas -1, "-1", -1.0 all 1; scale 0, "0", "-0" and "+2" are 0. The one
// that is at fault is `api`, in the middle of three in name order (a fine one
// before it and a fine one after), and it is not called `web`: a check that looked only at the first
// or the last service, or only at one named web, would pass this file.
func TestAScaleOrReplicaCountBelowZeroIsRefused(t *testing.T) {
	head := "services:\n  aaa:\n    image: alpine\n    scale: 1\n  worker:\n    image: alpine\n    scale: 2\n  api:\n    image: alpine\n"
	for _, tc := range []struct {
		name, tail, wantErr string
	}{
		{"scale zero", "    scale: 0\n", ""},
		{"scale a string zero", "    scale: \"0\"\n", ""},
		{"scale a string minus zero", "    scale: \"-0\"\n", ""},
		{"scale a plus sign", "    scale: \"+2\"\n", ""},
		{"scale a float zero", "    scale: 0.0\n", ""},
		{"scale a float minus zero", "    scale: -0.0\n", ""},
		{"replicas a float minus zero", "    deploy:\n      replicas: -0.0\n", ""},
		{"scale native negative", "    scale: -1\n", `services.api.scale -1 is below the least, 0`},
		{"scale string negative", "    scale: \"-1\"\n", `services.api.scale -1 is below the least, 0`},
		// The value in the sentence is the one written, not a fixed one: -3 as a
		// number and "-01" as a string (which reads as -1 and is refused as written).
		{"scale a different native negative", "    scale: -3\n", `services.api.scale -3 is below the least, 0`},
		{"scale a string with a leading zero", "    scale: \"-01\"\n", `services.api.scale -01 is below the least, 0`},
		{"scale a whole number with a fraction part", "    scale: -1.0\n", `services.api.scale -1 is below the least, 0`},
		{"scale in exponent form", "    scale: -1e0\n", `services.api.scale -1 is below the least, 0`},
		{"replicas zero", "    deploy:\n      replicas: 0\n", ""},
		{"replicas native negative", "    deploy:\n      replicas: -1\n", `services.api.deploy.replicas -1 is below the least, 0`},
		{"replicas string negative", "    deploy:\n      replicas: \"-1\"\n", `services.api.deploy.replicas -1 is below the least, 0`},
		{"replicas a whole number with a fraction part", "    deploy:\n      replicas: -1.0\n", `services.api.deploy.replicas -1 is below the least, 0`},
		// `profiles: []` names no profile, so the service is always active and docker
		// compose asks it (measured, rc 1): an exclusion that read "has a profiles
		// key" rather than "names one" would let this through.
		{"scale below zero with an empty profiles list", "    scale: -1\n    profiles: []\n", `services.api.scale -1 is below the least, 0`},
		{"scale below zero behind a profile", "    scale: -1\n    profiles: [x]\n", ""},
		{"replicas below zero behind a profile", "    deploy:\n      replicas: -1\n    profiles: [x]\n", ""},
		{"replicas next to other deploy keys", "    deploy:\n      mode: replicated\n      replicas: -3\n", `services.api.deploy.replicas -3 is below the least, 0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, head+tc.tail))
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("should load, and it was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("should be refused, and it loaded")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal does not say %q:\n%v", tc.wantErr, err)
			}
		})
	}
}

// docker compose checks the count of the model it built out of every file, not of
// each file (#1462; measured, v5.5.1, rc): a base's `scale: -1` set to 2 by an
// override is fine (0), and `deploy.replicas` alike; an override that sets it below
// zero is refused (1); an `extends` that takes a base service's `scale: -1` and
// sets its own is fine (0), and one that leaves it is refused (1), while a sibling
// service of the extended file that nothing extends is not asked (0). A service
// behind `profiles:` is asked only once the profile is active, which the loader
// does not know: `--profile x` with a negative `scale` on it is refused by docker
// compose and taken here, and without the profile both take it.
func TestTheCountIsAskedOfTheMergedProjectNotOfEachFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const svc = "services:\n  web:\n    image: alpine\n"
	baseNeg := write("base-neg.yaml", svc+"    scale: -1\n")
	baseRepNeg := write("base-rep-neg.yaml", svc+"    deploy:\n      replicas: -1\n")
	baseOK := write("base-ok.yaml", svc)
	setTwo := write("set-two.yaml", "services:\n  web:\n    scale: 2\n")
	setOne := write("set-one.yaml", "services:\n  web:\n    deploy:\n      replicas: 1\n")
	setNeg := write("set-neg.yaml", "services:\n  web:\n    scale: -1\n")
	resetIt := write("reset.yaml", "services:\n  web:\n    scale: !reset null\n")
	for _, tc := range []struct {
		name  string
		files []string
		want  string // "" loads
	}{
		{"a base scale set back by an override", []string{baseNeg, setTwo}, ""},
		{"a base replicas set back by an override", []string{baseRepNeg, setOne}, ""},
		{"a base scale reset by an override", []string{baseNeg, resetIt}, ""},
		{"an override that sets scale below zero", []string{baseOK, setNeg}, "services.web.scale -1 is below the least, 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles(tc.files, nil)
			if tc.want == "" {
				if err != nil {
					t.Errorf("docker compose takes this, and it was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a refusal saying %q, got %v", tc.want, err)
			}
		})
	}
	// extends across files, and a profile-gated service
	extBase := write("ext-base.yaml", "services:\n  basesvc:\n    image: alpine\n    scale: -1\n  sibling:\n    image: alpine\n    scale: -1\n")
	for _, tc := range []struct {
		name, body, want string
	}{
		{"extends that leaves the count", "services:\n  web:\n    extends:\n      file: " + extBase + "\n      service: basesvc\n", "scale -1 is below the least, 0"},
		{"extends that sets its own", "services:\n  web:\n    extends:\n      file: " + extBase + "\n      service: basesvc\n    scale: 2\n", ""},
		{"a service behind a profile", svc + "    scale: -1\n    profiles: [x]\n", ""},
		// A service that extends one behind a profile takes the profile with it, so
		// its own negative count is asked only once the profile is active: the
		// check is of the document with `extends` read, not of the one as written.
		{"a service that takes a profile by extends", "services:\n  base:\n    image: alpine\n    profiles: [x]\n  web:\n    extends: base\n    scale: -1\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if tc.want == "" {
				if err != nil {
					t.Errorf("docker compose takes this, and it was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a refusal saying %q, got %v", tc.want, err)
			}
		})
	}
}

// Two services at fault are named in name order, the same one every time: the
// merged check walks the services sorted, and a walk in map order would name
// whichever came first that run (#1469). Written with the names in the opposite
// order to the one they sort in, and enough of them that a map order would show.
func TestTwoServicesBelowZeroNameTheFirstInNameOrder(t *testing.T) {
	body := "services:\n" +
		"  zz:\n    image: alpine\n    scale: -1\n" +
		"  mm:\n    image: alpine\n    deploy:\n      replicas: -2\n" +
		"  bb:\n    image: alpine\n    scale: -3\n" +
		"  aa:\n    image: alpine\n    scale: 1\n"
	for round := 0; round < 20; round++ {
		_, err := Load(writeTemp(t, body))
		if err == nil || !strings.Contains(err.Error(), "services.bb.scale -3 is below the least, 0") {
			t.Fatalf("round %d: want bb named (the first at fault in name order), got %v", round, err)
		}
	}
}

// scale and deploy.replicas that are two different numbers (#1464). docker compose
// v5.5.1 reads both as numbers and refuses a service that gives them two: measured
// (`docker compose config`, rc) scale 2 with replicas 3, "2" with "3", 0 with 1 and
// 1 with 0 are 1; 2 with 2, "2" with 2, 2 with "2", "+2" with 2 and "02" with 2 are
// 0, as are one of them alone, a service behind a profile, and an override that
// makes them agree. It is asked of the merged project, like the count itself.
func TestScaleAndReplicasThatAreDifferentNumbersAreRefused(t *testing.T) {
	const distinct = "can't set distinct values on `scale` (%d) and `deploy.replicas` (%d)"
	head := "services:\n  web:\n    image: alpine\n    scale: 1\n  api:\n    image: alpine\n"
	for _, tc := range []struct {
		name, tail string
		scale, rep int64 // both non-zero-valued only for the refusing rows
		refuse     bool
	}{
		{"the same number", "    scale: 2\n    deploy:\n      replicas: 2\n", 0, 0, false},
		{"a string and a number", "    scale: \"2\"\n    deploy:\n      replicas: 2\n", 0, 0, false},
		{"a number and a string", "    scale: 2\n    deploy:\n      replicas: \"2\"\n", 0, 0, false},
		{"a sign", "    scale: \"+2\"\n    deploy:\n      replicas: 2\n", 0, 0, false},
		{"a leading zero", "    scale: \"02\"\n    deploy:\n      replicas: 2\n", 0, 0, false},
		{"only scale", "    scale: 3\n", 0, 0, false},
		// A deploy that has no replicas is not a replicas of nothing: only the one key is
		// compared, whatever else deploy holds (#1491 review).
		{"scale beside a deploy with no replicas", "    scale: 2\n    deploy:\n      resources:\n        limits:\n          cpus: \"1\"\n", 0, 0, false},
		{"scale beside an empty deploy mapping", "    scale: 2\n    deploy: {}\n", 0, 0, false},
		// A float that is a whole number reads as that number (docker compose refuses
		// scale 2.0 with replicas 3, measured); far past what an int64 holds it is
		// compared as the most it can be, as docker compose reads both big numbers as
		// the same.
		{"a float and the same number", "    scale: 2.0\n    deploy:\n      replicas: 2\n", 0, 0, false},
		{"a float and a different number", "    scale: 2.0\n    deploy:\n      replicas: 3\n", 2, 3, true},
		// A string is read in base 10, not as Go writes a number: `"010"` is ten (docker
		// compose reads scale 10 with replicas "010" as the same), and two numbers past
		// what a float64 tells apart are still two numbers (docker compose refuses
		// 9007199254740993 with 9007199254740992).
		{"a leading zero is not octal", "    scale: 10\n    deploy:\n      replicas: \"010\"\n", 0, 0, false},
		{"two numbers a float64 cannot tell apart", "    scale: 9007199254740993\n    deploy:\n      replicas: 9007199254740992\n", 9007199254740993, 9007199254740992, true},
		{"a float far past an int64 and a small number", "    scale: 1.0e+20\n    deploy:\n      replicas: 1\n", 9223372036854775807, 1, true},
		{"two floats far past an int64", "    scale: 1.0e+20\n    deploy:\n      replicas: 1.0e+19\n", 0, 0, false},

		{"only replicas", "    deploy:\n      replicas: 3\n", 0, 0, false},
		{"two different numbers", "    scale: 2\n    deploy:\n      replicas: 3\n", 2, 3, true},
		{"two different strings", "    scale: \"2\"\n    deploy:\n      replicas: \"3\"\n", 2, 3, true},
		{"a sign and a different number", "    scale: \"+2\"\n    deploy:\n      replicas: 3\n", 2, 3, true},
		{"a leading zero and a different number", "    scale: \"02\"\n    deploy:\n      replicas: 3\n", 2, 3, true},
		{"zero and one", "    scale: 0\n    deploy:\n      replicas: 1\n", 0, 1, true},
		{"one and zero", "    scale: 1\n    deploy:\n      replicas: 0\n", 1, 0, true},
		// Behind a profile the service is asked only once the profile is active, which the
		// loader does not know (see checkModelBounds).
		{"behind a profile", "    profiles: [x]\n    scale: 2\n    deploy:\n      replicas: 3\n", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, head+tc.tail))
			if !tc.refuse {
				if err != nil {
					t.Errorf("should load, and it was refused: %v", err)
				}
				return
			}
			want := fmt.Sprintf("services.api: "+distinct, tc.scale, tc.rep)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want a refusal saying %q, got %v", want, err)
			}
		})
	}
}

// The two counts are compared in the merged project, not file by file (#1464;
// measured, v5.5.1: a base with scale 2 and an override that adds replicas 3 is
// refused, and a base with both differing that an override makes equal is not).
func TestScaleAndReplicasAreComparedAfterTheFilesAreMerged(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const svc = "services:\n  web:\n    image: alpine\n"
	scaleOnly := write("scale.yaml", svc+"    scale: 2\n")
	bothDiffer := write("both.yaml", svc+"    scale: 2\n    deploy:\n      replicas: 3\n")
	addsThree := write("three.yaml", "services:\n  web:\n    deploy:\n      replicas: 3\n")
	makesTwo := write("two.yaml", "services:\n  web:\n    deploy:\n      replicas: 2\n")
	if _, err := LoadFiles([]string{scaleOnly, addsThree}, nil); err == nil || !strings.Contains(err.Error(), "can't set distinct values") {
		t.Errorf("an override that adds a different replicas should be refused, got %v", err)
	}
	if _, err := LoadFiles([]string{bothDiffer, makesTwo}, nil); err != nil {
		t.Errorf("an override that makes them agree should load, got %v", err)
	}
}

// A fraction is not a count to compare: the shape check refuses scale 2.5 first (as
// docker compose does), and the distinct-numbers refusal is not made over it.
func TestAFractionalScaleIsRefusedForItsShapeNotForBeingDifferent(t *testing.T) {
	body := "services:\n  web:\n    image: alpine\n    scale: 2.5\n    deploy:\n      replicas: 3\n"
	_, err := Load(writeTemp(t, body))
	if err == nil || !strings.Contains(err.Error(), "services.web.scale must be an integer or a string") {
		t.Fatalf("want the shape refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "distinct") {
		t.Errorf("the distinct-numbers refusal should not be added over a fraction, got %v", err)
	}
}

// Each key that docker compose reads as a byte size or a duration is asked whether its
// string reads as one (#1366): the parsers are held to docker compose's own answers by
// TestTheUnitCastsAgreeWithDockerCompose, and this holds the keys to the parsers — a key
// dropped from castKeys, or given the other kind, is not caught by that.
func TestEveryByteSizeAndDurationKeyIsAskedItsCast(t *testing.T) {
	for _, key := range []string{"shm_size", "mem_reservation", "memswap_limit", "mem_swappiness"} {
		t.Run(key, func(t *testing.T) {
			if castKeys[key] != "bytes" {
				t.Fatalf("%s should be a byte-size key in castKeys, is %q", key, castKeys[key])
			}
			for value, wantErr := range map[string]bool{`"64m"`: false, `"1.5 GiB"`: false, `"1_000"`: false, `"abc"`: true, `"1x"`: true, `" 1g"`: true, `"0x10"`: true} {
				_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    "+key+": "+value+"\n"))
				if wantErr && (err == nil || !strings.Contains(err.Error(), `does not read as a byte size`)) {
					t.Errorf("%s: %s should be refused as not a byte size, got %v", key, value, err)
				}
				if !wantErr && err != nil && strings.Contains(err.Error(), "does not read as") {
					t.Errorf("%s: %s reads as a byte size and was refused: %v", key, value, err)
				}
			}
		})
	}
	t.Run("stop_grace_period", func(t *testing.T) {
		if castKeys["stop_grace_period"] != "duration" {
			t.Fatalf("stop_grace_period should be a duration key in castKeys, is %q", castKeys["stop_grace_period"])
		}
		for value, wantErr := range map[string]bool{`"10s"`: false, `"1m30s"`: false, `"1d"`: false, `"0"`: false, `"10"`: true, `"abc"`: true, `"1S"`: true, `"1 s"`: true} {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    stop_grace_period: "+value+"\n"))
			if wantErr && (err == nil || !strings.Contains(err.Error(), `does not read as a duration`)) {
				t.Errorf("%s should be refused as not a duration, got %v", value, err)
			}
			if !wantErr && err != nil && strings.Contains(err.Error(), "does not read as") {
				t.Errorf("%s reads as a duration and was refused: %v", value, err)
			}
		}
	})
}

// A whole number from 2^63 to 2^64-1 is a uint64 to YAML, and docker compose takes it
// into an int64, where it is negative (#1493; measured, v5.5.1, `docker compose
// config`): `scale: 9223372036854775808` alone is refused for being less than 0 —
// as is `deploy.replicas` — while 9223372036854775807 is read, and so is
// 18446744073709551616, which is past a uint64 and so a float. With the other count
// beside it, two numbers there that differ are refused for that, and the same one
// twice for being negative.
func TestACountPastAnInt64IsWhatDockerComposeReadsAsNegative(t *testing.T) {
	const (
		svc      = "services:\n  web:\n    image: alpine\n"
		over     = "more than an int64 holds"
		distinct = "can't set distinct values"
	)
	for _, tc := range []struct {
		name, tail, want string // want: "" loads
	}{
		{"scale at the int64 limit", "    scale: 9223372036854775807\n", ""},
		{"scale one past it", "    scale: 9223372036854775808\n", "services.web.scale 9223372036854775808 is " + over},
		{"scale at the uint64 limit", "    scale: 18446744073709551615\n", "services.web.scale 18446744073709551615 is " + over},
		{"scale past a uint64 is a float", "    scale: 18446744073709551616\n", ""},
		{"replicas one past the int64 limit", "    deploy:\n      replicas: 9223372036854775808\n", "services.web.deploy.replicas 9223372036854775808 is " + over},
		{"replicas at the int64 limit", "    deploy:\n      replicas: 9223372036854775807\n", ""},
		{"scale past the limit and replicas a small number", "    scale: 9223372036854775808\n    deploy:\n      replicas: 2\n", "services.web.scale 9223372036854775808 is " + over},
		{"replicas past the limit and scale a small number", "    scale: 2\n    deploy:\n      replicas: 9223372036854775808\n", "services.web.deploy.replicas 9223372036854775808 is " + over},
		{"the same number past the limit twice", "    scale: 9223372036854775808\n    deploy:\n      replicas: 9223372036854775808\n", "services.web.scale 9223372036854775808 is " + over},
		{"two different small numbers, as a control", "    scale: 2\n    deploy:\n      replicas: 3\n", distinct},
		{"the most negative int64", "    scale: -9223372036854775808\n", "services.web.scale -9223372036854775808 is below the least, 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, svc+tc.tail))
			if tc.want == "" {
				if err != nil {
					t.Errorf("should load, and it was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a refusal saying %q, got %v", tc.want, err)
			}
		})
	}
}

// A count below zero that also differs from the other is refused for being below zero,
// where docker compose says the two are distinct: both refuse (rc 1) and the words
// differ, on purpose (the check for the least comes first, and says which key it is).
func TestANegativeCountThatAlsoDiffersIsRefusedForBeingNegative(t *testing.T) {
	for _, tc := range []struct{ name, tail, want string }{
		{"a negative scale", "    scale: -1\n    deploy:\n      replicas: 2\n", "services.web.scale -1 is below the least, 0"},
		{"a negative replicas", "    scale: 2\n    deploy:\n      replicas: -1\n", "services.web.deploy.replicas -1 is below the least, 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n"+tc.tail))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want the refusal for being below zero, %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "distinct") {
				t.Errorf("the check for the least comes first, so the distinct-values refusal is not the one made: %v", err)
			}
		})
	}
}

// An infinity or a NaN written as a number, anywhere in a file, is refused (#1507):
// docker compose reads `.inf`, `-.inf`, `+.inf` and `.nan` (in any of the cases YAML
// allows) as floats and cannot write them into the model it checks, so it refuses the
// whole file — a service key, an environment value, a label, a list item, an `x-`
// extension, the project name, a service behind a profile, and the anchor an alias
// points at alike (measured, v5.5.1, `docker compose config`, rc 1). What is a string
// is read: quoted, tagged `!!str`, expanded from a `${VAR}` (docker compose expands
// after it reads), and a number too big for a float.
func TestAnInfinityOrNaNWrittenAsANumberIsRefusedAnywhere(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		refuse     bool
		own        bool // this check's is the refusal made, no other check of opossum's says it first
	}{
		{"scale .inf", svc + "    scale: .inf\n", true, true},
		{"scale -.inf", svc + "    scale: -.inf\n", true, true},
		{"scale +.inf", svc + "    scale: +.inf\n", true, true},
		{"scale .Inf", svc + "    scale: .Inf\n", true, true},
		{"scale .INF", svc + "    scale: .INF\n", true, true},
		{"scale .nan", svc + "    scale: .nan\n", true, true},
		{"scale .NaN", svc + "    scale: .NaN\n", true, true},
		{"an environment value", svc + "    environment:\n      A: .inf\n", true, false},
		{"a label", svc + "    labels:\n      a: .inf\n", true, false},
		{"a list item", svc + "    command: [\"a\", .inf]\n", true, false},
		{"a top-level x- extension", "x-foo: .inf\n" + svc, true, true},
		{"an x- key that is one", "x-foo:\n  .inf: a\n" + svc, true, true},
		{"an x- key that is a NaN", "x-foo:\n  .nan: a\n" + svc, true, true},
		{"a list item under an x- extension", "x-foo: [1, .nan]\n" + svc, true, true},
		{"a mapping in a list under an x- extension", "x-foo:\n  - k: .NaN\n" + svc, true, true},
		{"an x- key of a service", svc + "    x-foo: .nan\n", true, true},
		{"an anchor that no alias points at", "x-a: &a .inf\n" + svc, true, true},
		{"a second document", svc + "---\nx-a: .inf\n", true, true},
		{"a third document", svc + "---\nx-a: 1\n---\nx-b: .nan\n", true, true},
		{"the project name", "name: .inf\n" + svc, true, false},
		{"a service behind a profile", svc + "    profiles: [x]\n    scale: .inf\n", true, true},
		{"the anchor an alias points at", "x-a: &a .inf\n" + svc + "    labels:\n      b: *a\n", true, false},
		{"tagged as a float", svc + "    labels:\n      a: !!float .inf\n", true, false},
		{"a value that is a quoted string", svc + "    labels:\n      a: \".inf\"\n", false, false},
		{"a value tagged as a string", svc + "    labels:\n      a: !!str .inf\n", false, false},
		{"a value a ${VAR} default makes", svc + "    labels:\n      a: ${NEKO_UNSET_INF:-.inf}\n", false, false},
		{"a number too big for a float", svc + "    labels:\n      a: 1.0e999\n", false, false},
		{"a word that only starts like one", svc + "    labels:\n      a: .infinity\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if !tc.refuse {
				if err != nil {
					t.Errorf("docker compose reads this, and it was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want the infinity or NaN refused, and it loaded")
			}
			// Where another check of opossum's already refused the value (an environment
			// value, a label, a list entry and the name must be strings and say so), it
			// says so first; the words of this check are held where it is the only one.
			if tc.own && !strings.Contains(err.Error(), "is a number docker compose cannot read") {
				t.Errorf("want this check's refusal, got %v", err)
			}
		})
	}
}

// A value tagged `!!float` is a float only if the reader can take it as one (#1509):
// docker compose refuses the others (measured, v5.5.1, `docker compose config -q`,
// 28 forms; the reader's own decode agrees with it on every one).
func TestAValueTaggedAsAFloatIsAFloat(t *testing.T) {
	for _, tc := range []struct {
		value  string
		refuse bool
	}{
		{"abc", true}, {`""`, true}, {"1.0e999", true}, {"-1.0e999", true},
		{"inf", true}, {"-inf", true}, {"nan", true}, {".infinity", true},
		{".iNf", true}, {"-.nan", true}, {"+.nan", true}, {`"abc"`, true},
		{"true", true}, {"null", true}, {"~", true}, {"1:30", true}, {".", true},
		{"1", false}, {"1.5", false}, {"-1", false}, {".5", false}, {"1e3", false},
		{"0x10", false}, {"0o7", false}, {"1_000", false}, {`"1.5"`, false},
		{"0b1", false}, {"+1", false}, {"-0", false}, {"-0.0", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\nx-a: !!float "+tc.value+"\n"))
			if tc.refuse != (err != nil) {
				t.Errorf("docker compose refuse=%v, got err=%v", tc.refuse, err)
			}
		})
	}
}

// A value tagged with another tag that names a plain value is that value or the file
// is refused (#1520; measured, v5.5.1, `docker compose config -q` over 408 tag-and-value
// forms: `Decode` of the node agrees on every one but the infinities, which the check
// above answers, and `!!int -0`, which the check below answers on its own, #1524). A `!!str`, a `!!seq` and a `!!map` read anything, and are not asked.
func TestAValueTaggedAsAPlainTypeIsThatType(t *testing.T) {
	for _, tc := range []struct {
		tag    string
		refuse []string
		read   []string
	}{
		{"!!int", []string{"abc", "1.5", "true", "yes", "null", `""`, "1e3", "99999999999999999999", ".inf", "2001-12-14", "-0", `"-0"`, `'-0'`}, []string{"1", "-3", "0x10", "0o7", "1_000", "+1", "0b1", "0", "+0", "-00", "-0x0", "-0_0", "-0b0", "-0o0"}},
		{"!!bool", []string{"yes", "on", "maybe", "1", "abc", "null", `""`, "Y"}, []string{"true", "false", "TRUE", "False"}},
		{"!!null", []string{"x", "1", "false", "yes", "abc"}, []string{"null", "~", `""`}},
		{"!!timestamp", []string{"x", "1", "abc", "2001-12-14 21:59:43.10 -5", `""`}, []string{"2001-12-14", "2001-12-14T21:59:43Z"}},
		{"!!binary", []string{"abc", "1", "yes", "x", ".inf"}, []string{"aGVsbG8=", "true", "null", "TRUE", "99999999999999999999"}},
	} {
		for _, refuse := range []bool{true, false} {
			values := tc.read
			if refuse {
				values = tc.refuse
			}
			for _, v := range values {
				t.Run(tc.tag+" "+v, func(t *testing.T) {
					_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\nx-a: "+tc.tag+" "+v+"\n"))
					if refuse && (err == nil || !strings.Contains(err.Error(), "is tagged "+tc.tag+" and is not a value")) {
						t.Errorf("docker compose refuses this, got err=%v", err)
					}
					if !refuse && err != nil {
						t.Errorf("docker compose reads this, and it was refused: %v", err)
					}
				})
			}
		}
	}
}

// A mapping key that is not a string is refused wherever it stands (#1525; measured,
// v5.5.1, `docker compose config -q`, in a top-level `x-` block, in a service's, in
// another service of an extended file, in a second document): `1: a`, `true: a`,
// `~: a`, `!!int 1: a` and an alias to one (and a list or a mapping as a key, below). A quoted key,
// a `!!str` key, `yes:` (a string in YAML 1.2), a key with a tag of its own (`!foo 1:`,
// `! true:`, `!!binary YQ==:`) and the `<<` merge key are strings.
func TestAMappingKeyThatIsNotAStringIsRefused(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	type place struct {
		name string
		body func(key string) string
	}
	places := []place{
		{"a top-level extension", func(k string) string { return svc + "x-a:\n  " + k + " v\n" }},
		{"an extension of a service", func(k string) string { return svc + "    x-a:\n      " + k + " v\n" }},
		{"a second document", func(k string) string { return svc + "---\nx-a:\n  " + k + " v\n" }},
	}
	for _, tc := range []struct {
		key    string
		refuse bool
	}{
		{"1:", true}, {"1.5:", true}, {"true:", true}, {"False:", true}, {"null:", true},
		{"~:", true}, {"!!int 1:", true}, {"!!bool true:", true}, {"1_0:", true},
		{"0x10:", true}, {"2001-12-14:", true},
		{`"1":`, false}, {"!!str 1:", false}, {"yes:", false}, {"a:", false},
		{"!foo k:", false}, {"!int 1:", false}, {"!x true:", false}, {"!x ~:", false},
		{`!foo "1":`, false}, {"!<!foo> 1:", false}, {"!<tag:example.com,2000:x> 1:", false},
		{"! 1:", false}, {"! true:", false}, {"! ~:", false}, {"!!binary YQ==:", false},
		{"!<tag:yaml.org,2002:int> 1:", true},
		{"! 1.5:", false}, {"&a ! 1:", false}, {"1.5:", true}, {"&a 1:", true},
		{"&a\t! 1:", false}, {"&a \t! 1:", false},
	} {
		for _, pl := range places {
			t.Run(pl.name+" "+strings.Fields(tc.key)[0], func(t *testing.T) {
				_, err := Load(writeTemp(t, pl.body(tc.key)))
				if tc.refuse && (err == nil || !strings.Contains(err.Error(), "is not a string")) {
					t.Errorf("docker compose refuses this, got err=%v", err)
				}
				if !tc.refuse && err != nil {
					t.Errorf("docker compose reads this, and it was refused: %v", err)
				}
			})
		}
	}
	// The bad key is not the only one, and not the first: the check goes through them all.
	t.Run("an alias to a key with a tag of its own", func(t *testing.T) {
		if _, err := Load(writeTemp(t, "x-k: &k ! 1\n"+svc+"x-b:\n  *k : a\n")); err != nil {
			t.Errorf("docker compose reads this, and it was refused: %v", err)
		}
	})
	// The column is counted in characters, not bytes: a key after a wide one on the same line.
	for _, w := range []string{"日本語", "😀", "é"} {
		t.Run("a key after "+w+" on its line", func(t *testing.T) {
			if _, err := Load(writeTemp(t, svc+"x-a: {"+w+": 1, ! 2: b}\n")); err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
			if _, err := Load(writeTemp(t, svc+"x-a: {"+w+": 1, 2: b}\n")); err == nil {
				t.Errorf("docker compose refuses this, and it was read")
			}
		})
	}
	t.Run("a key with a tag of its own, in an extended file, that is an infinity", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(svc[:10]+"  b:\n    image: alpine\n    x-a:\n      ! .inf: a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		main := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(main, []byte("services:\n  w2:\n    extends:\n      file: base.yaml\n      service: b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(main); err != nil {
			t.Errorf("docker compose reads this, and it was refused: %v", err)
		}
	})
	t.Run("a bad key after good ones", func(t *testing.T) {
		_, err := Load(writeTemp(t, svc+"x-a:\n  a: 1\n  b: 2\n  1: c\n"))
		if err == nil || !strings.Contains(err.Error(), "is not a string") {
			t.Errorf("docker compose refuses this, got err=%v", err)
		}
	})
	t.Run("a good key after a good one and before a bad one", func(t *testing.T) {
		_, err := Load(writeTemp(t, svc+"x-a:\n  a: 1\n  b: 2\n  true: c\n  d: 4\n"))
		if err == nil || !strings.Contains(err.Error(), "is not a string") {
			t.Errorf("docker compose refuses this, got err=%v", err)
		}
	})
	t.Run("a list as a key", func(t *testing.T) {
		_, err := Load(writeTemp(t, svc+"x-a:\n  ? [a]\n  : v\n"))
		if err == nil || !strings.Contains(err.Error(), "is not a string") {
			t.Errorf("docker compose refuses this, got err=%v", err)
		}
	})
	t.Run("a mapping as a key", func(t *testing.T) {
		_, err := Load(writeTemp(t, svc+"x-a:\n  ? {a: 1}\n  : v\n"))
		if err == nil || !strings.Contains(err.Error(), "is not a string") {
			t.Errorf("docker compose refuses this, got err=%v", err)
		}
	})
	t.Run("an alias that is a key", func(t *testing.T) {
		_, err := Load(writeTemp(t, "x-k: &k 1\n"+svc+"x-b:\n  *k : a\n"))
		if err == nil || !strings.Contains(err.Error(), "is not a string") {
			t.Errorf("docker compose refuses this, got err=%v", err)
		}
	})
	t.Run("an alias to a string that is a key", func(t *testing.T) {
		if _, err := Load(writeTemp(t, "x-k: &k a\n"+svc+"x-b:\n  *k : a\n")); err != nil {
			t.Errorf("docker compose reads this, and it was refused: %v", err)
		}
	})
	t.Run("the merge key", func(t *testing.T) {
		if _, err := Load(writeTemp(t, svc+"    x-a: &a\n      k: 1\n    x-b:\n      <<: *a\n      j: 2\n")); err != nil {
			t.Errorf("docker compose reads this, and it was refused: %v", err)
		}
	})
	t.Run("another service of an extended file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(svc[:10]+"  b:\n    image: alpine\n  c:\n    image: alpine\n    x-a:\n      1: a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		main := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(main, []byte("services:\n  w2:\n    extends:\n      file: base.yaml\n      service: b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(main); err == nil || !strings.Contains(err.Error(), "is not a string") {
			t.Errorf("docker compose refuses this as a key that is not a string, got %v", err)
		}
	})
}

// A port written as a float is refused (#1526): docker compose reads an entry of `ports`
// as a string or an integer, and `!!float 80` is neither though its text is a port
// (measured, v5.5.1, `config -q`). A `published:` tagged `!!float` is refused too; a
// `target:` tagged `!!float` is read, as there.
func TestAPortWrittenAsAFloatIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, ports string
		refuse      bool
		place       string // where the refusal says the float is, when the row is about that
	}{
		{"short entries", `["8080:80", "9090:90"]`, false, ""},
		{"a float short entry", "[!!float 80]", true, ""},
		{"a float short entry after others", `["80:80", 8081, !!float 8080]`, true, "ports[2]"},
		{"a float short entry, the first of two", `[!!float 8080, "80:80"]`, true, ""},
		{"an integer tag", "[!!int 80]", false, ""},
		{"a string tag", "[!!str 80]", false, ""},
		{"a bare number", "[80]", false, ""},
		{"a float that is no port", "[2.5]", true, ""},
		{"a float published", "\n      - target: 80\n        published: !!float 8080", true, ""},
		{"a float published after another entry", "\n      - 80\n      - target: 80\n        published: !!float 8080", true, "ports[1].published"},
		{"an integer published", "\n      - target: 80\n        published: 8080", false, ""},
		{"a float target", "\n      - target: !!float 80", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports: "+tc.ports+"\n"))
			if tc.refuse && err == nil {
				t.Errorf("docker compose refuses this, and it was read")
			}
			if tc.refuse && err != nil && tc.place != "" && !strings.Contains(err.Error(), tc.place) {
				t.Errorf("want the entry named as %q, got %v", tc.place, err)
			}
			if !tc.refuse && err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
}

// A name of no characters, in a map whose names are `.+` (`sysctls`, `extra_hosts`,
// `annotations`), is refused (#1530; measured, v5.5.1, `docker compose config -q`).
func TestAnEmptyNameInAMapOfNamesIsRefused(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, block string
		refuse      bool
	}{
		{"sysctls", "    sysctls:\n      \"\": 1\n", true},
		{"sysctls with a name beside it", "    sysctls:\n      net.core.somaxconn: 1\n      \"\": 1\n", true},
		{"extra_hosts", "    extra_hosts:\n      \"\": 1.2.3.4\n", true},
		{"annotations", "    annotations:\n      \"\": a\n", true},
		{"logging options", "    logging:\n      driver: json-file\n      options:\n        \"\": a\n", false},
		{"storage_opt", "    storage_opt:\n      \"\": a\n", false},
		{"a hook's environment, one level down", "    post_start:\n      - command: [\"true\"]\n        environment:\n          \"\": a\n", true},
		{"gpus options, one level down", "    gpus:\n      - driver: nvidia\n        options:\n          \"\": a\n", true},
		{"sysctls with names", "    sysctls:\n      net.core.somaxconn: 1\n", false},
		{"extra_hosts with names", "    extra_hosts:\n      h: 1.2.3.4\n", false},
		{"annotations with names", "    annotations:\n      a: b\n", false},
		{"sysctls as a list", "    sysctls: [\"net.core.somaxconn=1\"]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, svc+tc.block))
			if tc.refuse && (err == nil || !strings.Contains(err.Error(), "a name of no characters")) {
				t.Errorf("docker compose refuses this, got err=%v", err)
			}
			if !tc.refuse && err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
}

// The fields of a long-form port are the kinds docker compose reads (#1536; measured,
// v5.5.1, `config -q`): `name` and `app_protocol` are strings; `host_ip` and `protocol` are
// strings (a null is refused); `published` is a number or a string (a list and a mapping are
// refused). Each is asked in every entry, and in the file that writes it.
func TestTheFieldsOfALongFormPortAreTheKindsDockerComposeReads(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	one := func(f, v string) string {
		return svc + "    ports:\n      - target: 80\n        " + f + ": " + v + "\n"
	}
	for _, tc := range []struct {
		field, value string
		refuse       bool
	}{
		{"published", "[80]", true}, {"published", "{a: 1}", true}, {"published", "8080", false}, {"published", `"8080"`, false},
		{"host_ip", "~", true}, {"host_ip", `"127.0.0.1"`, false},
		{"protocol", "~", true}, {"protocol", "tcp", false},
		{"name", "1", true}, {"name", "true", true}, {"name", "~", true}, {"name", "!!float 1", true},
		{"name", "[a]", true}, {"name", "web", false}, {"name", `""`, false}, {"name", `"1"`, false},
		{"app_protocol", "1", true}, {"app_protocol", "true", true}, {"app_protocol", "~", true},
		{"app_protocol", "!!float 1", true}, {"app_protocol", "[a]", true}, {"app_protocol", "http", false},
		{"app_protocol", `""`, false},
	} {
		t.Run(tc.field+" "+tc.value, func(t *testing.T) {
			_, err := Load(writeTemp(t, one(tc.field, tc.value)))
			if tc.refuse && (err == nil || !strings.Contains(err.Error(), "."+tc.field+" must be")) {
				t.Errorf("docker compose refuses this, got err=%v", err)
			}
			if !tc.refuse && err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
	// Several fields in one entry: each is read from its own key, and one bad among good ones
	// is the one named (a read that takes another field's value, or stops at an entry with
	// more than two keys, passes the two-key rows above).
	for _, tc := range []struct{ name, entry, want string }{
		{"name good, app_protocol bad", "target: 80\n        name: web\n        app_protocol: 1", ".app_protocol must be"},
		{"name bad, app_protocol good", "target: 80\n        name: 1\n        app_protocol: http", ".name must be"},
		{"every field good but app_protocol", "target: 80\n        published: 8080\n        host_ip: 127.0.0.1\n        protocol: tcp\n        name: web\n        app_protocol: 1", ".app_protocol must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, svc+"    ports:\n      - "+tc.entry+"\n"))
			if err == nil || !strings.Contains(err.Error(), "ports[0]"+tc.want) {
				t.Errorf("want ports[0]%s, got %v", tc.want, err)
			}
		})
	}
	t.Run("the second entry, after a short one and a good long one", func(t *testing.T) {
		body := svc + "    ports:\n      - \"8080:80\"\n      - target: 81\n        name: ok\n      - target: 82\n        name: 2\n"
		if _, err := Load(writeTemp(t, body)); err == nil || !strings.Contains(err.Error(), "ports[2].name must be") {
			t.Errorf("want the third entry's name refused as ports[2].name, got %v", err)
		}
	})
}

// `deploy.replicas` reads as a whole number (#1467; measured, v5.5.1, `config -q`, 26 forms):
// 2, "2", 2.0, `!!float 2`, 0 and a number past int64 written bare (a float) do; "two", "",
// "1e2", "0x2", "1_0", " 2", 1.5, true, null, a list and a mapping do not. Asked in the file
// that writes it: a later `-f` that replaces the value does not help there. A negative one is
// refused by its bound (checkModelBounds), and not asked here.
func TestDeployReplicasReadsAsAWholeNumber(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		value  string
		refuse bool
	}{
		{"2", false}, {`"2"`, false}, {"2.0", false}, {"!!float 2", false}, {"0", false}, {`"0"`, false},
		{"99999999999999999999", false}, {`"+2"`, false}, {"1e2", false},
		{`"3000000000"`, false}, {`"9223372036854775807"`, false}, // past 32 bits, within int64
		{`"two"`, true}, {`""`, true}, {`"1e2"`, true}, {"0x2", false}, {`"0x2"`, true}, {`"1_0"`, true}, {`" 2"`, true},
		{"1.5", true}, {"true", true}, {"~", true}, {"[2]", true}, {"{a: 1}", true},
		{`"99999999999999999999"`, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			_, err := Load(writeTemp(t, svc+"    deploy:\n      replicas: "+tc.value+"\n"))
			if tc.refuse && (err == nil || !strings.Contains(err.Error(), "deploy.replicas must be a whole number")) {
				t.Errorf("docker compose refuses this, got err=%v", err)
			}
			if !tc.refuse && err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
	t.Run("a later file that replaces it does not help", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.yaml")
		b := filepath.Join(dir, "b.yaml")
		if err := os.WriteFile(a, []byte(svc+"    deploy:\n      replicas: two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b, []byte("services:\n  web:\n    deploy:\n      replicas: 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(a, b); err == nil {
			t.Errorf("docker compose refuses the file that writes it, and it was read")
		}
	})
	t.Run("the second service", func(t *testing.T) {
		body := svc + "  db:\n    image: alpine\n    deploy:\n      replicas: true\n"
		if _, err := Load(writeTemp(t, body)); err == nil || !strings.Contains(err.Error(), "services.db.deploy.replicas must be") {
			t.Errorf("want services.db.deploy.replicas refused, got %v", err)
		}
	})
}

// A `mode` in `ports[]` and `deploy` is a string (#1533; measured, v5.5.1, `config -q`): a
// number, a bool, a null, a list and a mapping are refused, in the file that writes them
// (docker compose asks by its schema, file by file: a later `-f` file that resets it does
// not help). The `mode` of `secrets` and `configs` is asked only after the merge there, and
// is left alone here (#1544).
func TestAModeOfAPortAndOfDeployIsAString(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	bodies := map[string]func(v string) string{
		"ports": func(v string) string { return svc + "    ports:\n      - target: 80\n        mode: " + v + "\n" },
		"ports second": func(v string) string {
			return svc + "    ports:\n      - 8080\n      - target: 80\n        mode: host\n      - target: 81\n        mode: " + v + "\n"
		},
		"ports after a short one": func(v string) string {
			return svc + "    ports:\n      - \"8080:80\"\n      - target: 81\n        mode: " + v + "\n"
		},
		"deploy": func(v string) string { return svc + "    deploy:\n      mode: " + v + "\n" },
	}
	for _, tc := range []struct {
		pos, value string
		refuse     bool
	}{
		{"ports", "1", true}, {"ports", "0440", true}, {"ports", "!!float 1", true}, {"ports", "~", true},
		{"ports", "true", true}, {"ports", "[a]", true}, {"ports", "{a: 1}", true},
		{"ports", "9223372036854775808", true}, {"ports", "18446744073709551615", true},
		{"ports", "host", false}, {"ports", "ingress", false}, {"ports", "weird", false}, {"ports", `""`, false},
		{"ports", `"1"`, false}, {"ports", "!!str 1", false},
		{"ports second", "1", true}, {"ports after a short one", "1", true}, {"ports second", "host", false},
		{"deploy", "1", true}, {"deploy", "~", true}, {"deploy", "true", true}, {"deploy", "[a]", true},
		{"deploy", "replicated", false}, {"deploy", "global", false}, {"deploy", "weird", false},
	} {
		t.Run(tc.pos+" "+tc.value, func(t *testing.T) {
			_, err := Load(writeTemp(t, bodies[tc.pos](tc.value)))
			if tc.refuse && (err == nil || !strings.Contains(err.Error(), ".mode must be a string")) {
				t.Errorf("docker compose refuses this, got err=%v", err)
			}
			if !tc.refuse && err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
	// A later file that resets the `mode` does not help a file that writes a bad one.
	t.Run("a later file resets it", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.yaml")
		b := filepath.Join(dir, "b.yaml")
		if err := os.WriteFile(a, []byte(svc+"    deploy:\n      mode: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b, []byte("services:\n  web:\n    deploy: !reset {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(a, b); err == nil {
			t.Errorf("docker compose refuses the file that writes it, and it was read")
		}
	})
	// `secrets` and `configs` are asked only after the merge: a bad one that a later file
	// replaces is left alone, as docker compose leaves it.
	t.Run("a secrets mode a later file replaces", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.yaml")
		b := filepath.Join(dir, "b.yaml")
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(a, []byte(svc+"    secrets:\n      - source: s\n        mode: 1.5\nsecrets:\n  s:\n    file: ./f\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b, []byte("services:\n  web:\n    secrets:\n      - source: s\n        mode: 0400\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(a, b); err != nil {
			t.Errorf("docker compose reads this (the mode is replaced before it is asked), and it was refused: %v", err)
		}
	})
}

// The `<<` merge key takes a mapping, or a list of mappings (an empty list too); a
// scalar, a null, an empty value, or a list with an item that is not a mapping is refused
// wherever the key stands (#1529; measured, v5.5.1, `docker compose config -q`). A quoted
// `"<<"` and a `!!str <<` are plain string keys.
func TestAMergeKeyThatHoldsNoMappingIsRefused(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	const anchors = "x-b: &b {k: 1}\nx-c: &c {m: 2}\nx-s: &s v\nx-z: &z ~\n"
	for _, tc := range []struct {
		name, block string
		refuse      bool
	}{
		{"a scalar", "x-a:\n  <<: v\n", true},
		{"an integer", "x-a:\n  <<: 1\n", true},
		{"a null", "x-a:\n  <<: ~\n", true},
		{"nothing", "x-a:\n  <<:\n", true},
		{"a bool", "x-a:\n  <<: true\n", true},
		{"an alias to a scalar", "x-a:\n  <<: *s\n", true},
		{"an alias to a null", "x-a:\n  <<: *z\n", true},
		{"a list with a scalar", "x-a:\n  <<: [*b, v]\n", true},
		{"a list of scalars", "x-a:\n  <<: [a, b]\n", true},
		{"a list of a null", "x-a:\n  <<: [~]\n", true},
		{"a list in a list", "x-a:\n  <<: [[*b]]\n", true},
		{"a bad value after good keys", "x-a:\n  p: 1\n  q: 2\n  <<: v\n", true},
		{"a mapping", "x-a:\n  <<: {k: 1}\n  j: 2\n", false},
		{"an alias to a mapping", "x-a:\n  <<: *b\n  j: 2\n", false},
		{"a list of mappings", "x-a:\n  <<: [*b, *c]\n", false},
		{"an empty list", "x-a:\n  <<: []\n", false},
		{"an alias to a `<<` used as a key", "x-k: &k <<\nx-a:\n  *k : v\n", false},
		{"a quoted key", "x-a:\n  \"<<\": v\n", false},
		{"a !!str key", "x-a:\n  !!str <<: v\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, anchors+svc+tc.block))
			if tc.refuse && (err == nil || !strings.Contains(err.Error(), "the merge key `<<`")) {
				t.Errorf("docker compose refuses this, got err=%v", err)
			}
			if !tc.refuse && err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
}

// A file that is only extended from (`extends: {file: …, service: b}`) gives docker
// compose the one service and nothing else of it: an infinity, which is a fault of the
// model it builds, is refused in `b` (and in what an alias in `b` points at) and not
// anywhere else in that file; a `!!float` that is no float, a fault of the read, is
// refused wherever it stands (#1510; measured, v5.5.1, `docker compose config -q`).
func TestAnInfinityInAnExtendedFileIsRefusedInTheServiceItTakes(t *testing.T) {
	const svc = "services:\n  b:\n    image: alpine\n"
	for _, tc := range []struct {
		name, base string
		refuse     bool
	}{
		{"the top level of the file", "x-q: .inf\n" + svc, false},
		{"a volume label of the file", "volumes:\n  v:\n    labels:\n      a: .nan\n" + svc, false},
		{"another service", svc + "  c:\n    image: alpine\n    x-a: .inf\n", false},
		{"the service it takes", svc + "    x-a: .inf\n", true},
		{"a key that is an infinity, in the service it takes", svc + "    x-a:\n      .inf: a\n", true},
		{"a key that is an infinity, in another service", svc + "  c:\n    image: alpine\n    x-a:\n      .inf: a\n", true},
		{"a key that is a NaN, at the top level", "x-q:\n  .nan: a\n" + svc, true},
		{"the service it takes, a negative infinity", svc + "    x-a: -.inf\n", true},
		{"the service it takes, a NaN in a list", svc + "    x-a: [1, .nan]\n", true},
		{"an anchor outside the service that the service points at", "x-q: &q .inf\n" + svc + "    x-a: *q\n", true},
		{"a service of the file that the service it takes extends", svc + "    extends: c\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"a service of the file that it extends with no file named", svc + "    extends:\n      service: c\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"a service two extends away", svc + "    extends: c\n  c:\n    extends: d\n  d:\n    image: alpine\n    x-a: .nan\n", true},
		{"a service of the file that nothing it takes extends", svc + "    extends: c\n  c:\n    image: alpine\n  d:\n    image: alpine\n    x-a: .nan\n", false},
		{"services written as an alias", "x-s: &s\n  b:\n    image: alpine\n    x-a: .inf\nservices: *s\n", true},
		{"a chain written through a merge key", "x-t: &t\n  extends: c\n" + "services:\n  b:\n    <<: *t\n    image: alpine\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"an extends written through a merge key", "x-e: &e\n  service: c\n" + svc + "    extends:\n      <<: *e\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"an extends written with an expansion", svc + "    extends: ${S:-c}\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"an extends map written with an expansion", svc + "    extends:\n      service: ${S:-c}\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"an extends written as an alias", "x-e: &e c\n" + svc + "    extends: *e\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"an extends map written as an alias", "x-e: &e\n  service: c\n" + svc + "    extends: *e\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"the service it takes written as an alias", "x-bb: &bb\n  image: alpine\n  extends: c\nservices:\n  b: *bb\n  c:\n    image: alpine\n    x-a: .inf\n", true},
		{"a key an anchor gives and the service writes again", "x-base: &base\n  x-a: .inf\n" + "services:\n  b:\n    <<: *base\n    image: alpine\n    x-a: 1\n", false},
		{"a key of the extends map that is one", svc + "    extends:\n      x-z: .inf\n      service: c\n  c:\n    image: alpine\n", false},
		{"the service it takes, an integer tag over an expansion", svc + "    environment:\n      A: !!int ${NEKO_UNSET_TAG}\n", true},
		{"another service, an integer tag over an expansion", svc + "  c:\n    image: alpine\n    environment:\n      A: !!int ${NEKO_UNSET_TAG}\n", true},
		{"the top level, tagged a float that is none", "x-a: !!float abc\n" + svc, true},
		{"another service, tagged a float that is none", svc + "  c:\n    image: alpine\n    x-a: !!float abc\n", true},
		{"the service it takes, tagged a float that is none", svc + "    x-a: !!float abc\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) string {
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			}
			write("base.yaml", tc.base)
			main := write("compose.yaml", "services:\n  w2:\n    extends:\n      file: base.yaml\n      service: b\n")
			_, err := Load(main)
			if tc.refuse != (err != nil) {
				t.Errorf("docker compose refuse=%v, got err=%v", tc.refuse, err)
			}
		})
	}
}

// The value an extended service holds is named by where it is, the same on every run.
func TestAnInfinityInAServiceIsNamedByItsPlace(t *testing.T) {
	svc := map[string]any{"x-c": []any{1, math.Inf(1)}, "x-a": map[string]any{"k": math.NaN()}, "x-b": math.Inf(-1)}
	for i := 0; i < 20; i++ {
		if got := nonFiniteInService(svc, "services.b"); got != "services.b.x-a.k" {
			t.Fatalf("want the first place by name, got %q", got)
		}
	}
	if got := nonFiniteInService(map[string]any{"x-c": []any{1, math.Inf(1)}}, "s"); got != "s.x-c[1]" {
		t.Errorf("want the list place named, got %q", got)
	}
	if got := nonFiniteInService(map[string]any{"x-c": []any{1, 2.5}, "n": "inf"}, "s"); got != "" {
		t.Errorf("want none, got %q", got)
	}
}

// An alias that refers to the block that holds it is refused elsewhere, and a take-down
// goes on past that: asking the service an extends takes must not follow the alias round
// and round (#1510).
func TestAnAliasToItsOwnBlockInAnExtendedServiceEndsTheAsk(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	if err := os.WriteFile(base, []byte("services:\n  b: &b\n    image: alpine\n    x-a: *b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(main, []byte("services:\n  w2:\n    extends:\n      file: base.yaml\n      service: b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = LoadFilesEnvDirSoft([]string{main}, nil, "")
}

// Two services of an extended file that extend each other are refused elsewhere and a
// take-down goes on past that: asking down the chain of extends must stop where it
// began (#1510).
func TestAnExtendsCycleInAnExtendedFileEndsTheAsk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("services:\n  b:\n    extends: c\n  c:\n    extends: b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(main, []byte("services:\n  w2:\n    extends:\n      file: base.yaml\n      service: b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = LoadFilesEnvDirSoft([]string{main}, nil, "")
}

// Each file is asked as it is written: an override that writes one is refused whether
// or not the file it is merged onto has the same key (docker compose validates each
// file before it merges: `validating base.yaml: json: unsupported value`).
func TestAnInfinityInAnyFileIsRefusedBeforeTheyAreMerged(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const svc = "services:\n  web:\n    image: alpine\n"
	infBase := write("inf-base.yaml", svc+"    scale: .inf\n")
	twoBase := write("two-base.yaml", svc+"    scale: 2\n")
	setTwo := write("set-two.yaml", "services:\n  web:\n    scale: 2\n")
	setInf := write("set-inf.yaml", "services:\n  web:\n    scale: .inf\n")
	for name, files := range map[string][]string{
		"a base that has one, set back by an override": {infBase, setTwo},
		"an override that has one":                     {twoBase, setInf},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFiles(files, nil); err == nil || !strings.Contains(err.Error(), "is a number docker compose cannot read") {
				t.Errorf("want the infinity refused in whichever file has it, got %v", err)
			}
		})
	}
}

// Where the text at a node's line and column starts with a `!`: the non-specific tag
// the reader does not keep. The column is in characters, a line ends in `\n`, `\r\n` or
// a lone `\r`, a byte order mark is not counted, and an anchor comes first (#1525).
func TestStartsWithBang(t *testing.T) {
	for _, tc := range []struct {
		name         string
		text         string
		line, column int
		want         bool
	}{
		{"the first column", "! 1: a\n", 1, 1, true},
		{"not a bang", "1: a\n", 1, 1, false},
		{"a later line", "a: 1\nb:\n  ! 2: c\n", 3, 3, true},
		{"after a wide character", "{日本語: 1, ! 2: b}\n", 1, 10, true},
		{"after an emoji", "{😀: 1, ! 2: b}\n", 1, 8, true},
		{"one column off, after a wide character", "{日本語: 1, ! 2: b}\n", 1, 9, false},
		{"after CRLF lines", "a: 1\r\nb:\r\n  ! 2: c\r\n", 3, 3, true},
		{"after lone CR lines", "a: 1\rb:\r  ! 2: c\r", 3, 3, true},
		{"after a byte order mark", "\xef\xbb\xbf{! 1: v}\n", 1, 2, true},
		{"after an anchor", "&a ! 1: v\n", 1, 1, true},
		{"an anchor and no bang", "&a 1: v\n", 1, 1, false},
		{"past the last line", "a: 1\n", 5, 1, false},
		{"past the end of the line", "a\n", 1, 9, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := startsWithBang([]byte(tc.text), tc.line, tc.column); got != tc.want {
				t.Errorf("startsWithBang(%q, %d, %d) = %v, want %v", tc.text, tc.line, tc.column, got, tc.want)
			}
		})
	}
}
