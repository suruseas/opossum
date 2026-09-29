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
// `cpu_percent` is also deliberately not here, despite being on the same
// list and in heldToTheSchema: measured separately from the other nine
// int-cast keys, and it does not share their cast. `cpu_shares: "7.0"` is
// refused (plain strconv.ParseInt); `cpu_percent: "7.0"` is ACCEPTED —
// docker compose casts it through a float first (also taking "7e0", "1e1",
// "7.", and Go's digit-separator syntax: "5_0", "1_0" and "1_00" all read as
// their obvious values). `strconv.ParseFloat` agrees with all of that —
// `cpu_percent: "1_000"` is where it stops agreeing, but not for the reason
// first assumed here: measured again, docker compose reads it as 1000 the
// same way it reads "1_00" as 100 (both are v5.5.1's "must be a string"
// schema-stage wording, not a cast failure), and refuses it for being
// **above the 0–100 bound** — the same bound `mismatch`'s own
// `Minimum`/`Maximum` check already reads off the schema, but only for a
// native number (see `number(v)` below); a string never reaches it. So the
// missing piece for `cpu_percent` is not really a cast rule of its own: it
// is ParseFloat, a whole-number check, and wiring the cast result back
// through that existing bound check — needs doing before a check can be
// written with the same confidence as the nine below.
//
// bytes-shaped (mem_reservation, mem_swappiness, memswap_limit, shm_size —
// mem_limit is, like cpus, a key opossum reads itself) and duration-shaped
// (stop_grace_period) keys are not in this map yet: docker compose's cast
// for those is not plain Go syntax (`1d` reads as a day, which
// time.ParseDuration does not take; `mem_swappiness` casts through the same
// byte-suffix reading as mem_limit despite being a percentage by name) and
// needs its own measurement before a check can be written with the same
// confidence as the two kinds below.
var castKeys = map[string]string{
	"attach": "bool", "oom_kill_disable": "bool", "privileged": "bool", "stdin_open": "bool",
	"cpu_count": "int", "cpu_shares": "int", "cpu_quota": "int",
	"cpu_period": "int", "cpu_rt_period": "int", "cpu_rt_runtime": "int",
	"oom_score_adj": "int", "pids_limit": "int", "scale": "int",
}

// castKindNames is what castMismatch's refusal calls each kind, matching
// describe()'s wording for the shape check above so the two read as one
// family of message rather than two different voices for the same key.
var castKindNames = map[string]string{"bool": "a boolean", "int": "an integer"}

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
	}
	return true
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
// (`cpu_percent: "150"`) is still a check of its own, not made anywhere yet: the
// `Minimum`/`Maximum` check further down only ever sees a native number, never a
// string that castOK has already confirmed reads as one.
func checkServiceShapes(path string, services map[string]any) error {
	spec, err := loadServiceSpec()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
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
			node := spec.Properties[k]
			if node == nil || !heldToTheSchema[k] {
				continue
			}
			where := "services." + name + "." + k
			// A boolean that docker compose does not read a string into (measured,
			// v5.5.1: `use_api_socket: "true"` is refused, `stdin_open: "true"` is not).
			if _, isString := svc[k].(string); isString && strictBooleanKeys[k] {
				return fmt.Errorf("compose file %s: %s must be a boolean", path, where)
			}
			if msg := node.mismatch(svc[k], where); msg != "" {
				return fmt.Errorf("compose file %s: %s", path, msg)
			}
			// The shape above took the string on the strength of its kind's
			// string branch; this asks whether docker compose can actually
			// read it into the kind that branch exists for (#1366).
			if kind, ok := castKeys[k]; ok {
				if s, isString := svc[k].(string); isString && !castOK(kind, s) {
					return fmt.Errorf("compose file %s: %s %q does not read as %s",
						path, where, s, castKindNames[kind])
				}
			}
		}
	}
	return nil
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
