package compose

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

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

// innerNode is a place inside an anchored mapping: a key written with a `!reset` or `!override`
// (tagged), a key whose value is a mapping with some inside (kids), a key whose value is an alias to an anchored mapping (target), or a
// merge key to one (merge, with target).
type innerNode struct {
	key    string
	tagged bool
	merge  bool
	target *yaml.Node
	kids   []innerNode
}

// maxInnerTags is how many places a file may look at, through the anchors it holds and merges, for tags written inside them.
const maxInnerTags = 20000

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
// project's `name:`; any other tag; tags inside an anchored list reached through an
// alias. The tags inside an anchored mapping are read where it is used, by a merge
// key to that one anchor (`x-c: &c {tmpfs: !reset []}` and `web: {<<: *c}`; not a list
// of anchors, which docker compose leaves plain) or as the value of a key
// (`web: *c`), on the keys of the mapping that uses it. An alias to an anchor that is itself tagged
// (`x-z: &z !reset [/z]` and `tmpfs: *z`) is read at each place it is used, as
// docker compose reads it: a `!reset` alias takes the key (or the list item)
// out, an `!override` alias stands whole. The anchor is not copied, so many
// uses of a large or deeply nested anchor cost what one does. A mapping that
// writes a key twice is left whole, for the decode to refuse it as it refuses
// the same file without tags.
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
	// The tags the anchors were written with, before the walk takes them off: an alias reads the
	// tag of its anchor where it is used, however many times, and the walk may have been through
	// the anchor's own place already.
	anchored := map[*yaml.Node]string{}
	var tags []mergeTag
	// tagOf is the tag a value is read with: its own, or its anchor's where it is an alias.
	tagOf := func(v *yaml.Node) string {
		if v.Kind == yaml.AliasNode && v.Alias != nil {
			return anchored[v.Alias]
		}
		return v.Tag
	}
	// The tags written inside each anchored mapping that is not tagged itself (`x-b: &b {ports: !reset []}`), taken before the walk
	// takes them off the anchor: docker compose reads them at each place the anchor is used, by a merge key (`<<: *b`, one anchor,
	// not a list of them) or as the value of a key (`web: *b`), as on the keys of the mapping that uses it — also where that
	// mapping writes the key itself. What is kept is the shape of the anchor (where its tags are, which anchors it merges or
	// holds), not the list of paths: a lattice of anchors holds one path per way through it, and is read only where it is used.
	local := map[*yaml.Node][]innerNode{}
	var build func(m *yaml.Node) []innerNode
	build = func(m *yaml.Node) []innerNode {
		if m.Kind != yaml.MappingNode || writesAKeyTwice(m) {
			return nil
		}
		// An anchored mapping inside another is built once, not again for each one that holds it (a chain of D of them is D builds, not D*D).
		if m.Anchor != "" && anchored[m] == "" {
			if got, ok := local[m]; ok {
				return got
			}
		}
		var out []innerNode
		for i := 0; i+1 < len(m.Content); i += 2 {
			k, v := m.Content[i], m.Content[i+1]
			if k.Value == "<<" {
				if t := mergedMapping(v); t != nil && anchored[t] == "" {
					out = append(out, innerNode{merge: true, target: t})
				}
				continue
			}
			switch tag := tagOf(v); {
			case tag == "!reset" || tag == "!override":
				out = append(out, innerNode{key: k.Value, tagged: true})
			case v.Kind == yaml.MappingNode && v.Tag != "!override":
				if kids := build(v); len(kids) > 0 {
					out = append(out, innerNode{key: k.Value, kids: kids})
				}
			case mergedMapping(v) != nil && anchored[v.Alias] == "":
				out = append(out, innerNode{key: k.Value, target: v.Alias})
			}
		}
		if m.Anchor != "" && anchored[m] == "" {
			local[m] = out
		}
		return out
	}
	var collect func(n *yaml.Node)
	collect = func(n *yaml.Node) {
		if n.Anchor != "" && (n.Tag == "!reset" || n.Tag == "!override") {
			anchored[n] = n.Tag
		}
		for _, c := range n.Content {
			collect(c)
		}
	}
	collect(root)
	var precompute func(n *yaml.Node)
	precompute = func(n *yaml.Node) {
		if n.Anchor != "" && n.Kind == yaml.MappingNode && anchored[n] == "" {
			build(n)
		}
		for _, c := range n.Content {
			precompute(c)
		}
	}
	precompute(root)
	// reaches is whether an anchor holds a tag, itself or through the anchors it merges or holds (memoised, one visit each).
	reaches := map[*yaml.Node]bool{}
	var reachesNodes func(nodes []innerNode, seen map[*yaml.Node]bool) bool
	var reachesAnchor func(t *yaml.Node, seen map[*yaml.Node]bool) bool
	reachesAnchor = func(t *yaml.Node, seen map[*yaml.Node]bool) bool {
		if got, ok := reaches[t]; ok {
			return got
		}
		if seen[t] {
			return false
		}
		seen[t] = true
		got := reachesNodes(local[t], seen)
		reaches[t] = got
		return got
	}
	reachesNodes = func(nodes []innerNode, seen map[*yaml.Node]bool) bool {
		for _, n := range nodes {
			if n.tagged || (n.target != nil && reachesAnchor(n.target, seen)) || reachesNodes(n.kids, seen) {
				return true
			}
		}
		return false
	}
	var tooMany error
	budget := maxInnerTags
	// innerAt puts the tags of the anchor a value stands for under the place that uses it. The places looked at, over the whole file, are
	// counted: that many ways through anchors is not a file anyone wrote, and each use of a lattice would otherwise cost every way through it.
	innerAt := func(v *yaml.Node, at []string) {
		if at == nil || tooMany != nil {
			return
		}
		// A tag under a key the model does not have (an `x-` extension field, which is where anchors are written, at the top or
		// in a service) stands on nothing.
		for _, seg := range at {
			if strings.HasPrefix(seg, "x-") {
				return
			}
		}
		t := mergedMapping(v)
		if t == nil || anchored[t] != "" {
			return
		}
		active := map[*yaml.Node]bool{t: true}
		var expand func(nodes []innerNode, prefix []string)
		expand = func(nodes []innerNode, prefix []string) {
			for _, n := range nodes {
				// Only what leads to a tag is looked at: a lattice with none in it costs nothing, wherever it sits.
				if n.target != nil && !reachesAnchor(n.target, map[*yaml.Node]bool{}) {
					continue
				}
				if budget--; budget < 0 {
					if tooMany == nil {
						tooMany = fmt.Errorf("the anchors this file reads a `!reset` or `!override` through hold anchors, twice over, too many times (more than %d places to look): write the file with fewer", maxInnerTags)
					}
					return
				}
				here := append(append([]string(nil), prefix...), n.key)
				if n.target != nil {
					// An anchor that holds itself (`x-a: &a {p: *a}`) is not a file that was read before; docker compose says `cycle detected`.
					if active[n.target] {
						tooMany = errors.New("an anchor holds itself (cycle detected)")
						return
					}
					active[n.target] = true
					if n.merge {
						expand(local[n.target], prefix)
					} else {
						expand(local[n.target], here)
					}
					delete(active, n.target)
				} else if n.tagged {
					tags = append(tags, mergeTag{path: here})
				} else {
					expand(n.kids, here)
				}
				if tooMany != nil {
					return
				}
			}
		}
		expand(local[t], at)
	}
	// stand takes the tag off an `!override` value so that it reads as plain, where it stands: an alias
	// is left as it is — a copy of what it stands for is no alias to the decoder, which counts the aliases
	// it opens and refuses a file that opens too many, and would decode the whole of it at each use (the
	// tag the anchor was written with is in anchored, for every other use).
	stand := func(v *yaml.Node) {
		if v.Kind != yaml.AliasNode {
			untag(v)
		}
	}
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
				tag := tagOf(v)
				if k.Value == "<<" {
					// A merge key to a value that is itself tagged — an anchor, or the value written there —
					// docker compose reads the tag as on the mapping that holds the merge key (measured):
					// `!override` (of a mapping) makes that mapping stand whole over the files before it, with
					// the value's keys in it; `!reset` does too with none of them, and whatever it holds (a list
					// or a scalar brings nothing as well; in a list item there is nothing to stand over). A
					// merge of a list of anchors, of an anchor that is not tagged, and one at the top of the
					// file, is not read here. What the anchor holds is not walked: no tag in it is read, and it
					// is shared with every other use.
					if path == nil || len(path) > 0 {
						switch {
						case tag == "!reset":
							if path != nil {
								tags = append(tags, mergeTag{path: append([]string(nil), path...)})
							}
							continue
						case tag == "!override" && (mergedMapping(v) != nil || v.Kind == yaml.MappingNode):
							if path != nil {
								tags = append(tags, mergeTag{path: append([]string(nil), path...)})
							}
							stand(v)
							kept = append(kept, k, v)
							continue
						}
					}
					tag = v.Tag
					// One anchor (not a list of them) that is not itself tagged: the tags written inside it are read on the keys of
					// this mapping, whether or not it writes the key itself (measured, v5.5.1).
					if len(path) > 0 {
						innerAt(v, path)
					}
				}
				switch tag {
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
					stand(v)
				default:
					if k.Value != "<<" {
						innerAt(v, here)
					}
					walk(v, here, false)
				}
				kept = append(kept, k, v)
			}
			n.Content = kept
		case yaml.SequenceNode:
			kept := n.Content[:0]
			for _, item := range n.Content {
				// An alias is not walked (nothing here takes it into what it stands for: it is shared with
				// every other use), and `!override` on a list item means nothing: the item stands.
				if tagOf(item) == "!reset" {
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
	if tooMany != nil {
		return one, nil, tooMany
	}
	return interpolated{node: doc, raw: one.raw, written: one.written}, tags, nil
}

// mergedMapping is the mapping an alias stands for, nil where it is not an alias to one.
func mergedMapping(v *yaml.Node) *yaml.Node {
	if v.Kind == yaml.AliasNode && v.Alias != nil && v.Alias.Kind == yaml.MappingNode {
		return v.Alias
	}
	return nil
}

// withoutItemTags is the tags without those on an item of a section the parent merges with the included file's (a service of `services`,
// a network, volume, config or secret by its name): docker compose reads a `!reset` or `!override` on such an item over an earlier `-f`
// file, but not over an `include` — there the item is merged with the included one as it is written, and a tag on a key under it is read
// (measured, v5.5.1, for `networks`, `volumes`, `configs`, `secrets` as for `services`).
func withoutItemTags(tags []mergeTag) []mergeTag {
	var kept []mergeTag
	for _, t := range tags {
		if len(t.path) == 2 {
			switch t.path[0] {
			case "services", "networks", "volumes", "configs", "secrets":
				continue
			}
		}
		kept = append(kept, t)
	}
	return kept
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
	applyMergeTagsOver(tree, tags, listsAsMaps, nil)
}

// applyMergeTagsOver is applyMergeTags that leaves a name of a mapping a tag stands on where two earlier files wrote the mapping: docker
// compose (measured, v5.5.1) holds a list-or-mapping key (`environment`, `labels`, `build.args`, `extra_hosts`, `build.extra_hosts`; docker compose holds
// more keys the same way, which `config` does not show here and are not measured) as a list once a second file has merged into it, and a `!reset` or `!override` on one of its names, written in a third
// file, does not reach it, nor does a tag reach the name the second file wrote. mixed holds the paths (`services.web.environment`) that two files have written.
func applyMergeTagsOver(tree map[string]any, tags []mergeTag, listsAsMaps bool, mixed map[string]mixedKey) {
	for _, t := range tags {
		if _, ok := mixed[strings.Join(t.path[:len(t.path)-1], ".")]; ok {
			continue
		}
		parent := tree
		for _, seg := range t.path[:len(t.path)-1] {
			next := parent[seg]
			if list, ok := next.([]any); ok && listsAsMaps {
				var m map[string]any
				switch seg {
				case "networks":
					m, ok = networksAsMap(list)
				case "depends_on":
					m, ok = dependsOnAsMap(list, true)
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

// listOrMappingKeys are the keys of a service that docker compose reads as a list or a mapping and holds as a list once two files have
// written them: by the path under the service.
var listOrMappingKeys = [][]string{
	{"environment"}, {"labels"}, {"build", "args"}, {"extra_hosts"}, {"build", "extra_hosts"},
}

// mixedKey is a list-or-mapping key of a service that two of the files merged so far wrote.
type mixedKey struct {
	service string
	path    []string
}

func (k mixedKey) name() string { return "services." + k.service + "." + strings.Join(k.path, ".") }

// dropGone forgets the keys in mixed that a `!reset` or `!override` has taken out of merged (or the whole service): a file written
// again after that is the first to write it, as docker compose reads it.
func dropGone(merged map[string]any, mixed map[string]mixedKey) {
	for name, k := range mixed {
		if !has(merged, append([]string{"services", k.service}, k.path...)) {
			delete(mixed, name)
		}
	}
}

// markMixed notes in mixed the list-or-mapping keys that both the services merged so far and the file about to be merged in write
// (a key written as null counts as written, as docker compose counts it).
func markMixed(merged, next map[string]any, mixed map[string]mixedKey) {
	before, _ := merged["services"].(map[string]any)
	after, _ := next["services"].(map[string]any)
	for name, svc := range after {
		bs, _ := before[name].(map[string]any)
		ns, _ := svc.(map[string]any)
		if bs == nil || ns == nil {
			continue
		}
		for _, path := range listOrMappingKeys {
			if has(bs, path) && has(ns, path) {
				k := mixedKey{name, path}
				mixed[k.name()] = k
			}
		}
	}
}

// has reports whether a path of keys leads to a key in a mapping, whatever its value (null included).
func has(m map[string]any, path []string) bool {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		v, ok := mm[k]
		if !ok {
			return false
		}
		cur = v
	}
	return true
}
