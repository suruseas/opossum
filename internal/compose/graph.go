package compose

import (
	"fmt"
	"sort"
)

// StartupOrder returns service names ordered so that every service appears after
// all of its depends_on targets. It errors on dependency cycles. Ordering is
// deterministic: independent services keep alphabetical order.
func (p *Project) StartupOrder() ([]string, error) {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(p.Services))
	var order []string

	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var visit func(name string, stack []string) error
	visit = func(name string, stack []string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("dependency cycle detected: %v -> %s", stack, name)
		}
		state[name] = visiting

		deps := p.Services[name].DependsOn.Names()
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep, append(stack, name)); err != nil {
				return err
			}
		}
		state[name] = done
		order = append(order, name)
		return nil
	}

	for _, name := range names {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}
