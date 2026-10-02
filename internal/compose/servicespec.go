package compose

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// servicespec.json is the shape of a service as docker compose v5.5.1 checks it,
// cut out of the compose-spec schema its plugin embeds (testdata/tools/gen-service-spec.py
// says how). The schema is the Compose Specification's (https://github.com/compose-spec/compose-spec,
// Apache License 2.0); what is kept of it is its structure — types, items, oneOf,
// properties, patternProperties, enum, pattern and bounds — without its prose. docker compose refuses a service key whose value is not of the shape
// the schema gives it (`services.web.hostname must be a string`); opossum read the
// keys it acts on through its decoder, which refused what it could not read, and
// took every other key as it came, whatever it held.
//
//go:embed servicespec.json
var serviceSpecJSON []byte

// specNode is one node of the schema: what a value may be.
type specNode struct {
	Type              json.RawMessage      `json:"type"`
	Items             *specNode            `json:"items"`
	OneOf             []*specNode          `json:"oneOf"`
	Properties        map[string]*specNode `json:"properties"`
	PatternProperties map[string]*specNode `json:"patternProperties"`
	Additional        *specNode            `json:"additional"`
	Enum              []any                `json:"enum"`
	Pattern           string               `json:"pattern"`
	Minimum           *float64             `json:"minimum"`
	Maximum           *float64             `json:"maximum"`
	UniqueItems       bool                 `json:"uniqueItems"`

	kinds []string
	re    *regexp.Regexp
	pats  []patternNode
}

type patternNode struct {
	re   *regexp.Regexp
	node *specNode
}

var (
	serviceSpecOnce sync.Once
	serviceSpec     *specNode
	serviceSpecErr  error
)

func loadServiceSpec() (*specNode, error) {
	serviceSpecOnce.Do(func() {
		var n specNode
		if err := json.Unmarshal(serviceSpecJSON, &n); err != nil {
			serviceSpecErr = fmt.Errorf("the service schema does not read: %w", err)
			return
		}
		serviceSpecErr = n.prepare()
		serviceSpec = &n
	})
	return serviceSpec, serviceSpecErr
}

func (n *specNode) prepare() error {
	if len(n.Type) > 0 {
		var one string
		if err := json.Unmarshal(n.Type, &one); err == nil {
			n.kinds = []string{one}
		} else if err := json.Unmarshal(n.Type, &n.kinds); err != nil {
			return err
		}
	}
	if n.Pattern != "" {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return err
		}
		n.re = re
	}
	children := []*specNode{n.Items, n.Additional}
	children = append(children, n.OneOf...)
	for _, c := range n.Properties {
		children = append(children, c)
	}
	names := make([]string, 0, len(n.PatternProperties))
	for p := range n.PatternProperties {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		re, err := regexp.Compile(p)
		if err != nil {
			return err
		}
		n.pats = append(n.pats, patternNode{re, n.PatternProperties[p]})
		children = append(children, n.PatternProperties[p])
	}
	for _, c := range children {
		if c == nil {
			continue
		}
		if err := c.prepare(); err != nil {
			return err
		}
	}
	return nil
}

// heldToTheSchema are the service keys whose value is checked against the schema:
// the ones opossum took as they came, whatever they held (58, found by writing every
// key of the schema in 13 forms and asking each of docker compose and opossum). The
// others are read into a typed shape by the decode, or by a check of their own
// that says what is wrong in its own words, and are left to those.
var heldToTheSchema = map[string]bool{}

func init() {
	for _, k := range []string{
		"annotations", "attach", "blkio_config", "cgroup", "cgroup_parent",
		"container_name", "cpu_count", "cpu_percent", "cpu_period", "cpu_quota",
		"cpu_rt_period", "cpu_rt_runtime", "cpu_shares", "cpuset", "credential_spec",
		"device_cgroup_rules", "devices", "dns", "dns_opt", "dns_search",
		"domainname", "expose", "external_links", "extra_hosts", "gpus",
		"hostname", "ipc", "isolation", "label_file", "links",
		"logging", "mem_reservation", "mem_swappiness", "memswap_limit", "models",
		"oom_kill_disable", "oom_score_adj", "pid", "pids_limit", "post_start",
		"pre_start", "pre_stop", "privileged", "provider", "pull_policy",
		"pull_refresh_after", "runtime", "scale", "security_opt", "shm_size",
		"stdin_open", "stop_grace_period", "stop_signal", "storage_opt", "sysctls",
		"use_api_socket", "userns_mode", "uts",
	} {
		heldToTheSchema[k] = true
	}
}

// strictBooleanKeys are the service keys whose schema says a boolean and which
// docker compose does not read a string into, so that a string is refused as any
// other value of the wrong kind is; for the others the schema says `boolean or
// string` (the string is read into the boolean, or refused by that reading).
var strictBooleanKeys = map[string]bool{"use_api_socket": true}

// castKeys is the kind of value docker compose casts a string into, for the
// keys of heldToTheSchema whose schema gives them a string branch alongside a
// boolean or number/integer one (#1366's cast layer, stage 2: the shape
// check above takes the string as the right kind on the strength of that
// branch; this asks whether the string is one docker compose can actually
// read). Measured against docker compose v5.5.1, quoted so YAML hands the
// value through as a string rather than reading it as the native kind
// itself (a bare `cpu_shares: 7.0` never reaches this check — it is already
// a number).
//
// `cpus`, `init`, `read_only` and `tty` are deliberately not here despite
// being on #1366's original list of 24: none of the four is in
// heldToTheSchema — opossum already reads and validates each through its own
// decoder (`cpus` names its own error, "cpus: not a number of CPUs"; `init`,
// `read_only` and `tty` go through the existing `readQuotedBools`/`boolWord`
// in types.go, measured to accept the identical YAML-1.1-word set as
// yamlBools below), so this check would never run for any of them, and at
// least `cpus`' own decoder does not agree with docker compose's cast in
// every case measured here (it takes "inf", "nan" and a leading space,
// docker compose's cast does not). All four are a gap of their own, for
// keys opossum reads, not for the ignored-key layer this map is about.
//
// `cpu_percent` is not in castKeys, despite being on the same list and in
// heldToTheSchema: measured separately from the other nine int-cast keys, and
// it does not share their cast. `cpu_shares: "7.0"` is refused (plain
// strconv.ParseInt); `cpu_percent: "7.0"` is ACCEPTED — docker compose casts
// it through a float first (also taking "7e0", "1e1", "7.", and Go's
// digit-separator syntax: "5_0", "1_0" and "1_00" all read as their obvious
// values). `strconv.ParseFloat` agrees with all of that. What refuses it
// afterwards is not the cast: it is the schema's own bound (0–100) and its
// requirement that the number be a whole one, the same two `kindOK` already
// applies to a *native* `cpu_percent: 7.5` or `cpu_percent: 150` (measured:
// both refused there today) — a string only reaches as far as castOK's
// siblings above, never that check. cpuPercentMismatch below is that check,
// read off the schema's own oneOf branch rather than the 0/100 hardcoded, so
// it stays in step with whatever the embedded schema says.
//
// Byte-size keys (mem_reservation, mem_swappiness, memswap_limit, shm_size) and
// duration keys (stop_grace_period) are in the map with kinds of their own, "bytes"
// and "duration" (unitcast.go): docker compose's cast for those is not plain Go
// syntax (a duration reads `1d` as a day, which time.ParseDuration does not;
// mem_swappiness is a percentage by name and reads through the same byte-suffix
// cast as mem_limit), so the two were measured on their own, against a sweep
// of 3348 strings (testdata/unit-forms.json; `shm_size` for the sizes, and `stop_grace_period`), and the
// other three size keys on a sample of 231 of them. `mem_limit`, like `cpus`, is a key opossum
// reads itself.
var castKeys = map[string]string{
	"attach": "bool", "oom_kill_disable": "bool", "privileged": "bool", "stdin_open": "bool",
	"cpu_count": "int", "cpu_shares": "int", "cpu_quota": "int",
	"cpu_period": "int", "cpu_rt_period": "int", "cpu_rt_runtime": "int",
	"oom_score_adj": "int", "pids_limit": "int", "scale": "int",
	// Byte sizes and a duration (#1366): docker compose reads a number with a unit
	// after it, and refuses what is not one; measured against 3348 strings
	// (testdata/unit-forms.json). `mem_swappiness` is a percentage by its name and
	// reads as a size like the others.
	"shm_size": "bytes", "mem_reservation": "bytes", "memswap_limit": "bytes", "mem_swappiness": "bytes",
	"stop_grace_period": "duration",
}

// castKindNames is what castMismatch's refusal calls each kind, matching
// describe()'s wording for the shape check above so the two read as one
// family of message rather than two different voices for the same key.
var castKindNames = map[string]string{"bool": "a boolean", "int": "an integer", "bytes": "a byte size", "duration": "a duration"}

// yamlBools are the spellings YAML 1.1 (which docker compose's cast reads
// through, case-insensitively) takes as true or false — measured: `y`, `n`,
// `on`, `off`, and the full words all work in any case; `t`, `f`, `1`, `0` do
// not (docker compose's own message on those: `invalid boolean: <value>`).
var yamlBools = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true,
	"y": true, "n": true, "on": true, "off": true,
}

// castOK reports whether s is a string docker compose's cast reads into kind,
// for the keys castKeys names. int is plain strconv.ParseInt(s, 10, 64) —
// measured: cpu_shares takes no hex (`0x10`), no digit separators
// (`1_000`), no surrounding space, and no fractional spelling of a whole
// number (`7.0`), none of which base-10 ParseInt takes either.
func castOK(kind, s string) bool {
	switch kind {
	case "bool":
		return yamlBools[strings.ToLower(s)]
	case "int":
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	case "bytes":
		return bytesOK(s)
	case "duration":
		return durationOK(s)
	}
	return true
}

// cpuPercentBounds reads the minimum and maximum cpu_percent's own oneOf
// branch gives an integer, off the embedded schema rather than a hardcoded
// 0/100 — cpuPercentMismatch's answer then follows whatever the schema says,
// not a copy of it made once and left to drift.
func cpuPercentBounds(node *specNode) (min, max *float64) {
	for _, o := range node.OneOf {
		if o.Minimum != nil || o.Maximum != nil {
			min, max = o.Minimum, o.Maximum
		}
	}
	return min, max
}

// cpuPercentMismatch says how a string cpu_percent is not what docker
// compose's cast and schema together take for it, or "" when it is (#1366,
// stage 3 — see the comment on castKeys above for what was measured). Unlike
// castOK's siblings, a whole number in bounds is not enough on its own to
// call this "" for every input: "abc" fails to parse at all, and that is a
// different question from "150" parsing fine and then failing the bound.
func cpuPercentMismatch(node *specNode, s, where string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Sprintf("%s %q does not read as a number", where, s)
	}
	if f != math.Trunc(f) {
		return fmt.Sprintf("%s %v is not a whole number", where, s)
	}
	min, max := cpuPercentBounds(node)
	if min != nil && f < *min {
		return fmt.Sprintf("%s %v is below the least, %v", where, s, *min)
	}
	if max != nil && f > *max {
		return fmt.Sprintf("%s %v is above the most, %v", where, s, *max)
	}
	return ""
}

// intBoundMismatch says how a string an int-cast key reads into is outside the
// bounds the schema's integer branch gives it, or "" when it is inside (or the
// key has none) (#1451). castOK has already confirmed s reads as a base-10
// integer, which is all this needs; the bounds are read off the schema like
// cpuPercentMismatch's, so `oom_score_adj: "2000"` (−1000 to 1000) and
// `cpu_count: "-1"` (at least 0) are refused as docker compose refuses them,
// and a key with no bound (`cpu_shares`, `pids_limit`) is untouched.
func intBoundMismatch(node *specNode, s, where string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return ""
	}
	f := float64(n)
	min, max := cpuPercentBounds(node)
	if min != nil && f < *min {
		return fmt.Sprintf("%s %v is below the least, %v", where, s, *min)
	}
	if max != nil && f > *max {
		return fmt.Sprintf("%s %v is above the most, %v", where, s, *max)
	}
	return ""
}

// negativeModelValue reports the text of v when it is a negative number, native
// or a string that reads as one, and "" otherwise (a value of another kind is
// the business of the shape check, and a number that is not negative is fine).
// It is for the keys docker compose refuses below zero not through the schema
// (which gives `scale` and `deploy.replicas` no bound) but through its own model
// check, `must be greater than or equal to 0` (#1462). A whole number written
// with a fraction part (`-1.0`, `-1e0`) is a float once read and is refused the
// same way; a fractional `scale` (`-1.5`) is refused by the shape check before
// it, and a fractional `deploy.replicas`, which has none, is refused here.
func negativeModelValue(v any) string {
	switch x := v.(type) {
	case int:
		if x < 0 {
			return strconv.Itoa(x)
		}
	case uint64:
		// YAML reads a whole number from 2^63 up to 2^64-1 as a uint64, and docker
		// compose takes it into an int64, where it is negative (measured, v5.5.1:
		// `scale: 9223372036854775808` is refused for being less than 0, and
		// 9223372036854775807 is not) (#1493).
		if x > math.MaxInt64 {
			return strconv.FormatUint(x, 10)
		}
	case float64:
		if x < 0 {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil && n < 0 {
			return x
		}
	}
	return ""
}

// belowTheLeast is what a count that negativeModelValue found is said to be: a
// negative number is below the least, and one past what an int64 holds is what
// docker compose takes for a negative one.
func belowTheLeast(text string) string {
	if n, err := strconv.ParseUint(text, 10, 64); err == nil && n > math.MaxInt64 {
		return "is more than an int64 holds, which docker compose reads as less than 0"
	}
	return "is below the least, 0"
}

// checkModelBounds refuses a `scale` or a `deploy.replicas` below zero, and a service
// that sets both to two different numbers, in the document the project is loaded from —
// the merge of every file, with `extends` read — because that is what docker compose
// checks: its `must be greater than or equal to 0` is a check of the model it built, not
// of each file (measured, v5.5.1: a base file's `scale: -1` set to 2 by an override is
// fine; the same asked of the base alone would be refused).
//
// The least is asked first, so `scale: -1` with `replicas: 2`, and `scale: 2` with
// `replicas: -1`, are refused for being below zero (docker compose says the two are
// distinct; both refuse).
//
// A service behind `profiles:` is left alone: docker compose asks it only once a profile
// that names it is active, which the loader does not know, so it takes one docker compose
// would refuse then rather than refuse one docker compose would take (#1462).
func checkModelBounds(name string, services map[string]any) error {
	names := make([]string, 0, len(services))
	for n := range services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc, ok := services[n].(map[string]any)
		if !ok {
			continue
		}
		if profiles, ok := svc["profiles"].([]any); ok && len(profiles) > 0 {
			continue
		}
		if text := negativeModelValue(svc["scale"]); text != "" {
			return fmt.Errorf("%s: services.%s.scale %s %s", name, n, text, belowTheLeast(text))
		}
		if deploy, ok := svc["deploy"].(map[string]any); ok {
			if text := negativeModelValue(deploy["replicas"]); text != "" {
				return fmt.Errorf("%s: services.%s.deploy.replicas %s %s", name, n, text, belowTheLeast(text))
			}
			// docker compose reads both as numbers (`"2"`, `+2` and `02` are 2) and
			// refuses a service that gives them two different ones (#1464).
			if scale, ok := wholeNumber(svc["scale"]); ok {
				if replicas, ok := wholeNumber(deploy["replicas"]); ok && scale != replicas {
					return fmt.Errorf("%s: services.%s: can't set distinct values on `scale` (%d) and `deploy.replicas` (%d)", name, n, scale, replicas)
				}
			}
		}
	}
	return nil
}

// wholeNumber is the integer v reads as (a uint64 is not one it is asked of: a
// count past an int64 is refused by negativeModelValue first) — a number, or a string that reads as one —
// and whether it is one. Anything else (a null, a list, a word) is the shape
// check's business, not a count to compare.
func wholeNumber(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case float64:
		switch {
		case x != math.Trunc(x): // a fraction, or NaN
			return 0, false
		case x >= math.MaxInt64: // beyond what an int64 holds: saturated, not left to the conversion
			return math.MaxInt64, true
		case x <= math.MinInt64:
			return math.MinInt64, true
		}
		return int64(x), true
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// takenServices is the services of a file that is only extended from (`only` is the one
// `extends: {file: …, service: only}` names) that docker compose takes: that service, and the
// services of the same file it extends in turn (`extends: c`, or a map with a service and no
// file), as far as the chain runs. Nil when the file is read whole.
func takenServices(services map[string]any, only string) map[string]bool {
	if only == "" {
		return nil
	}
	taken := map[string]bool{}
	for name := only; name != "" && !taken[name]; {
		taken[name] = true
		svc, _ := services[name].(map[string]any)
		name = ""
		switch e := svc["extends"].(type) {
		case string:
			name = e
		case map[string]any:
			if _, hasFile := e["file"]; !hasFile {
				name, _ = e["service"].(string)
			}
		}
	}
	return taken
}

// checkServiceShapes refuses a service key whose value is not what docker compose
// takes for it. Only the keys the schema gives a shape are looked at: a key it
// does not know is the business of the check that says which keys a service may
// have, and a newer docker compose may take one this schema does not.
//
// A value of the wrong kind is refused. A string where the schema says a number or
// a boolean is taken: the schema of the keys checked here gives such a key a string
// branch as well (`number or string`, `boolean or string`), for the value that
// docker compose reads into the number or boolean (`cpu_shares: "7"`). Whether the
// string reads (`cpu_shares: "abc"`) is castKeys/castOK's business, made right below
// — for the keys castKeys names. The bounds of the number a string reads into
// (`cpu_percent: "150"`) is a check of its own, made further down by
// cpuPercentMismatch: the `Minimum`/`Maximum` check on this node only ever
// sees a native number, never a string that castOK's sibling has confirmed
// reads as one.
func checkServiceShapes(path string, services map[string]any, values *[]error, only string) error {
	spec, err := loadServiceSpec()
	if err != nil {
		return err
	}
	// A value docker compose reads as another kind (a string that is not a number,
	// one outside the key's bounds) is refused here, or — where the caller gave a
	// sink, for the commands that take a project down (#1468) — kept there and the
	// read goes on: an earlier opossum passed such a value on.
	fault := func(err error) error {
		if values == nil {
			return err
		}
		*values = append(*values, err)
		return nil
	}
	taken := takenServices(services, only)
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		// In a file that is only extended from, a service docker compose does not take is
		// asked for less: its `deploy.replicas` is refused only when it is a string that does
		// not read as a whole number (#1553; measured, v5.5.1: `"two"` and `""` are, `1.5`,
		// `true`, `~`, a list and a mapping are not, there).
		notTaken := taken != nil && !taken[name]
		svc, ok := services[name].(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(svc))
		for k := range svc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if notTaken && readAsItIsWhereNotTaken[k] {
				continue
			}
			// A port written as a float (`!!float 80`, or `published: !!float 8080`) is
			// refused by docker compose, which reads an entry as a string or an integer
			// (#1526); its text is a port, so the loader's own reading let it through.
			if k == "ports" {
				if err := portFloats(path, "services."+name+".ports", svc[k], notTaken, fault); err != nil {
					return err
				}
			}
			if k == "ports" {
				if err := portFieldKinds(path, "services."+name+".ports", svc[k], notTaken, fault); err != nil {
					return err
				}
			}
			// A short mount with an empty first section (`:/b`, `:ro`) or an empty last one
			// (`/a:/b:`), which docker compose refuses (`empty section between colons`) in the file
			// that writes it — a later file that writes over it does not make it fine — in a
			// service taken and in one that is not (#1637; measured, v5.5.1). An earlier opossum
			// started such a project (the runtime takes the empty source as an anonymous volume),
			// so it is asked here, where taking a project down warns and goes on, and not in the
			// decode, which it does not.
			if k == "volumes" {
				if err := emptyMountSections(path, "services."+name+".volumes", svc[k], fault); err != nil {
					return err
				}
			}
			// A `mode` that is not the string docker compose reads in `ports[].mode` and
			// `deploy.mode` (#1533; measured, v5.5.1).
			if err := modeKinds(path, "services."+name+"."+k, k, svc[k], notTaken, fault); err != nil {
				return err
			}
			// `cpus` is a key opossum reads itself, which a service docker compose does not take
			// is not asked: docker compose still casts it there, with strconv.ParseFloat as it is,
			// and refuses a string that does not read (`cpus: abc`, `"0x2"`, `" 2"`, `"_1"`, `"1__0"`;
			// `"1_0"`, `"1e2"`, `"inf"` read) (#1566; measured, v5.5.1). A service taken is read by
			// the decode.
			if k == "cpus" && notTaken {
				if str, isString := svc[k].(string); isString {
					if _, err := strconv.ParseFloat(str, 64); err != nil {
						if err := fault(fmt.Errorf("compose file %s: services.%s.cpus %q does not read as a number", path, name, str)); err != nil {
							return err
						}
					}
				}
			}
			// A key of these blocks written with nothing after it, where the schema gives it no null
			// (`deploy: {labels: ~}`, `build: {network: ~}`, `healthcheck: {disable: ~}`, a network's
			// `aliases: ~`): docker compose refuses it in the file that writes it. A file that is only
			// extended from is not asked: docker compose merges the extending service over it before it
			// asks, so a null the extender writes a value over is read (#1583, #1592, #1598; measured,
			// v5.5.1).
			if nullChecked[k] && only == "" {
				if msg := spec.Properties[k].nothingWhereNoneIsTaken(svc[k], "services."+name+"."+k, 0, false); msg != "" {
					if err := fault(fmt.Errorf("compose file %s: %s", path, msg)); err != nil {
						return err
					}
				}
			}
			node := spec.Properties[k]
			if node == nil || !heldToTheSchema[k] {
				continue
			}
			where := "services." + name + "." + k
			// A boolean that docker compose does not read a string into (measured,
			// v5.5.1: `use_api_socket: "true"` is refused, `stdin_open: "true"` is not).
			if _, isString := svc[k].(string); isString && strictBooleanKeys[k] {
				if err := fault(fmt.Errorf("compose file %s: %s must be a boolean", path, where)); err != nil {
					return err
				}
			}
			// A name of no characters, in a service docker compose does not take of a file that
			// is only extended from, is not asked for (#1559; measured, v5.5.1).
			shaped := svc[k]
			if notTaken {
				shaped = withoutEmptyName(shaped)
			}
			if msg := node.mismatch(shaped, where); msg != "" {
				if err := fault(fmt.Errorf("compose file %s: %s", path, msg)); err != nil {
					return err
				}
			}
			// The shape above took the string on the strength of its kind's
			// string branch; this asks whether docker compose can actually
			// read it into the kind that branch exists for (#1366).
			// In a service docker compose does not take, a string that reads as no integer or
			// boolean for the keys that cast one, and a `cpu_percent` that reads as no number, is
			// refused there as everywhere (`scale: two`, `privileged: "7"`, `cpu_percent: abc`), and
			// the bounds of the number a string reads into are not (`oom_score_adj: "2000"`,
			// `cpu_count: "-5"`, `cpu_percent: "150"`). The byte sizes, the duration and `attach` are
			// among the keys it asks nothing of there (readAsItIsWhereNotTaken, above), so none of
			// them reaches this. `cpus` is cast just above, for a service not taken only (#1559,
			// #1571; measured, v5.5.1).
			if kind, ok := castKeys[k]; ok {
				if s, isString := svc[k].(string); isString && !castOK(kind, s) {
					if err := fault(fmt.Errorf("compose file %s: %s %q does not read as %s",
						path, where, s, castKindNames[kind])); err != nil {
						return err
					}
				}
				if s, isString := svc[k].(string); isString && kind == "int" && !notTaken {
					if msg := intBoundMismatch(node, s, where); msg != "" {
						if err := fault(fmt.Errorf("compose file %s: %s", path, msg)); err != nil {
							return err
						}
					}
				}
			}
			// cpu_percent's cast is a kind of its own (see castKeys above), not
			// bool or int, so it is asked here rather than through castOK.
			if k == "cpu_percent" {
				if s, isString := svc[k].(string); isString {
					msg := cpuPercentMismatch(node, s, where)
					if notTaken && !strings.Contains(msg, "does not read as a number") {
						msg = ""
					}
					if msg != "" {
						if err := fault(fmt.Errorf("compose file %s: %s", path, msg)); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// readAsItIsWhereNotTaken are the service keys docker compose asks nothing of in a service it does
// not take (a service of a file that is only extended from, which the extending service does not
// name): measured, v5.5.1, `config -q`, for each of the keys below at every place the schema gives it,
// with a number, a null, a list, a mapping, a word, `true`, a quoted number and a float, in a service
// taken and one not — taken refuses what the schema does not take, and not taken reads all of it.
// The other keys of a service are read into a type before docker compose knows whether the service
// is taken (`ports`, `volumes`, `build`, `depends_on`, `env_file`, the numbers and booleans it casts,
// and what the file reads by its own tags), so a value of the wrong kind in those is refused there
// as everywhere.
var readAsItIsWhereNotTaken = map[string]bool{
	"attach": true, "blkio_config": true, "cgroup": true, "cgroup_parent": true, "command": true,
	"container_name": true, "cpuset": true, "credential_spec": true, "device_cgroup_rules": true,
	"domainname": true, "entrypoint": true, "external_links": true, "extra_hosts": true,
	"group_add": true, "hostname": true, "image": true, "ipc": true, "isolation": true,
	"logging": true, "mac_address": true, "mem_limit": true, "mem_reservation": true,
	"mem_swappiness": true, "memswap_limit": true, "network_mode": true, "pid": true,
	"platform": true, "post_start": true, "pre_start": true, "pre_stop": true, "provider": true,
	"pull_policy": true, "pull_refresh_after": true, "restart": true, "runtime": true,
	"security_opt": true, "shm_size": true, "stop_grace_period": true, "stop_signal": true,
	"storage_opt": true, "use_api_socket": true, "user": true, "userns_mode": true, "uts": true,
	"volumes_from": true, "working_dir": true,
}

// nullChecked are the service keys whose blocks are read into a typed shape by the decode,
// which passes a key of them that holds nothing: asked here, by the schema.
var nullChecked = map[string]bool{"deploy": true, "build": true, "healthcheck": true, "networks": true, "depends_on": true}

// nothingWhereNoneIsTaken says where v, a mapping, holds a key with nothing after it that
// the node gives no null — at any depth, through the branches of a oneOf that take a
// mapping — or "" when it holds none. A list is not looked into, and a key the node does not
// name is the business of the check of unknown keys.
//
// contexts says that v is the `additional_contexts` of a build: known from where the walk came
// down, not from how where is spelt — a network or a dependency can be named
// `x.build.additional_contexts` and is only a name (#1616).
func (n *specNode) nothingWhereNoneIsTaken(v any, where string, depth int, contexts bool) string {
	if n == nil {
		return ""
	}
	if m, ok := v.(map[any]any); ok {
		conv := make(map[string]any, len(m))
		for k, x := range m {
			conv[fmt.Sprint(k)] = x
		}
		v = conv
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	branches := n.objectBranches()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var child *specNode
		for _, b := range branches {
			if child = b.child(k); child != nil {
				break
			}
		}
		if child == nil {
			continue
		}
		if m[k] == nil {
			// A dependency with nothing after its name reads as `service_started`, and an
			// `additional_contexts` with nothing after it names no context: read so before the
			// schema was asked, and still (docker compose refuses both; a known difference).
			readPast := (depth == 0 && strings.HasSuffix(where, ".depends_on")) ||
				(depth == 0 && strings.HasSuffix(where, ".build") && k == "additional_contexts")
			// The schema takes a null entry of `additional_contexts`; docker compose does not.
			entry := contexts
			if (!child.takesNull() || entry) && !readPast {
				return where + "." + k + " has nothing after it — write the value, or remove the key"
			}
			continue
		}
		below := depth == 0 && strings.HasSuffix(where, ".build") && k == "additional_contexts"
		if msg := child.nothingWhereNoneIsTaken(m[k], where+"."+k, depth+1, below); msg != "" {
			return msg
		}
	}
	return ""
}

// objectBranches is the node and the branches of its oneOf that take a mapping.
func (n *specNode) objectBranches() []*specNode {
	var out []*specNode
	if len(n.Properties) > 0 || len(n.pats) > 0 || n.Additional != nil {
		out = append(out, n)
	}
	for _, o := range n.OneOf {
		out = append(out, o.objectBranches()...)
	}
	return out
}

// child is the node for the key k of a mapping this node takes: a named property, or the
// first pattern it matches, or the one every other key takes.
func (n *specNode) child(k string) *specNode {
	if c := n.Properties[k]; c != nil {
		return c
	}
	for _, p := range n.pats {
		if p.re.MatchString(k) {
			return p.node
		}
	}
	return n.Additional
}

// takesNull reports whether the node, or a branch of its oneOf, is given null.
func (n *specNode) takesNull() bool {
	for _, k := range n.kinds {
		if k == "null" {
			return true
		}
	}
	for _, o := range n.OneOf {
		if o.takesNull() {
			return true
		}
	}
	return len(n.kinds) == 0 && len(n.OneOf) == 0
}

// mismatch says how v is not what the node takes, or "" when it is.
func (n *specNode) mismatch(v any, where string) string {
	if len(n.OneOf) > 0 {
		var narrowed []string
		for _, o := range n.OneOf {
			msg := o.mismatch(v, where)
			if msg == "" {
				return n.own(v, where)
			}
			// A branch that takes the kind of the value and refuses what is in it
			// says more than "must be one of these": which item, which bound.
			if len(o.kinds) > 0 && o.kindOK(v) {
				narrowed = append(narrowed, msg)
			}
		}
		if len(narrowed) == 1 {
			return narrowed[0]
		}
		return where + " must be " + n.describe()
	}
	return n.own(v, where)
}

// own checks what the node itself says, not its oneOf.
func (n *specNode) own(v any, where string) string {
	if m, ok := v.(map[any]any); ok {
		// A mapping with a key that is not a string.
		conv := make(map[string]any, len(m))
		for k, x := range m {
			conv[fmt.Sprint(k)] = x
		}
		v = conv
	}
	if len(n.kinds) > 0 && !n.kindOK(v) {
		return where + " must be " + n.describe()
	}
	if s, ok := v.(string); ok && n.re != nil && !n.re.MatchString(s) {
		return fmt.Sprintf("%s %q does not match the pattern %s", where, s, n.Pattern)
	}
	if len(n.Enum) > 0 && !inEnum(n.Enum, v) {
		return fmt.Sprintf("%s %v is not one of %v", where, v, n.Enum)
	}
	if f, ok := number(v); ok {
		if n.Minimum != nil && f < *n.Minimum {
			return fmt.Sprintf("%s %v is below the least, %v", where, v, *n.Minimum)
		}
		if n.Maximum != nil && f > *n.Maximum {
			return fmt.Sprintf("%s %v is above the most, %v", where, v, *n.Maximum)
		}
	}
	switch x := v.(type) {
	case []any:
		if n.Items != nil {
			for i, item := range x {
				if msg := n.Items.mismatch(item, fmt.Sprintf("%s[%d]", where, i)); msg != "" {
					return msg
				}
			}
		}
		// uniqueItems is not asked: docker compose refuses a repeat in some lists
		// (`security_opt: [a, a]`) and takes it in the others (`dns_search`, `expose`,
		// `cap_add`, `env_file`; measured, v5.5.1), and the schema says unique for all.
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := n.Properties[k]
			if child == nil {
				for _, p := range n.pats {
					if p.re.MatchString(k) {
						child = p.node
						break
					}
				}
			}
			if child == nil {
				child = n.Additional
			}
			// A name of no characters matches no pattern of a map whose names are `.+`
			// (`sysctls`, `extra_hosts`, `annotations`), and docker compose refuses it
			// (#1530; measured, v5.5.1).
			if child == nil && k == "" && n.PatternProperties[".+"] != nil {
				return fmt.Sprintf("%s has a name of no characters — docker compose refuses an empty name; write the name, or remove the entry", where)
			}
			if child == nil {
				continue
			}
			if msg := child.mismatch(x[k], where+"."+k); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func (n *specNode) kindOK(v any) bool {
	for _, k := range n.kinds {
		switch k {
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
			// A date written without quotes is a timestamp to the YAML reader and a
			// string to docker compose, which reads it into JSON before the schema
			// (`org.opencontainers.image.created: 2024-01-15` in `annotations`).
			if _, ok := v.(time.Time); ok {
				return true
			}
		case "number":
			if _, ok := number(v); ok {
				return true
			}
			if _, ok := v.(string); ok {
				return true // read into the number by docker compose; one that does not read is another check
			}
		case "integer":
			if f, ok := number(v); ok && f == math.Trunc(f) {
				return true
			}
			if _, ok := v.(string); ok {
				return true // read into the integer by docker compose, as above
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
			if _, ok := v.(string); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "object":
			switch v.(type) {
			case map[string]any, map[any]any:
				return true
			}
		}
	}
	return false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func inEnum(enum []any, v any) bool {
	for _, e := range enum {
		if fmt.Sprint(e) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}

// describe is what the node takes, for a refusal.
func (n *specNode) describe() string {
	if len(n.OneOf) > 0 {
		parts := make([]string, 0, len(n.OneOf))
		for _, o := range n.OneOf {
			parts = append(parts, o.describe())
		}
		return strings.Join(dedupe(parts), " or ")
	}
	names := make([]string, 0, len(n.kinds))
	for _, k := range n.kinds {
		switch k {
		case "string":
			names = append(names, "a string")
		case "number":
			names = append(names, "a number")
		case "integer":
			names = append(names, "an integer")
		case "boolean":
			names = append(names, "a boolean")
		case "null":
			names = append(names, "empty")
		case "array":
			if n.Items != nil && len(n.Items.kinds) == 1 && n.Items.kinds[0] == "string" {
				names = append(names, "a list of strings")
			} else {
				names = append(names, "a list")
			}
		case "object":
			names = append(names, "a mapping")
		}
	}
	if len(names) == 0 {
		return "a value of another kind"
	}
	return strings.Join(dedupe(names), " or ")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// portFloats asks the entries of a `ports` list for a float: an entry that is one, or a
// long-form entry whose `published` is one. A float the loader reads as a string
// (`2.5`) is refused before this by the port's own reading; what comes here is the
// float whose text is a port.
// A service docker compose does not take of a file that is only extended from (notTaken) is asked
// for less by portFloats, portFieldKinds and modeKinds (#1541; measured, v5.5.1, 48 forms): a short
// entry that is a float is refused there as everywhere, and the rest — a long entry's `published`
// as a float, list or mapping, its `name`, `app_protocol`, `host_ip` and `protocol` of another
// kind, and every `mode` — is left, where docker compose does not read it.
func portFloats(path, where string, list any, notTaken bool, fault func(error) error) error {
	entries, _ := list.([]any)
	say := func(at string, f float64) error {
		text := strconv.FormatFloat(f, 'f', -1, 64)
		return fault(fmt.Errorf("compose file %s: %s%s %s is a float, and a port is an integer or a string — write it as `%s` or `\"%s\"`", path, where, at, text, text, text))
	}
	for i, e := range entries {
		switch v := e.(type) {
		case float64:
			if err := say(fmt.Sprintf("[%d]", i), v); err != nil {
				return err
			}
		case map[string]any:
			if notTaken {
				continue
			}
			if f, ok := v["published"].(float64); ok {
				if err := say(fmt.Sprintf("[%d].published", i), f); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emptyMountSections asks the short mounts of a `volumes` list for an empty first section or an
// empty last one, which docker compose refuses as an empty section between colons: `:/b`,
// `:/b:ro`, `:ro` and `/a:/b:` (a bare `:` is read, and so is a one-byte section after a leading
// colon, `:a` and `:1`). A middle section of one letter (`/a:a:`, `/a:é:`, `/a:字:`) is read as
// well, docker compose taking it for a Windows drive; one that is a digit or a symbol (`/a:1:`,
// `/a:_:`) is refused (measured, v5.5.1: a single rune of a Unicode letter, as unicode.IsLetter
// has it, and a letter with a combining mark after it is two runes). What is between the colons
// is the business of shortMountFields, in the decode.
func emptyMountSections(path, where string, v any, fault func(error) error) error {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	for i, item := range list {
		entry, ok := item.(string)
		if !ok {
			continue
		}
		fields := strings.Split(entry, ":")
		switch {
		// A one-letter middle section is what docker compose takes for a Windows drive (`/a:a:` is
		// read there), which this does not follow, and does not refuse for it either; `:a` is read
		// there at one byte, whatever it is (`:1`, `:.`), and refused at two (`:é`).
		case len(fields) == 2 && fields[0] == "" && len(fields[1]) > 1, len(fields) == 3 && fields[0] == "" && fields[1] != "":
			if err := fault(fmt.Errorf("compose file %s: %s entry %d of %d: %q has nothing before its first colon — a short mount is SOURCE:TARGET or SOURCE:TARGET:MODE, and docker compose refuses an empty section between colons; a source that comes from a variable is empty when the variable is not set", path, where, i+1, len(list), entry)); err != nil {
				return err
			}
		case len(fields) == 3 && fields[0] != "" && fields[1] != "" && !isDriveLetter(fields[1]) && fields[2] == "":
			if err := fault(fmt.Errorf("compose file %s: %s entry %d of %d: %q has nothing after its second colon — write SOURCE:TARGET, or a mode after it (SOURCE:TARGET:ro); docker compose refuses an empty section between colons", path, where, i+1, len(list), entry)); err != nil {
				return err
			}
		}
	}
	return nil
}

// modeKinds asks the `mode` of a service's `ports` entries and of its `deploy` for a string,
// the kind docker compose reads there (`host`, `ingress`; `replicated`, `global`): a number,
// a bool, a null, a list and a mapping are refused, in the file that writes them, as docker
// compose refuses them by its schema, file by file. The `mode` of `secrets` and `configs`
// entries is not asked here: docker compose refuses one that is no whole number and no
// octal string only after the files are merged, so a later `-f` file that replaces or resets
// it makes the file it was in fine there (#1544).
func modeKinds(path, where, key string, v any, notTaken bool, fault func(error) error) error {
	say := func(at, want string, got any) error {
		return fault(fmt.Errorf("compose file %s: %s%s.mode must be %s, and this is %s", path, where, at, want, describeYAMLValue(got)))
	}
	switch key {
	case "deploy":
		if m, ok := v.(map[string]any); ok {
			// `deploy.replicas` reads as a whole number (#1467; measured, v5.5.1, `config -q`):
			// 2, "2", 2.0 and !!float 2 do; "two", "", "1e2", "0x2", 1.5, true, null, a list and a
			// mapping do not — refused in the file that writes them, as a later `-f` that replaces
			// the value does not help there.
			if replicas, present := m["replicas"]; present && !replicasReadable(replicas) && !(notTaken && !isString(replicas)) {
				if err := fault(fmt.Errorf("compose file %s: %s.replicas must be a whole number, and this is %s", path, where, describeYAMLValue(replicas))); err != nil {
					return err
				}
			}
			if mode, present := m["mode"]; present && !notTaken {
				if _, isString := mode.(string); !isString {
					return say("", "a string (`replicated` or `global`)", mode)
				}
			}
		}
	case "ports":
		entries, _ := v.([]any)
		for i, e := range entries {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if mode, present := m["mode"]; present && !notTaken {
				if _, isString := mode.(string); !isString {
					if err := say(fmt.Sprintf("[%d]", i), "a string (`host` or `ingress`)", mode); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// describeYAMLValue names a decoded value's kind for a message.
func describeYAMLValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null (nothing after the key)"
	case string:
		return fmt.Sprintf("%q", x)
	case bool:
		return fmt.Sprintf("the boolean %v", x)
	case int, int64, uint64:
		return fmt.Sprintf("the number %v", x)
	case float64:
		return fmt.Sprintf("the number %v", strconv.FormatFloat(x, 'g', -1, 64))
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	return fmt.Sprintf("%v", v)
}

// portFieldKinds asks the fields of a long-form `ports` entry for the kind docker compose
// reads (#1536; measured, v5.5.1, `config -q`): `name` and `app_protocol` are strings (a
// number, a bool, a null, a float and a list are refused), `host_ip` and `protocol` are
// strings (a null is refused; another kind is refused by the port's own reading), and
// `published` is a string or a whole number (a list or a mapping is refused).
func portFieldKinds(path, where string, list any, notTaken bool, fault func(error) error) error {
	if notTaken {
		return nil
	}
	entries, _ := list.([]any)
	for i, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		for _, f := range []string{"name", "app_protocol", "host_ip", "protocol", "published"} {
			v, present := m[f]
			if !present {
				continue
			}
			bad, want := false, "a string"
			switch f {
			case "name", "app_protocol":
				_, isString := v.(string)
				bad = !isString
			case "host_ip", "protocol":
				bad = v == nil
			case "published":
				switch v.(type) {
				case []any, map[string]any:
					bad, want = true, "a port number or a string"
				}
			}
			if bad {
				if err := fault(fmt.Errorf("compose file %s: %s[%d].%s must be %s, and this is %s", path, where, i, f, want, describeYAMLValue(v))); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// replicasReadable reports whether a `deploy.replicas` value reads as a whole number the way
// docker compose reads it: an integer, a float with no fraction, or a string of decimal digits.
func replicasReadable(v any) bool {
	switch x := v.(type) {
	case int, int64, uint64:
		// `int64` is a branch yaml.v3 does not return on a 64-bit build, and `uint64` (a value past
		// int64) is refused by its bound (checkModelBounds) before this is asked; both stay so that
		// this reads an integer of any width.
		return true
	case float64:
		// The guard against an infinity and a NaN is one nothing reaches today: checkNonFiniteNumbers
		// refuses `.inf` and `.nan` first (#1507). It stays so that this reads a float on its own.
		return !math.IsNaN(x) && !math.IsInf(x, 0) && x == math.Trunc(x)
	case string:
		_, err := strconv.ParseInt(x, 10, 64)
		return err == nil
	}
	return false
}

func isString(v any) bool { _, ok := v.(string); return ok }

// withoutEmptyName is v without the entry a name of no characters gives it, when v is a mapping
// (a copy: v itself is left as it is).
func withoutEmptyName(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if _, has := m[""]; !has {
		return v
	}
	c := make(map[string]any, len(m))
	for k, x := range m {
		if k != "" {
			c[k] = x
		}
	}
	return c
}

// isDriveLetter says whether a section is one rune that is a letter, which docker compose takes
// for a Windows drive: `é`, `Ω` and `字` are, `1`, `_` and a letter with a combining mark are not.
func isDriveLetter(section string) bool {
	r, size := utf8.DecodeRuneInString(section)
	return size == len(section) && unicode.IsLetter(r)
}
