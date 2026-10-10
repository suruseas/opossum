package compose

import (
	"strconv"

	"gopkg.in/yaml.v3"
)

// buildRefuses are, for the keys of `build` of a service nothing takes, the shapes of value docker compose refuses (measured, v5.5.1, `config -q`, with `context: .` beside
// it, each key of the schema with each of the twenty-two shapes of valueShape; #1958, #1970, #1980). Every other shape of a key is read there — a word for a list, a number for a
// boolean, a list for a word — and a key the schema does not have is read in all of them; `context` is not in the table (it is asked as it has been). A shape that is not
// one of the twenty-two (a list of mixed items, a mapping inside a mapping) is not told, and is asked as it has been.
var buildRefuses = map[string][]string{
	"args":                {"[1]", "[{a: b}]", "[~]", "[{a: ~}]", "[[x]]", "[true]", "[2024-01-01]", "[1.5]", "[{a: 1}]"},
	"ssh":                 {"5", "1.5", "true", "abc", `""`, "[x]", "[1]", "[{a: b}]", "~", "[~]", "[{a: ~}]", "2024-01-01", "[[x]]", "[true]", "[2024-01-01]", "[1.5]", "[{a: 1}]"},
	"labels":              {"[1]", "[{a: b}]", "[~]", "[{a: ~}]", "[[x]]", "[true]", "[2024-01-01]", "[1.5]", "[{a: 1}]"},
	"additional_contexts": {"5", "1.5", "true", "abc", `""`, "[x]", "[1]", "{a: 1}", "[{a: b}]", "~", "[~]", "{a: ~}", "[{a: ~}]", "2024-01-01", "[[x]]", "[true]", "[2024-01-01]", "{a: true}", "{a: 2024-01-01}", "[1.5]", "[{a: 1}]"},
	"secrets":             {"[1]", "{a: 1}", "[~]", "{a: ~}", "[[x]]", "[true]", "[2024-01-01]", "{a: true}", "{a: 2024-01-01}", "[1.5]"},
	"tags":                {"[1]", "[{a: b}]", "[~]", "[{a: ~}]", "[[x]]", "[true]", "[2024-01-01]", "[1.5]", "[{a: 1}]"},
	"ulimits":             {"[x]", "{a: b}", "[~]", "{a: ~}", "[[x]]", "[true]", "[2024-01-01]", "{a: true}", "{a: 2024-01-01}", "[1.5]"},
}

// valueShape names the shape of a value among the twenty-two docker compose was measured with: "5" (a whole number), "1.5", "true", "abc" (any word), `""` (a blank),
// "2024-01-01" (a date), "~" (a null); the lists "[x]" (of words), "[1]" (of whole numbers), "[1.5]", "[true]", "[2024-01-01]", "[~]" and "[[x]]" (a list of lists of words); the
// mappings "{a: b}" (of words), "{a: 1}", "{a: true}", "{a: 2024-01-01}" and "{a: ~}"; the lists of mappings "[{a: b}]", "[{a: 1}]" and "[{a: ~}]"; "" for any other (a list of
// mixed items, a mapping inside a mapping, an empty list).
func valueShape(n *yaml.Node) string {
	n = unalias(n)
	// scalarKind is the kind of a scalar: "~", "1" (a whole number), "f" (a fraction), "b" (a boolean), "t" (a date), "x" (a word), "" for any other.
	scalarKind := func(c *yaml.Node) string {
		c = unalias(c)
		if c.Kind != yaml.ScalarNode {
			return ""
		}
		switch {
		case isNothing(c):
			return "~"
		case c.Tag == "!!int":
			return "1"
		case c.Tag == "!!float":
			return "f"
		case c.Tag == "!!bool":
			return "b"
		case c.Tag == "!!timestamp":
			return "t"
		case c.Tag == "!!str":
			return "x"
		}
		return ""
	}
	// kindOf is the kind of all the scalars of a list, or the values of a mapping, and "" where they differ.
	kindOf := func(vals []*yaml.Node) string {
		kind := ""
		for i, v := range vals {
			k := scalarKind(v)
			if k == "" || i > 0 && k != kind {
				return ""
			}
			kind = k
		}
		return kind
	}
	listNames := map[string]string{"x": "[x]", "1": "[1]", "f": "[1.5]", "b": "[true]", "t": "[2024-01-01]", "~": "[~]"}
	mapNames := map[string]string{"x": "{a: b}", "1": "{a: 1}", "b": "{a: true}", "t": "{a: 2024-01-01}", "~": "{a: ~}"}
	itemNames := map[string]string{"x": "[{a: b}]", "1": "[{a: 1}]", "~": "[{a: ~}]"}
	switch n.Kind {
	case yaml.ScalarNode:
		switch {
		case isNothing(n):
			return "~"
		case n.Tag == "!!int":
			return "5"
		case n.Tag == "!!float":
			return "1.5"
		case n.Tag == "!!bool":
			return "true"
		case n.Tag == "!!timestamp":
			return "2024-01-01"
		case n.Tag == "!!str" && n.Value == "":
			return `""`
		case n.Tag == "!!str":
			return "abc"
		}
	case yaml.SequenceNode:
		if len(n.Content) == 0 {
			return ""
		}
		if k := kindOf(n.Content); k != "" {
			return listNames[k]
		}
		// A list of lists of words, or of mappings with values of one kind each and the same kind throughout.
		inner, item := "", ""
		for _, c := range n.Content {
			c = unalias(c)
			switch c.Kind {
			case yaml.SequenceNode:
				if item != "" || len(c.Content) == 0 || kindOf(c.Content) != "x" {
					return ""
				}
				inner = "x"
			case yaml.MappingNode:
				k := kindOf(mappingValues(c))
				if inner != "" || k == "" || itemNames[k] == "" || item != "" && k != item {
					return ""
				}
				item = k
			default:
				return ""
			}
		}
		if inner != "" {
			return "[[x]]"
		}
		return itemNames[item]
	case yaml.MappingNode:
		if len(n.Content) == 0 {
			return ""
		}
		return mapNames[kindOf(mappingValues(n))]
	}
	return ""
}

// mappingValues are the values of a mapping.
func mappingValues(m *yaml.Node) []*yaml.Node {
	var out []*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, m.Content[i+1])
	}
	return out
}

// withoutReadValues is the value of a key of a service nothing takes without what docker compose reads there whatever it holds (measured, v5.5.1, #1958, #1970), or nil where
// there is none of it: the entries of a `build` that are of a shape docker compose does not refuse for the key (buildRefuses), the items of an `env_file` that are no mapping
// or a mapping with a word for its `path`, and in `ulimits` the limits and the items that are mappings, and the items that are whole numbers, but for a `soft` or `hard` that is a word.
func withoutReadValues(name string, v *yaml.Node) *yaml.Node {
	switch {
	case name == "build" && v.Kind == yaml.MappingNode:
		// A `build` that takes keys from a merge key is asked as it has been: a key written beside it stands over the merged one, and what it hides is read when the key is
		// taken out (`{<<: {args: [1]}, args: {a: b}}`).
		for i := 0; i+1 < len(v.Content); i += 2 {
			if unalias(v.Content[i]).Tag == "!!merge" {
				return nil
			}
		}
		kept := copyMapping(v)
		kept.Content = kept.Content[:0]
		for i := 0; i+1 < len(v.Content); i += 2 {
			key := unalias(v.Content[i])
			if key.Tag != "!!merge" && key.Value != "context" && readsShape(key.Value, v.Content[i+1]) {
				continue
			}
			kept.Content = append(kept.Content, v.Content[i], v.Content[i+1])
		}
		if len(kept.Content) != len(v.Content) {
			return kept
		}
	case name == "env_file" && v.Kind == yaml.SequenceNode:
		kept := copyMapping(v)
		kept.Content = kept.Content[:0]
		for _, item := range v.Content {
			if u := unalias(item); u.Kind != yaml.MappingNode || hasWordPath(u) {
				continue
			}
			kept.Content = append(kept.Content, item)
		}
		if len(kept.Content) != len(v.Content) {
			return kept
		}
	case name == "ulimits" && (v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode):
		kept := copyMapping(v)
		kept.Content = kept.Content[:0]
		step := 1
		if v.Kind == yaml.MappingNode {
			step = 2
		}
		for i := 0; i+step <= len(v.Content); i += step {
			// A merge key brings its limits from elsewhere: asked as it has been.
			if step == 2 && unalias(v.Content[i]).Tag == "!!merge" {
				kept.Content = append(kept.Content, v.Content[i:i+step]...)
				continue
			}
			entry := unalias(v.Content[i+step-1])
			switch {
			case entry.Kind == yaml.MappingNode && !hasMergeKey(entry):
				// What stays of the limit (of the mapping form or of an item of the list form) is a `soft` or `hard` that is a word that is no number: it is cast to a
				// number whether the service is taken or not, and refused (measured, v5.5.1: `{nofile: {soft: ~, hard: abc}}` and `[{soft: abc}]` are rc 1), where a quoted
				// number, a fraction, `true`, a list and a key that is neither are read.
				rest := copyMapping(entry)
				rest.Content = rest.Content[:0]
				for j := 0; j+1 < len(entry.Content); j += 2 {
					if k := unalias(entry.Content[j]).Value; k != "soft" && k != "hard" {
						continue
					}
					if w := unalias(entry.Content[j+1]); w.Kind == yaml.ScalarNode && w.Tag == "!!str" {
						if _, err := strconv.Atoi(w.Value); err != nil {
							rest.Content = append(rest.Content, entry.Content[j], entry.Content[j+1])
						}
					}
				}
				if len(rest.Content) > 0 {
					kept.Content = append(kept.Content, v.Content[i:i+step-1]...)
					kept.Content = append(kept.Content, rest)
				}
				continue
			case step == 1 && entry.Kind == yaml.ScalarNode && entry.Tag == "!!int":
				continue
			}
			kept.Content = append(kept.Content, v.Content[i:i+step]...)
		}
		changed := len(kept.Content) != len(v.Content)
		for i := 0; !changed && i < len(kept.Content); i++ {
			changed = kept.Content[i] != v.Content[i]
		}
		if changed {
			return kept
		}
	}
	return nil
}

// hasMergeKey says that a mapping has a merge key.
func hasMergeKey(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if unalias(m.Content[i]).Tag == "!!merge" {
			return true
		}
	}
	return false
}

// hasWordPath says that a mapping has a `path` that is a word (a blank too).
func hasWordPath(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if unalias(m.Content[i]).Value == "path" {
			w := unalias(m.Content[i+1])
			return w.Kind == yaml.ScalarNode && w.Tag == "!!str"
		}
	}
	return false
}

// readsShape says that a key of a `build` and its value are one docker compose reads in a service nothing takes.
func readsShape(key string, v *yaml.Node) bool {
	shape := valueShape(v)
	if shape == "" {
		return false
	}
	if !buildKeys[key] {
		return true
	}
	for _, refused := range buildRefuses[key] {
		if refused == shape {
			return false
		}
	}
	return true
}

// buildKeys are the keys the schema of `build` has.
var buildKeys = map[string]bool{
	"context": true, "dockerfile": true, "dockerfile_inline": true, "args": true, "ssh": true, "labels": true, "cache_from": true, "cache_to": true, "no_cache": true,
	"additional_contexts": true, "network": true, "provenance": true, "sbom": true, "pull": true, "target": true, "shm_size": true, "extra_hosts": true, "isolation": true,
	"privileged": true, "secrets": true, "tags": true, "ulimits": true, "platforms": true, "entitlements": true,
}
