package compose

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
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

// checkServiceShapes refuses a service key whose value is not what docker compose
// takes for it. Only the keys the schema gives a shape are looked at: a key it
// does not know is the business of the check that says which keys a service may
// have, and a newer docker compose may take one this schema does not.
//
// A value of the wrong kind is refused. A string where the schema says a number or
// a boolean is taken: the schema of the keys checked here gives such a key a string
// branch as well (`number or string`, `boolean or string`), for the value that
// docker compose reads into the number or boolean (`cpu_shares: "7"`); whether the
// string reads (`cpu_shares: "abc"`, and the bounds of the number it reads into —
// `cpu_percent: "150"`) is a check of its own, not made here.
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
