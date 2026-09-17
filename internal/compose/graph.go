package compose

import (
	"fmt"
	"sort"
)

// StartupOrder returns service names ordered so that every service appears after
// all of its depends_on targets. It errors on dependency cycles. Ordering is
// deterministic: independent services keep alphabetical order.
func (p *Project) StartupOrder() ([]string, error) { return p.StartupOrderReading(nil) }

// StartupOrderReading is StartupOrder over a project the caller reads only
// part of. Every service gets a place and every depends_on is followed, so
// each still lands after what it needs — what the given set changes is which
// cycles are an error: one whose every service the caller reads is refused as
// before, and one that runs through a service it does not read — behind a
// profile it has not turned on, say — is not the caller's problem, and the
// walk goes through it instead. A nil set reads the whole project, so every
// cycle in it is an error.
//
// The two questions are asked separately, because the answer to the first is
// not a property of any one walk: a cycle among the services being read can
// hide under a walk that reached it through a service that is not, and be
// gone by the time that walk comes back. So the cycles are looked for over
// the part of the project being read, by itself, and the order is built after
// that over all of it.
func (p *Project) StartupOrderReading(read []string) ([]string, error) {
	in := map[string]bool{}
	for _, name := range read {
		in[name] = true
	}
	reading := func(name string) bool { return read == nil || in[name] }
	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	if err := p.cycleAmong(names, reading); err != nil {
		return nil, err
	}
	return p.order(names), nil
}

const (
	unvisited = 0
	visiting  = 1
	done      = 2
)

// cycleAmong reports a dependency cycle among the services being read, in the
// words this package has always used for one: the services walked through to
// reach it, and the one it comes back to.
func (p *Project) cycleAmong(names []string, reading func(string) bool) error {
	state := make(map[string]int, len(p.Services))
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
			// Only the part being read: a dependency on a service outside it
			// is not this walk's business, and neither is anything beyond.
			if p.Services[dep] == nil || !reading(dep) {
				continue
			}
			if err := visit(dep, append(stack, name)); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	for _, name := range names {
		if !reading(name) {
			continue
		}
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// order places every service after the ones it depends on, following every
// depends_on there is. A cycle left in what is not being read is broken at
// the edge the walk comes back on: it has been ruled out as an error by then,
// and every other dependency still decides where its service goes.
func (p *Project) order(names []string) []string {
	state := make(map[string]int, len(p.Services))
	order := make([]string, 0, len(p.Services))
	var visit func(name string)
	visit = func(name string) {
		if state[name] != unvisited {
			return // done, or an ancestor: the edge back to it is not followed
		}
		state[name] = visiting
		deps := p.Services[name].DependsOn.Names()
		sort.Strings(deps)
		for _, dep := range deps {
			if p.Services[dep] == nil {
				// A depends_on the file does not define. Reading the file
				// refuses that, whatever the profiles say, so this is reached
				// only by a caller that assembled a project itself — and
				// there is nothing here to place either way.
				continue
			}
			visit(dep)
		}
		state[name] = done
		order = append(order, name)
	}
	for _, name := range names {
		visit(name)
	}
	return order
}
