package compose

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What docker compose takes in the blocks of `deploy` besides `mode` and `replicas` (#1605; every cell measured, v5.5.1,
// `config -q`): the compose specification's schema gives each key its type and refuses a key it does not know, file by file
// — a later file that writes the block over does not help — and the value is cast to its Go type as it is read. A service
// that is not taken is read for none of this.

// dkind is what a key of `deploy` takes.
type dkind int

const (
	dString       dkind = iota // a string (any: `condition`, `failure_action`, `endpoint_mode`)
	dDuration                  // a string that reads as a duration (a Go duration, with the units `d` and `w` too)
	dUint                      // a whole number, not below zero: an integer, a float with a whole value, or a string of digits
	dInt                       // the same, with a sign
	dNumber                    // a number, a float included, or a string that reads as one
	dOrder                     // `start-first` or `stop-first`
	dSize                      // a string that reads as a size (`512m`)
	dStringList                // a list of strings
	dListOrDict                // a list of strings, or a mapping of names to scalars
	dBlock                     // a mapping of keys named in fields
	dBlockList                 // a list of mappings of keys named in fields
	dCount                     // a device count: a whole number with a sign, or `all`
	dGenericValue              // a generic resource's value: a number, or a string of digits
)

// dshape is the shape of one key: its kind and, for a block, the keys of it.
type dshape struct {
	kind     dkind
	path     string // the place specKeys names for a block's keys
	fields   map[string]dshape
	required []string
}

var deployShapesByKey = map[string]dshape{
	"endpoint_mode": {kind: dString},
	"labels":        {kind: dListOrDict},
	"restart_policy": {kind: dBlock, path: "services.*.deploy.restart_policy", fields: map[string]dshape{
		"condition": {kind: dString}, "delay": {kind: dDuration}, "max_attempts": {kind: dUint}, "window": {kind: dDuration}}},
	"placement": {kind: dBlock, path: "services.*.deploy.placement", fields: map[string]dshape{
		"constraints": {kind: dStringList}, "max_replicas_per_node": {kind: dUint},
		"preferences": {kind: dBlockList, path: "services.*.deploy.placement.preferences[]", fields: map[string]dshape{"spread": {kind: dString}}}}},
	"update_config":   updateConfigShape("services.*.deploy.update_config"),
	"rollback_config": updateConfigShape("services.*.deploy.rollback_config"),
}

func updateConfigShape(path string) dshape {
	return dshape{kind: dBlock, path: path, fields: map[string]dshape{
		"parallelism": {kind: dUint}, "delay": {kind: dDuration}, "failure_action": {kind: dString}, "monitor": {kind: dDuration},
		"max_failure_ratio": {kind: dNumber}, "order": {kind: dOrder}}}
}

// deployResourceShapes are the blocks of `deploy.resources` that are asked here: what its `limits` and `reservations`
// hold besides `cpus` and `memory` of the limits, which the decode reads.
var deployResourceShapes = map[string]dshape{
	"limits": {kind: dBlock, path: "services.*.deploy.resources.limits", fields: map[string]dshape{"pids": {kind: dInt}}},
	"reservations": {kind: dBlock, path: "services.*.deploy.resources.reservations", fields: map[string]dshape{
		"cpus": {kind: dNumber}, "memory": {kind: dSize},
		"devices": {kind: dBlockList, path: "services.*.deploy.resources.reservations.devices[]", required: []string{"capabilities"}, fields: map[string]dshape{
			"capabilities": {kind: dStringList}, "driver": {kind: dString}, "count": {kind: dCount}, "device_ids": {kind: dStringList}, "options": {kind: dListOrDict}}},
		"generic_resources": {kind: dBlockList, path: "services.*.deploy.resources.reservations.generic_resources[]", fields: map[string]dshape{
			"discrete_resource_spec": {kind: dBlock, path: "services.*.deploy.resources.reservations.generic_resources[].discrete_resource_spec", fields: map[string]dshape{
				"kind": {kind: dString}, "value": {kind: dGenericValue}}}}}}},
}

// deployShapes asks the blocks of a service's `deploy` for what docker compose takes in them, and says the first it does not.
// nulls says a key with nothing after it is asked too (of a merged service, where nothing else asks it).
func deployShapes(path, where string, deploy map[string]any, nulls bool, fault func(error) error) error {
	say := func(at, problem string) error {
		return fault(fmt.Errorf("compose file %s: %s.%s", path, where, joinProblem(at, problem)))
	}
	keys := make([]string, 0, len(deploy))
	for k := range deploy {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		shape, ok := deployShapesByKey[k]
		if !ok {
			continue
		}
		if problem := dcheck(deploy[k], shape, false, nulls); problem != "" {
			if err := say(k, problem); err != nil {
				return err
			}
		}
	}
	if res, ok := deploy["resources"].(map[string]any); ok {
		for _, k := range []string{"limits", "reservations"} {
			if block, present := res[k]; present {
				if problem := dcheck(block, deployResourceShapes[k], false, nulls); problem != "" {
					if err := say("resources."+k, problem); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// deployCasts asks, of a service that is not taken, what docker compose still refuses when it reads the service into its model:
// a count (`max_attempts`, `max_replicas_per_node`, `parallelism`, `max_failure_ratio`) written as a string that reads as no
// number, and an entry of `labels` that is no string. The rest of the blocks is not read there (measured, v5.5.1).
func deployCasts(path, where string, deploy map[string]any, fault func(error) error) error {
	say := func(at, what string, v any) error {
		return fault(fmt.Errorf("compose file %s: %s.%s must be %s, and this is %s", path, where, at, what, describeYAMLValue(v)))
	}
	counts := map[string][]string{"restart_policy": {"max_attempts"}, "placement": {"max_replicas_per_node"}, "update_config": {"parallelism", "max_failure_ratio"}, "rollback_config": {"parallelism", "max_failure_ratio"}}
	blocks := make([]string, 0, len(counts))
	for b := range counts {
		blocks = append(blocks, b)
	}
	sort.Strings(blocks)
	for _, b := range blocks {
		m, ok := deploy[b].(map[string]any)
		if !ok {
			continue
		}
		for _, k := range counts[b] {
			str, isString := m[k].(string)
			if !isString {
				continue
			}
			if k == "max_failure_ratio" && !floatText(str) || k != "max_failure_ratio" && !int64Text(str) {
				if err := say(b+"."+k, "a number", str); err != nil {
					return err
				}
			}
		}
	}
	if list, ok := deploy["labels"].([]any); ok {
		for i, item := range list {
			if _, isString := item.(string); !isString {
				if err := say(fmt.Sprintf("labels[%d]", i), "a string", item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// dcheck is what is wrong with a value for a shape, or "". A null where a block belongs is read as none.
func dcheck(v any, s dshape, inItem, nulls bool) string {
	// A key with nothing after it is asked by its own check (nullChecked): refused in a file, and read where the
	// service that extends the file writes the key over it. Only the block that docker compose refuses a null for is asked here.
	if v == nil && !inItem && !nulls {
		return ""
	}
	switch s.kind {
	case dBlock:
		if v == nil {
			if inItem || nulls {
				return "must be a mapping, and this is " + describeYAMLValue(v)
			}
			return ""
		}
		m, ok := v.(map[string]any)
		if !ok {
			return "must be a mapping, and this is " + describeYAMLValue(v)
		}
		return dblock(m, s, inItem, nulls)
	case dBlockList:
		list, ok := v.([]any)
		if !ok {
			return "must be a list, and this is " + describeYAMLValue(v)
		}
		for i, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return fmt.Sprintf("[%d] must be a mapping, and this is %s", i, describeYAMLValue(item))
			}
			if problem := dblock(m, s, true, nulls); problem != "" {
				return fmt.Sprintf("[%d]%s", i, problem)
			}
		}
		return ""
	case dString:
		if _, ok := v.(string); !ok {
			return "must be a string, and this is " + describeYAMLValue(v)
		}
	case dDuration:
		str, ok := v.(string)
		if !ok {
			return "must be a duration written as a string, as in `30s`, and this is " + describeYAMLValue(v)
		}
		if !composeDuration(str) {
			return fmt.Sprintf("%q is not a duration — use a unit, e.g. 30s, 1m, or 500ms", str)
		}
	case dUint, dInt:
		if !dockerWholeNumber(v, s.kind == dInt) {
			return "must be a whole number, and this is " + describeYAMLValue(v)
		}
	case dNumber:
		if !numberValue(v) {
			return "must be a number, and this is " + describeYAMLValue(v)
		}
	case dOrder:
		if str, ok := v.(string); !ok || str != "start-first" && str != "stop-first" {
			return "must be `start-first` or `stop-first`, and this is " + describeYAMLValue(v)
		}
	case dSize:
		str, ok := v.(string)
		if !ok {
			return "must be a size written as a string, as in `\"512m\"`, and this is " + describeYAMLValue(v)
		}
		if _, err := parseMemoryBytes(str); err != nil {
			return fmt.Sprintf("%q is not a size — write a number and a unit, as in `512m`", str)
		}
	case dStringList:
		list, ok := v.([]any)
		if !ok {
			return "must be a list of strings, and this is " + describeYAMLValue(v)
		}
		for i, item := range list {
			if _, ok := item.(string); !ok {
				return fmt.Sprintf("[%d] must be a string, and this is %s", i, describeYAMLValue(item))
			}
		}
	case dListOrDict:
		switch x := v.(type) {
		case []any:
			for i, item := range x {
				if _, ok := item.(string); !ok {
					return fmt.Sprintf("[%d] must be a string, and this is %s", i, describeYAMLValue(item))
				}
			}
		case map[string]any:
			for name, item := range x {
				if name == "" {
					return "has a name of no characters"
				}
				switch item.(type) {
				case nil, string, bool, int, int64, uint64, float64, time.Time:
				default:
					return fmt.Sprintf(".%s must be a string, a number, a boolean or nothing, and this is %s", name, describeYAMLValue(item))
				}
			}
		default:
			return "must be a list or a mapping, and this is " + describeYAMLValue(v)
		}
	case dCount:
		switch x := v.(type) {
		case int, int64:
		case string:
			if !strings.EqualFold(x, "all") && !int64Text(x) {
				return fmt.Sprintf("must be a whole number or `all`, and this is %q", x)
			}
		default:
			return "must be a whole number or `all`, and this is " + describeYAMLValue(v)
		}
	case dGenericValue:
		switch x := v.(type) {
		case int, int64, uint64:
		case float64:
			if math.IsInf(x, 0) || math.IsNaN(x) {
				return "must be a number, and this is " + describeYAMLValue(v)
			}
		case string:
			if !int64Text(x) {
				return fmt.Sprintf("must be a number, and this is %q", x)
			}
		default:
			return "must be a number, and this is " + describeYAMLValue(v)
		}
	}
	return ""
}

// dblock asks the keys of one mapping: those the specification does not name, those it requires, and each one's value.
func dblock(m map[string]any, s dshape, inItem, nulls bool) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if s.path != "" && !specKnows(s.path, k) {
			return fmt.Sprintf("has %q, which docker compose does not take there", k)
		}
		field, ok := s.fields[k]
		if !ok {
			continue
		}
		if problem := dcheck(m[k], field, inItem, nulls); problem != "" {
			return "." + joinProblem(k, problem)
		}
	}
	for _, k := range s.required {
		if _, present := m[k]; !present {
			return "." + k + " is required"
		}
	}
	return ""
}

var intText = regexp.MustCompile(`^[+-]?[0-9]+$`)

// int64Text reports whether s is a whole number written as digits with an optional sign that an int64 holds: docker compose
// casts a string with `Atoi`, and one past it is refused.
func int64Text(s string) bool {
	if !intText.MatchString(s) {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// joinProblem puts a key before what is wrong with it: a continuation of the path (`[0].count`, `.a`) follows it directly.
func joinProblem(at, problem string) string {
	if strings.HasPrefix(problem, "[") || strings.HasPrefix(problem, ".") {
		return at + problem
	}
	return at + " " + problem
}

// dockerWholeNumber reports whether v is a whole number as docker compose reads one: an integer, a float with a whole value,
// or a string of digits with an optional sign. Unless signed, one below zero is not (`-0` is zero).
func dockerWholeNumber(v any, signed bool) bool {
	switch x := v.(type) {
	case int:
		return signed || x >= 0
	case int64:
		return signed || x >= 0
	case uint64:
		return true
	case float64:
		return !math.IsInf(x, 0) && !math.IsNaN(x) && x == math.Trunc(x) && (signed || x >= 0)
	case string:
		if !int64Text(x) {
			return false
		}
		if signed || !strings.HasPrefix(x, "-") {
			return true
		}
		return strings.Trim(x[1:], "0") == ""
	}
	return false
}

// floatText reports whether s reads as a float (the cast of a service that is not taken asks no more: `inf` reads).
func floatText(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// numberValue reports whether v is a number as docker compose reads one: an integer or a finite float, or a string that
// reads as a finite one (`1_0` and `1e2` do; a hex number, `inf` and `nan` do not).
func numberValue(v any) bool {
	switch x := v.(type) {
	case int, int64, uint64:
		return true
	case float64:
		return !math.IsInf(x, 0) && !math.IsNaN(x)
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	}
	return false
}

var durationText = regexp.MustCompile(`^[-+]?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:ns|us|µs|μs|ms|s|m|h|d|w))+$`)

// composeDuration reports whether s is a duration as docker compose reads one: a Go duration (`1h30m`, `1.5s`, `0`),
// and the units `d` and `w` besides.
func composeDuration(s string) bool {
	if _, err := time.ParseDuration(s); err == nil {
		return true
	}
	if !durationText.MatchString(s) || !strings.ContainsAny(strings.TrimLeft(s, "+-0123456789."), "dw") {
		return false
	}
	// The units sum to what an int64 of nanoseconds holds (`106752d` does not: docker compose refuses it).
	var total float64
	for _, m := range dwDurationPart.FindAllStringSubmatch(strings.TrimLeft(s, "+-"), -1) {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return false
		}
		total += n * dwDurationUnits[m[2]]
	}
	return total < math.MaxInt64
}

var dwDurationPart = regexp.MustCompile(`([0-9]+(?:\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h|d|w)`)

var dwDurationUnits = map[string]float64{"ns": 1, "us": 1e3, "µs": 1e3, "μs": 1e3, "ms": 1e6, "s": 1e9, "m": 60e9, "h": 3600e9, "d": 24 * 3600e9, "w": 7 * 24 * 3600e9}

// untakenDeployKept is what of a `deploy` mapping docker compose still reads in a service that is not taken, and so what
// the copy of the service keeps: the number of replicas, the counts deployCasts asks, and `labels` when it is a list.
func untakenDeployKept(d map[string]any) map[string]any {
	kept := map[string]any{}
	if v, ok := d["replicas"]; ok {
		kept["replicas"] = v
	}
	counts := map[string][]string{"restart_policy": {"max_attempts"}, "placement": {"max_replicas_per_node"}, "update_config": {"parallelism", "max_failure_ratio"}, "rollback_config": {"parallelism", "max_failure_ratio"}}
	for block, keys := range counts {
		m, ok := d[block].(map[string]any)
		if !ok {
			continue
		}
		for _, k := range keys {
			if v, ok := m[k]; ok {
				inner, _ := kept[block].(map[string]any)
				if inner == nil {
					inner = map[string]any{}
					kept[block] = inner
				}
				inner[k] = v
			}
		}
	}
	if list, ok := d["labels"].([]any); ok {
		kept["labels"] = list
	}
	return kept
}
