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

	// A dependency naming no service at all is usually caught while the file
	// is read (compose.validateDeps) — except when the service that names it
	// carries `profiles:` and was not yet known to be read, which is deferred
	// to here (#1094): it is this walk's business now that reading(name) says
	// so, before cycleAmong and order below, which both still pass over one
	// outside what is being read (that half of the file is not this walk's
	// business at all, undefined dependency included).
	for _, name := range names {
		if !reading(name) {
			continue
		}
		for _, dep := range p.Services[name].DependsOn.Names() {
			if p.Services[dep] == nil {
				return nil, fmt.Errorf("service %q depends on unknown service %q — define %q under services: or remove it from depends_on", name, dep, dep)
			}
		}
	}
	if err := p.cycleAmong(names, reading); err != nil {
		return nil, err
	}
	return p.order(names), nil
}

// StartupOrderTolerant is StartupOrderReading without the cycle refusal:
// every service still gets a place, each after what it depends on where that
// is possible, but a dependency cycle among the services read does not stop
// the walk — it is broken by not placing a service a second time once it is
// already being placed (the same fallback order() always falls back to;
// cycleAmong exists only to turn that into an error, and this skips it). A
// dependency naming no service at all is still refused, exactly as
// StartupOrderReading refuses it — that is a different problem a cycle-shaped
// fallback cannot paper over, and is not what #1093 is about.
//
// For commands that only act on containers already there — they do not start
// anything in dependency order, so there is nothing for a cycle to actually
// break — refusing a whole project over a cycle in the file would leave it
// stuck running with no way back down through opossum (#1093). docker
// compose does not refuse for its own commands of that kind either, given
// `-p` and left to discover the file (measured on v5.5.1: `ps`, `images`,
// `logs`, `stop`, `kill`, `down`, `restart`, `stats` all go through; `up`,
// `run`, `pull`, `build` and `config --services` still refuse, and so does
// StartupOrderReading above, which serves them). opossum's own `import`,
// `start`, `volumes` and `watch` stay refused too, for the same reason as
// `pull` and `build` — see docs/compatibility.md.
func (p *Project) StartupOrderTolerant(read []string) ([]string, error) {
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

	for _, name := range names {
		if !reading(name) {
			continue
		}
		for _, dep := range p.Services[name].DependsOn.Names() {
			if p.Services[dep] == nil {
				return nil, fmt.Errorf("service %q depends on unknown service %q — define %q under services: or remove it from depends_on", name, dep, dep)
			}
		}
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
