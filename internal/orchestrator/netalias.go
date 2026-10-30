package orchestrator

import (
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// networkAliases is the aliases `up` passes for a service, by the runtime's
// name of each network: `cache` is passed as `cache.<project>.<domain>`, the
// form of the services' own names, so a peer in the project reaches it as
// `cache` through the search domain every service is given, as it reaches a
// service by its name.
//
// Apple container 1.6.1 refuses a run whose alias has one label (unless the
// machine has a default DNS domain), and answers only for names under the DNS
// domain, so an alias is passed under it, or not at all. Its names are not kept
// apart by network: an alias given on one network answers on every network
// (measured), so each is passed once, on the first network that writes it. It
// refuses a run whose alias is the name of a container that exists, its own
// included, or one another container has taken, and a run that gives one alias
// twice (all measured, testdata/error-wordings/network-aliases-161.txt). Each
// of those would stop a service that started before aliases were passed, so
// they are left out, and so is an alias with a dot, which a peer reaches by its
// short name only where its resolver searches a name with a dot (glibc does,
// musl does not; measured), and a 33rd alias on a network (its source takes 32).
// What is left out is said, once for each alias, with why. A name a container
// outside the file holds — one an earlier `up` left, running or stopped, or
// another project's of the same name — is not known here: the run that meets
// it is made again without aliases (runDroppingHeldAliases).
//
// An alias that is a service's name is left out (that name reaches a service
// already); one that two services write goes to the first of them by name.
func (o *Orchestrator) networkAliases(name string, svc *compose.Service) map[string][]string {
	if len(svc.NetworkAliases) == 0 {
		return nil
	}
	var written []string // in network order, as the runtime would be given them
	byNet := map[string][]string{}
	for _, key := range o.networkKeys(svc) {
		for _, a := range svc.NetworkAliases[key] {
			written = append(written, a)
			byNet[key] = append(byNet[key], a)
		}
	}
	if len(written) == 0 {
		return nil
	}
	skip := func(alias, why string) {
		o.warnf(codeIgnoredField, "service %q: network alias %q is not passed: %s\n", name, alias, why)
	}
	search := o.searchDomain()
	if search == "" {
		for _, a := range written {
			skip(a, "there is no DNS domain to give it under (`--dns-domain` is empty)")
		}
		return nil
	}
	if ok, version := o.runtimeTakesAliases(); !ok {
		why := "Apple container " + version + " does not take network aliases; 1.6.0 and later do"
		if !cliVersion.MatchString(version) {
			why = "the runtime did not say its version (`container system status --format json`), and network aliases need 1.6.0 or later"
		}
		for _, a := range written {
			skip(a, why)
		}
		return nil
	}
	services := map[string]bool{}
	for n := range o.Project.Services {
		services[strings.ToLower(n)] = true
	}
	out := map[string][]string{}
	given := map[string]bool{}
	for _, key := range o.networkKeys(svc) {
		for _, a := range byNet[key] {
			low := strings.ToLower(a)
			switch {
			case strings.Contains(a, "."):
				skip(a, "a peer would reach a name with a dot by its short name on some images and not on others; write one without a dot")
				continue
			case !compose.IsAliasLabel(a):
				skip(a, "an alias is passed as one DNS label: letters, digits, `-` and `_`, at most 63 characters, starting and ending with a letter or digit")
				continue
			case services[low]:
				skip(a, "it is the name of a service in this project, which reaches that service already")
				continue
			case given[low]:
				continue // given on an earlier network, which answers on this one too
			}
			if owner := o.aliasOwner(low); owner != name {
				skip(a, "service \""+owner+"\" takes it, as the first service by name that writes it")
				continue
			}
			rn := o.resolveNetwork(key).name
			if len(out[rn]) == maxAliasesPerNetwork {
				skip(a, "the runtime takes at most 32 aliases on a network")
				continue
			}
			given[low] = true
			out[rn] = append(out[rn], a+"."+search)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// aliasOwner is the first service by name that writes the alias (compared
// without regard to case, as DNS compares names), on any network.
func (o *Orchestrator) aliasOwner(low string) string {
	names := make([]string, 0, len(o.Project.Services))
	for n := range o.Project.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := o.Project.Services[n]
		if svc == nil || svc.NetworkMode == compose.NetworkModeNone {
			continue
		}
		for _, list := range svc.NetworkAliases {
			for _, a := range list {
				if strings.ToLower(a) == low {
					return n
				}
			}
		}
	}
	return ""
}

// cliVersion is the start of a version the runtime said: `1.6.1`.
var cliVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+`)

// maxAliasesPerNetwork is the most aliases Apple container 1.6.1 takes on one
// network (its AliasPolicy; read in the source, not measured).
const maxAliasesPerNetwork = 32

// runtimeTakesAliases asks the runtime once whether it takes network aliases,
// and says the client version it read ("" where it read none).
func (o *Orchestrator) runtimeTakesAliases() (bool, string) {
	if o.aliasesTaken == nil {
		v := o.rt.SystemStatus().ClientVersion
		ok := runtime.AcceptsNetworkAliases(v)
		o.aliasesTaken, o.aliasesVersion = &ok, v
	}
	return *o.aliasesTaken, o.aliasesVersion
}

// runDroppingHeldAliases runs a service's container, and when the runtime
// refuses the run because another container already holds one of its aliases —
// one an earlier `up` left, running or stopped, or another project's of the
// same name, which 1.6.1 counts whatever their state (measured) — runs it again
// without its aliases and says so. The refusal makes no container (measured),
// and the label that fingerprints the run is kept, so a later `up` leaves the
// container as it is until it is recreated.
func (o *Orchestrator) runDroppingHeldAliases(name string, opts runtime.RunOptions) error {
	err := o.rt.Run(opts)
	if err == nil || len(opts.NetworkAliases) == 0 {
		return err
	}
	var re *runtime.RunError
	if !errors.As(err, &re) || !aliasesHeld(re.Stderr) {
		return err
	}
	o.warnf(codeIgnoredField, "service %q: its network aliases are not passed: another container already holds one of them — "+
		"remove it (`opossum up --remove-orphans` for one this project no longer has, or `opossum down`), then `opossum up --force-recreate %s`\n", name, name)
	opts.NetworkAliases = nil
	return o.rt.Run(opts)
}

// aliasesHeld reports whether the runtime refused a run because another
// container holds one of its aliases: 1.6.1's own line, which begins the line
// (testdata/error-wordings/network-aliases-161.txt).
func aliasesHeld(stderr string) bool {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "Error: alias(es) already exist: [") {
			return true
		}
	}
	return false
}

// hostnameHeldAsAlias reports whether the runtime refused a run because another
// container holds the container's own name as a network alias: 1.6.1's own
// line, which begins the line (testdata/error-wordings/network-aliases-161.txt).
func hostnameHeldAsAlias(stderr string) bool {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "Error: hostname(s) already exist: [") {
			return true
		}
	}
	return false
}
