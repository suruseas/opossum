package compose

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// hostGateway is the special value of an `extra_hosts` entry: docker adds the
// name with the address of the host. Apple `container` has no way to add a
// host entry, so a service that writes one is told what to use instead.
const hostGateway = "host-gateway"

// hostGatewayNames is the names an `extra_hosts` value maps to host-gateway,
// sorted and once each. An entry is `name=address` or `name:address`, with `=`
// taking precedence (docker compose, v5.6.1, reads `a:b=host-gateway` as the
// name `a:b`), or a mapping whose value is an address or a list of them. The
// word is compared exactly, as docker compose carries it.
func hostGatewayNames(n *yaml.Node) []string {
	var names []string
	n = unalias(n)
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if name, addr, ok := cutHostEntry(unalias(item)); ok && addr == hostGateway {
				names = append(names, name)
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := unalias(n.Content[i]), unalias(n.Content[i+1])
			if key.Tag == "!!merge" {
				names = append(names, hostGatewayNames(value)...)
				continue
			}
			if isHostGatewayValue(value) {
				names = append(names, key.Value)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// cutHostEntry splits a list entry into its name and address.
func cutHostEntry(n *yaml.Node) (name, addr string, ok bool) {
	if n.Kind != yaml.ScalarNode {
		return "", "", false
	}
	if name, addr, ok = strings.Cut(n.Value, "="); ok {
		return name, addr, true
	}
	return strings.Cut(n.Value, ":")
}

// isHostGatewayValue reports whether a mapping's value is host-gateway, or a
// list of addresses with host-gateway among them.
func isHostGatewayValue(n *yaml.Node) bool {
	if n.Kind == yaml.ScalarNode {
		return n.Value == hostGateway
	}
	if n.Kind != yaml.SequenceNode {
		return false
	}
	return slices.ContainsFunc(n.Content, func(item *yaml.Node) bool {
		item = unalias(item)
		return item.Kind == yaml.ScalarNode && item.Value == hostGateway
	})
}
