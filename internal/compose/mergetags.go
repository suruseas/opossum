package compose

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// mergeTag is a key a compose file writes with `!reset` or `!override`:
// docker compose v5.5.0 reads `key: !reset …` as "this file takes the key
// away" (whatever is written after the tag) and `key: !override …` as "this
// file's value stands whole, not merged with an earlier file's" (measured,
// for -f files, include and extends). path is the key's place in the file,
// mapping keys from the root (`services`, `web`, `tmpfs`).
type mergeTag struct {
	path []string
}

// takeMergeTags reads the `!reset` and `!override` tags out of a document,
// the way docker compose v5.5.0 reads them, and returns the document without
// them and the keys they were on. A `!reset` key is taken out of the
// document; an `!override` key keeps its value, read as the plain value
// would be. What is left reads as any file does, and a caller merging the
// document over earlier ones takes those keys out of the earlier tree first
// (applyMergeTags), so an earlier value neither stays nor merges in.
//
// A tag on a list item: an item tagged `!reset` is dropped, and `!override`
// there means nothing (docker compose, measured: `ports: [!reset "8080:80"]`
// adds no port, `tmpfs: [!override /u]` adds `/u`); inside an item written
// as a mapping, a `!reset` key is taken out of the item (`ports: [{target:
// 80, published: !reset "8080"}]` publishes nothing) and an `!override` key
// is left tagged, as docker compose reads its value as a string there. Inside an `!override`
// value the tags are not read, as docker compose does not read them there
// (`environment: !override {A: !reset null}` sets A to the string `null`).
// These are left as written, as docker compose leaves them: a tag on the
// project's `name:`; any other tag; tags reached through an alias or a
// merge key (`<<: *base`) — docker compose reads those, and opossum does not
// yet. A mapping that writes a key twice is left whole, for the decode to
// refuse it as it refuses the same file without tags.
func takeMergeTags(one interpolated) (interpolated, []mergeTag, error) {
	if !bytes.Contains(one.raw, []byte("!reset")) && !bytes.Contains(one.raw, []byte("!override")) {
		return one, nil, nil
	}
	doc := one.node
	if doc == nil {
		var parsed yaml.Node
		if err := yaml.Unmarshal(one.raw, &parsed); err != nil {
			return one, nil, nil // the decode says so in its own words
		}
		doc = &parsed
	}
	root := documentRoot(doc)
	if root == nil {
		return one, nil, nil
	}
	var tags []mergeTag
	var walk func(n *yaml.Node, path []string, top bool)
	walk = func(n *yaml.Node, path []string, top bool) {
		switch n.Kind {
		case yaml.MappingNode:
			if writesAKeyTwice(n) {
				return
			}
			kept := n.Content[:0]
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if top && k.Value == "name" {
					kept = append(kept, k, v)
					continue
				}
				var here []string
				if path != nil {
					here = append(append([]string(nil), path...), k.Value)
				}
				switch v.Tag {
				case "!reset":
					if here != nil {
						tags = append(tags, mergeTag{path: here})
					}
					continue
				case "!override":
					// Inside a list item there is nothing to merge by, so the
					// tag hands nothing on; the value reads as a string either
					// way (docker compose refuses `mode: !override 0755` in a
					// long-form tmpfs item as one, measured).
					if here != nil {
						tags = append(tags, mergeTag{path: here})
					}
					untag(v)
				default:
					walk(v, here, false)
				}
				kept = append(kept, k, v)
			}
			n.Content = kept
		case yaml.SequenceNode:
			kept := n.Content[:0]
			for _, item := range n.Content {
				if item.Tag == "!reset" {
					continue
				}
				// An item has no key to merge by: its own keys' tags are read
				// in the item, and none is handed to the merge — unless the
				// item is tagged `!override`, inside which tags are not read.
				if item.Tag != "!override" {
					walk(item, nil, false)
				}
				kept = append(kept, item)
			}
			n.Content = kept
		}
	}
	walk(root, []string{}, true)
	return interpolated{node: doc, raw: one.raw}, tags, nil
}

// writesAKeyTwice reports whether a mapping writes one key twice.
func writesAKeyTwice(n *yaml.Node) bool {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Kind == yaml.ScalarNode {
			if seen[k.Value] {
				return true
			}
			seen[k.Value] = true
		}
	}
	return false
}

// untag gives an `!override` value the tag docker compose v5.5.0 reads it
// with: a mapping and a list as plain, and a scalar as a string, which each
// field then reads as it reads a quoted value (measured: `user: !override
// 1000` is the string "1000", `environment: {P: !override 08080}` keeps
// "08080", `retries: !override 3` is the count 3).
func untag(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		n.Tag = "!!map"
	case yaml.SequenceNode:
		n.Tag = "!!seq"
	default:
		n.Tag = "!!str"
	}
}

// applyMergeTags takes the keys a later document tagged `!reset` or
// `!override` out of the tree it is about to be merged over: the reset key
// then has no value, and the override key takes the later value as it is.
// With listsAsMaps, an earlier `networks:` or `depends_on:` written as a
// list is read as the mapping it merges as, so `db: !reset null` takes `db`
// out of `[db, db2]` — as docker compose v5.5.0 does across -f files and an
// include (measured). Extends passes false: see resolveExtends. Any other list is not reached: an earlier
// `environment: [A=1]` keeps `A=1` under a later `environment: {A: !reset
// null}` (measured).
func applyMergeTags(tree map[string]any, tags []mergeTag, listsAsMaps bool) {
	for _, t := range tags {
		parent := tree
		for _, seg := range t.path[:len(t.path)-1] {
			next := parent[seg]
			if list, ok := next.([]any); ok && listsAsMaps {
				var m map[string]any
				switch seg {
				case "networks":
					m, ok = networksAsMap(list)
				case "depends_on":
					m, ok = dependsOnAsMap(list)
				default:
					ok = false
				}
				if ok {
					parent[seg] = m
					next = m
				}
			}
			m, ok := next.(map[string]any)
			if !ok {
				parent = nil
				break
			}
			parent = m
		}
		if parent != nil {
			delete(parent, t.path[len(t.path)-1])
		}
	}
}

// serviceTagsKey holds, inside a service's tree, the keys the file tagged in
// that service, for `extends` to take out of the service it extends. It is
// put in once the file is checked and taken out once extends is read, so no
// check and no decode sees it.
const serviceTagsKey = "\x00merge-tags"

// markServiceTags puts each service's own tags into its tree for extends.
func markServiceTags(tree map[string]any, tags []mergeTag) {
	services, _ := tree["services"].(map[string]any)
	for _, t := range tags {
		if len(t.path) < 3 || t.path[0] != "services" {
			continue
		}
		svc, ok := services[t.path[1]].(map[string]any)
		if !ok {
			continue
		}
		own, _ := svc[serviceTagsKey].([]mergeTag)
		svc[serviceTagsKey] = append(own, mergeTag{path: t.path[2:]})
	}
}

// unmarkServiceTags takes what markServiceTags put in back out.
func unmarkServiceTags(tree map[string]any) {
	services, _ := tree["services"].(map[string]any)
	for _, s := range services {
		if svc, ok := s.(map[string]any); ok {
			delete(svc, serviceTagsKey)
		}
	}
}
