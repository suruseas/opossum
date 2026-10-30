package orchestrator

import (
	"regexp"
	"sort"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// aliasLabel is the one DNS label an alias is passed as: letters, digits, `-`
// and `_` (container 1.6.1 takes `_` and capitals, measured), starting and
// ending with a letter or digit, and at most 63 characters (1.6.1 refuses a
// longer label: `label too long`). Anything else would be refused by the
// runtime or cut the `--network` value short (a comma), and the service would
// not start where it started before aliases were passed.
var aliasLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?$`)

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
// twice (all measured). Each of those would stop a service that started before
// aliases were passed, so they are left out, and so is an alias with a dot,
// which a peer reaches by its short name only where its resolver searches a
// name with a dot (glibc does, musl does not; measured). What is left out is
// said, once for each alias, with why.
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
	if !o.runtimeTakesAliases() {
		for _, a := range written {
			skip(a, "Apple container takes network aliases from 1.6.0 on")
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
			case !aliasLabel.MatchString(a):
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
			given[low] = true
			rn := o.resolveNetwork(key).name
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

// runtimeTakesAliases asks the runtime once whether it takes network aliases.
func (o *Orchestrator) runtimeTakesAliases() bool {
	if o.aliasesTaken == nil {
		ok := runtime.AcceptsNetworkAliases(o.rt.SystemStatus().ClientVersion)
		o.aliasesTaken = &ok
	}
	return *o.aliasesTaken
}
