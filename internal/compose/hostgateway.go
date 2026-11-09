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
// sorted and once each, read the way docker compose (v5.6.1) reads these forms.
// A list entry is `name=addresses` or `name:addresses`, with `=` looked for
// first; the addresses are separated by commas. A mapping's value is one address
// or a list of them, and there a comma does not separate. An address with a
// bracket at both ends loses that one pair, in either form. Merge keys follow
// YAML, which the decoder applies: a key the mapping writes wins over a merged
// one, and the first merged mapping wins over a later one. The word is compared
// exactly.
func hostGatewayNames(n *yaml.Node) []string {
	var names []string
	n = unalias(n)
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if name, addrs, ok := cutHostEntry(unalias(item)); ok && listNamesHostGateway(addrs) {
				names = append(names, name)
			}
		}
	case yaml.MappingNode:
		var hosts map[string]any
		if n.Decode(&hosts) != nil {
			return nil
		}
		for name, value := range hosts {
			if isHostGatewayValue(value) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// cutHostEntry splits a list entry into its name and addresses.
func cutHostEntry(n *yaml.Node) (name, addrs string, ok bool) {
	if n.Kind != yaml.ScalarNode {
		return "", "", false
	}
	if name, addrs, ok = strings.Cut(n.Value, "="); ok {
		return name, addrs, true
	}
	return strings.Cut(n.Value, ":")
}

// listNamesHostGateway reports whether host-gateway is among a list entry's
// comma-separated addresses.
func listNamesHostGateway(addrs string) bool {
	for _, addr := range strings.Split(addrs, ",") {
		if isHostGatewayAddress(addr) {
			return true
		}
	}
	return false
}

// isHostGatewayValue reports whether a mapping's value is host-gateway, or a
// list of addresses with host-gateway among them.
func isHostGatewayValue(value any) bool {
	switch v := value.(type) {
	case string:
		return isHostGatewayAddress(v)
	case []any:
		return slices.ContainsFunc(v, func(item any) bool {
			addr, ok := item.(string)
			return ok && isHostGatewayAddress(addr)
		})
	}
	return false
}

// isHostGatewayAddress compares one address, without the pair of brackets it may
// have at both ends.
func isHostGatewayAddress(addr string) bool {
	if len(addr) >= 2 && addr[0] == '[' && addr[len(addr)-1] == ']' {
		addr = addr[1 : len(addr)-1]
	}
	return addr == hostGateway
}
