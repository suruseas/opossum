package compose

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// selfMerging finds a block that a merge key brings in and that holds the block that brings it in (`x-a: &a {K: v, <<: *a}`, or a list that holds a block that
// merges it: `x-l: &l [{K: v, <<: *l}]`): docker compose refuses the file wherever the block is used or not, and every walk of the tree that follows a merge key
// goes round for ever in it. It is asked once, before any of them, over the whole tree, following the merge keys only: an alias is not followed by the walk of
// the tree (its block is where it is written, and is walked there), only by a merge key, and a block that a merge key brings in is walked once, so a lattice of
// merges is no more than the number of its blocks. It returns the mapping (or the list) that is brought in by itself, or nil (#1900, #1902).
//
// A block that holds the block that merges it by being a part of it (`x-a: &a {name: n, x: {<<: *a}}`) is not found here: it holds it by what is written in it,
// and not by a merge key, and the walks that go round in that are not asked about (#1909).
//
// With cut, the merge key (or the item of a list) that brings the block in by itself is taken out of the tree where it is found, and the first block found is
// returned: the commands that take a project down read a file they refuse otherwise, as they read one whose alias holds its own block (#1475), and a tree
// with the cycle taken out is one the walks go through.
func selfMerging(root *yaml.Node, cut bool) *yaml.Node {
	const (
		onPath = 1
		done   = 2
	)
	color := map[*yaml.Node]int{}
	var first *yaml.Node
	var viaMerge func(n *yaml.Node) *yaml.Node
	viaMerge = func(n *yaml.Node) *yaml.Node {
		n = unalias(n)
		if n == nil || (n.Kind != yaml.MappingNode && n.Kind != yaml.SequenceNode) {
			return nil
		}
		switch color[n] {
		case onPath:
			return n
		case done:
			return nil
		}
		color[n] = onPath
		whole := true
		defer func() {
			if whole {
				color[n] = done
			} else {
				delete(color, n) // gone through only as far as the block found: what is left of it is walked when it is reached again
			}
		}()
		// What is found is the block on the way that the way comes back to. With cut, it is taken out where that block starts it (its merge key, or its item
		// for a list), and the walk of the block goes on; a block in between hands it up, and keeps what it holds, which is more than the cycle.
		if n.Kind == yaml.SequenceNode {
			for i := 0; i < len(n.Content); i++ {
				if found := viaMerge(n.Content[i]); found != nil {
					if !cut {
						return found
					}
					if found != n {
						whole = false
						return found
					}
					if first == nil {
						first = found
					}
					n.Content = append(n.Content[:i], n.Content[i+1:]...)
					i--
				}
			}
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Tag == "!!merge" {
				if found := viaMerge(n.Content[i+1]); found != nil {
					if !cut {
						return found
					}
					if found != n {
						whole = false
						return found
					}
					if first == nil {
						first = found
					}
					// What the block merges is written there (`<<: [*c, *a]`, `<<: {name: p, <<: *a}`): only what brings the block in goes, the item of the list or
					// the merge key of the mapping, wherever it is in what is written there, and the rest stays (a `name` that the mapping holds is the
					// name of what the file starts). A list or a mapping that an alias stands for is shared with whatever else uses it, and it is the key that goes.
					if value := n.Content[i+1]; (value.Kind == yaml.SequenceNode || value.Kind == yaml.MappingNode) && cutBlockIn(value, found) {
						i -= 2
						continue
					}
					n.Content = append(n.Content[:i], n.Content[i+2:]...)
					i -= 2
				}
			}
		}
		return nil
	}
	var walk func(n *yaml.Node) *yaml.Node
	walk = func(n *yaml.Node) *yaml.Node {
		if n == nil {
			return nil
		}
		if n.Kind == yaml.MappingNode {
			if found := viaMerge(n); found != nil {
				return found
			}
		}
		for _, c := range n.Content {
			if found := walk(c); found != nil {
				return found
			}
		}
		return nil
	}
	if found := walk(root); found != nil {
		return found
	}
	return first
}

// cutBlockIn takes the way to the block out of what is written in a merge key: the items of a list that are the block (or an alias to it), the merge keys of a
// mapping that bring it in, and the same in a list or a mapping written in either, one inside another. It reports whether there was one. What an alias stands for
// is not looked into: it is another place's, and is cut where it is written.
func cutBlockIn(node, block *yaml.Node) bool {
	cut := false
	switch node.Kind {
	case yaml.SequenceNode:
		kept := node.Content[:0]
		for _, item := range node.Content {
			if unalias(item) == block {
				cut = true
				continue
			}
			if item.Kind != yaml.AliasNode && cutBlockIn(item, block) {
				cut = true
			}
			kept = append(kept, item)
		}
		node.Content = kept
	case yaml.MappingNode:
		kept := node.Content[:0]
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Tag == "!!merge" {
				if value := node.Content[i+1]; unalias(value) == block {
					cut = true
					continue
				} else if value.Kind != yaml.AliasNode && cutBlockIn(value, block) {
					cut = true
				}
			}
			kept = append(kept, node.Content[i], node.Content[i+1])
		}
		node.Content = kept
	}
	return cut
}

// errSelfMerging is the refusal of the block selfMerging found, in the words the walks that refuse it by themselves use. It is asked for where a file is
// validated, before the walks that would go round in it; the decode that follows refuses the cycles of aliases itself, and a list is not put where its own block
// is (spreadSequenceMerges), so the decode is not given one with no alias in it.
func errSelfMerging(n *yaml.Node) error {
	return fmt.Errorf("a merge key brings in the block that holds it (line %d) — a block cannot merge itself", n.Line)
}
