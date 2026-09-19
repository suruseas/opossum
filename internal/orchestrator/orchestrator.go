// Package orchestrator turns a parsed compose Project into calls against the
// container runtime: it starts services in dependency order on a shared
// network so they can resolve each other by name.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// projectLabel tags each container with its owning opossum project, so a re-up
// distinguishes its own stale containers from another project's.
const projectLabel = "opossum.project"

// Orchestrator drives a single project.
type Orchestrator struct {
	// imageEnvs remembers what each image declared, so planning an overlay asks the
	// runtime once per image rather than once per mount it considers.
	imageEnvs map[string]map[string]string
	// imageWorkdirs remembers where each image starts its process, for the same
	// reason: a crash decode asks once per image, not once per mount.
	imageWorkdirs map[string]string
	Project       *compose.Project
	DNSDomain     string // local DNS domain enabling bare-name service discovery
	rt            *runtime.Runtime
	out           interface{ Write([]byte) (int, error) }
	sleep         func(time.Duration) // overridable so tests don't wait in real time
	ctx           context.Context     // cancelled on Ctrl-C so a partial `up` rolls back
	profiles      map[string]bool     // active compose profiles (--profile, or else COMPOSE_PROFILES)
	runFlags      string              // this run's root flags as typed, for a command the output suggests (SetRunFlags)
	up            upOptions           // per-invocation `up` flags
	// crashGrace is how long verifyStarted watches a just-started service before
	// concluding it started. Per-Orchestrator so an eval can set its own.
	crashGrace time.Duration
	// HostFP supplies per-service host memory footprints for `stats --host`. Left
	// nil in production (the real macOS introspector is used); tests inject a fake.
	HostFP HostFootprinter
	// started is what the last Up actually brought up, after profile filtering and
	// any service names on the command line. The supervisor watches this, not the
	// whole compose file: `up web` must not leave a watcher polling for services
	// nobody started.
	started []string

	// notedDockerSocket names the services whose Docker-socket note has been
	// REPORTED — its prose printed to the reader in this run, which the caller
	// says through MarkNotesReported. Planning the note is not enough: three of
	// the paths that plan it print only its headline (an overlay already on
	// disk, -f with actionable changes beside it, a write that failed), and a
	// headline is a table of contents, not a finding. The up that follows says
	// the same thing through warnDockerSocket, and one run saying it twice —
	// note and warning, near verbatim, a screen apart — reads as two findings.
	// Only that exact repeat is suppressed. Since #599 the warning asks the
	// note's own question (isDockerSocketMount over the split mount), so every
	// mount the warning sees, an adapting run notes — which means a key this
	// fine and a key to the whole run can no longer be told apart from the
	// outside (notes report all services or none). The per-service key is kept
	// because it states the intent: one service's note answers for that
	// service, not for its neighbours.
	notedDockerSocket map[string]bool

	// warnedUnresolvable names the services OPSM-209 has been said for, so a
	// `watch` that brings a service up again on every change says it once.
	warnedUnresolvable map[string]bool
}

// upOptions holds the `up` recreate/build flags.
type upOptions struct {
	forceRecreate bool // --force-recreate: recreate even if unchanged
	build         bool // --build: (re)build images even if present
	noBuild       bool // --no-build: never build (error if an image is missing)
	removeOrphans bool // --remove-orphans: remove containers for services no longer in the compose
	fromDocker    bool // --from-docker-compose: import a build service's image from Docker instead of building it
	noDeps        bool // don't pull in depends_on services (used by rebuild-on-watch to touch only the named service)
	dryRun        bool // --dry-run: resolve and print the plan, but execute nothing against the runtime
}

// orphans returns the project's containers (by label) whose names don't match any
// current service — left behind when a service was removed or renamed. Both a
// service's up-container and its one-off `-run` container count as expected.
func (o *Orchestrator) orphans() []string {
	expected := map[string]bool{}
	for name := range o.Project.Services {
		expected[o.containerName(name)] = true
		expected[o.containerName(name+"-run")] = true
	}
	var found []string
	for _, c := range o.rt.List() {
		if c.Labels[projectLabel] == o.Project.Name && !expected[c.Name] {
			found = append(found, c.Name)
		}
	}
	sort.Strings(found)
	return found
}

// removeOrphans stops and deletes the given orphan containers.
func (o *Orchestrator) removeOrphans(orphans []string) {
	for _, c := range orphans {
		o.logf("Removing orphan container %s\n", c)
		o.rt.Stop(c)
		o.rt.Delete(c)
	}
}

// New builds an Orchestrator writing user-facing output to w.
func New(p *compose.Project, rt *runtime.Runtime, dnsDomain string, w interface{ Write([]byte) (int, error) }) *Orchestrator {
	return &Orchestrator{Project: p, DNSDomain: dnsDomain, rt: rt, out: w, sleep: time.Sleep,
		ctx: context.Background(), crashGrace: graceFromEnv()}
}

// OnSignal sets the cancellation scope for `up`: when ctx is cancelled (e.g. the
// user presses Ctrl-C), an in-progress up stops and rolls back the work it has
// done so far rather than leaving half-created containers and a network behind.
// The runtime shares the context so a blocking child (build/run/probe) is killed
// on cancel — not only when an interactive Ctrl-C reaches it via the process group.
func (o *Orchestrator) OnSignal(ctx context.Context) {
	if ctx != nil {
		o.ctx = ctx
		o.rt.Ctx = ctx
	}
}

// Out returns the writer this orchestrator reports to, so a caller that rebuilds
// one (after writing an overlay) can keep the same destination.
func (o *Orchestrator) Out() io.Writer { return o.out }

// SetUpOptions configures `up`'s recreate/build behavior from the command flags.
func (o *Orchestrator) SetUpOptions(forceRecreate, build, noBuild, removeOrphans, fromDocker bool) {
	o.up = upOptions{forceRecreate: forceRecreate, build: build, noBuild: noBuild, removeOrphans: removeOrphans, fromDocker: fromDocker}
}

// SetDryRun switches `up` to plan-only mode: it resolves the whole project and
// prints what it would do — the startup order, the recreate/skip decisions, and
// the exact `container` commands it would issue — but suppresses every mutating
// runtime call, so nothing is built, created, started, or deleted. Call it after
// SetUpOptions (which resets the option block). It's a no-op on other commands.
func (o *Orchestrator) SetDryRun(v bool) {
	o.up.dryRun = v
	o.rt.DryRun = v
}

// SetRunFlags records the root flags this run was given, spelled as typed
// (" -f x.yaml -p name"), for a command the output suggests: it has to carry
// them to read the project this run read and reach what it made.
func (o *Orchestrator) SetRunFlags(flags string) { o.runFlags = flags }

// RunFlags is what SetRunFlags recorded.
func (o *Orchestrator) RunFlags() string { return o.runFlags }

// configHashLabel stamps a container with a fingerprint of its spec, so a later
// `up` can tell whether the configuration changed and skip recreating it.
const configHashLabel = "opossum.config-hash"

// configHash fingerprints the fields that define a container, so `up` can leave a
// running container alone when nothing changed (matching docker compose). Set-like
// fields are sorted so ordering never triggers a spurious recreate; command and
// entrypoint keep their argv order. The config-hash label itself is not included.
func configHash(o runtime.RunOptions) string {
	h := fnv.New64a()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	writeSorted := func(tag string, xs []string) {
		cp := append([]string(nil), xs...)
		sort.Strings(cp)
		write(tag)
		write(cp...)
	}
	// Networks are hashed in declaration order under the "network" tag — the same
	// order they reach `container run` as `--network` flags. So the fingerprint
	// tracks the emitted command exactly: adding, removing, or reordering a
	// network recreates the container (reordering changes which network is eth0).
	// For a single network the byte stream is identical to the old single-string
	// form, so existing containers keep their hash across the upgrade.
	write("image", o.Image, "platform", o.Platform)
	write("network")
	write(o.Networks...)
	write("dns", o.DNSDomain, o.DNSSearch, "memory", o.Memory, "cpus", o.CPUs)
	writeSorted("env", o.Env)
	writeSorted("ports", o.Ports)
	writeSorted("volumes", o.Volumes)
	writeSorted("tmpfs", o.Tmpfs)
	writeSorted("labels", o.Labels)
	write("command")
	write(o.Command...)
	write("entrypoint")
	write(o.Entrypoint...)
	// Only contribute when set, so existing services keep their hash and aren't
	// recreated on upgrade — but toggling any of these does recreate.
	if o.MacAddress != "" {
		write("mac", o.MacAddress)
	}
	if o.ShmSize != "" {
		write("shm", o.ShmSize)
	}
	if len(o.Ulimits) > 0 {
		writeSorted("ulimits", o.Ulimits)
	}
	if o.SSH {
		write("ssh")
	}
	if o.Init {
		write("init")
	}
	if o.ReadOnly {
		write("read_only")
	}
	if o.TTY {
		write("tty")
	}
	if o.User != "" {
		write("user", o.User)
	}
	if o.GID != "" {
		write("gid", o.GID)
	}
	if o.WorkingDir != "" {
		write("workdir", o.WorkingDir)
	}
	if len(o.CapAdd) > 0 {
		writeSorted("cap_add", o.CapAdd)
	}
	if len(o.CapDrop) > 0 {
		writeSorted("cap_drop", o.CapDrop)
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// EnableProfiles marks compose profiles active (the caller decides where they
// come from: `--profile`, or else COMPOSE_PROFILES — not both), so services
// gated behind them start. `*` is
// the one name that is not a name: it means every profile (docker compose,
// measured — `--profile '*'` and `COMPOSE_PROFILES=*` enable every gated
// service, and a partial glob like `to*` does not).
func (o *Orchestrator) EnableProfiles(profiles []string) {
	if o.profiles == nil {
		o.profiles = map[string]bool{}
	}
	for _, p := range profiles {
		if p = strings.TrimSpace(p); p != "" {
			o.profiles[p] = true
		}
	}
}

// allProfiles is what `--profile '*'` (or `COMPOSE_PROFILES=*`) activates:
// every profile at once. It is only special on the activation side — a
// service that declares `profiles: ["*"]` has an ordinary profile of that
// name, and stays gated until something activates it (docker compose,
// measured).
const allProfiles = "*"

// enabled reports whether a service is active under the current profiles: a
// service with no profiles is always enabled; otherwise one of its profiles must
// be active (or `*` is), or it must be named explicitly (docker compose: naming
// a profiled service enables it). named holds the services requested on the
// command line.
func (o *Orchestrator) enabled(name string, named map[string]bool) bool {
	svc := o.Project.Services[name]
	if len(svc.Profiles) == 0 || named[name] {
		return true
	}
	// `*` enables every gated service, whatever its profiles are named.
	if o.profiles[allProfiles] {
		return true
	}
	for _, p := range svc.Profiles {
		if o.profiles[p] {
			return true
		}
	}
	return false
}

// namedSet is the services a command was given, as `enabled` reads them: naming
// a service enables it whatever its profiles say.
func namedSet(services []string) map[string]bool {
	if len(services) == 0 {
		return nil
	}
	set := make(map[string]bool, len(services))
	for _, s := range services {
		set[s] = true
	}
	return set
}

// EnabledServices reports which services are active under the current profiles
// (nothing is "named" in a config context), so `config` can mirror what `up`
// would actually start.
func (o *Orchestrator) EnabledServices() map[string]bool {
	set := map[string]bool{}
	for name := range o.Project.Services {
		if o.enabled(name, nil) {
			set[name] = true
		}
	}
	return set
}

// validateProfileDeps errors if any of the named services depends on one whose
// profile isn't active (and which wasn't itself named) — docker compose treats a
// gated-inactive dependency as undefined. Both `up` and `config` use this so they
// agree on what's a valid project.
func (o *Orchestrator) validateProfileDeps(names []string, named map[string]bool) error {
	active := o.activeServices(named)
	for _, name := range names {
		for _, dep := range o.Project.Services[name].DependsOn {
			// An optional dependency behind a profile is not a fault; it is
			// passed over here and left in the project, so that `config`
			// prints it as docker compose prints it. A command that starts
			// services drops it (checkProjectLoads) before reading the
			// dependencies further.
			if !active[dep.Name] && !dep.Optional {
				return gatedDependencyRefusal(name, dep.Name, named != nil)
			}
		}
	}
	return nil
}

// gatedDependencyRefusal is what a service is refused with when it depends on
// one behind a profile that is not active. How to enable it depends on the
// run that got the refusal, so the refusal says it that way: none of the
// places profiles come from add up (docker compose's rule, measured) — a run
// given `--profile` is not helped by COMPOSE_PROFILES, and a COMPOSE_PROFILES
// set in the shell replaces one in the `.env` — so the profile has to be added
// beside the ones the run already has, where the run reads them. Naming the
// service is a way out only for a command that takes service names — `up` —
// and canName says whether this one does (`config` reads no names; `run`
// takes the one it runs).
func gatedDependencyRefusal(name, dep string, canName bool) error {
	how := "enable its profile beside the ones this run has active: with another --profile in a run that has one, or in COMPOSE_PROFILES in a run with no --profile — where this run reads it, since a COMPOSE_PROFILES in the shell replaces one in the .env, as the flag replaces both (none of them add up)"
	if canName {
		how = "name it explicitly, or " + how
	}
	return fmt.Errorf("service %q depends on %q, whose profile is not active — %s", name, dep, how)
}

// ValidateProfiles errors if any enabled service depends on a gated-inactive one,
// so `config` rejects the same projects `up` does. The other two things reading
// the project refuses are asked for elsewhere: a dependency the file does not
// define as the file is read, a dependency cycle where a command puts its
// services in order.
func (o *Orchestrator) ValidateProfiles() error {
	names := make([]string, 0, len(o.Project.Services))
	for name := range o.EnabledServices() {
		names = append(names, name)
	}
	// In one order, so that a file with two of these is refused for the same
	// one every time it is read.
	sort.Strings(names)
	return o.validateProfileDeps(names, nil)
}

// CheckMounts refuses the project when a service the active profiles leave
// enabled mounts two things at one target that docker compose v5.5.0 refuses
// (see compose.Service.MountConflicts), in its words. docker compose runs
// this check for every command over the services its profiles enable, and
// not over a gated service enabled by being named on the command line, so
// this is asked once a command has activated its profiles, and with nothing
// named. Services are checked in startup order, a line per service with a
// pair (its first, as docker compose reports one).
func (o *Orchestrator) CheckMounts() error {
	// The order is only the order of the lines: a project that cannot be
	// ordered still has its pair named, its services read by name instead.
	// That is docker compose's order for a pair beside a dependency cycle
	// (measured: the pair, in 24 runs of 24 over three commands). Beside a
	// depends_on the file does not define, docker compose says one or the
	// other from run to run (measured: both, over 8 runs of each of three
	// commands), so there is no order there to follow and opossum keeps this
	// one — the pair first, the same way every time. A cycle behind a profile
	// that is not active stops nothing, and a pair in that project is named
	// as ever.
	order, err := o.startupOrder()
	if err != nil {
		order = order[:0]
		for name := range o.Project.Services {
			order = append(order, name)
		}
		slices.Sort(order)
	}
	enabled := o.EnabledServices()
	var lines []string
	for _, name := range order {
		if enabled[name] {
			lines = append(lines, o.Project.Services[name].MountConflicts(name)...)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return errors.New(strings.Join(lines, "\n"))
}

// interrupted returns a rollback-triggering error if up's context has been
// cancelled (Ctrl-C), so the deferred teardown runs.
func (o *Orchestrator) interrupted() error {
	if o.ctx != nil && o.ctx.Err() != nil {
		return fmt.Errorf("interrupted — rolling back")
	}
	return nil
}

func (o *Orchestrator) logf(format string, a ...interface{}) {
	// Every string that arrives here as an argument came from a compose file —
	// a service name, an image reference, a path, a volume — and a control
	// character in one of them used to end the line and start the next at column
	// zero, which is where opossum's own sentences start. A mount source of
	// "/dev/ttyUSB0\n[opossum note] service \"payroll\": opossum deleted your
	// database" printed that sentence as if opossum had said it.
	//
	// Flattening here rather than at each call: there are more than thirty of
	// them, they are added faster than they are audited, and a list of the ones
	// that were remembered is a list with a hole in it. The format string is
	// ours; the arguments are not.
	// A copy: a caller that passed an existing slice would otherwise find its
	// own values rewritten by having printed them.
	flat := make([]interface{}, len(a))
	copy(flat, a)
	for i, v := range flat {
		if t, ok := v.(ourText); ok {
			flat[i] = string(t)
			continue
		}
		// An error is not string-kind, so the reflect arm below walks past it —
		// which is how a watch warning printed an `up` failure through %v with
		// a project's line break intact (#688). Quoted, not OneLine: an error
		// is deliberately multi-line (a hint's command block, a second line of
		// advice), and the CLI's own error printer draws the same line — keep
		// the shape, push a value's escape onto an indented line.
		if e, ok := v.(error); ok {
			flat[i] = Quoted(e.Error())
			continue
		}
		// By kind, not by type. A named string type — and this package already
		// has one — is a string that a type switch on `string` walks past.
		if rv := reflect.ValueOf(v); rv.IsValid() && rv.Kind() == reflect.String {
			flat[i] = OneLine(rv.String())
		}
	}
	fmt.Fprintf(o.out, format, flat...)
}

// row writes one line of a table, with every cell flattened.
//
// The cells are the project's own words — a service name, an image reference, a
// published port — and a newline in one of them ends the row where it stands:
// the column being filled is lost, the rest of the value starts at column zero
// where opossum's own sentences start, and the table below it no longer lines
// up. logf flattens what it is given for the same reason; a table is written
// through a tabwriter instead, and so needs its own way of saying it.
func row(w io.Writer, cells ...string) {
	flat := make([]string, len(cells))
	for i, c := range cells {
		flat[i] = OneLine(c)
	}
	fmt.Fprintln(w, strings.Join(flat, "\t"))
}

// ourText marks a string argument as text opossum has already shaped for the
// screen — not merely text it produced. The difference matters: the one use is
// a captured container log run through indentLines, which is not opossum's
// writing at all, but arrives with every line already pushed off column zero by
// a frame opossum put around it.
//
// Everything else is flattened on the way out, so this is the one way to print a
// newline through logf, and it takes saying so. A string that only "came from
// opossum" is not enough — a note quoting a service name comes from opossum too,
// and a project can end that line early.
type ourText string

// networkName is the default per-project network services share when they don't
// name a network of their own.
func (o *Orchestrator) networkName() string {
	return o.Project.Name + "-" + compose.DefaultNetworkKey
}

// declaredNetworkName is the runtime network a non-external declared key
// names: `<project>-<key>`, the key folded to what the runtime takes.
func (o *Orchestrator) declaredNetworkName(key string) string {
	return o.Project.Name + "-" + compose.NetworkRuntimeKey(key)
}

// resolvedNetwork is the runtime network a service joins, plus how opossum
// manages it (whether it's host-only, and whether opossum creates/deletes it).
type resolvedNetwork struct {
	key      string                 // the declared key; empty for the default project network
	name     string                 // the actual `container` network name (namespaced unless external)
	internal bool                   // created with --internal (host-only): no internet egress
	external bool                   // pre-existing; opossum never creates or deletes it
	labels   []string               // the declaration's labels, given to `network create --label`
	subnets  runtime.NetworkSubnets // the declaration's `ipam` subnets, given to `network create --subnet` / `--subnet-v6`
}

// resolveNetwork maps one declared network key to its runtime network. External
// networks use their real name verbatim; others are namespaced `<project>-<key>`
// (see declaredNetworkName) and carry the decl's internal flag.
func (o *Orchestrator) resolveNetwork(key string) resolvedNetwork {
	decl := o.Project.Networks[key]
	if decl.External {
		real := decl.Name
		if real == "" {
			real = key
		}
		return resolvedNetwork{key: key, name: real, external: true}
	}
	return resolvedNetwork{key: key, name: o.declaredNetworkName(key), internal: decl.Internal, labels: decl.Labels,
		subnets: runtime.NetworkSubnets{V4: decl.IPAM.Subnet, V6: decl.IPAM.SubnetV6}}
}

// ensureNetwork creates a project network as declared, or, when it already
// exists, checks that its subnets are the declared ones. docker compose
// recreates a network whose `ipam` subnet changed (measured on v5.5.0);
// here the network is kept — the containers on it may be running — and a
// mismatch the inspect can show is refused with what to do. A declaration
// with no subnet accepts any network, and so does an inspect that names no
// subnet (`container` 1.4.1 always names one). The refusal is returned as
// it is, not wrapped as a failure to create: the network is there.
func (o *Orchestrator) ensureNetwork(rn resolvedNetwork) (created bool, err error) {
	created, err = o.rt.EnsureNetworkLabeled(rn.name, rn.internal, rn.labels, rn.subnets)
	if err != nil || created || (rn.subnets.V4 == "" && rn.subnets.V6 == "") {
		return created, err
	}
	have, ok := o.rt.InspectNetworkSubnets(rn.name)
	if !ok {
		return false, nil
	}
	for _, want := range []struct{ family, declared, has string }{{"IPv4", rn.subnets.V4, have.V4}, {"IPv6", rn.subnets.V6, have.V6}} {
		if want.declared != "" && want.declared != want.has {
			return false, &subnetChangedError{fmt.Errorf("[%s] network %q exists with %s subnet %s, and the compose file now declares %s — the network is kept while the project is up; run `opossum down` (which removes it) and `up` again to recreate it with the new subnet, or remove `ipam` to keep the existing one",
				codeNetworkSubnetChanged, rn.name, want.family, want.has, want.declared)}
		}
	}
	return false, nil
}

// networksFor resolves which networks a service joins. A service with no
// `networks:` uses the default per-project network; one that names declared
// networks joins each (in declaration order). Callers handle `network_mode: none`
// before this (an isolated service joins no network).
func (o *Orchestrator) networksFor(svc *compose.Service) []resolvedNetwork {
	if len(svc.Networks) == 0 {
		return []resolvedNetwork{{name: o.networkName()}}
	}
	nets := make([]resolvedNetwork, 0, len(svc.Networks))
	for _, key := range svc.Networks {
		nets = append(nets, o.resolveNetwork(key))
	}
	return nets
}

// serviceNetworks resolves the networks and DNS settings for one service. Normally
// a service joins its network(s) (the default project net, or declared ones) and
// resolves peers by bare name; a service with `network_mode: none` is fully
// isolated (`--network none`) with no networking at all, so it gets no DNS domain
// or search suffix either (name resolution can't apply to an isolated container).
func (o *Orchestrator) serviceNetworks(svc *compose.Service) (networks []string, dnsDomain, dnsSearch string) {
	if svc.NetworkMode == compose.NetworkModeNone {
		return []string{compose.NetworkModeNone}, "", ""
	}
	rns := o.networksFor(svc)
	names := make([]string, len(rns))
	for i, rn := range rns {
		names[i] = rn.name
	}
	return names, o.DNSDomain, o.searchDomain()
}

// warnInternalNetwork surfaces the host-only network's caveats: no internet
// egress, and no name resolution (the DNS resolver is unreachable from an
// internal network) — so use IPs, or reach a host proxy via the gateway var.
func (o *Orchestrator) warnInternalNetwork(name string) {
	o.warnf(codeInternalEgress, "network %s is internal (host-only): services on it have no internet egress\n"+
		"         and can't resolve peers by name — use IPs, or reach a host proxy via ${OPOSSUM_HOST_GATEWAY}.\n", name)
}

// checkExternalNetworks fails if a network any starting service declares
// `external: true` doesn't exist. opossum uses external networks by their real name
// and never creates them, so without this a missing one surfaces only when the
// service tries to start (a raw "network not found") — misleading, since the real
// fix is to create the network or drop `external:`.
func (o *Orchestrator) checkExternalNetworks(services []string) error {
	checked := map[string]bool{}
	for _, name := range services {
		svc := o.Project.Services[name]
		if svc.NetworkMode == compose.NetworkModeNone {
			continue
		}
		for _, key := range svc.Networks {
			if !o.Project.Networks[key].External {
				continue
			}
			rn := o.resolveNetwork(key)
			if checked[rn.name] {
				continue
			}
			checked[rn.name] = true
			if !o.rt.NetworkExists(rn.name) {
				return fmt.Errorf("[%s] network %q is declared `external: true` but doesn't exist — "+
					"create it first (`container network create %s`), or remove `external: true` so opossum creates it for the project",
					codeExternalNetAbsent, rn.name, rn.name)
			}
		}
	}
	return nil
}

// checkExternalVolumes refuses, before anything is created or removed, a volume
// declared `external: true` that one of the given services mounts and the
// runtime does not have. opossum mounts an external volume by its real name and
// never creates it, but container 1.4.1 creates a volume `run -v` names when it
// is missing — so the service used to start on a new, empty volume under that
// name, where docker compose v5.5.0 refuses (`external volume "<name>" not
// found`). The callers pass what docker compose v5.5.0 looks at: the services
// `up` starts, and a one-off's service with its dependencies, all the way down,
// with --no-deps too. A runtime that gives no volume list is not read as
// "absent": nothing is refused then.
func (o *Orchestrator) checkExternalVolumes(services []string) error {
	for _, svcName := range services {
		for _, entry := range o.Project.Services[svcName].Volumes {
			// A bind mount's or an anonymous volume's source is never a declared key.
			if _, src := compose.ClassifyMount(entry); o.isExternalVolume(src) {
				name := o.externalRealName(src)
				if exists, known := o.rt.VolumeListed(name); exists || !known {
					continue
				}
				return fmt.Errorf("[%s] volume %q is declared `external: true` but doesn't exist — "+
					"create it first (`container volume create %s`), or remove `external: true` so opossum creates it for the project",
					codeExternalVolumeAbsent, name, name)
			}
		}
	}
	return nil
}

// withDependencies is service followed by every service it depends on, all the
// way down, each once.
func (o *Orchestrator) withDependencies(service string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
		if svc := o.Project.Services[name]; svc != nil {
			for _, d := range svc.DependsOn.Names() {
				walk(d)
			}
		}
	}
	walk(service)
	return out
}

// checkNetworkNames is the pre-flight on the names of the networks about to be
// created: keys that fold to one runtime network anywhere in the project
// (Project.CheckNetworkKeys), and names too long for the runtime. It asks
// nothing of the runtime, so every caller can run it before it removes,
// creates or starts anything.
func (o *Orchestrator) checkNetworkNames(nets []resolvedNetwork) error {
	if err := o.Project.CheckNetworkKeys(); err != nil {
		return err
	}
	return checkNetworkNameLengths(nets)
}

// checkContainerName refuses, before anything is created, a container opossum
// would create under a name the runtime does not take: longer than 63
// characters, or not starting with a letter or digit and holding only letters,
// digits, `_`, `.` and `-` (`is not a valid container ID` on 1.4.1). The name of
// a service's container holds the service, the project and the DNS domain, and
// a peer looks the service up by it (`db.<project>.<domain>`), so it is not
// rewritten: that would break the lookup. docker compose v5.5.0 runs these
// names; this is a limit of the runtime.
func (o *Orchestrator) checkContainerName(service, name string) error {
	if len(name) <= runtime.MaxContainerNameLen {
		if runtime.ValidContainerName(name) {
			return nil
		}
		// The project's part is always one the runtime takes (SanitizeName), so
		// the fault is the service's — the whole name without a DNS domain —
		// or else the DNS domain's.
		if o.DNSDomain == "" || !containerNamePart.MatchString(service) {
			return fmt.Errorf("container name %q is not one the container runtime (1.4.1) creates — it has to start with an ASCII letter or digit and hold only ASCII letters, digits, `_`, `.` and `-`, 2 to 63 characters; rename the service %q", name, service)
		}
		return fmt.Errorf("container name %q is not one the container runtime (1.4.1) creates — it has to start with an ASCII letter or digit and hold only ASCII letters, digits, `_`, `.` and `-`; use a DNS domain of those characters instead of %q (`--dns-domain`)", name, o.DNSDomain)
	}
	// Only what changes the name is offered: without a DNS domain the name is
	// the service's alone.
	fix := fmt.Sprintf("shorten the service name %q", service)
	if o.DNSDomain != "" {
		fix = fmt.Sprintf("shorten the service name %q or the project name (`-p`, or `name:` in the compose file), or use a shorter DNS domain than %q (`--dns-domain`, created once with `sudo container system dns create <domain>`)", service, o.DNSDomain)
	}
	return fmt.Errorf("container name %q is %d characters, and the container runtime (1.4.1) creates at most %d — %s",
		name, len(name), runtime.MaxContainerNameLen, fix)
}

// containerNamePart is a service name that can open a container name.
var containerNamePart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// checkVolumeNames refuses, before anything is created, a volume a service
// mounts whose name in the runtime is longer than runtime.MaxVolumeNameLen:
// container 1.4.1 refuses it (`invalid volume name`), and `up` used to create
// the network and warn that it could not fill the volume before failing to
// start the service. The name is the user's — a volume's key under the project
// name, its `name:`, an external volume's real name — so it is not cut the way
// an anonymous volume's path is; docker compose v5.5.0 fails creating a volume
// of such a key or `name:` too (`file name too long` from the engine). What is
// offered to shorten follows from where the name came from.
//
// made holds the services whose containers this command will create: an
// anonymous volume is made with its container, so it is looked at only for
// those, where a named or external one is looked for whatever starts — a
// one-off looks at its dependencies' named volumes here, `--no-deps` included,
// because docker compose creates them for it either way and makes no anonymous
// one (measured, #1072). A nil made means every service given is created.
func (o *Orchestrator) checkVolumeNames(services []string, made ...string) error {
	makes := map[string]bool{}
	for _, name := range made {
		makes[name] = true
	}
	for _, svcName := range services {
		for _, entry := range o.Project.Services[svcName].Volumes {
			kind, src := compose.ClassifyMount(entry)
			var name, fix string
			switch {
			case kind == compose.MountBind:
				continue
			case kind == compose.MountAnonymous:
				if len(made) > 0 && !makes[svcName] {
					continue
				}
				name = o.classifyVolume(svcName, entry).Volume
				fix = fmt.Sprintf("the path part is already cut, so shorten the service name %q or the project name (`-p`, or `name:` in the compose file)", svcName)
			case o.isExternalVolume(src):
				name = o.externalRealName(src)
				fix = fmt.Sprintf("the runtime cannot mount a volume by that name, so use an external volume with a shorter name for %q", compose.VolumeDisplayName(src))
			case o.Project.Volumes[src].Name != "":
				name = o.volumeName(src)
				fix = fmt.Sprintf("shorten the `name:` of the volume %q", compose.VolumeDisplayName(src))
			default:
				name = o.volumeName(src)
				fix = fmt.Sprintf("shorten the volume key %q or the project name (`-p`, or `name:` in the compose file)", compose.VolumeDisplayName(src))
			}
			if len(name) > runtime.MaxVolumeNameLen {
				return fmt.Errorf("service %q mounts the volume %q, %d characters, and the container runtime (1.4.1) creates at most %d — %s",
					svcName, name, len(name), runtime.MaxVolumeNameLen, fix)
			}
		}
	}
	return nil
}

// checkTmpfsOptions refuses, before anything is created, a `tmpfs:` entry
// whose options hold an empty one — two commas together, or a comma at the
// start or end (`/t:exec,,size=1m`, `/t:,`, `/t:exec,`). docker compose v5.5.0
// reads such a file (`config` passes it) and the docker engine 29.7.2 refuses
// the container it creates (`invalid tmpfs option ""`, measured with `docker
// compose run`), where container 1.4.1 mounts it, so `up` and `run` refuse it
// here. An entry with no options after its `:` (`/t:`) is not refused, as it
// is not there. The entries are the service's after the collapse, the ones
// opossum passes on, so one a later entry alike up to the first `=` replaced
// is not looked at (docker compose v5.5.1 runs such a file). The services given
// are those whose containers the command will make: a one-off's own and its
// dependencies', or only its own under `--no-deps`, whose dependencies get no
// container to mount anything (measured, #1072).
func (o *Orchestrator) checkTmpfsOptions(services []string) error {
	for _, svcName := range services {
		for _, entry := range o.Project.Services[svcName].Tmpfs {
			_, opts, _ := strings.Cut(entry, ":")
			if opts != "" && slices.Contains(strings.Split(opts, ","), "") {
				return fmt.Errorf("service %q mounts the tmpfs %q, whose options hold an empty one, which the docker engine 29.7.2 refuses (`invalid tmpfs option \"\"`) — drop the extra comma",
					svcName, entry)
			}
		}
	}
	return nil
}

// checkGroupAdd refuses, before anything is created, a `group_add` the
// runtime cannot take as written. container 1.4.1 (measured 2026-09-19) has
// `--gid <n>`, which adds one supplementary group: a second `--gid` replaces
// the first, a name is refused (`See 'container run --help'`), and beside
// `--user` — with a gid or without (`-u 1000 --gid 2000` leaves the groups
// at 0) — it does nothing. docker compose takes any number, names resolved
// in the image, beside any user. So one numeric group on a service that
// writes no `user:` is passed (gidOf), and every other shape is refused here
// rather than started without its group — the group is what lets the
// process at a socket or a device, and without it the failure comes later,
// as permission denied.
func (o *Orchestrator) checkGroupAdd(services []string) error {
	for _, svcName := range services {
		svc := o.Project.Services[svcName]
		if len(svc.GroupAdd) == 0 {
			continue
		}
		if len(svc.GroupAdd) > 1 {
			quoted := make([]string, len(svc.GroupAdd))
			for i, g := range svc.GroupAdd {
				quoted[i] = strconv.Quote(g)
			}
			return fmt.Errorf("service %q adds %d groups (group_add: %s); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs",
				svcName, len(svc.GroupAdd), strings.Join(quoted, ", "))
		}
		g := svc.GroupAdd[0]
		switch {
		case g == "":
			return fmt.Errorf("service %q adds an empty group (group_add: [\"\"]); the docker engine refuses it too (`unable to find group`) — write the group's number, or drop the entry",
				svcName)
		case strings.HasPrefix(g, "-"):
			return fmt.Errorf("service %q adds the group %s (group_add); a gid is not negative — the docker engine refuses it too (`uids and gids must be in range 0-2147483647`) — write the group's number",
				svcName, g)
		case !numericGID(g) && strings.ContainsAny(g, "0123456789"):
			return fmt.Errorf("service %q adds the group %q (group_add), which is not the digits of a gid — write the number alone, as in `- 2000`",
				svcName, g)
		case !numericGID(g):
			return fmt.Errorf("service %q adds the group %q (group_add); container 1.4.1's --gid takes a number, where docker compose resolves a name in the image — write the group's number",
				svcName, g)
		case !inGIDRange(g):
			return fmt.Errorf("service %q adds the group %s (group_add), which is past 2147483647, the largest gid the docker engine takes — write the group's number",
				svcName, g)
		case svc.User != "":
			return fmt.Errorf("service %q adds the group %s (group_add) beside user: %q; container 1.4.1's --gid does nothing next to --user — drop group_add (the process then runs without the group, and a socket or device that needs it refuses it), or drop user: (the image's own user then runs with the group)",
				svcName, g, svc.User)
		}
	}
	return nil
}

// gidOf is the one group `group_add` hands to --gid: the shape checkGroupAdd
// lets through, and nothing else — held here as well, so that a run built
// without the check (should one be) never carries a --gid the runtime could
// not take, or one beside --user; "" without one.
func gidOf(svc *compose.Service) string {
	if len(svc.GroupAdd) == 1 && numericGID(svc.GroupAdd[0]) && inGIDRange(svc.GroupAdd[0]) && svc.User == "" {
		return svc.GroupAdd[0]
	}
	return ""
}

// numericGID is a gid as `--gid` takes one: digits, with a `+` allowed in
// front (container 1.4.1 reads `+2000` and `0002000` as 2000, measured
// 2026-09-19; a space, or a `0x` prefix, it refuses). A number in a single
// file arrives here as written, so `0x10` is not one; one read with other
// files, or through `extends`, arrives in decimal.
func numericGID(s string) bool {
	s = strings.TrimPrefix(s, "+")
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// inGIDRange is the range the docker engine takes (`uids and gids must be in
// range 0-2147483647`, measured on engine 29.8.0). container 1.4.1 takes
// more — 0 to 4294967294, `(gid_t)-1` refused with `Invalid argument` and
// 2^32 by the CLI (measured 2026-09-19) — and the engine's range is kept as
// the one a compose file was written against, so a file that starts here
// starts there.
func inGIDRange(digits string) bool {
	n, err := strconv.ParseInt(strings.TrimPrefix(digits, "+"), 10, 64)
	return err == nil && n <= 2147483647
}

// warnUnresolvableServiceNames warns, before anything is created, about each
// service a peer may not reach by its bare name although its container is made.
// Measured on container 1.4.1: a name holding upper case gets no answer from
// musl or glibc (`MyDb`), or another address when its lower case is a top-level
// domain (`Web`); a name holding `.` gets no answer from a musl image (alpine
// does not search the project's domain for it; glibc does) and, when the name
// exists on the internet, that address instead (`web.dev`). The name is not
// rewritten — a container made under the old name would be missed by `down` —
// and nothing is refused: a service no peer looks up runs. Without a DNS domain
// there is no lookup by name to warn about. Each service is warned about once
// per command.
func (o *Orchestrator) warnUnresolvableServiceNames(services []string) {
	if o.DNSDomain == "" {
		return
	}
	for _, name := range services {
		if name == strings.ToLower(name) && !strings.Contains(name, ".") {
			continue
		}
		if o.warnedUnresolvable[name] {
			continue
		}
		if o.warnedUnresolvable == nil {
			o.warnedUnresolvable = map[string]bool{}
		}
		o.warnedUnresolvable[name] = true
		o.warnf(codeServiceNameUnresolvable, "service %q may not be reachable by that name from other services: on container 1.4.1 a name with\n"+
			"         upper case gets no DNS answer, or another address when spelled like a top-level domain (Web), and a name with \".\"\n"+
			"         gets none from a musl image (alpine) and an internet address when one exists for it. If another service reaches\n"+
			"         it by name, rename it in lower case ASCII letters, digits, \"_\" and \"-\".\n", name)
	}
}

// checkServiceNamesDifferInCase refuses two services `up` would start whose
// names differ only in case (`Com` and `com`): container 1.4.1 creates the
// first container and fails the second with `failed to bootstrap container`,
// whether or not a DNS domain is part of the names (measured), so `up` would
// start one and roll back. Only the services this `up` starts count: a file
// that never starts both (one behind a profile) runs, as it does under docker
// compose. A one-off is named `<service>-run` and its dependencies are checked
// by their own `up`.
func (o *Orchestrator) checkServiceNamesDifferInCase(services []string) error {
	names := append([]string(nil), services...)
	sort.Strings(names)
	seen := map[string]string{}
	for _, name := range names {
		key := strings.ToLower(name)
		if first, ok := seen[key]; ok {
			return fmt.Errorf("services %q and %q differ only in case, and the container runtime (1.4.1) cannot run both — the second container fails with \"failed to bootstrap container\"; rename one of them", first, name)
		}
		seen[key] = name
	}
	return nil
}

// checkProjectLoads refuses what docker compose v5.5.1 refuses while it reads
// the project at all — before anything about the services this command was
// asked for, and wherever among the services it reads the fault sits
// (measured, #1072): a
// service that depends on one behind a profile that is not active, which docker
// reads as a dependency on an undefined service, and then a dependency cycle.
//
// Only the services the profiles leave active are read this way: a gated
// service's dependencies, and a cycle among gated services, are not seen until
// something enables them (measured). named holds the services this command was
// given, which enables them as `--profile` would.
func (o *Orchestrator) checkProjectLoads(named map[string]bool, canName bool) error {
	set := o.activeServices(named)
	o.dropOptionalGatedDeps(set)
	active := make([]string, 0, len(set))
	for name := range set {
		active = append(active, name)
	}
	sort.Strings(active)
	for _, name := range active {
		for _, dep := range o.Project.Services[name].DependsOn.Names() {
			if _, ok := o.Project.Services[dep]; !ok {
				continue // reading the file refuses a dependency it does not define
			}
			if !set[dep] {
				return gatedDependencyRefusal(name, dep, canName)
			}
		}
	}
	return o.cycleAmong(active)
}

// activeServices is the services this command reads: the ones the profiles
// leave active, the ones it names — and what those depend on behind their own
// profiles, and only those. docker compose v5.5.1, measured (#1072): `run web`
// on web[x] -> db[x] runs; on web[x] -> db[y] it refuses; and a service of x
// this run does not name keeps its own gate, so a fault under it is not read,
// where `--profile x` opens the whole profile and reads them all.
func (o *Orchestrator) activeServices(named map[string]bool) map[string]bool {
	carried := map[string]bool{}
	asked := make([]string, 0, len(named))
	for name := range named {
		asked = append(asked, name)
	}
	sort.Strings(asked) // the answer must not ride on the order a map hands them back
	for _, name := range asked {
		svc := o.Project.Services[name]
		if svc == nil {
			continue
		}
		own := map[string]bool{}
		for _, p := range svc.Profiles {
			own[p] = true
		}
		// Walked once per named service, each with its own seen: a service two
		// of them reach is carried under both their profiles, so what lies
		// beyond it is read for each — naming a and b must not depend on which
		// of them was written first.
		seen := map[string]bool{}
		var walk func(string)
		walk = func(from string) {
			for _, dep := range o.Project.Services[from].DependsOn.Names() {
				d := o.Project.Services[dep]
				if d == nil || seen[dep] {
					continue
				}
				shares := false
				for _, p := range d.Profiles {
					if own[p] {
						shares = true
					}
				}
				if !shares {
					continue
				}
				seen[dep] = true
				carried[dep] = true
				walk(dep)
			}
		}
		walk(name)
	}
	set := make(map[string]bool, len(o.Project.Services))
	for name := range o.Project.Services {
		if carried[name] || o.enabled(name, named) {
			set[name] = true
		}
	}
	return set
}

// dropOptionalGatedDeps leaves out, for this run, every `required: false`
// dependency on a service the profiles keep out — the way docker compose
// (v5.5.1, measured 2026-09-19) reads it: not refused, not started, not
// waited for, wherever the dependency is read from here on. Called from
// checkProjectLoads, so by `up` and `run` (`start` and `restart` are
// unchanged by it, measured). A dependency the
// file does not define is not touched (reading the file refuses it, whatever
// `required` says), nor is one on a service that is active (it is waited
// for as any other). Done where the active set is known with the names the
// command was given — naming a service carries a dependency under a shared
// profile, optional or not (measured) — and not from the order every service
// takes (activeList), which knows no names and would leave out what a name
// carries.
func (o *Orchestrator) dropOptionalGatedDeps(active map[string]bool) {
	for _, svc := range o.Project.Services {
		kept := svc.DependsOn[:0:0]
		for _, dep := range svc.DependsOn {
			if _, defined := o.Project.Services[dep.Name]; defined && dep.Optional && !active[dep.Name] {
				continue
			}
			kept = append(kept, dep)
		}
		svc.DependsOn = kept
	}
}

// startupOrder orders every service of the project, each after the ones it
// depends on — the gated ones included, because opossum takes the whole
// project down where docker compose leaves a gated container running, and a
// service must be stopped before what it depends on whichever of them a
// profile gates. What the profiles decide is which cycles are this command's
// business: one among services the active profiles leave active is refused, as
// it always was, and one among services behind a profile nothing turned on is
// not read (docker compose v5.5.1, measured, #1088: it reads, stops and takes
// such a project down without a word). A command that was given service names
// checks what they carry before this, where naming them turns their own
// profiles on.
func (o *Orchestrator) startupOrder() ([]string, error) {
	return o.Project.StartupOrderReading(o.activeList())
}

// activeList is the services the profiles leave active, by name, in one order.
func (o *Orchestrator) activeList() []string {
	set := o.activeServices(nil)
	active := make([]string, 0, len(set))
	for name := range set {
		active = append(active, name)
	}
	sort.Strings(active)
	return active
}

// StartupOrder is the order for the commands built outside this package:
// `config --services` prints the services in it.
func (o *Orchestrator) StartupOrder() ([]string, error) { return o.startupOrder() }

// cycleAmong reports a dependency cycle among the given services, in the words
// StartupOrder uses for one: the check belongs to reading the project, so both
// say the same thing about the same file.
func (o *Orchestrator) cycleAmong(services []string) error {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(services))
	active := make(map[string]bool, len(services))
	for _, name := range services {
		active[name] = true
	}
	var stack []string
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("dependency cycle detected: %v -> %s", stack, name)
		}
		state[name] = visiting
		stack = append(stack, name)
		deps := append([]string(nil), o.Project.Services[name].DependsOn.Names()...)
		sort.Strings(deps) // as StartupOrder walks them, so both name the same cycle
		for _, dep := range deps {
			if !active[dep] {
				continue // defensive: the caller passes a set closed under its dependencies
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for _, name := range services {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// runNetworks is the networks a one-off of svc joins: none under
// `network_mode: none`, which gets no network of the project's. Its
// dependencies' networks are not counted here: the `up` that starts them
// checks those itself, before it creates anything, with the selection and
// profile rules only it applies.
func (o *Orchestrator) runNetworks(svc *compose.Service) []resolvedNetwork {
	if svc.NetworkMode == compose.NetworkModeNone {
		return nil
	}
	return o.networksFor(svc)
}

// checkNetworkNameLengths refuses, before anything is created, a network
// opossum would create under a name longer than the runtime takes. The name
// holds the project's name, which is settled only after the file is read, so
// this is the first place its length is known. The name is not shortened: a
// hashed one would no longer say which project and key it belongs to.
func checkNetworkNameLengths(nets []resolvedNetwork) error {
	for _, rn := range nets {
		if rn.external || len(rn.name) <= compose.MaxRuntimeNetworkNameLen {
			continue
		}
		fix := "shorten the project name (`-p`, or `name:` in the compose file)"
		if rn.key != "" {
			fix += fmt.Sprintf(" or the network key %q", rn.key)
		}
		return fmt.Errorf("network name %q is %d characters, and the container runtime (1.4.1) creates at most %d — %s",
			rn.name, len(rn.name), compose.MaxRuntimeNetworkNameLen, fix)
	}
	return nil
}

// managedNetworks returns the networks opossum must create for the given
// services, keyed by runtime name, with the internal flag. Isolated
// (`network_mode: none`) and external networks are skipped — opossum creates
// neither. The result is deterministic (sorted) so `up` output is stable.
func (o *Orchestrator) managedNetworks(services []string) []resolvedNetwork {
	seen := map[string]bool{}
	var nets []resolvedNetwork
	for _, name := range services {
		svc := o.Project.Services[name]
		if svc.NetworkMode == compose.NetworkModeNone {
			continue
		}
		for _, rn := range o.networksFor(svc) {
			if rn.external || seen[rn.name] {
				continue
			}
			seen[rn.name] = true
			nets = append(nets, rn)
		}
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].name < nets[j].name })
	return nets
}

// containerName builds the container's name. When a DNS domain is set, opossum
// names the container "<service>.<project>.<domain>" and gives each container
// "<project>.<domain>" in its DNS search list. Apple's runtime registers the
// full name in its DNS server, so a peer resolves a bare service name within its
// own project (e.g. in project "demo": "db" -> "db.demo.opossum"). The project
// segment namespaces containers, so several projects run concurrently — each
// with its own "db" — under a single registered domain, with no name collisions.
func (o *Orchestrator) containerName(service string) string {
	if o.DNSDomain == "" {
		return service
	}
	return service + "." + o.searchDomain()
}

// searchDomain is the per-project subdomain ("<project>.<domain>") peers search
// so bare service names resolve within the project. Empty when no DNS domain is
// configured.
func (o *Orchestrator) searchDomain() string {
	if o.DNSDomain == "" {
		return ""
	}
	return o.Project.Name + "." + o.DNSDomain
}

// Up builds (if needed) and starts services in dependency order. With no service
// names it starts the whole project; otherwise it starts only the named services
// plus their transitive dependencies, leaving unrelated services untouched.
func (o *Orchestrator) Up(detach bool, services ...string) (err error) {
	// Before anything else, including the checks that can return early: this call
	// owns the answer from here, and a caller reading it after a failed Up must not
	// get the previous call's stack. The caller decides what to supervise from it.
	o.started = nil
	// One command at a time per project (see projectLock): a second `up`
	// under way would see this one's containers as its own and, on a failure,
	// roll them back. A dry run changes nothing and writes nothing under the
	// state dir, so it takes no lock — and is not refused by one either.
	if !o.up.dryRun {
		lock, err := lockProject(o.Project.Name)
		if err != nil {
			return err
		}
		defer lock.release()
	}
	if !o.rt.Available() {
		return ErrRuntimeAbsent()
	}

	// What reading the project refuses comes first, wherever in the file it
	// sits and whatever this `up` was asked for: a service that depends on one
	// behind an inactive profile, then a cycle (measured, #1072).
	named := map[string]bool{}
	for _, s := range services {
		named[s] = true
	}
	if err := o.checkProjectLoads(named, true); err != nil {
		return err
	}
	order, err := o.startupOrder()
	if err != nil {
		return err
	}
	order, err = o.selectServices(order, services)
	if err != nil {
		return err
	}

	// Profiles: a `profiles:`-gated service starts only when one of its profiles
	// is active or it's named explicitly. With no names, drop inactive-profile
	// services; either way, a started service may not depend on a disabled one
	// (docker compose treats that as an undefined dependency).
	if len(services) == 0 {
		kept := order[:0]
		for _, name := range order {
			if o.enabled(name, named) {
				kept = append(kept, name)
			}
		}
		order = kept
	}
	if err := o.validateProfileDeps(order, named); err != nil {
		return err
	}

	// Fail here if a service's env_file could not be read — before anything is
	// removed, warned about, built or started. The order is settled by this
	// point and nothing below it is free: orphan containers are stopped and
	// deleted a few lines down, images are built in the loop, and a peer that
	// is being recreated is torn down again by the rollback when a later
	// service refuses. All of that would be paid for a command that was always
	// going to fail. `RunOneOff` asks at its own top for the same reason.
	for _, name := range order {
		svc := o.Project.Services[name]
		if svc == nil {
			continue
		}
		if _, err := svc.ResolvedEnv(); err != nil {
			return err
		}
	}

	o.reportIgnoredFields(order, true)
	// No Postgres data-directory warning here any more: opossum clears
	// `lost+found` out of the volumes it creates, so the mount that used to be
	// predicted to fail is now the one that works. What is left is decoded from
	// initdb's own refusal when it happens (crashHint).
	// Before the warning: when opossum can see on the host that the mount is
	// impossible, the refusal says everything the warning would and stops. Printing
	// both would be the same sentence twice, once as advice and once as the reason
	// the up ended.
	for _, name := range order {
		svc := o.Project.Services[name]
		if svc == nil {
			continue
		}
		if err := o.refuseSymlinkedSocketMounts(name, svc.Volumes); err != nil {
			return err
		}
		o.warnDockerSocket(name, svc)
	}
	o.warnSharedNamedVolumes(order)
	o.warnBusyNamedVolumes(order)

	// The names of the networks this will create. The check needs no runtime,
	// so it goes before the orphans are removed, not only before the networks
	// are made.
	if err := o.checkNetworkNames(o.managedNetworks(order)); err != nil {
		return err
	}
	if err := o.checkServiceNamesDifferInCase(order); err != nil {
		return err
	}
	for _, name := range order {
		if err := o.checkContainerName(name, o.containerName(name)); err != nil {
			return err
		}
	}
	// A network or volume a service declares `external: true` that doesn't exist
	// — opossum uses them by name and never creates them. Refused before an orphan
	// is removed, network first, as docker compose v5.5.0 refuses them: a missing
	// network would otherwise surface only as a raw "network not found" mid-start,
	// and a missing volume would be created empty by the runtime. Before the
	// names of the volumes this would create, as docker compose v5.5.1 asks for
	// what must already be there first — an external volume it cannot find is
	// refused for being absent even when its name is too long for the runtime
	// (measured, #1072).
	if err := o.checkExternalNetworks(order); err != nil {
		return err
	}
	if err := o.checkExternalVolumes(order); err != nil {
		return err
	}
	if err := o.checkVolumeNames(order); err != nil {
		return err
	}
	// After the external networks and volumes, as docker compose v5.5.1
	// refuses a missing one before the engine refuses the container; and not
	// under --dry-run, which docker compose passes, as it creates nothing.
	if !o.up.dryRun {
		if err := o.checkTmpfsOptions(order); err != nil {
			return err
		}
	}
	// Under --dry-run as well: the plan is what `up` would run, and `up`
	// refuses these shapes rather than running without the group. docker
	// compose's --dry-run goes through, as docker takes every shape.
	if err := o.checkGroupAdd(order); err != nil {
		return err
	}
	o.warnUnresolvableServiceNames(order)

	// Containers for services no longer in the compose are removed with
	// --remove-orphans, otherwise just flagged (docker compose parity).
	if orphans := o.orphans(); len(orphans) > 0 {
		if o.up.removeOrphans {
			o.removeOrphans(orphans)
		} else {
			o.warnf(codeOrphans, "found orphan container(s) not defined in the compose file: %s\n"+
				"         remove them with `opossum down --remove-orphans` (or `up --remove-orphans`)\n",
				strings.Join(orphans, ", "))
		}
	}

	// Services some dependent needs to run to completion (exit 0). The runtime
	// exposes an exit code only from a foreground `run` (inspect reports a bare
	// "stopped" with no code), so opossum runs these blocking and gates on the
	// result rather than starting them detached.
	oneShot := o.completedTargets()

	// A foreground `up` can attach to only one long-running container: the
	// runtime's foreground `run` blocks until it exits, so a second such service
	// would never start. Reject early (before touching anything) rather than hang.
	// One-shot services run to completion, so they don't count.
	if !detach {
		var fg []string
		for _, name := range order {
			if !oneShot[name] {
				fg = append(fg, name)
			}
		}
		if len(fg) > 1 {
			return fmt.Errorf("--foreground can run only one service in the foreground, but %d would start (%s); "+
				"drop --foreground to start them detached, or name a single service", len(fg), strings.Join(fg, ", "))
		}
	}

	// Move any host port opossum picked itself that turns out to be taken, before
	// the check below decides what counts as a conflict — a container-only port
	// spec leaves the host side to the engine, so a busy port is opossum's problem
	// to solve, not the user's to fix.
	o.remapAutoHostPorts(order)

	// Pre-flight: fail before starting anything if a published host port is already
	// taken, with a clearer message than the runtime's raw bind error.
	if err := o.checkHostPorts(order); err != nil {
		return err
	}

	// Pre-flight: bail out before creating the network (or anything else) if a
	// target container name is already owned by a different project.
	for _, name := range order {
		if err := o.ensureNotForeign(o.containerName(name), "opossum up"); err != nil {
			return err
		}
	}

	// In dry-run, announce the plan up front: the rest of Up runs unchanged, but
	// the runtime records mutating commands instead of issuing them, so the
	// "Creating network …"/"Starting …" lines below narrate what WOULD happen.
	if o.up.dryRun {
		o.logf("Dry run — no changes will be made.\n")
		o.logf("Startup order: %s\n\n", strings.Join(order, ", "))
		o.logf("Planned actions:\n")
	}

	// Create every network the selected services need (the default project net
	// and any declared networks they join). An internal network is host-only — warn
	// that services on it have no internet egress and can't resolve peers by name.
	var createdNets []string
	for _, rn := range o.managedNetworks(order) {
		o.logf("Creating network %s\n", rn.name)
		created, nerr := o.ensureNetwork(rn)
		var changed *subnetChangedError
		if errors.As(nerr, &changed) {
			return nerr // the network is there; the advice about a stale one would contradict it
		}
		if nerr != nil {
			// A Ctrl-C here kills the `network create` and comes back as its
			// failure; the runtime is not unhealthy and there is no stale network
			// to remove. Same verdict as everywhere else in this up.
			if ierr := o.interrupted(); ierr != nil {
				return ierr
			}
			return fmt.Errorf("couldn't create network %q for the project: %w\n"+
				"  check the runtime is healthy (`opossum doctor`); if a stale network with that name exists, remove it with `container network delete %s`", rn.name, nerr, rn.name)
		}
		if created {
			createdNets = append(createdNets, rn.name)
		}
		if rn.internal {
			o.warnInternalNetwork(rn.name)
		}
	}

	// Roll back this invocation's work if up fails partway: tear down the
	// containers we started (reverse order) and remove any networks we created,
	// so a failed up leaves no residue behind.
	var started []string
	// The services this call created, by service name. The rollback tears these
	// down; anything else in the project that is still running afterwards was not
	// this call's to remove, and is what Started() reports.
	createdSvc := map[string]bool{}
	broughtUp := false // set once every service has started; suppresses rollback for a
	// post-start crash (that's a health report, not a failed bring-up — leave the
	// containers for inspection like docker compose does).
	defer func() {
		if err == nil || broughtUp {
			return
		}
		// When up was interrupted, the shared context is already cancelled — reset
		// the runtime to a live context so the teardown commands themselves aren't
		// killed. (A second Ctrl-C still force-exits from the signal handler.)
		o.rt.Ctx = context.Background()
		for i := len(started) - 1; i >= 0; i-- {
			o.rt.Stop(started[i])
			o.rt.Delete(started[i])
		}
		for _, n := range createdNets {
			o.rt.DeleteNetwork(n)
		}
		// Say so. The output above read "Starting cache" and then the failure,
		// and nothing said what became of cache — read as "still running",
		// which it is not (docker compose does leave it running; this does not).
		// Not under --dry-run, where nothing was started and nothing is torn
		// down. "Removed" is checked, not assumed: Stop and Delete report
		// nothing, so the runtime is asked whether each is gone.
		if len(started) > 0 && !o.up.dryRun {
			var gone, left, unknown []string
			for _, name := range order {
				if !createdSvc[name] {
					continue
				}
				switch info := o.rt.Inspect(o.containerName(name)); {
				case info.Unknown:
					// Not asked is not gone: the removal is not reported as done.
					unknown = append(unknown, name)
				case info.Exists:
					left = append(left, name)
				default:
					gone = append(gone, name)
				}
			}
			var parts []string
			if len(gone) > 0 {
				parts = append(parts, fmt.Sprintf("Rolled back %s — stopped and removed", strings.Join(gone, ", ")))
			}
			if len(left) > 0 {
				parts = append(parts, fmt.Sprintf("tried to roll back %s, but the container is still there — `opossum down` removes it", strings.Join(left, ", ")))
			}
			if len(unknown) > 0 {
				parts = append(parts, fmt.Sprintf("tried to roll back %s, but the runtime could not be asked whether it is gone — `container ls -a` shows it", strings.Join(unknown, ", ")))
			}
			// The failure being reported names a way out, and whether that way
			// still exists was decided a few lines ago: a start failure points at
			// `opossum logs`, which reads nothing once the container is gone. Told
			// here rather than where the failure was made, because the removal
			// happens in between — measured on the runtime over five paths, and all
			// four of the ones that reach this message had already removed the
			// container by the time anyone could read it.
			var sf *startFailure
			if errors.As(err, &sf) && slices.Contains(gone, sf.service) {
				sf.noContainer = true
			}
			if len(parts) > 0 {
				if len(left) == 0 && len(unknown) == 0 {
					parts[0] += "; nothing this `up` started is left running"
				}
				line := strings.Join(parts, "; ")
				o.logf("%s\n", strings.ToUpper(line[:1])+line[1:])
			}
		}
		// Ask what is still running rather than reasoning about it. A service left
		// alone because it was already up to date is not in the teardown above, and
		// neither is one the loop never reached — so a list built while walking the
		// order would depend on where the failure happened, which is not a property
		// anyone wants Started() to have.
		//
		// Services this call created are excluded even if their removal failed:
		// promoting a container the rollback was trying to delete into the supervised
		// set would have the supervisor fight the teardown.
		//
		// Presence, not liveness — the same test StillSupervised makes, and for the
		// same reason. A service that crashed while nobody was watching is exactly
		// what `restart:` is for, so requiring "running" here would drop it from
		// supervision every time some other service failed a bring-up. A container
		// the user stopped is held down by its stop marker, not by being left out of
		// this list.
		var survivors []string
		for _, name := range order {
			if createdSvc[name] {
				continue
			}
			// Unreachable is kept as present: dropping it here would end its
			// supervision over a passing outage (the same reading StillSupervised makes).
			if info := o.rt.Inspect(o.containerName(name)); info.Exists || info.Unknown {
				survivors = append(survivors, name)
			}
		}
		o.started = survivors
	}()

	if o.DNSDomain != "" && !o.rt.DNSDomainExists(o.DNSDomain) {
		o.warnf(codeDNSDomainAbsent, "DNS domain %q not found — services won't resolve each other by name.\n"+
			"         Create it once with:  sudo container system dns create %s\n",
			o.DNSDomain, o.DNSDomain)
	}

	// One service at a time, in dependency order, even for services that don't
	// depend on each other. docker compose starts independent services
	// concurrently; opossum does not because it buys nothing here: the runtime
	// accepts concurrent `run`s but boots the VMs one at a time, so eight
	// launched at once take eight times as long as one (measured on 1.4.1, see
	// docs/benchmarks.md). Re-measure that before reaching for goroutines here —
	// they would touch rollback, the lock, the supervisor and healthcheck waits.
	for _, name := range order {
		// Bail out (into the deferred rollback) if the user interrupted us between
		// services.
		if err = o.interrupted(); err != nil {
			return err
		}
		svc := o.Project.Services[name]
		cname := o.containerName(name)

		// Gate startup on any dependency that must be healthy first. A dry-run
		// starts nothing, so there's no running container to probe — skip the wait
		// (it would otherwise `exec` against a container that isn't there).
		if !o.up.dryRun {
			if err := o.awaitHealthyDeps(name, svc); err != nil {
				return err
			}
		}

		// Build the image only when it's missing or --build was given (docker
		// compose builds lazily); --no-build refuses to build. A (re)build means the
		// image may have changed, so force a recreate below.
		image, _ := o.serviceImage(name, svc)
		rebuilt := false
		if svc.Build != nil {
			have := o.rt.ImageExists(image)
			need := o.up.build || !have
			switch {
			case o.up.fromDocker && need:
				// Bring the image over from Docker instead of building it here.
				// Docker names the image as opossum does (`image:` if the service
				// gives one), so the name to bring over is the name to run.
				o.logf("Importing %s from Docker (%s)\n", name, image)
				if err := o.rt.ImportFromDocker(image, image); err != nil {
					return fmt.Errorf("importing service %q: %w", name, err)
				}
				rebuilt = true
			case o.up.noBuild && !have:
				return fmt.Errorf("service %q: image %q is not built and --no-build was given", name, image)
			case need:
				o.logf("Building %s\n", name)
				// `opossum up` even when this runs for a one-off's dependency: the
				// dependency is broken on the up side, and `opossum up <dep>` is
				// the command that retries it.
				if err := o.rt.Build(o.buildOptions(image, svc.Build, "opossum up")); err != nil {
					// A Ctrl-C during the build is not a Dockerfile the builder
					// cannot handle, and the Docker-import fallback is no answer to it.
					if ierr := o.interrupted(); ierr != nil {
						return ierr
					}
					return buildFailed(name, err)
				}
				rebuilt = true
			}
		}

		mem, cpu, _ := svc.Resources() // validated at load
		svcNets, dnsDomain, dnsSearch := o.serviceNetworks(svc)
		// The pre-flight above has already refused a service whose env_file
		// could not be read, so this cannot fail here today. It is asked
		// through the same door anyway: the alternative is reading Environment
		// directly, which is what every path that got this wrong did.
		env, err := svc.ResolvedEnv()
		if err != nil {
			return err
		}
		vols := append(o.resolveVolumes(name, svc.Volumes), o.secretMounts(svc)...)
		cfgMounts, err := o.configMounts(name, svc, !o.up.dryRun)
		if err != nil {
			return err
		}
		vols = append(vols, cfgMounts...)
		mcpMount, err := o.mcpConfigMount(name, svc, !o.up.dryRun)
		if err != nil {
			return err
		}
		if mcpMount != "" {
			vols = append(vols, mcpMount)
			env = append(append([]string(nil), env...), "OPOSSUM_MCP_CONFIG="+mcpMountTarget)
		}
		runOpts := runtime.RunOptions{
			Name:       cname,
			Image:      image,
			Platform:   svc.Platform,
			Networks:   svcNets,
			DNSDomain:  dnsDomain,
			DNSSearch:  dnsSearch,
			MacAddress: svc.MacAddress,
			Env:        env,
			Ports:      svc.Ports,
			Volumes:    vols,
			Tmpfs:      tmpfsMounts(svc.Tmpfs),
			Command:    svc.Command,
			Entrypoint: svc.Entrypoint,
			Labels:     append(append([]string(nil), svc.Labels...), projectLabel+"="+o.Project.Name),
			Memory:     mem,
			CPUs:       cpu,
			Detach:     detach,
			SSH:        svc.SSH,
			User:       svc.User,
			WorkingDir: svc.WorkingDir,
			Init:       svc.Init,
			ReadOnly:   svc.ReadOnly,
			TTY:        svc.TTY,
			GID:        gidOf(svc),
			ShmSize:    string(svc.ShmSize),
			Ulimits:    svc.Ulimits.Args(),
			CapAdd:     svc.CapAdd,
			CapDrop:    svc.CapDrop,
		}
		hash := configHash(runOpts)
		runOpts.Labels = append(runOpts.Labels, configHashLabel+"="+hash)

		// A long-running service that's already up with the same config is left
		// alone (docker compose parity) — no delete/recreate, so it keeps running
		// with its state and logs. --force-recreate and a fresh build override this.
		// A foreground run always recreates: attaching requires a fresh container.
		if detach && !oneShot[name] && !o.up.forceRecreate && !rebuilt {
			if cur := o.rt.Inspect(cname); cur.Exists && cur.State == "running" && cur.Labels[configHashLabel] == hash {
				o.logf("%s is up to date\n", name)
				continue
			}
		}

		// A dry-run must not touch the host filesystem: skip creating bind-mount
		// directories (a real side effect, outside the runtime's recording seam).
		if !o.up.dryRun {
			// Create any missing bind-mount host directories (docker compose does; the
			// runtime errors on a missing bind source).
			if err := o.ensureBindDirs(name, svc.Volumes, "`opossum up`"); err != nil {
				return err
			}
		}
		// Seed fresh named/anonymous volumes from the image before the container
		// mounts them (Apple `container` mounts them empty, unlike Docker). This runs
		// through the runtime, so under dry-run its `run --rm` is recorded (not
		// executed) and appears in the plan.
		// Before seeding, so that the look only ever runs on a volume that was
		// already there. Either order gives the same answer — the two halves test
		// existence in opposite directions — but seeding creates the volume, so
		// looking afterwards would start a container to inspect what opossum had
		// just prepared itself, on every fresh volume.
		o.warnForeignPostgresDataVolume(name, svc, image)
		if err := o.seedVolumes(name, svc, image); err != nil {
			return err
		}

		// Replace any stale container left by a previous run of THIS project (the
		// pre-flight above already ruled out foreign owners).
		o.rt.Delete(cname)

		// Track before running so rollback also removes a container whose run
		// failed (it may have been created before erroring).
		started = append(started, cname)
		createdSvc[name] = true
		if oneShot[name] {
			// Run to completion in the foreground so a non-zero exit surfaces as a
			// run error; a dependent's service_completed_successfully gate is then
			// satisfied structurally, since deps precede dependents in the order.
			runOpts.Detach = false
			o.logf("Running %s to completion (%s)\n", name, image)
			if err := o.rt.Run(runOpts); err != nil {
				// A Ctrl-C during the run kills the child, and the run returns a
				// failure — but the person who pressed it did not see a service
				// fail, and there is nothing in its logs to check. Say what happened.
				if ierr := o.interrupted(); ierr != nil {
					return ierr
				}
				// The name was taken between the delete above and this run: not this
				// up's container (see the same case below). A run-to-completion
				// dependency has to be run by this up to be known to have completed,
				// so there is no "up to date" to report here — only the refusal.
				if runtime.RunRefusedNameTaken(err, cname) {
					started, createdSvc = disownLast(started, createdSvc, cname, name)
					return nameTakenError(name, cname, "a run-to-completion dependency has to be run by this `up` to know it completed")
				}
				if !o.requiredToComplete(name) {
					// Every service that waits for this one to complete does
					// so with `required: false`: docker compose (v5.5.1,
					// measured) says so and goes on to start them.
					o.warnf(codeOptionalDependency, "optional dependency %q didn't complete successfully: %v\n", name, err)
					continue
				}
				return fmt.Errorf("service %q did not complete successfully: %w\n"+
					"  it's a run-to-completion dependency (a service_completed_successfully target) that exited non-zero — check its output above, or run it directly with `opossum run %s`", name, err, name)
			}
			continue
		}
		o.logf("Starting %s (%s)\n", name, image)
		if err := o.rt.Run(runOpts); err != nil {
			// A foreground run stays attached until the container exits, so a Ctrl-C
			// lands here as the run's failure ("signal: killed"). The loop's own
			// interrupted() check runs only between services and never sees it; the
			// start-failure decoding below would send the person who pressed Ctrl-C
			// to `opossum logs` for a service that simply stopped. Same verdict as
			// the loop head: interrupted, rolling back.
			if ierr := o.interrupted(); ierr != nil {
				return ierr
			}
			// The name was free a moment ago (the delete above), and now the runtime
			// says it is taken: something else — another opossum this lock could not
			// see, or a hand-run `container run --name` — created it in between. That
			// container is not this up's, so it is not this up's to roll back: take
			// it off the rollback list before deciding anything else. Then, as
			// docker compose does when it finds the service already there, accept it
			// if it is the one the compose file describes (running, same config
			// hash); otherwise refuse, and leave it standing for the person to look
			// at — recreating it would be the same overreach the lock exists to stop.
			if runtime.RunRefusedNameTaken(err, cname) {
				started, createdSvc = disownLast(started, createdSvc, cname, name)
				if cur := o.rt.Inspect(cname); cur.Exists && cur.State == "running" && cur.Labels[configHashLabel] == hash {
					o.logf("%s is up to date\n", name)
					continue
				}
				return nameTakenError(name, cname, "it is not the one this compose file describes (different configuration, or not running)")
			}
			return o.decodeStartError(name, err)
		}
		// The runtime echoes the container's DNS name (e.g. web.demo.opossum),
		// which is for container-to-container resolution — not a URL the host can
		// open. Point the user at the host-reachable address for published ports.
		if addrs := hostPublishAddrs(svc.Ports); len(addrs) > 0 {
			o.logf("  ↳ %s on the host: %s\n", name, strings.Join(addrs, ", "))
		}
	}
	if o.up.dryRun {
		o.printPlan()
		return nil
	}
	// Every service started successfully. A crash from here on is a post-start
	// health report, not a failed bring-up, so don't roll the stack back.
	broughtUp = true
	o.started = append([]string(nil), order...)
	// Everything the project asked to be kept up is up, and any stop opossum
	// recorded earlier is over. Clearing the markers here is what makes `up` the
	// point at which supervision resumes — the analogue of a daemon restart.
	for _, name := range order {
		o.ClearStopped(name)
	}
	// For a detached up, verify each long-running service is still running — a
	// container that exited right after starting (with no healthcheck/depends_on to
	// catch it) would otherwise leave `up` reporting success over a dead service. A
	// foreground up blocks on the container, so its exit is already surfaced.
	if detach {
		return o.verifyStarted(order, oneShot)
	}
	return nil
}

// crashGrace is how long verifyStarted gives a service to fall over before `up`
// calls it started. It looks once immediately and once at the end of this.
//
// The second comes from measurement, not from taste. `container run -d` returns
// while the container is still deciding whether it can start, and four real
// misconfigurations — postgres refusing a data directory holding `lost+found`,
// postgres and mysql with no password set, redis pointed at a config file that
// isn't there — all inspected as "running" at that moment and were gone
// 0.08–0.44s later. A correct postgres was still up thirty seconds on. So the
// window has to be wider than half a second, and a second leaves room for a
// slower machine without waiting on anything a person would notice.
//
// Two looks rather than a poll: sampling more often would report a failure a
// fraction sooner but cannot catch anything the final look misses, and each look
// is a `container inspect` per service (~18ms measured) — real work that scales
// with the size of the project, bought for nothing.
//
// The cost lands on every successful detached `up`, flat: a healthy three-service
// up measured 4.0s and a single-service one 1.0s, so this is a quarter again on
// the first and double the second. That is the price of `up` not reporting
// success over a dead service.
//
// A var, not a const, so the suite can turn the wait off: nearly every `up` eval
// would otherwise pay it for nothing. The evals that are about this window set
// their own — see TestUpWaitsBeforeConcludingAServiceStarted.
var defaultCrashGrace = time.Second

// graceFromEnv reads OPOSSUM_CRASH_GRACE, a duration (`0`, `250ms`, `2s`) saying
// how long `up` watches a service before calling it started. It exists in the
// same spirit as OPOSSUM_CONTAINER_BIN: it is what lets an eval that drives the
// whole CLI reach this setting, and having ~40 of them each sleep a second to
// prove something none of them are about is a waste of everyone's time. Someone
// who wants `up` back to its old, faster, less truthful behaviour can also set it
// to 0. A value that doesn't parse is ignored rather than fatal — this is not
// worth failing an `up` over.
//
// Read per Orchestrator rather than once at startup, so setting it takes effect
// for whatever is constructed afterwards.
func graceFromEnv() time.Duration {
	if v := os.Getenv("OPOSSUM_CRASH_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return defaultCrashGrace
}

// verifyStarted checks that each long-running service just brought up is still
// running, reporting any that exited (with its last log lines) and failing the
// up. One-shots (completed-targets) are expected to exit, so they're skipped.
//
// It looks twice, a grace apart, rather than taking one snapshot. A snapshot
// right after start only catches a container that is already gone, and a service
// dying from bad config is typically still "running" at that instant — which is
// how a postgres refusing to initialise reported a successful `up` and turned up
// `stopped` in `ps` afterwards. It is still a bounded look: something that dies
// minutes later belongs to a healthcheck, not to `up`.
//
// The first look is not merely an optimisation for the report: a project whose
// services are all already gone fails without waiting out the grace at all.
func (o *Orchestrator) verifyStarted(order []string, oneShot map[string]bool) error {
	watching := make([]string, 0, len(order))
	for _, name := range order {
		if !oneShot[name] {
			watching = append(watching, name)
		}
	}
	exited := map[string]string{} // service -> the state it was found in
	for look := 0; ; look++ {
		var still []string
		for _, name := range watching {
			info := o.rt.Inspect(o.containerName(name))
			// `container run -d` returns after the container is running, so a healthy
			// service inspects as "running" here (the OPSM-401 fail-fast keys off the
			// same predicate). Treat a missing/empty state as "keep watching" — only a
			// concrete non-running state (stopped/exited) is a crash. If Apple
			// `container` ever surfaces a transient "created"/"starting" state
			// post-run, revisit.
			if info.Exists && info.State != "" && info.State != "running" {
				exited[name] = info.State
				continue
			}
			still = append(still, name)
		}
		watching = still
		if len(watching) == 0 || look > 0 {
			break
		}
		// Ctrl-C during the grace, like every other wait in `up`. Nothing is rolled
		// back from here — the services are up — but the second between a person
		// pressing it and the prompt coming back is a second this change added.
		//
		// Unlike the waits before this point, an interrupt here is not an error: it
		// only means the second look never happened, and "we did not finish looking"
		// is not "a service crashed". Anything already found dead is still reported
		// below. Those earlier waits return one because there is a bring-up left to
		// undo; by here there is nothing to undo, and the return value is only the
		// exit status.
		if o.interrupted() != nil {
			break
		}
		o.sleep(o.crashGrace)
	}
	if len(exited) == 0 {
		return nil
	}
	// Report in compose order rather than in the order they happened to be caught,
	// so the same failing project reads the same way every time.
	var crashed []string
	for _, name := range order {
		state, ok := exited[name]
		if !ok {
			continue
		}
		if logs := o.rt.CaptureLogs(o.containerName(name), 15); logs != "" {
			// The container's own last lines, indented into a block. This is the
			// one place opossum hands a whole capture to the reader, and the
			// flattening in logf would run fifteen lines together.
			o.warnf(codeServiceExited, "service %q exited right after starting (state %q); its last log lines:\n%s%s\n",
				name, state, ourText(indentLines(logs)), o.crashHint(name, logs))
		} else {
			o.warnf(codeServiceExited, "service %q exited right after starting (state %q) — check its command, image, and mounts\n", name, state)
		}
		crashed = append(crashed, name)
	}
	sort.Strings(crashed)
	return fmt.Errorf("[%s] %d service(s) exited right after starting: %s — see the logs above, fix the cause, and run `opossum up` again",
		codeServiceExited, len(crashed), strings.Join(quoteAll(crashed), ", "))
}

// printPlan lists the exact `container` invocations a dry-run recorded but did
// not run — the argv `up` would have issued, in order. Read-only queries (the
// inspects that resolve recreate/skip) aren't listed; only the mutating commands
// (network create, delete, run, build, …) that a real up would execute.
//
// One command, one line. That holds because logf flattens what a project gave
// it, not because anything here checks — an argument carries whatever the
// compose file put in it, and a newline in there once ended the line early and
// started the rest at column zero.
//
// The listing is for reading, not for running: the argv reaches the runtime as
// an array, and these bytes are already a rendering that lost the argument
// boundaries to a space when the plan was recorded. Anyone wanting to run one of
// these has to quote it themselves either way.
func (o *Orchestrator) printPlan() {
	if len(o.rt.Plan) == 0 {
		return
	}
	o.logf("\nCommands that would run:\n")
	for _, argv := range o.rt.Plan {
		o.logf("  %s %s\n", o.rt.Bin, argv)
	}
}

// hostPublishAddrs turns published port specs into host-facing "addr:port"
// strings the user can reach from the host (e.g. "localhost:4200"). Specs with
// only a container port (runtime-assigned host port) are skipped, since the host
// port isn't known here. A protocol suffix (/tcp, /udp) only ever attaches to the
// container port, and we emit only the host part, so it never reaches the output.
func hostPublishAddrs(ports []string) []string {
	var out []string
	for _, p := range ports {
		spec := strings.TrimSpace(p)
		// Drop a trailing /proto (only ever on the container port).
		if i := strings.LastIndexByte(spec, '/'); i >= 0 {
			spec = spec[:i]
		}
		// Format is [[IP:]HOST:]CONTAINER, parsed right-to-left so an IPv6 IP
		// (which itself contains colons) doesn't confuse the split. The last
		// segment is the container port; drop it.
		i := strings.LastIndexByte(spec, ':')
		if i < 0 {
			continue // container-only: the host port is runtime-assigned, unknown
		}
		hostPart := spec[:i] // [IP:]HOST
		host, hostPort := "localhost", hostPart
		if j := strings.LastIndexByte(hostPart, ':'); j >= 0 {
			// An IP is present; the host port is the last segment.
			hostPort = hostPart[j+1:]
			if ip := strings.Trim(hostPart[:j], "[] "); ip != "" && ip != "0.0.0.0" && ip != "::" {
				host = ip
			}
		}
		if hostPort = strings.TrimSpace(hostPort); hostPort == "" {
			continue
		}
		if strings.Contains(host, ":") { // bracket an IPv6 host so addr:port is clear
			host = "[" + host + "]"
		}
		out = append(out, host+":"+hostPort)
	}
	return out
}

// remapAutoHostPorts moves a published port opossum chose itself, when the port
// it chose is already taken.
//
// Compose says a container-only entry (`ports: ["3000"]`) leaves the host port to
// the engine, and docker picks a free one. Apple `container` has no such option,
// so opossum mirrors the container port — predictable, and right nearly always —
// but a mirror is a guess, and a taken port turns a project docker compose would
// have started into a hard failure. So the guess is retried rather than fatal.
//
// Only mirrored entries move. An explicit `"3000:3000"` is a contract the user
// wrote down, so it still fails loudly (checkHostPorts) rather than quietly
// listening somewhere else.
func (o *Orchestrator) remapAutoHostPorts(order []string) {
	// Host ports this project has already spoken for. Probing the OS alone isn't
	// enough: two services that both declare `ports: ["3000"]` would each find
	// 3000 free and both take it, and only the second would fail — at bind time,
	// far from the compose file that caused it. docker compose gives them
	// different ports, so track what's been handed out here too.
	claimed := map[string]bool{}
	note := func(spec string) {
		if _, addr, _, ok := hostPortBinding(spec); ok {
			claimed[addr] = true
		}
	}
	for _, name := range order {
		svc := o.Project.Services[name]
		if svc == nil {
			continue
		}
		if len(svc.AutoHostPort) == 0 {
			for _, spec := range svc.Ports {
				note(spec) // an explicit mapping still occupies the port
			}
			continue
		}
		// What this service's own RUNNING container publishes. Reusing those keeps
		// the port — and so the config hash — stable across re-ups. It must be
		// running: a stopped container doesn't hold its ports, so reusing them
		// would hand back a port something else may have taken meanwhile, which is
		// exactly the failure this function exists to prevent.
		held := map[string]int{} // "<container port>/<proto>" -> host port we published
		if info := o.rt.Inspect(o.containerName(name)); info.Exists &&
			info.State == "running" && info.Labels[projectLabel] == o.Project.Name {
			for _, pm := range info.Ports {
				held[fmt.Sprintf("%d/%s", pm.ContainerPort, pm.Proto)] = pm.HostPort
			}
		}
		for i, spec := range svc.Ports {
			if !svc.AutoHostPort[spec] {
				note(spec)
				continue
			}
			network, address, port, ok := hostPortBinding(spec)
			if !ok {
				continue // no fixed host port to move (a range)
			}
			key := fmt.Sprintf("%d/%s", specContainerPort(spec), network)
			if h, sticky := held[key]; sticky && h != 0 {
				// Keep what we already published, even though it reads as in use —
				// it is in use by this very container.
				if newSpec, ok := withHostPort(spec, h); ok {
					svc.Ports[i] = newSpec
					delete(svc.AutoHostPort, spec)
					svc.AutoHostPort[newSpec] = true
					note(newSpec)
					continue
				}
			}
			if !claimed[address] && !hostPortInUse(network, address) {
				note(spec) // the mirror is free: keep it, nothing to explain
				continue
			}
			free, err := freeHostPort(network, address)
			if err != nil {
				continue // fall through to checkHostPorts' error, no worse than before
			}
			newSpec, ok := withHostPort(spec, free)
			if !ok {
				continue
			}
			svc.Ports[i] = newSpec
			delete(svc.AutoHostPort, spec)
			svc.AutoHostPort[newSpec] = true
			note(newSpec)
			o.warnf(codeHostPortRemapped, "service %q publishes container port %s, and the compose file "+
				"doesn't say which host port to use — host port %s is taken, so opossum published it on %d "+
				"instead. docker compose picks a free port here too. Run `opossum ps` for the ports actually "+
				"in use; to pin one, write it in the compose file as \"<host>:%d\".\n",
				name, port, port, free, specContainerPort(spec))
		}
	}
}

// specContainerPort returns the container-side port of a normalized spec, or 0.
func specContainerPort(spec string) int {
	s := spec
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return 0
	}
	return n
}

// withHostPort rewrites a normalized spec's host port, keeping any host IP and
// protocol suffix. ok is false for a spec it can't rewrite safely (a range).
func withHostPort(spec string, host int) (string, bool) {
	s, proto := spec, ""
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		proto, s = s[i:], s[:i]
	}
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", false
	}
	container := s[i+1:]
	if strings.ContainsAny(container, "-") { // a range can't be remapped to one port
		return "", false
	}
	rest := s[:i] // "host" or "ip:host"
	j := strings.LastIndexByte(rest, ':')
	prefix := ""
	if j >= 0 {
		prefix = rest[:j+1] // keep "ip:"
	}
	return fmt.Sprintf("%s%d:%s%s", prefix, host, container, proto), true
}

// freeHostPort asks the OS for an unused port by binding to :0 and releasing it.
// There is a window between this and the container binding it, the same one
// docker lives with; losing that race just yields the runtime's own bind error,
// which is where an unremapped port would have ended up anyway.
func freeHostPort(network, address string) (int, error) {
	// Ask on the same scope the spec publishes on: a port free on loopback can be
	// held on another interface, and a wildcard publish would then fail at bind.
	probe := "127.0.0.1:0"
	if isWildcardAddr(address) {
		probe = ":0"
		network += "4" // wildcards are published on IPv4 (see probeNetworks)
	}
	if strings.HasPrefix(network, "udp") {
		c, err := net.ListenPacket(network, probe)
		if err != nil {
			return 0, err
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port, nil
	}
	l, err := net.Listen(network, probe)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// checkHostPorts fails if any service's published host port is already in use,
// with a clearer message (and a macOS AirPlay hint) than the runtime's raw
// "Address already in use" that appears mid-startup after a partial rollback.
func (o *Orchestrator) checkHostPorts(order []string) error {
	seen := map[string]bool{}
	var conflicts []string
	for _, name := range order {
		// Skip a service whose own running container already holds these ports:
		// `up` will delete and recreate it (freeing them), so a re-up must not
		// mistake its own published ports for a foreign conflict.
		if info := o.rt.Inspect(o.containerName(name)); info.Exists &&
			info.State == "running" && info.Labels[projectLabel] == o.Project.Name {
			continue
		}
		for _, spec := range o.Project.Services[name].Ports {
			network, address, port, ok := hostPortBinding(spec)
			if !ok || seen[network+" "+address] {
				continue
			}
			seen[network+" "+address] = true
			if hostPortInUse(network, address) {
				conflicts = append(conflicts, fmt.Sprintf("%s/%s (service %q)%s", port, network, name, airPlayHint(port)))
			}
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("[%s] host port already in use:\n  - %s\nfree the port or remap it in the compose file, then retry",
			codeHostPortInUse, strings.Join(conflicts, "\n  - "))
	}
	return nil
}

// hostPortBinding returns the host-side listen network and address for a
// published port spec (e.g. "tcp", ":5000"), so opossum can probe whether it's
// free. ok is false for specs with no fixed host port (container-only) or a port
// range, which it can't meaningfully probe.
func hostPortBinding(spec string) (network, address, port string, ok bool) {
	network = "tcp"
	s := strings.TrimSpace(spec)
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		if p := strings.ToLower(s[i+1:]); p == "tcp" || p == "udp" {
			network = p
		}
		s = s[:i]
	}
	i := strings.LastIndexByte(s, ':') // drop the container port (last segment)
	if i < 0 {
		return "", "", "", false // container-only: host port is runtime-assigned
	}
	hostPart, host := s[:i], ""
	port = hostPart
	if j := strings.LastIndexByte(hostPart, ':'); j >= 0 {
		port = hostPart[j+1:]
		if ip := strings.Trim(hostPart[:j], "[] "); ip != "" && ip != "0.0.0.0" && ip != "::" {
			host = ip
		}
	}
	if port = strings.TrimSpace(port); port == "" || strings.Contains(port, "-") {
		return "", "", "", false // dynamic or a range
	}
	return network, net.JoinHostPort(host, port), port, true
}

// hostPortInUse reports whether the host address can't be bound (already taken).
// A wildcard probe must name the address family. Asking for "tcp" on a wildcard
// address gets a dual-stack IPv6 socket, and on macOS that binds happily
// alongside an existing IPv4-only listener — so the port reads as free while
// something is very much using it. That is the case the check most needs to
// catch: the daemons that squat ports (AirPlay's receiver on 5000/7000 among
// them) listen on IPv4.
func hostPortInUse(network, address string) bool {
	for _, n := range probeNetworks(network, address) {
		if portBusy(n, address) {
			return true
		}
	}
	return false
}

// probeNetworks picks the families to test. A wildcard is probed as IPv4 only,
// because that is what Apple `container` publishes on: an IPv4 probe already
// fails against a dual-stack listener, so adding an IPv6 probe would detect
// nothing extra — it would only report a conflict for an IPv6-only listener,
// which the runtime happily binds alongside. Reporting that would turn a project
// that starts fine into a refusal. An address that names a host carries its own
// family, so it is probed once, as given.
func probeNetworks(network, address string) []string {
	if !isWildcardAddr(address) {
		return []string{network}
	}
	if strings.HasPrefix(network, "udp") {
		return []string{"udp4"}
	}
	return []string{"tcp4"}
}

// portBusy reports whether binding address on network fails. Any bind error
// counts as "in use": the probe can't tell EADDRINUSE from a rarer refusal, and
// erring toward "taken" turns a would-be silent startup failure into the clearer
// pre-flight message.
func portBusy(network, address string) bool {
	if strings.HasPrefix(network, "udp") {
		c, err := net.ListenPacket(network, address)
		if err != nil {
			return true
		}
		c.Close()
		return false
	}
	l, err := net.Listen(network, address)
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// isWildcardAddr reports whether an address names no specific host, in any of the
// spellings that mean "every interface" — so the probe covers the family the
// runtime will actually bind rather than trusting one dual-stack bind.
func isWildcardAddr(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return host == "" || host == "0.0.0.0" || host == "::"
}

// airPlayHint flags the ports macOS's AirPlay Receiver commonly holds, a frequent
// surprise when a compose publishes host port 5000.
func airPlayHint(port string) string {
	if port == "5000" || port == "7000" {
		return " — on macOS this is often the AirPlay Receiver; turn it off in" +
			" System Settings › General › AirDrop & Handoff, or remap the host port"
	}
	return ""
}

// selectServices filters the full startup order down to the requested services
// and all their transitive dependencies, preserving dependency order. With no
// request it returns the full order unchanged. Unknown service names are an error.
func (o *Orchestrator) selectServices(order, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return order, nil
	}
	for _, r := range requested {
		if _, ok := o.Project.Services[r]; !ok {
			return nil, o.unknownServiceErr(r)
		}
	}
	want := map[string]bool{}
	if o.up.noDeps {
		// Scope to exactly the requested services: their dependencies are already
		// running (rebuild-on-watch must not rebuild/recreate a dependency).
		for _, r := range requested {
			want[r] = true
		}
	} else {
		var visit func(name string)
		visit = func(name string) {
			if want[name] {
				return
			}
			want[name] = true
			// want is set before the walk goes on, so a cycle among services
			// this command does not read — which is not refused — is walked
			// once and no further.
			for _, dep := range o.Project.Services[name].DependsOn {
				visit(dep.Name)
			}
		}
		for _, r := range requested {
			visit(r)
		}
	}
	out := make([]string, 0, len(want))
	for _, name := range order {
		if want[name] {
			out = append(out, name)
		}
	}
	return out, nil
}

// ensureNotForeign refuses to reuse a container name that belongs to a different
// opossum project. Two projects sharing a DNS domain would name a service the
// same (e.g. db.opossum); without this guard opossum's stale-cleanup Delete would
// silently destroy the other project's container. A container of the name that
// carries no project label at all was made outside opossum (every service
// container and one-off opossum makes carries one), so it is refused too, as
// docker compose v5.5.0 refuses a container of the name without its labels
// (`Conflict. The container name … is already in use`). A missing container is
// safe to use — a container whose owner the runtime could not be asked about is
// not: reusing that name would force-delete it unseen, whoever it belongs to
// (measured on container 1.4.1: with only that container's inspect failing,
// `up` deleted another project's container and ran its own in its place,
// saying nothing).
func (o *Orchestrator) ensureNotForeign(cname, command string) error {
	owner, unlabeled, unknown := o.otherOwner(cname)
	if unknown {
		return ownerRefusal{unanswered: []string{cname}, err: fmt.Errorf("container %q: the runtime gave no readable answer about which project owns it, so it is left alone; "+
			"`container inspect %s` shows what the runtime says — run `%s` again once it answers, "+
			"or, if it belongs to another project, give this project its own DNS domain (e.g. --dns-domain %s)", cname, cname, command, o.Project.Name)}
	}
	if unlabeled {
		return ownerRefusal{err: fmt.Errorf("container %q already exists and carries no %s label, so it was not made by this project and is left alone; "+
			"remove it (`container delete --force %s`) to free the name, or give this project its own DNS domain (e.g. --dns-domain %s)",
			cname, projectLabel, cname, o.Project.Name)}
	}
	if owner != "" {
		return ownerRefusal{err: fmt.Errorf("container %q is already in use by project %q; give this project its own DNS domain so names don't collide "+
			"(e.g. --dns-domain %s, created once with `sudo container system dns create %s`) — see README (multi-project)",
			cname, owner, o.Project.Name, o.Project.Name)}
	}
	return nil
}

// ownerRefusal is a command declining a container because it is another
// project's, or because the runtime would not say whose it is. The message
// carries its own next step; a caller that adds advice about a failed action
// (a container that may be gone, one that may not be running) checks for this
// first, since nothing failed and that advice would point the wrong way.
// unanswered names the containers the runtime would not say anything about;
// empty when the refusal is another project's container.
type ownerRefusal struct {
	err        error
	unanswered []string
}

func (r ownerRefusal) Error() string { return r.err.Error() }

// otherOwner reads, from one inspect, whether a container name is this
// project's to stop, delete or reuse: owner is the other project whose label is
// on it; unlabeled that it is there with no project label at all, made outside
// opossum; unknown that the runtime gave no readable answer. A missing
// container is none of them. One inspect for all three answers: a second call
// could fail after the first answered, or answer after the first failed.
func (o *Orchestrator) otherOwner(cname string) (owner string, unlabeled, unknown bool) {
	return ownerOf(o.rt.Inspect(cname), o.Project.Name)
}

// ownerOf reads a look at a container: whose project it is, whether it carries
// no project label at all, and whether the runtime would not say.
func ownerOf(info runtime.ContainerInfo, project string) (owner string, unlabeled, unknown bool) {
	if info.Unknown {
		return "", false, true
	}
	if !info.Exists {
		return "", false, false
	}
	switch proj := info.Labels[projectLabel]; {
	case proj == "":
		return "", true, false
	case proj != project:
		return proj, false, false
	}
	return "", false, false
}

// withContainers keeps the services whose container ownContainers found: the
// names of the ones that are there, taken from the same look, so nothing is
// asked of the runtime twice.
func (o *Orchestrator) withContainers(services, created []string) []string {
	there := make(map[string]bool, len(created))
	for _, cname := range created {
		there[cname] = true
	}
	kept := make([]string, 0, len(services))
	for _, name := range services {
		if there[o.containerName(name)] {
			kept = append(kept, name)
		}
	}
	return kept
}

// worksOn reports whether the command should work on this service: the
// container is this project's, and — for a command working over the whole
// project rather than services it was given — there. Such a command passes by
// a service nobody has started: one behind a profile that was never turned on,
// or one an `up <service>` did not reach. There is nothing to stop, start or
// read the logs of, and docker compose v5.5.1 exits 0 over such a project and
// says nothing about that service (measured, #1096), where opossum used to
// fail on it — or say it was stopping a container that was not there.
//
// A service the command was given by name goes through ours instead: what the
// answer should be when someone asks for a service that is not running is a
// question of its own (#1098), and silence there would hide a name typed
// wrong.
//
// A container made between this look and the work is not worked on, which is
// what docker compose does: its `stop` and `logs` act on the containers the
// look found. `down` is the one that still asks about every service either
// way — it promises a project that is gone, not a project as it was a moment
// ago.
func (o *Orchestrator) worksOn(name string, whole bool, unanswered *[]string) bool {
	cname := o.containerName(name)
	info := o.rt.Inspect(cname)
	if !o.oursByInfo(cname, info, unanswered) {
		return false
	}
	return !whole || info.Exists
}

// ours says whether the container name is this project's to stop, start, kill
// or delete. Another project's container, and one that carries no project
// label (made outside opossum), is left and said; one the runtime gives no
// readable answer about is left and added to unanswered, for the command to
// name when it is done. A missing container is ours: there is nothing there
// to take from anyone.
func (o *Orchestrator) ours(cname string, unanswered *[]string) bool {
	return o.oursByInfo(cname, o.rt.Inspect(cname), unanswered)
}

// oursByInfo is ours over a look already taken: the commands that need to know
// both who owns the container and whether it is there ask the runtime once and
// answer both from that, because two looks can disagree about a container that
// is starting or ending between them.
func (o *Orchestrator) oursByInfo(cname string, info runtime.ContainerInfo, unanswered *[]string) bool {
	owner, unlabeled, unknown := ownerOf(info, o.Project.Name)
	switch {
	case unknown:
		*unanswered = append(*unanswered, cname)
		return false
	case unlabeled:
		o.logf("Leaving container %s alone: it carries no %s label, so it was not made by this project (remove it with `container delete --force %s` for this project to use the name)\n", cname, projectLabel, cname)
		return false
	case owner != "":
		o.logf("Leaving container %s alone: it belongs to project %q (give this project its own DNS domain so names don't collide)\n", cname, owner)
		return false
	}
	return true
}

// unansweredOwners is the error for the containers a command left because the
// runtime gave no readable answer about who owns them; nil when there were none.
func unansweredOwners(unanswered []string, command string) error {
	if len(unanswered) == 0 {
		return nil
	}
	return ownerRefusal{unanswered: unanswered, err: fmt.Errorf("the runtime gave no readable answer about which project owns %d container(s), so they were left: %s — "+
		"`container inspect <name>` shows what the runtime says; run `%s` again once it answers",
		len(unanswered), strings.Join(unanswered, ", "), command)}
}

// requiredToComplete is whether some dependent in the file needs name to run
// to completion and requires it (`required` not written false); with every
// such dependent optional, a failure to complete is noted and passed over.
// Every dependent in the file counts, as completedTargets counts every one
// when deciding what runs to completion: a required dependent behind a
// profile that is not active keeps the failure fatal here, where docker
// compose (v5.5.1, measured) reads only the dependents it starts and goes
// on — a known difference, kept so that the two sets are one.
func (o *Orchestrator) requiredToComplete(name string) bool {
	for _, svc := range o.Project.Services {
		for _, dep := range svc.DependsOn {
			if dep.Name == name && dep.Condition == compose.ConditionCompleted && !dep.Optional {
				return true
			}
		}
	}
	return false
}

// completedTargets is the set of services that some dependent needs to run to
// completion (depends_on condition: service_completed_successfully).
func (o *Orchestrator) completedTargets() map[string]bool {
	m := map[string]bool{}
	for _, svc := range o.Project.Services {
		for _, dep := range svc.DependsOn {
			if dep.Condition == compose.ConditionCompleted {
				m[dep.Name] = true
			}
		}
	}
	return m
}

// awaitHealthyDeps blocks until every dependency of svc that is declared
// `condition: service_healthy` passes its healthcheck. Dependencies come earlier
// in the startup order, so they are already running by the time we probe them.
func (o *Orchestrator) awaitHealthyDeps(name string, svc *compose.Service) error {
	for _, dep := range svc.DependsOn {
		if dep.Condition != compose.ConditionHealthy {
			continue
		}
		hc := o.Project.Services[dep.Name].Healthcheck
		// Same trio as the load-time check in compose.validateDeps, and kept in step
		// with it deliberately: hc.Disabled is redundant while both spellings of "off"
		// clear the test, and load-bearing the moment one stops.
		if hc == nil || hc.Disabled || len(hc.Test) == 0 {
			// Load-time validation rejects this; guard defensively anyway.
			o.warnf(codeDepNoHealth, "%s wants %s healthy but it has no healthcheck — not waiting\n", name, dep.Name)
			continue
		}
		o.logf("Waiting for %s to be healthy\n", dep.Name)
		if err := o.waitHealthy(dep.Name, hc); err != nil {
			if dep.Optional {
				// Waited for all the same, then passed over: docker compose
				// (v5.5.1, measured) waits the whole healthcheck out and
				// starts the dependent anyway.
				o.warnf(codeOptionalDependency, "optional dependency %q of service %q is not healthy: %v — starting %s anyway\n", dep.Name, name, err, name)
				continue
			}
			return fmt.Errorf("dependency %q for service %q: %w", dep.Name, name, err)
		}
	}
	return nil
}

// defaultProbeTimeout bounds a healthcheck attempt when the compose sets no (or a
// non-positive) timeout — matching docker compose, where `0` means "use the
// default", not "run unbounded". Without this a hung probe could still block
// `up` forever on a `timeout: 0s` (#139).
const defaultProbeTimeout = 30 * time.Second

func probeTimeout(hc *compose.Healthcheck) time.Duration {
	if hc.Timeout <= 0 {
		return defaultProbeTimeout
	}
	return hc.Timeout
}

// waitHealthy runs a service's healthcheck via `container exec`, retrying up to
// Retries times with Interval between attempts, after an initial StartPeriod.
func (o *Orchestrator) waitHealthy(name string, hc *compose.Healthcheck) error {
	cname := o.containerName(name)
	if hc.StartPeriod > 0 {
		o.sleep(hc.StartPeriod)
	}
	attempts := hc.Retries
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		// A Ctrl-C during a long health wait should abort into rollback, not keep
		// probing.
		if err := o.interrupted(); err != nil {
			return err
		}
		if i > 0 {
			o.sleep(hc.Interval)
		}
		if err := o.rt.Exec(cname, hc.Test, probeTimeout(hc)); err == nil {
			return nil
		} else {
			last = err
		}
		// If the container has exited, it won't recover by polling — fail fast
		// with the real cause instead of an opaque "healthcheck did not pass".
		if info := o.rt.Inspect(cname); info.Exists && info.State != "" && info.State != "running" {
			// Grab its last logs now, before a failed-up rollback removes the
			// container — otherwise the usual "check `opossum logs`" hint points at a
			// container that's already gone.
			if logs := o.rt.CaptureLogs(cname, 15); logs != "" {
				return fmt.Errorf("[%s] container is not running (state %q); its last log lines:\n%s%s",
					codeDepNotRunning,
					info.State, indentLines(logs), o.crashHint(name, logs))
			}
			return fmt.Errorf("[%s] container is not running (state %q)", codeDepNotRunning, info.State)
		}
	}
	return fmt.Errorf("healthcheck did not pass after %d attempt(s): %w", attempts, last)
}

// Down stops and removes every service in reverse dependency order, deletes the
// project network, and — when removeVolumes is set — removes the project's named
// volumes.
func (o *Orchestrator) Down(removeVolumes bool, rmi string, removeOrphans bool) error {
	// Same lock as Up: a `down` under an `up` would remove what the `up` is
	// starting, and the `up` would report it started.
	lock, err := lockProject(o.Project.Name)
	if err != nil {
		return err
	}
	defer lock.release()
	// Before anything is torn down: a supervisor watching this project would see
	// containers stopping and try to bring them back, fighting the teardown it
	// can't know about.
	if StopSupervisor(o.Project.Name) {
		o.logf("Stopped the restart supervisor\n")
	}
	// …and forget what it was watching: the stack is coming down, so a later
	// `up <service>` has nothing here to carry over. This is the call that counts —
	// the one `down` makes before the compose file is read can only work the
	// project name out without the file (`-p`, COMPOSE_PROJECT_NAME, the
	// directory), which is wrong whenever the file names it.
	ClearWatched(o.Project.Name)
	order, err := o.startupOrder()
	if err != nil {
		return err
	}
	// Only this project's containers are stopped and deleted. The names come from
	// the compose file, and a container of another project can carry the same one
	// — with `--dns-domain ""` every project names its containers by the bare
	// service name (measured on container 1.4.1: `down` stopped and deleted a
	// running container labeled for another project, from a project never brought
	// up). A name held by another project, one that carries no project label
	// (made outside opossum, which `up` refuses to reuse), or one the runtime
	// gives no readable answer about, is left and said.
	var unanswered []string
	mine := func(cname string) bool { return o.ours(cname, &unanswered) }
	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]
		cname := o.containerName(name)
		info := o.rt.Inspect(cname)
		if o.oursByInfo(cname, info, &unanswered) {
			// Said only for a container that is there: a service nobody
			// started has nothing to stop, and saying so reads as work done
			// (#1096). The stop and the delete are asked for either way —
			// they are best-effort, and a container that appeared between the
			// look and here must still come down.
			if info.Exists {
				o.logf("Stopping %s\n", name)
			}
			o.rt.Stop(cname)
			o.rt.Delete(cname)
		}
		// Also clear any leftover one-off container from `run` (no --rm).
		if run := o.containerName(name + "-run"); mine(run) {
			o.rt.Delete(run)
		}
	}
	// Containers for services no longer in the compose are removed with
	// --remove-orphans (docker compose parity).
	if removeOrphans {
		o.removeOrphans(o.orphans())
	}
	// Remove the default project net and every declared network opossum created
	// (skipping external ones, which it never owns). Deletion is best-effort and
	// silent when a network is already gone or still in use.
	o.rt.DeleteNetwork(o.networkName())
	for _, net := range o.declaredNetworks() {
		o.rt.DeleteNetwork(net)
	}
	if removeVolumes {
		for _, v := range o.namedVolumes() {
			o.logf("Removing volume %s\n", v)
			o.rt.DeleteVolume(v)
		}
	}
	if rmi == "local" || rmi == "all" {
		o.removeImages(order, rmi == "all")
	}
	return unansweredOwners(unanswered, "opossum down")
}

// removeImages deletes the services' images after teardown. "local" removes the
// images under opossum's own name for them, `<project>-<service>:latest`; "all"
// also removes the images services name. Deduped and best-effort.
//
// "local" goes by that name and no other. Under it is what a build of this
// project made, and nothing else gives an image that name — which cannot be
// said of a name `image:` gives: the same name may hold something pulled, tagged
// by hand or built by another project, and `up` builds only when nothing is
// there, so a file with `build:` can be up without anything having been built.
// docker compose tells its own builds apart by a label and removes those
// (measured on v5.5.1); doing the same here is its own change (#1126), and
// until then an image built under an `image:` name is left by "local" and
// removed by "all".
//
// A service that names its image still had `<project>-<service>:latest` before
// `image:` was read, and a project brought up back then has its image under
// it. Nothing else reads that name now, so it is cleared out here — when it is
// there — rather than left behind for good.
func (o *Orchestrator) removeImages(order []string, all bool) {
	seen := map[string]bool{}
	remove := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		o.logf("Removing image %s\n", ref)
		o.rt.DeleteImage(ref)
	}
	for _, name := range order {
		svc := o.Project.Services[name]
		if former := o.formerBuiltImage(name, svc); former != "" && o.rt.ImageExists(former) {
			remove(former)
		}
		// opossum's own name for this service's image — which is also what a
		// file that spells it out as `image:` has.
		ref, built := o.serviceImage(name, svc)
		if all || (built && ref == o.builtImageDefaultName(name)) {
			remove(ref)
		}
	}
}

// serviceImage is the image reference opossum uses for a service, and the one
// place the name of a built image is put together: what builds, looks for,
// runs, shows (`ps`, `images`), removes (`down --rmi`, `destroy`) and imports a
// service's image asks here, because one that built under one name and removed
// under another would leave the image behind. (What reads `image:` for what it
// says about the image — `pull`, the image-side checks — reads the field
// itself; for a service that builds and names its image the two are the same.)
// built reports whether the service has a build, which is not whether anything
// was built: `up` builds only when no image of that name is there.
//
// A service with a build is built under the name its `image:` gives, as docker
// compose builds it (measured on v5.5.1), and as `<project>-<service>:latest`
// when it gives none. That is what lets a second service use the first one's
// image by name: it used to be built as `<project>-<service>:latest` whatever
// `image:` said, so the second went to a registry for a name that was only
// ever local. The name is handed over as written — the runtime reads one with
// no tag as `:latest` when it builds, inspects and runs (measured on 1.4.1), as
// docker compose does.
func (o *Orchestrator) serviceImage(name string, svc *compose.Service) (ref string, built bool) {
	if svc.Build == nil {
		return svc.Image, false
	}
	if svc.Image != "" {
		return svc.Image, true
	}
	return o.builtImageDefaultName(name), true
}

// builtImageDefaultName is what a built image is called when the service gives
// it no name.
func (o *Orchestrator) builtImageDefaultName(name string) string {
	return o.Project.Name + "-" + name + ":latest"
}

// formerBuiltImage is the name a service's built image had before `image:` was
// read — `<project>-<service>:latest` — for a service that builds and names its
// image, and "" otherwise. A project brought up before the change has its image under it,
// and nothing else reads that name now, so the commands that remove a project's
// images clear it out as well rather than leave it behind for good.
func (o *Orchestrator) formerBuiltImage(name string, svc *compose.Service) string {
	if svc.Build == nil || svc.Image == "" {
		return ""
	}
	// Where `image:` spells out that very name there is one image, not two;
	// both callers take each name once, so nothing here has to tell.
	return o.builtImageDefaultName(name)
}

// ImagesOptions selects how `opossum images` shows each service's image.
type ImagesOptions struct {
	Format string // "table" (default) or "json"
}

// ImageStatus is one row of `opossum images`: a service's image, whether
// opossum built it or pulled it, and whether it's present locally.
type ImageStatus struct {
	Service string `json:"Service"`
	Image   string `json:"Image"`
	Source  string `json:"Source"` // "built" or "pulled"
	Present bool   `json:"Present"`
}

// imageStatuses gathers one ImageStatus per service, in startup order —
// the data behind both the table and the JSON array Images prints.
func (o *Orchestrator) imageStatuses() ([]ImageStatus, error) {
	// Same reasoning as Ps: a stopped daemon makes every `image inspect` fail, which
	// would print a confident `PRESENT=no` for images that may well be present. Probe
	// first so `PRESENT` reflects reality rather than "couldn't ask the runtime".
	if !o.rt.SystemRunning() {
		return nil, ErrRuntimeStopped()
	}
	order, err := o.startupOrder()
	if err != nil {
		return nil, err
	}
	rows := make([]ImageStatus, 0, len(order))
	for _, name := range order {
		ref, built := o.serviceImage(name, o.Project.Services[name])
		source := "pulled"
		if built {
			source = "built"
		}
		rows = append(rows, ImageStatus{
			Service: name,
			Image:   ref,
			Source:  source,
			Present: ref != "" && o.rt.ImageExists(ref),
		})
	}
	return rows, nil
}

// Images lists each service's image, whether opossum builds it, and whether
// it's present locally — the image-side counterpart to Ps — as a table, or a
// JSON array under Format "json".
func (o *Orchestrator) Images(opts ImagesOptions) error {
	rows, err := o.imageStatuses()
	if err != nil {
		return err
	}
	switch opts.Format {
	case "json":
		b, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		fmt.Fprintln(o.out, string(b))
		return nil
	default:
		tw := tabwriter.NewWriter(o.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tIMAGE\tSOURCE\tPRESENT")
		for _, r := range rows {
			present := "no"
			if r.Present {
				present = "yes"
			}
			row(tw, r.Service, dash(r.Image), r.Source, present)
		}
		return tw.Flush()
	}
}

// volumeName is the runtime name of a project volume. A declaration with a
// `name:` keeps that name — docker compose creates the volume under it, not
// under the project's prefix, which is how a file fixes a volume's name for a
// backup script or a second project to find (#937). Otherwise the volume is
// namespaced by project, matching docker compose's `<project>_<volume>`
// convention, so concurrent projects that share a volume key don't collide on
// a single global volume — the same isolation the `<service>.<project>.<domain>`
// container naming already gives (see #9/#63). Every path that names a project
// volume — the `-v` of `up` and `run`, seeding, `down -v`, `destroy`, `volumes`
// — goes through here, so what is created is what is removed.
func (o *Orchestrator) volumeName(src string) string {
	if n := o.Project.Volumes[src].Name; n != "" {
		return n
	}
	return o.Project.Name + "_" + compose.VolumeDisplayName(src)
}

// secretMounts renders a service's file-based secret references as read-only
// bind mounts at /run/secrets/<target> — the path official images read via
// their *_FILE env vars (e.g. POSTGRES_PASSWORD_FILE). Refs are validated
// against the project's file-based secrets at load time (#76).
func (o *Orchestrator) secretMounts(svc *compose.Service) []string {
	var out []string
	for _, ref := range svc.Secrets {
		sec := o.Project.Secrets[ref.Source]
		out = append(out, o.resolvePath(sec.File)+":/run/secrets/"+ref.Target+":ro")
	}
	return out
}

// configMounts renders a service's config references as read-only bind
// mounts at their targets, as docker compose places configs: a `file:`
// config is the host file itself; a `content:` or `environment:` config is
// written to a file under the project's state directory first (the text as
// written in the compose file — interpolated with the rest of it — or the
// variable's value the load read from the project's environment), then
// mounted the same way. Refs are
// validated against the project's configs at load time. With write false
// (a dry run) nothing is written and the path the file would have is used.
func (o *Orchestrator) configMounts(service string, svc *compose.Service, write bool) ([]string, error) {
	var out []string
	for _, ref := range svc.Configs {
		cfg := o.Project.Configs[ref.Source]
		var src string
		switch {
		case cfg.File != "":
			src = o.resolvePath(cfg.File)
		default:
			var body []byte
			if cfg.Content != nil {
				body = []byte(*cfg.Content)
			} else {
				if !cfg.EnvSet {
					return nil, fmt.Errorf("service %q: environment variable %q required by config %q is not set — set it in the shell or in .env, or declare the config with a file: or content:", service, cfg.EnvVar, ref.Source)
				}
				body = []byte(cfg.EnvValue)
			}
			dir, err := projectStateDir(o.Project.Name)
			if err != nil {
				return nil, err
			}
			src = filepath.Join(dir, "configs", service, ref.Source)
			if write {
				if err := writeConfigFile(src, body); err != nil {
					return nil, fmt.Errorf("service %q: writing config %q: %w", service, ref.Source, err)
				}
			}
		}
		out = append(out, src+":"+ref.Target+":ro")
	}
	return out, nil
}

// writeConfigFile puts body at path as a read-only (0444) file. The file
// opossum wrote last time is read-only too, so it cannot be opened for
// writing: the new content goes to a temporary file in the same directory
// and is renamed over it, which also means a second reference to the same
// config in one `up`, or the next `up`, finds the file whole rather than
// half-written. The temporary name is random, not `<name>.tmp`: config
// names share this directory, and `x.tmp` is a name docker compose takes.
func writeConfigFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".opossum-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o444); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// isNamedVolume reports whether a volume mount's source is a named volume (not a
// bind-mount host path and not empty). resolveVolumes (startup) and namedVolumes
// (down -v) share this predicate so the name opossum creates and the name it
// removes stay symmetric.
func isNamedVolume(src string) bool {
	return src != "" && !isHostPath(src)
}

// sortByVolumeName orders the loader's volume keys by the names written in the
// compose file, byte for byte. A key for a name that starts with `.` carries a
// NUL prefix, so sorting the keys themselves put `.hid` before `-m`. (This is
// not the order `config` prints its map keys in — YAML's, with `db9` before
// `db10` — nor, for a volume with `name:` or an external one, the order
// `volumes` lists runtime names in.)
func sortByVolumeName(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		return compose.VolumeDisplayName(keys[i]) < compose.VolumeDisplayName(keys[j])
	})
}

// warnSharedNamedVolumes warns when two or more services being started mount the
// same named volume. Apple `container` attaches a named volume as an exclusive
// block device, so only the first service to start gets it and the others fail to
// bootstrap ("The storage device attachment is invalid"). Docker shares named
// volumes; bind mounts (host paths) are shareable here too.
func (o *Orchestrator) warnSharedNamedVolumes(order []string) {
	// A one-shot (service_completed_successfully target) runs to completion and
	// frees its volume before dependents start, so it can legitimately share a
	// named volume (e.g. an init/seed step) — don't count it as a concurrent user.
	oneShot := o.completedTargets()
	users := map[string][]string{}
	for _, name := range order {
		if oneShot[name] {
			continue
		}
		seen := map[string]bool{}
		for _, v := range o.Project.Services[name].Volumes {
			src := strings.SplitN(v, ":", 2)[0]
			if isNamedVolume(src) && !seen[src] {
				seen[src] = true
				users[src] = append(users[src], name)
			}
		}
	}
	var shared []string
	for src, svcs := range users {
		if len(svcs) >= 2 {
			shared = append(shared, src)
		}
	}
	sortByVolumeName(shared)
	for _, src := range shared {
		svcs := append([]string(nil), users[src]...)
		sort.Strings(svcs)
		quoted := make([]string, len(svcs))
		for i, s := range svcs {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		o.warnf(codeSharedVolume, "services %s share named volume %q, but Apple container attaches a "+
			"named volume to only one running container at a time — the others fail to start. "+
			"Use a bind mount (a host path) for shared data, or bake it into the image.\n",
			strings.Join(quoted, ", "), compose.VolumeDisplayName(src))
	}
}

// reportIgnoredFields surfaces compose fields opossum doesn't act on, so an agent
// (or a human) who wrote an invalid field learns it was dropped instead of
// assuming it took effect — silent ignoring breeds cargo-cult config. Under
// --verbose it prints the full per-scope list (coded OPSM-501/502); otherwise it
// prints a single low-key `note:` line with the count, a representative field, and
// a pointer to `opossum config` for the details. These fields don't affect
// startup, so a per-field warning on every up would be more alarming than useful —
// the note is the middle ground. (Fields that DO change behavior, e.g. a Postgres
// datadir on a named volume, still warn unconditionally elsewhere.)
func (o *Orchestrator) reportIgnoredFields(services []string, includeTopLevel bool) {
	if o.rt.Verbose {
		if includeTopLevel {
			if u := o.Project.Unsupported; len(u) > 0 {
				o.warnf(codeIgnoredTopField, "ignoring unsupported top-level field(s): %s\n", strings.Join(u, ", "))
			}
		}
		for _, name := range services {
			if u := o.Project.Services[name].Unsupported; len(u) > 0 {
				o.warnf(codeIgnoredField, "service %q: ignoring unsupported field(s): %s\n", name, strings.Join(u, ", "))
			}
		}
		return
	}
	if note := o.ignoredFieldsNote(services, includeTopLevel); note != "" {
		// Not ourText: the note is one line and it quotes a service and a field
		// name out of the compose file, so it is exactly the kind of sentence a
		// project could otherwise end early. The trailing newline is the format's
		// to add.
		o.logf("%s\n", strings.TrimRight(note, "\n"))
	}
}

// ignoredFieldsNote builds the one-line summary of ignored compose fields across
// the given services (plus top-level when includeTopLevel), or "" when nothing is
// ignored. The representative favors a service-level field (the common agent
// mistake), falling back to a top-level one; the whole list is deterministic so the
// note is stable. includeTopLevel is false when the caller (a one-off with
// dependencies) delegates top-level reporting to the deps' Up, so the project-wide
// fields aren't counted twice in a single command.
func (o *Orchestrator) ignoredFieldsNote(services []string, includeTopLevel bool) string {
	type pair struct{ scope, field string }
	var pairs []pair
	names := append([]string(nil), services...)
	sort.Strings(names)
	for _, name := range names {
		for _, f := range o.Project.Services[name].Unsupported {
			pairs = append(pairs, pair{name, f})
		}
	}
	if includeTopLevel {
		top := append([]string(nil), o.Project.Unsupported...)
		sort.Strings(top)
		for _, f := range top {
			pairs = append(pairs, pair{"top-level", f})
		}
	}
	if len(pairs) == 0 {
		return ""
	}
	rep := fmt.Sprintf("%s: %s", pairs[0].scope, pairs[0].field)
	if len(pairs) == 1 {
		return fmt.Sprintf("note: 1 compose field is ignored (%s) — run `opossum config` for details\n", rep)
	}
	return fmt.Sprintf("note: %d compose fields are ignored (e.g. %s) — run `opossum config` for details\n", len(pairs), rep)
}

// unknownServiceErr reports a service name that isn't in the project, listing the
// names that ARE defined so the user (or an agent) can fix the typo without running
// a second command to discover them.
func (o *Orchestrator) unknownServiceErr(name string) error {
	names := make([]string, 0, len(o.Project.Services))
	for n := range o.Project.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		// Practically unreachable (a project with no services fails to load), but keep
		// the message coherent for a hand-built project.
		return fmt.Errorf("unknown service %q — this project defines no services", name)
	}
	return fmt.Errorf("unknown service %q — this project defines: %s (run `opossum config --services` to list them)",
		name, strings.Join(names, ", "))
}

// serviceRuntimeVolumes returns the runtime-level named volumes a service mounts
// — the exact names a running container would show — covering project-namespaced
// (or declared-name), anonymous, and external volumes. Bind mounts (host paths) are excluded: they're
// shareable and never cause an attach conflict.
func (o *Orchestrator) serviceRuntimeVolumes(name string) []string {
	svc := o.Project.Services[name]
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, m := range o.serviceMounts(name, svc.Volumes) {
		add(m.Volume) // named + anonymous (namespaced)
	}
	// External volumes mount under their real name and aren't tracked in m.Volume,
	// but they're still exclusive block devices, so a cross-project holder conflicts.
	for _, v := range svc.Volumes {
		src := strings.SplitN(v, ":", 2)[0]
		if isNamedVolume(src) && o.isExternalVolume(src) {
			add(o.externalRealName(src))
		}
	}
	return out
}

// volumeHolders maps each of the given runtime volumes to the running containers
// currently attached to it, skipping `exclude` (the container opossum is about to
// (re)create for this service). Used to name the culprit in an attach-conflict.
func (o *Orchestrator) volumeHolders(volnames []string, exclude string) map[string][]string {
	if len(volnames) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, v := range volnames {
		want[v] = true
	}
	holders := map[string][]string{}
	for _, c := range o.rt.List() {
		if c.Name == exclude || c.State != "running" {
			continue
		}
		for _, v := range c.Volumes {
			if want[v] {
				holders[v] = append(holders[v], c.Name)
			}
		}
	}
	return holders
}

// isStorageAttachmentError reports whether a failed run's stderr is the VZError
// Apple `container` emits when a service tries to attach a named volume that's
// already held by another running container. The device is exclusive, so the
// second attach is rejected with an opaque virtualization error — this is the
// signature opossum decodes into OPSM-103.
func isStorageAttachmentError(stderr string) bool {
	return strings.Contains(stderr, "VZErrorDomain") &&
		strings.Contains(stderr, "Code=2") &&
		strings.Contains(stderr, "storage device attachment is invalid")
}

// decodeStartError turns a service's raw run failure into an actionable
// diagnostic. When the failure is the exclusive-attach VZError (OPSM-103), it
// names the volume and the running container holding it — the cryptic
// virtualization error becomes a fix. Any other failure gets the generic
// start-failed pointer to the logs.
func (o *Orchestrator) decodeStartError(name string, err error) error {
	if decoded, ok := o.decodeVolumeAttachError(name, o.containerName(name), err); ok {
		return decoded
	}
	if hint := runErrorHint(o.Project.Services[name], err); hint != "" {
		return fmt.Errorf("starting service %q: %w%s", name, err, hint)
	}
	// The registry would not hand the image over, so no container was made and
	// there are no logs to read: the way out is the name and whether it can be
	// reached, which is what `pull` says over the same failure. Asked after the
	// coded hints, so a failure they name keeps its code.
	// Not for a service the compose file builds: its image is made here, so a
	// registry that refuses is not about a name anyone typed, and `opossum
	// build` is where the answer is. (measured: nothing in the five shapes
	// comes from a built image — they are all a run of an image by name.)
	if svc := o.Project.Services[name]; svc != nil && svc.Build == nil && o.imageFetchFailed(name, err) {
		// svc.Image is what the run asked the registry for: this is a service
		// that builds nothing, so its image is the one `image:` names.
		return fmt.Errorf("starting service %q: %w\n  %s", name, err, imageUnreachable(svc.Image))
	}
	return startFailed(name, err)
}

// runErrorHint decodes a failed `container run` (its captured stderr) into an
// actionable hint for a known Apple-`container` gotcha the pre-flight can't catch:
// a host-port conflict the host probe can't see (a privileged port held by the
// runtime's own DNS), an image with no arm64 build, or a bind mount of a config
// FILE whose host source is missing. Returns "" when no signature matches (so
// callers fall back to the generic start-failed message). The raw stderr was
// already streamed live; this only adds the fix.
//
// Each hint carries a code, because prose is what a person reads and a code is
// what a program branches on. Anything sorting failures into diagnosed and
// undiagnosed has only the code to sort on, so an accurate hint without one gets
// filed as undiagnosed — the wording is unmatchable and changes freely.
//
// Two of the three reuse a code rather than minting one. A code indexes a fix, not
// a place in the source: the port conflict is the one OPSM-201's pre-flight names
// and sometimes cannot see, and the unresolvable bind mount is the placeholder
// directory OPSM-107 warns about, arriving as a failure instead of a warning. Same
// cause, same way out, so a reader who looks up either code should land on one
// entry rather than two that agree.
//
// The generic startFailed stays uncoded on purpose. It is the case where opossum
// has nothing specific to say, so there is no fix for a code to index — and coding
// it would make every start failure look diagnosed, which is exactly the
// distinction this decoding exists to draw.
func runErrorHint(svc *compose.Service, err error) string {
	var re *runtime.RunError
	if !errors.As(err, &re) {
		return ""
	}
	s := re.Stderr
	switch {
	// Two wordings for one failure, and 1.2.2 says it both ways. Measured on the
	// corpus: an image whose index lists no arm64 fails early with `Error: platform
	// linux/arm64`, while one that is fetched before the mismatch is found still
	// says `does not support required platforms`. Which one a given image produces
	// is not something to guess at, so both are matched — and the older wording is
	// also all that anyone on 1.1.0 ever sees.
	//
	// The 1.2.2 wording names the platform that is *missing*, not the one being
	// run: an image built only for arm64 and asked for as amd64 says `platform
	// linux/amd64`. So it is matched for arm64 alone, and anchored to the start of
	// the message — `platform linux/arm64` on its own is also a substring of
	// `--platform linux/arm64`, which is an ordinary thing for a service to ask
	// for, and this case sits first and would answer for the others.
	case strings.Contains(s, "Error: platform linux/arm64"),
		strings.Contains(s, "does not support required platforms"):
		// Except when amd64 is what was asked for and what is missing. The older
		// wording does not say which platform it wanted, so the service is what
		// says it: told to add what it already has, a reader is being told to
		// repeat what just failed.
		if svc != nil && runtime.IsAMD64(svc.Platform) {
			return ""
		}
		return "\n  → [" + string(codeImageNoArm64) + "] this image has no build for Apple silicon (arm64). " +
			"Add `platform: linux/amd64` to the service — opossum runs an amd64 image through Rosetta."
	// The receiver for a conflict the pre-flight missed, and there is one it does
	// miss. checkHostPorts asks one question — is this address busy on the host
	// right now — and asks it before anything starts. It never asks whether two
	// services in this project want the same address, and at the time it runs the
	// answer to the question it does ask is "no" for both of them. So they both
	// pass, the first one binds, and the second one's bind fails here. Reached
	// that way on 1.2.2; the recipe is in testdata/real-cli-output.md.
	//
	// (The shared `seen` set in that function is a dedupe, not the cause. Probing
	// per service would change nothing: nothing holds the port yet.)
	//
	// The example this used to give — the runtime's own DNS on 53 — is not one of
	// them: the pre-flight sees 53, and sees a port published by another project's
	// running container too. A loopback-only listener is genuinely invisible to
	// the probe, and still does not arrive here, because the runtime binds
	// alongside it without complaint. All three were tried.
	case strings.Contains(s, "Address already in use"):
		hint := "\n  → [" + string(codeHostPortInUse) + "] a published host port is already in use, which opossum's " +
			"pre-flight did not see. " +
			"Remap the port in the compose file"
		if svc != nil {
			if addrs := hostPublishAddrs(svc.Ports); len(addrs) > 0 {
				hint += " (this service publishes " + strings.Join(addrs, ", ") + ")"
				// Only flag the common macOS culprits this service actually uses.
				var dns, airplay bool
				for _, a := range addrs {
					switch a[strings.LastIndexByte(a, ':')+1:] {
					case "53":
						dns = true
					case "5000", "7000":
						airplay = true
					}
				}
				var culprits []string
				if dns {
					culprits = append(culprits, "53 is the runtime's built-in DNS")
				}
				if airplay {
					culprits = append(culprits, "5000/7000 are AirPlay")
				}
				if len(culprits) > 0 {
					hint += " — on macOS, " + strings.Join(culprits, " and ")
				}
			}
		}
		return hint + "."
	case strings.Contains(s, "failed to resolve") && strings.Contains(s, "in rootfs"):
		path := betweenQuotes(s, "failed to resolve '", "'")
		where := "a bind mount"
		if path != "" {
			where = "the bind mount for " + path
		}
		return "\n  → [" + string(codeBindFilePlaceholder) + "] " + where + " couldn't be resolved. If it's a config " +
			"FILE, create it on the host first — opossum creates a missing bind source as a directory, which can't " +
			"mount onto a file path."
	}
	return ""
}

// betweenQuotes returns the substring of s between the first occurrence of open and
// the next occurrence of close after it, or "" if not found.
func betweenQuotes(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// decodeVolumeAttachError decodes the exclusive-attach VZError (OPSM-103) when
// that's what failed, naming the volume and the running container holding it.
// excludeContainer is skipped while scanning for holders (the caller's own
// container). It returns (nil, false) for any other failure, so callers that must
// preserve their own error semantics — e.g. a one-off propagating its command's
// exit code — can leave those untouched.
func (o *Orchestrator) decodeVolumeAttachError(name, excludeContainer string, err error) (error, bool) {
	var re *runtime.RunError
	if !errors.As(err, &re) || !isStorageAttachmentError(re.Stderr) {
		return nil, false
	}
	vols := o.serviceRuntimeVolumes(name)
	if len(vols) == 0 {
		// The signature matched but the service mounts no named volume opossum owns
		// (only bind mounts, say) — not an attach conflict we can explain.
		return nil, false
	}
	holders := o.volumeHolders(vols, excludeContainer)
	// Report the specific volume(s) and their holders when we can identify them;
	// fall back to naming just the service's volumes (the holder may have exited
	// between the failed run and this lookup).
	var busy []string
	// A holder that is the volume's own seeding container — another `up` or
	// `run` filling this very volume from the image — is not one to stop: that
	// would leave the volume half-filled, and the next start would take it as
	// already there. The advice for that holder is to wait. The match is the
	// exact name opossum gives the seed of this volume, not a prefix: a service
	// called `seed-something`, or the seed of some other volume, is a holder
	// like any other.
	var filling []string
	for _, v := range vols {
		if hs := holders[v]; len(hs) > 0 {
			sort.Strings(hs)
			if seedIsFilling(v, hs) {
				filling = append(filling, fmt.Sprintf("%q (being filled by %q)", v, hs[0]))
				continue
			}
			busy = append(busy, fmt.Sprintf("%q (held by running container %s)", v, strings.Join(quoteAll(hs), ", ")))
		}
	}
	sort.Strings(filling)
	verb, those := "is", "that fill"
	if len(filling) > 1 {
		verb, those = "are", "those fills"
	}
	if len(busy) == 0 && len(filling) > 0 {
		return fmt.Errorf("[%s] service %q can't start: %s %s still being filled from the image by another `up` or `run`, "+
			"and Apple `container` attaches a named volume to only one running container at a time. Wait for %s "+
			"to finish, then retry — do not stop the filler: a volume it leaves half-filled is taken as already there by the next start",
			codeVolumeAttachBusy, name, strings.Join(filling, ", "), verb, those), true
	}
	if len(busy) == 0 {
		for _, v := range vols {
			busy = append(busy, fmt.Sprintf("%q", v))
		}
	}
	sort.Strings(busy)
	// A fill in progress beside some other holder: the stop advice is for the
	// other holder, and the fill is still not one to stop — say both, so the
	// person does not stop the wrong container and then meet the seed next.
	var alsoFilling string
	if len(filling) > 0 {
		which := "that one, do not stop it"
		if len(filling) > 1 {
			which = "those, do not stop them"
		}
		alsoFilling = fmt.Sprintf("; %s %s still being filled from the image — wait for %s", strings.Join(filling, ", "), verb, which)
	}
	return fmt.Errorf("[%s] service %q can't start: Apple `container` attaches a named volume to only "+
		"one running container at a time, and %s is already attached elsewhere — the second attach fails "+
		"with a storage-device (VZError) error. Stop the container holding it (`container stop <name>`), "+
		"or give this service its own volume; for shared data use a bind mount (a host path) instead%s",
		codeVolumeAttachBusy, name, strings.Join(busy, ", "), alsoFilling), true
}

// seedIsFilling reports whether the only thing holding volume v is v's own
// seeding container — another `up` or `run` still filling it from the image.
// The match is the exact name opossum gives the seed of this volume, not a
// prefix: a service called `seed-something`, or the seed of some other
// volume, is a holder like any other. Both exits of OPSM-103 — the
// pre-flight warning and the decoded start failure — decide with this.
func seedIsFilling(v string, holders []string) bool {
	return len(holders) == 1 && holders[0] == runtime.SeedContainerName(v)
}

// warnBusyNamedVolumes warns, before starting anything, when a service's named
// volume is already attached to a running container from *another* project (or a
// hand-run container). warnSharedNamedVolumes covers same-compose collisions from
// the compose file alone; this catches cross-project holders that only the live
// runtime knows about, so the user sees the conflict up front rather than as a
// mid-`up` bootstrap failure.
func (o *Orchestrator) warnBusyNamedVolumes(order []string) {
	ours := map[string]bool{}
	for _, name := range order {
		ours[o.containerName(name)] = true
	}
	o.warnBusyVolumesFor(order, ours)
}

// warnBusyVolumesFor warns when any of the given services' named volumes is already
// attached to a running container that isn't in `ours`. Split out so a one-off can
// pass the right exclusion: its own container is the `<service>-run` container, so
// the service's regular `up` container (if running and holding the volume) is a
// genuine foreign holder to warn about — unlike during `up`, where it's ours.
func (o *Orchestrator) warnBusyVolumesFor(services []string, ours map[string]bool) {
	// One container per volume is enough to flag it; collect holders once.
	all := map[string][]string{} // volume -> running holders
	for _, c := range o.rt.List() {
		if c.State != "running" {
			continue
		}
		for _, v := range c.Volumes {
			all[v] = append(all[v], c.Name)
		}
	}
	if len(all) == 0 {
		return
	}
	warned := map[string]bool{}
	for _, name := range services {
		for _, v := range o.serviceRuntimeVolumes(name) {
			if warned[v] {
				continue
			}
			var foreign []string
			for _, h := range all[v] {
				if !ours[h] { // our own stale container is deleted before its run
					foreign = append(foreign, h)
				}
			}
			if len(foreign) == 0 {
				continue
			}
			warned[v] = true
			sort.Strings(foreign)
			if seedIsFilling(v, foreign) {
				o.warnf(codeVolumeAttachBusy, "service %q mounts named volume %q, which is still being filled from the image "+
					"by another `up` or `run` (%s) — Apple container attaches a named volume to only one running container "+
					"at a time, so this service will fail to start until that fill ends. Wait for it, then retry; "+
					"do not stop the filler, or the volume is left half-filled and taken as already there by the next start.\n",
					name, v, quoteAll(foreign)[0])
				continue
			}
			o.warnf(codeVolumeAttachBusy, "service %q mounts named volume %q, which is already attached to "+
				"running container %s — Apple container attaches a named volume to only one running "+
				"container at a time, so this service will fail to start until that container stops. "+
				"Stop it (`container stop %s`), or use a separate volume / a bind mount for shared data.\n",
				name, v, strings.Join(quoteAll(foreign), ", "), foreign[0])
		}
	}
}

// quoteAll wraps each string in %q for embedding a list in a message.
func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// postgresDataDir is Postgres's default data directory. `initdb` refuses to
// initialise into it unless it is empty — which a volume was not, here, until
// opossum began clearing ext4's `lost+found` out of the ones it creates. A volume
// opossum made is now fine to mount straight at this path; one made elsewhere
// still is not, and that is what the two OPSM-101 sites are about. This was the
// single most common snag in real self-hosted app composes (gitea, nextcloud, …).
// MySQL/MariaDB tolerate a non-empty data directory, so this is Postgres-specific
// (#57/#103).
const postgresDataDir = "/var/lib/postgresql/data"

// postgresBaseDir is where Postgres 18 and newer want the single mount: the
// cluster lives in a major-version subdirectory below it, which is what makes
// `pg_upgrade --link` possible without crossing a mount point.
const postgresBaseDir = "/var/lib/postgresql"

// pgVersionedLayoutHint decodes Postgres 18 and newer refusing to start because
// the mount sits where 17 and earlier kept the data.
//
// Postgres 18 moved the data: the image wants one mount at /var/lib/postgresql and
// puts the cluster in a major-version subdirectory below it. A mount at the old
// /var/lib/postgresql/data is then data the image will not use, so it prints what
// it found and exits. Measured on the real runtime with postgres:18-alpine, each
// run kept in testdata/error-wordings/: the refusal at the old path with a bind
// mount and with a named volume, a project differing only in that one path
// starting once the mount moves up a level, and a host path working there — the
// image makes the subdirectory itself, so it has nothing to chown. What it will
// not do is take over a cluster an earlier major version wrote: handed one at the
// path it asks for, 18 makes an empty `18/docker` below it and then refuses,
// leaving the old PG_VERSION alone — which is why this hint says so rather than implying
// the move carries the data.
//
// It runs before the chown decoder deliberately. When both signatures appear the
// mount is at the old path, and moving it is the answer that needs nothing else: a
// named volume there starts only once a PGDATA below it is added as well (both
// measured). The chown decoder offers the volume on its own, and records the
// failure so a later `up --from-docker-compose` can offer it too — half a fix on
// 18, and the reason nothing is recorded here.
//
// The signature has to survive the crash report's window of last lines, so it is
// taken from the tail of the message rather than its `Error:` opening, which a
// message this long pushes out of view.
func (o *Orchestrator) pgVersionedLayoutHint(svc *compose.Service, logs string) string {
	if !strings.Contains(logs, "(unused mount/volume)") || !strings.Contains(logs, "pg_upgrade") {
		return ""
	}
	// Name the mount that has to move, and only the one at the old data directory:
	// "there" in the sentence points at that path, so a mount sitting elsewhere
	// below /var/lib/postgresql — an injected postgresql.conf, a separate pg_wal —
	// would be named for a place it is not. Anything other than exactly one leaves
	// the sentence without the clause; the paths in it already say enough.
	where, found := "", 0
	for _, v := range svc.Volumes {
		src, target, _, ok := splitMount(v)
		if !ok || target != postgresDataDir {
			continue
		}
		found++
		where = fmt.Sprintf(" (this service mounts %s there)", compose.VolumeDisplayName(src))
	}
	if found != 1 {
		where = ""
	}
	return fmt.Sprintf("\n  → [%s] Postgres 18 and newer keep the cluster in a major-version subdirectory, "+
		"so the mount belongs one level up: mount `%s` instead of `%s`%s. A host path works there — the image "+
		"creates the subdirectory itself. Data an earlier major version wrote is a separate question: the image "+
		"asks for `pg_upgrade`, which moving the mount does not do.",
		codePGVersionedLayout, postgresBaseDir, postgresDataDir, where)
}

// initdbNotEmptyHint decodes Postgres refusing to initialise a data directory
// that is not empty because it still holds `lost+found`.
//
// This used to be a prediction: any named volume at the data directory earned a
// warning before anything ran. That was right while every volume arrived with
// `lost+found` in it. Now that opossum removes it from volumes it creates, the
// prediction would be wrong for the ordinary case — a volume opossum prepared and
// Postgres initialised counts as "already exists" on every later `up`, so the
// warning would fire on stacks that work, every time. A warning that is wrong
// half the time is worse than none: it teaches the reader to skip the paragraph
// that also holds the true ones.
//
// So it reads what initdb said instead. Two gates, both from the message the
// image itself prints: `initdb:` and `lost+found`. What is left is the real case —
// a volume opossum did not prepare (an older opossum made it, or `container
// volume create`, or another project) — and it arrives with its own evidence.
func (o *Orchestrator) initdbNotEmptyHint(svcName string, svc *compose.Service, logs string) string {
	if !strings.Contains(logs, "initdb:") || !strings.Contains(logs, "lost+found") {
		return ""
	}
	// The mount is resolved the same way the rest of `up` resolves it, so the name
	// in the message is the name `container volume ls` shows — and so a volume
	// opossum does not own (a bind mount, an `external: true` volume) comes back
	// with an empty Volume and is never named here.
	vol := ""
	for _, m := range o.serviceMounts(svcName, svc.Volumes) {
		if m.Volume != "" && strings.TrimRight(m.Target, "/") == postgresDataDir {
			vol = m.Volume
		}
	}
	const why = "Postgres will not initialise a data directory that isn't empty, and it still holds " +
		"`lost+found` — the directory ext4 puts in every filesystem it makes. opossum removes it from " +
		"volumes it creates, so this one was made before that or made elsewhere. "
	pgdata := fmt.Sprintf("keep the data below the mount point by adding `environment: PGDATA=%s/pgdata` "+
		"to the service.", postgresDataDir)
	if vol == "" {
		// Nothing opossum manages sits there, so the volume is the user's to
		// recreate or not — and `down -v`, which does not touch an external volume,
		// would be advice that cannot work.
		return fmt.Sprintf("\n  → [%s] %sThis one is not opossum's to replace, so: %s",
			codePGDATADatadir, why, pgdata)
	}
	// `down -v` is offered as a choice, not a step, and with what it costs said out
	// loud: initdb refusing means Postgres never wrote here, but opossum cannot see
	// what else may have.
	return fmt.Sprintf("\n  → [%s] %sThe volume is %q. Two ways out: let opossum make it "+
		"(`opossum down -v` removes this project's volumes, then `opossum up` — initdb refusing means "+
		"Postgres never wrote here, but check nothing else did), or %s",
		codePGDATADatadir, why, vol, pgdata)
}

// MarkNotesReported records which services' Docker-socket notes were actually
// shown to the reader — prose, not just the headline — so the up that follows
// does not repeat them. The caller is the layer that printed: only it knows
// whether the prose made it out.
func (o *Orchestrator) MarkNotesReported(changes []Adaptation) {
	for _, c := range changes {
		if c.Kind == "note" && c.Code == string(codeDockerSocket) {
			if o.notedDockerSocket == nil {
				o.notedDockerSocket = map[string]bool{}
			}
			o.notedDockerSocket[c.Service] = true
		}
	}
}

// warnDockerSocket warns when a service mounts the Docker daemon socket. Apple
// `container` has none to expose: it runs these containers over XPC, so nothing
// here answers on that path about them. Something else may — where the path is a
// symlink something put it there, and if it is listening a container started here
// can reach it — and then a tool mounting this socket to watch its neighbours
// (Portainer, Traefik) is shown a different set instead of nothing at all.
//
// It used to say the mount fails at runtime. It does not: bind-mounting a host
// socket works, and #614 has the measurement.
func (o *Orchestrator) warnDockerSocket(name string, svc *compose.Service) {
	if o.notedDockerSocket[name] {
		// The note in this same run already said this, in the same words. See
		// the field's comment for what is and is not suppressed.
		return
	}
	for _, v := range svc.Volumes {
		// The same question the note asks: does either end of the mount name
		// docker.sock? This used to read the unsplit string, which warned on
		// three shapes where the warning's own sentence — "mounts the Docker
		// socket" — is false:
		//
		// An anonymous volume (`- /var/run/docker.sock`) mounts nothing from the
		// host under docker either — Docker Compose v5.4.0 canonicalizes it to
		// `type: volume` with no host source — so there is no divergence to
		// report, and no socket was mounted. A directory called `docker.sock.d/`
		// and a socket called `my-docker.sock` are somebody else's sockets;
		// telling a gpg-agent owner they mount the Docker socket is just wrong.
		// It also ends one run saying opposite things about one mount — the
		// note reads its mounts through this predicate already.
		if src, tgt, _, ok := splitMount(v); ok && isDockerSocketMount(src, tgt) {
			o.warnf(codeDockerSocket, "service %q mounts the Docker socket (%s), which does not "+
				"answer for the containers here. Apple `container` runs them, and it has no socket "+
				"to share; if a Docker daemon answers on that path, it is a different one and knows "+
				"a different set. A tool that wants this socket in order to watch its neighbours will "+
				"be told about theirs.\n", name, compose.VolumeDisplayName(v))
			return
		}
	}
}

// refuseSymlinkedSocketMounts fails the up when a bind source is a symlink whose
// target is a socket, before anything starts.
//
// That combination, and only that combination, cannot be mounted here. Measured
// against `container` 1.1.0 and again against 1.2.2 — the boundary is the same on
// both — separating the three things that a single earlier measurement had
// confounded:
//
//	socket, by its real path            works
//	symlink → regular file              works
//	symlink → directory                 works
//	symlink → socket                    mount failed with errno 95
//
// Location has nothing to do with it: a regular file in /var/run mounts, and the
// failure reproduces for a socket and a symlink made side by side in a temp
// directory. Where Docker Desktop is installed it links /var/run/docker.sock to
// a socket, which is why an earlier version of this read the failure as "a socket
// cannot be a bind source" and refused mounts that work.
//
// This only reads the host, so it runs under --dry-run too: a dry run is where
// you want to hear that the real one cannot work. The bind-directory check below
// is skipped there instead, because that one creates directories.
//
// Refusing rather than warning, for the same reason as the bind directory above:
// the start was going to fail, a failed start rolls the project back anyway, and
// the runtime's own words for it are four levels of nested `internalError`.
// Takes the same (service, vols) shape as ensureBindDirs so that both callers of
// one are callers of the other: `up` and `run` reach the runtime by different
// paths, and a check wired into only one of them is a hole that shows up as the
// runtime's own error the first time somebody uses the other verb.
func (o *Orchestrator) refuseSymlinkedSocketMounts(service string, vols []string) error {
	for _, v := range vols {
		mount, target, _, ok := splitMount(v)
		if !ok || !isHostPath(mount) {
			continue
		}
		src := o.resolvePath(mount)
		// Lstat sees the link itself; Stat follows it. Both halves are the
		// finding: the link alone is fine, and the socket alone is fine. Stat
		// also fails outright on a dangling link, which is not this problem —
		// ensureBindDirs has words for that one.
		li, lerr := os.Lstat(src)
		if lerr != nil || li.Mode()&os.ModeSymlink == 0 {
			continue
		}
		fi, serr := os.Stat(src)
		if serr != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		resolved, _ := filepath.EvalSymlinks(src)
		// The same way out whatever the socket is. A Docker socket used to get
		// a different one, on the reading that mounting what the link points at
		// could not help because Apple container has no daemon behind it. That
		// was wrong in exactly the case that produces the message: the name is
		// a symlink on the machines that have Docker Desktop, and a container
		// started here reaches the daemon through the resolved path — measured.
		// What is worth saying about that daemon belongs with the note that
		// speaks about Docker, not here, where the reader may not have one.
		hint := ""
		if resolved != "" {
			hint = fmt.Sprintf("\n  mounting %s — what the link points at — works, so that is the way out "+
				"if the service really needs this socket", resolved)
		}
		return fmt.Errorf("[%s] service %q mounts %s at %s, and that cannot be done here: the path is a "+
			"symlink to a socket, which this runtime refuses (`mount failed with errno 95`)\n"+
			"  a socket reached by its own path mounts fine, and so does a symlink to a file or a "+
			"directory — it is the combination that fails%s",
			codeSymlinkedSocket, service, src, target, hint)
	}
	return nil
}

// indentLines puts opossum's shape around a captured block — container logs, say
// — so it can be embedded in a message: every line begins two spaces in, and no
// line begins anywhere else.
//
// The second half is why every other control character goes first. A carriage
// return moves the cursor back to the start of the line the block is already
// printing on, and a container's output is no more opossum's than a compose
// file is: without this, a log line could put a sentence at column zero, where
// opossum's own sentences begin.
func indentLines(s string) string {
	// Every control character but the newline: the newlines are what the shape
	// is made of, and OneLine would take them too.
	flat := strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return "  " + strings.ReplaceAll(flat, "\n", "\n  ")
}

// crashHint decodes a crashed container's last log lines into an actionable hint
// for a known Apple-`container` gotcha, appended to the crash report. It knows
// three signatures: a chown-permission failure on a bind mount (Apple `container`
// bind mounts are host-owned via virtiofs and can't be chowned from inside, so a
// DB image that chowns its data directory fails with "chown: … Operation not
// permitted" — a named volume IS chownable, so that's the fix), Postgres refusing
// a data directory that still holds `lost+found`, and Postgres 18 and newer
// refusing a mount at the data directory 17 and earlier used.
//
// All three are decoders, not predictions: they answer a failure that has already
// happened, in the words of the program that failed. Returns "" when nothing
// matches (so callers can append unconditionally).
func (o *Orchestrator) crashHint(name, logs string) string {
	svc := o.Project.Services[name]
	if svc == nil {
		return ""
	}
	if h := o.pgVersionedLayoutHint(svc, logs); h != "" {
		return h
	}
	if h := o.chownCrashHint(name, svc, logs); h != "" {
		return h
	}
	return o.initdbNotEmptyHint(name, svc, logs)
}

// crashWhy is the half of the guidance that is the same in every form: what went
// wrong, and why a named volume is the way out.
const crashWhy = "Apple `container` bind mounts are host-owned and can't be chowned from inside the container, so an " +
	"image that takes ownership of its data directory at startup fails here. A named volume can be chowned"

// crashCost is the other half every form ends with. The overlay's own suggestion
// spends four lines on it, and two of these three forms hand the same change to
// the reader to make by hand — but it belongs on all three, because the cost is a
// property of the change, not of who makes it. A host directory full of data and
// an output that looks like success either way is what this sentence is for.
const crashCost = "; what is in the host directory now stays there, and the service stops seeing it"

// chownCrashHint decodes a container that died chowning a bind mount, and records
// which mount it was so a later `up --from-docker-compose` can propose a fix for
// that mount and no other.
func (o *Orchestrator) chownCrashHint(name string, svc *compose.Service, logs string) string {
	if !strings.Contains(logs, "chown") || !strings.Contains(logs, "Operation not permitted") {
		return ""
	}
	// The same double gate decides whether to remember it. What is written down is
	// the one thing a suggestion needs and guessing cannot supply: which mount died.
	src, target, known := blamedMount(svc, logs, func() string { return o.startingDir(svc) })
	recorded := false
	if known {
		recorded = o.recordChownFailure(chownFailure{Service: name, Target: target, Source: src})
	}
	for _, v := range svc.Volumes {
		if isHostPath(strings.SplitN(v, ":", 2)[0]) {
			// Which mount died and whether the note about it survived are two
			// different facts, and folding them into one said "opossum could not
			// work out which mount" about a container that had just named the
			// directory on the line above. Where the mount is known it gets named,
			// whatever happened to the note.
			switch {
			case !known:
				// Several things stop it being known — a log that names no
				// directory, one that names a directory this service does not
				// mount, several mounts that could each be it — and this says the
				// one thing true of all of them, because saying which would be a
				// guess about the reader's own file.
				return fmt.Sprintf("\n  → [%s] %s. opossum could not work out which of %q's mounts this container "+
					"died on — change the mount holding its data to a named volume yourself%s.",
					codeBindDataDirChown, crashWhy, name, crashCost)
			case !recorded:
				return fmt.Sprintf("\n  → [%s] %s. This container died on %s, and opossum could not keep a note of "+
					"that in this project directory — change %[3]s to a named volume yourself%[4]s.",
					codeBindDataDirChown, crashWhy, target, crashCost)
			default:
				// It says what died and what to change, and stops there. Four
				// earlier versions of this sentence described what the next command
				// would do — write the swap, write it commented, offer it to apply
				// or ignore — and each was false for some project. What that command
				// does is its own to report.
				return fmt.Sprintf("\n  → [%s] %s. This container died on %s, and opossum has that on record: "+
					"`opossum up --from-docker-compose` reads it. Changing %[3]s to a named volume is what gets "+
					"past this%[4]s.",
					codeBindDataDirChown, crashWhy, target, crashCost)
			}
		}
	}
	return ""
}

// isExternalVolume reports whether a named volume is declared `external: true`
// at the top level. External volumes are used by their real name (not
// namespaced) and never removed by `down -v` — the user manages them (#64).
func (o *Orchestrator) isExternalVolume(src string) bool {
	return o.Project.Volumes[src].External
}

// externalRealName is the real volume name to mount for an external volume: its
// declared `name:` if set (compose lets an external volume have a real name
// different from its key), otherwise the compose key (#64). The key is shown
// through VolumeDisplayName for its spelling's sake only: a mounted external
// volume whose key starts with `.` is refused at load without a `name:`, since
// no runtime can create a volume by that name.
func (o *Orchestrator) externalRealName(src string) string {
	if n := o.Project.Volumes[src].Name; n != "" {
		return n
	}
	return compose.VolumeDisplayName(src)
}

// namedVolumes lists the distinct named volumes referenced by services (the
// source of a `name:/path` mount that isn't a host path), under their runtime
// names — `<project>_<key>`, or the declared `name:`.
func (o *Orchestrator) namedVolumes() []string {
	seen := map[string]bool{}
	var out []string
	for name, svc := range o.Project.Services {
		for _, m := range o.serviceMounts(name, svc.Volumes) {
			// Volume != "" covers named and anonymous volumes; bind mounts and
			// user-managed external volumes are left empty (never removed, #64).
			if m.Volume != "" && !seen[m.Volume] {
				seen[m.Volume] = true
				out = append(out, m.Volume)
			}
		}
	}
	sort.Strings(out)
	return out
}

// PsOptions selects how `opossum ps` shows the project's services.
type PsOptions struct {
	Format string // "table" (default) or "json"
}

// ServiceStatus is one row of `opossum ps`: a service's container, image, IP,
// published ports (rendered docker-ps style, e.g. "0.0.0.0:8080->80/tcp"), and
// status — the columns the table prints, as JSON.
type ServiceStatus struct {
	Service   string `json:"Service"`
	Container string `json:"Container"`
	Image     string `json:"Image"`
	IP        string `json:"IP"`
	Ports     string `json:"Ports"`
	Status    string `json:"Status"`
}

// notThisProjects says why a container of a service's name is not this
// project's to list, read or measure: it belongs to another project, it
// carries no project label at all (made outside opossum), or the runtime gave
// no readable answer about it — the same three answers `up` and `down` act
// on. "" for this project's own and for a missing container.
func (o *Orchestrator) notThisProjects(info runtime.ContainerInfo) string {
	if info.Unknown {
		return "could not be asked: the runtime gave no readable answer about which project owns it"
	}
	if !info.Exists {
		return ""
	}
	switch proj := info.Labels[projectLabel]; {
	case proj == "":
		return "carries no " + projectLabel + " label, so it was not made by this project"
	case proj != o.Project.Name:
		return fmt.Sprintf("belongs to project %q", proj)
	}
	return ""
}

// serviceStatuses is the `ps` table: one row per service whose container is
// there and this project's. A container of the name that is another
// project's, or carries no project label, gets no row — as docker compose
// lists only the containers that carry its project's labels — and is named
// in notes, for `ps` to say. The containers the runtime gave no readable
// answer about are in unanswered as well, for `ps` to exit non-zero over: an
// empty table must not read as "nothing is running" when the truth is the
// runtime would not say.
func (o *Orchestrator) serviceStatuses() (rows []ServiceStatus, notes, unanswered []string, err error) {
	// A dead daemon makes every per-service inspect look like "container absent",
	// which would render as an empty table — a lie ("nothing is running") when the
	// truth is the runtime is unreachable. Probe the system once up front so an
	// empty `ps` means genuinely empty, not "couldn't ask". (The CLI-absent case is
	// caught earlier by the root preflight; here the CLI is present but stopped.)
	if !o.rt.SystemRunning() {
		return nil, nil, nil, ErrRuntimeStopped()
	}
	order, err := o.startupOrder()
	if err != nil {
		return nil, nil, nil, err
	}
	rows = make([]ServiceStatus, 0, len(order))
	for _, name := range order {
		svc := o.Project.Services[name]
		cname := o.containerName(name)
		image, _ := o.serviceImage(name, svc)
		info := o.rt.Inspect(cname)
		// Whose it is comes first: a container the runtime gave no readable
		// answer about is not "not there".
		if why := o.notThisProjects(info); why != "" {
			notes = append(notes, fmt.Sprintf("%s: container %s %s — not listed", name, cname, why))
			if info.Unknown {
				unanswered = append(unanswered, cname)
			}
			continue
		}
		if !info.Exists {
			continue
		}
		status := "stopped"
		if info.State != "" {
			status = info.State
		}
		rows = append(rows, ServiceStatus{
			Service:   name,
			Container: cname,
			Image:     image,
			IP:        info.IP,
			Ports:     formatPorts(info.Ports),
			Status:    status,
		})
	}
	return rows, notes, unanswered, nil
}

// Ps prints the project's services (see serviceStatuses) as a
// SERVICE/CONTAINER/IMAGE/IP/PORTS/STATUS table, or a JSON array under Format
// "json".
func (o *Orchestrator) Ps(opts PsOptions) error {
	rows, notes, unanswered, err := o.serviceStatuses()
	if err != nil {
		return err
	}
	switch opts.Format {
	case "json":
		b, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		fmt.Fprintln(o.out, string(b))
	default:
		tw := tabwriter.NewWriter(o.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tCONTAINER\tIMAGE\tIP\tPORTS\tSTATUS")
		for _, r := range rows {
			row(tw, r.Service, r.Container, r.Image, dash(r.IP), dash(r.Ports), r.Status)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	// What is not in the table goes to stderr: `ps` is a column table (or a
	// JSON array) that agents and scripts parse, and a prose line on stdout
	// would break either form. First the containers left out and why, then a
	// background process the user didn't name, which should be visible
	// wherever they look at the project, not only in the line `up` printed once.
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	if pid := SupervisorPID(o.Project.Name); pid != 0 {
		msg := fmt.Sprintf("restart supervisor: running (pid %d)", pid)
		if log, err := SupervisorLogFile(o.Project.Name); err == nil {
			msg += " — " + log
		}
		fmt.Fprintln(os.Stderr, msg)
	}
	return unansweredOwners(unanswered, "opossum ps")
}

// Port prints, on one line, the host side of a service's published container
// port — `0.0.0.0:65345` — the way `docker compose port` does, so a script can
// read the host port opossum settled on: `ports: - "3000"` is mapped to 3000
// on the host when that is free and moved to a free port when it is not (see
// remapAutoHostPorts), and until now the only place to read the outcome was
// the `ps` table.
//
// A service that is not running says so, as docker compose does; a port that
// is not published lists the ones that are, in the container's order, so the
// reader sees whether they asked for the wrong port or the wrong protocol.
func (o *Orchestrator) Port(service string, containerPort int, proto string) error {
	// The same probe as Ps: a stopped runtime makes the inspect fail, which
	// would read as "service is not running" — the wrong advice.
	if !o.rt.SystemRunning() {
		return ErrRuntimeStopped()
	}
	if _, ok := o.Project.Services[service]; !ok {
		return o.unknownServiceErr(service)
	}
	cname := o.containerName(service)
	// An absent container reports no state at all, so one check covers "never
	// created", "removed by down" and "stopped" alike.
	info := o.rt.Inspect(cname)
	// A container of the name that is not this project's has no port of this
	// project's to read, whatever it publishes.
	if why := o.notThisProjects(info); why != "" {
		return fmt.Errorf("service %q has no container of this project's: %s %s", service, cname, why)
	}
	if info.State != "running" {
		return fmt.Errorf("service %q is not running", service)
	}
	for _, p := range info.Ports {
		if p.ContainerPort == containerPort && p.Proto == proto {
			fmt.Fprintf(o.out, "%s:%d\n", p.HostAddress, p.HostPort)
			return nil
		}
	}
	published := make([]string, 0, len(info.Ports))
	for _, p := range info.Ports {
		published = append(published, fmt.Sprintf("%d/%s", p.ContainerPort, p.Proto))
	}
	list := strings.Join(published, ", ")
	if list == "" {
		list = "(none published)"
	}
	return fmt.Errorf("no port %d/%s for container %s: %s", containerPort, proto, cname, list)
}

// LsOptions selects what `opossum ls` shows and how.
type LsOptions struct {
	All    bool   // include projects with no running container
	Quiet  bool   // names only, one per line
	Format string // "table" (default) or "json"
}

// ProjectStatus is one row of `opossum ls`: a project found on this machine by
// the `opossum.project` label its containers carry, and a count of its
// containers by state — `running(2)`, or `running(1), stopped(1)` — the shape
// `docker compose ls` prints.
type ProjectStatus struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

// ListProjects folds every container the runtime lists into the projects that
// made them, by the `opossum.project` label; containers without it (the
// builder, anything made by hand) are not opossum's and are left out. Without
// All only running containers count and a project with none is not listed, as
// `docker compose ls` hides a project whose containers have all exited; with
// All every container counts and the states are listed side by side.
func ListProjects(rt *runtime.Runtime, all bool) ([]ProjectStatus, error) {
	// The same probe as Ps: with the runtime stopped the listing is empty, and
	// an empty answer would read as "nothing is running" — the wrong advice.
	if !rt.SystemRunning() {
		return nil, ErrRuntimeStopped()
	}
	// Names and states are gathered in the order the listing shows them and
	// sorted afterwards, rather than read out of the maps: a map's order is
	// arbitrary, and an arbitrary order is sorted often enough by chance that
	// a missing sort would not be seen to fail.
	counts := map[string]map[string]int{}
	var names []string
	states := map[string][]string{}
	for _, c := range rt.List() {
		project := c.Labels[projectLabel]
		if project == "" {
			continue
		}
		if !all && c.State != "running" {
			continue
		}
		if counts[project] == nil {
			counts[project] = map[string]int{}
			names = append(names, project)
		}
		if counts[project][c.State] == 0 {
			states[project] = append(states[project], c.State)
		}
		counts[project][c.State]++
	}
	sort.Strings(names)
	out := make([]ProjectStatus, 0, len(names))
	for _, n := range names {
		sort.Strings(states[n])
		parts := make([]string, 0, len(states[n]))
		for _, st := range states[n] {
			parts = append(parts, fmt.Sprintf("%s(%d)", st, counts[n][st]))
		}
		out = append(out, ProjectStatus{Name: n, Status: strings.Join(parts, ", ")})
	}
	return out, nil
}

// Ls prints the projects on this machine (see ListProjects) as a NAME / STATUS
// table, names only under Quiet, or a JSON array under Format "json" — the
// forms `docker compose ls` has, less its CONFIG FILES column, which opossum
// cannot fill: it does not record which compose file made a project.
func Ls(rt *runtime.Runtime, w io.Writer, opts LsOptions) error {
	projects, err := ListProjects(rt, opts.All)
	if err != nil {
		return err
	}
	switch {
	case opts.Quiet:
		for _, p := range projects {
			fmt.Fprintln(w, p.Name)
		}
	case opts.Format == "json":
		b, err := json.Marshal(projects)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(b))
	default:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATUS")
		for _, p := range projects {
			fmt.Fprintf(tw, "%s\t%s\n", p.Name, p.Status)
		}
		return tw.Flush()
	}
	return nil
}

// VolumesOptions selects how `opossum volumes` shows the project's volumes.
type VolumesOptions struct {
	Quiet  bool   // names only, one per line
	Format string // "table" (default) or "json"
}

// VolumeStatus is one row of `opossum volumes`: a volume of this project that
// exists on the runtime, under the name the runtime knows it by
// (`<project>_<volume>`), and its driver.
type VolumeStatus struct {
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
}

// ProjectVolumes lists the volumes the named services (every service when
// none is named) mount that exist on the runtime, in name order — what
// `docker compose volumes` shows. A volume is opossum's when a service mounts
// it as a named or anonymous volume: it is then created under the project's
// name (or the `name:` its declaration gives) on first use, and that is the
// name looked for in `container volume ls`.
// Declared but unmounted volumes are never created, so they do not appear;
// external volumes are the user's, not the project's, and are left out as
// docker compose leaves them out; a bind mount is a directory, not a volume.
func (o *Orchestrator) ProjectVolumes(services []string) ([]VolumeStatus, error) {
	// The same probe as Ps: a stopped runtime lists nothing, and nothing would
	// read as "no volumes yet" — the wrong advice.
	if !o.rt.SystemRunning() {
		return nil, ErrRuntimeStopped()
	}
	targets, err := o.resolveServices(services)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, name := range targets {
		for _, m := range o.serviceMounts(name, o.Project.Services[name].Volumes) {
			if m.Volume != "" && !seen[m.Volume] {
				seen[m.Volume] = true
				names = append(names, m.Volume)
			}
		}
	}
	sort.Strings(names)
	driver := map[string]string{}
	exists := map[string]bool{}
	for _, row := range o.rt.ListVolumeRows() {
		exists[row.Name] = true
		driver[row.Name] = row.Driver
	}
	out := make([]VolumeStatus, 0, len(names))
	for _, n := range names {
		if exists[n] {
			out = append(out, VolumeStatus{Name: n, Driver: driver[n]})
		}
	}
	return out, nil
}

// Volumes prints the project's volumes (see ProjectVolumes) as a DRIVER /
// VOLUME NAME table, names only under Quiet, or a JSON array under Format
// "json" — the columns `docker compose volumes` prints.
func (o *Orchestrator) Volumes(services []string, opts VolumesOptions) error {
	vols, err := o.ProjectVolumes(services)
	if err != nil {
		return err
	}
	switch {
	case opts.Quiet:
		for _, v := range vols {
			fmt.Fprintln(o.out, v.Name)
		}
	case opts.Format == "json":
		b, err := json.Marshal(vols)
		if err != nil {
			return err
		}
		fmt.Fprintln(o.out, string(b))
	default:
		tw := tabwriter.NewWriter(o.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "DRIVER\tVOLUME NAME")
		for _, v := range vols {
			fmt.Fprintf(tw, "%s\t%s\n", dash(v.Driver), v.Name)
		}
		return tw.Flush()
	}
	return nil
}

// formatPorts renders published ports docker-ps style: "0.0.0.0:8080->80/tcp".
//
// The example is asymmetric on purpose. It said 8080->8080 until a sweep pointed
// out that the two numbers were exchangeable without any test noticing — and an
// example where the host port and the container port are the same number cannot
// show a reader which side is which either.
func formatPorts(ports []runtime.PortMapping) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", p.HostAddress, p.HostPort, p.ContainerPort, p.Proto))
	}
	return strings.Join(parts, ", ")
}

// dash returns "-" for an empty field so columns stay aligned and readable.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ErrInterrupted is what `logs` returns when a Ctrl-C or a SIGTERM ended it:
// docker compose v5.5.1 `logs` says nothing then and exits 130 (measured),
// and the CLI does the same with this.
var ErrInterrupted = errors.New("interrupted")

// Logs streams container logs. With no service names it shows every service in
// dependency order; otherwise just the named ones (validated against the
// project), each line prefixed with its service (see logPrefixes) unless
// opts.NoLogPrefix. Following several services multiplexes them; one service
// followed, or several not followed, are read one after another. A Ctrl-C
// ends it with ErrInterrupted.
func (o *Orchestrator) Logs(services []string, opts runtime.LogsOptions) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	// Only this project's containers are read: a container of a service's
	// name that is another project's, carries no project label, or that the
	// runtime gave no readable answer about is left out and said on stderr
	// (docker compose shows nothing for it, silently), and the rest are shown.
	// With every container asked for someone else's, nothing is printed and
	// the exit is zero, as docker compose's; the ones the runtime gave no
	// readable answer about make the exit non-zero once the rest are shown,
	// as `down` exits over the ones it left.
	// A service nobody has started has no logs to show, and is passed by when
	// the command was given no names (#1096); one asked for by name still
	// answers for itself.
	targets, created, unanswered := o.ownContainers(targets, "not shown")
	if len(services) == 0 {
		targets = o.withContainers(targets, created)
	}
	left := unansweredOwners(unanswered, "opossum logs")
	prefixes := logPrefixes(targets, opts.NoLogPrefix)
	base := o.ctx
	if base == nil {
		base = context.Background()
	}
	// Not SIGHUP: a terminal that closes ends opossum and the runtime it runs
	// as it always did (129, as docker compose), and `nohup opossum logs
	// --follow` keeps following, where taking it would undo the nohup.
	ctx, stop := signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Following several services multiplexes their streams into one output;
	// Ctrl-C stops all.
	if opts.Follow && len(targets) > 1 {
		err := o.followMultiplexed(ctx, targets, opts, prefixes)
		if ctx.Err() != nil {
			return ErrInterrupted
		}
		if err != nil {
			return withOwners(err, left)
		}
		return left
	}
	// One service followed, or each service in turn: every line carries the
	// prefix, as docker compose v5.5.1 writes it whether it follows or not and
	// for one service too. The lines of one service are kept together, in the
	// order of targets, where docker compose reads the services at once and
	// the order of the services can change from one run to the next. A Ctrl-C
	// ends it there, whatever the stream was doing: what the cancel did to the
	// stream (a child killed, one not started) is not a failure to read.
	for i, name := range targets {
		stream := func(c context.Context, w io.Writer) error {
			return o.rt.PrefixedLogs(c, o.containerName(name), opts, w, os.Stderr, prefixes[i])
		}
		var err error
		exited := false
		if opts.Follow {
			exited, err = o.followUntilExit(ctx, name, o.out, stream)
		} else {
			err = stream(ctx, o.out)
		}
		if ctx.Err() != nil {
			return ErrInterrupted
		}
		if err != nil {
			// A failure to read still names the containers the runtime would
			// not answer about, as `start` names them beside its failures.
			return withOwners(fmt.Errorf("logs for service %q: %w\n  confirm the service is up with `opossum ps`", name, err), left)
		}
		if exited {
			o.logf("%s exited\n", name+"-1")
		}
	}
	return left
}

// LogsExitPoll is how often `logs --follow` looks at the state of a container
// it follows; LogsExitSettle is how long the container must stay not running
// before that counts as its end; LogsExitDrain is how long its stream must then
// go without a new line — none being written, and none finished — before it
// is ended.
var (
	LogsExitPoll   = time.Second
	LogsExitSettle = 3 * time.Second
	LogsExitDrain  = 500 * time.Millisecond
)

// lastWrite is a writer that remembers when a write last finished, and
// whether one is under way (a slow reader holds it).
type lastWrite struct {
	w       io.Writer
	at      atomic.Int64 // unix nanoseconds
	writing atomic.Int32
}

func (l *lastWrite) Write(p []byte) (int, error) {
	l.writing.Add(1)
	defer l.writing.Add(-1)
	n, err := l.w.Write(p)
	l.at.Store(time.Now().UnixNano())
	return n, err
}

// idle reports whether nothing has been written for d and nothing is being.
func (l *lastWrite) idle(now time.Time, d time.Duration) bool {
	return l.writing.Load() == 0 && now.Sub(time.Unix(0, l.at.Load())) >= d
}

// followUntilExit runs stream, a service's followed logs written to w, until it
// ends, and ends it once the service's container has ended: container 1.4.1's
// `container logs -f` goes on after the container exits, is stopped or is
// removed (measured), where docker compose v5.5.1 ends a followed container's
// stream then and says so (`web-1 exited with code 3`).
//
// The container counts as ended once it has been seen running while followed
// — docker compose goes on following one that had already exited before
// `logs -f` began — and has then stayed not running (stopped, or gone) for
// LogsExitSettle: a restart (`opossum restart`) leaves it stopped for a moment
// and starts the same container again, whose stream goes on, as docker compose
// goes on following it; while `opossum restart` is under way (it holds a
// marker) the gap is not counted at all, however long it lasts. A look the
// runtime does not answer changes nothing: the time since the container was
// first seen not running goes on being counted through it. Once ended, the
// stream is not cut at once: the runtime may still be handing over what the
// container wrote last, so it is ended when it has gone LogsExitDrain without
// a new line, a line being written to a slow reader counting as one. A service
// with a `restart:` policy is not watched: the supervisor starts the same
// container again and the stream goes on with it.
// exited reports that the container's end ended the stream. The watch is
// over when this returns.
func (o *Orchestrator) followUntilExit(ctx context.Context, service string, w io.Writer, stream func(context.Context, io.Writer) error) (exited bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lw := &lastWrite{w: w}
	lw.at.Store(time.Now().UnixNano())
	var ended atomic.Bool
	if pol, perr := o.Project.Services[service].RestartPolicy(); perr != nil || !pol.Wants() {
		done := make(chan struct{})
		watched := make(chan struct{})
		defer func() {
			close(done)
			// A look under way is waited for; after a Ctrl-C or SIGTERM —
			// already taken, or taken while waiting — only for
			// runtime.LogsCancelGrace, the span a stream that ended on its own
			// gives the cancel: a runtime that does not answer would hold the
			// command up, and one that answers in time leaves no `container
			// inspect` of this command still running once it has returned. The
			// watcher starts no look once it has seen either is over.
			select {
			case <-watched:
				return
			case <-ctx.Done():
			}
			select {
			case <-watched:
			case <-time.After(runtime.LogsCancelGrace):
			}
		}()
		go func() {
			defer close(watched)
			tick := time.NewTicker(LogsExitPoll)
			defer tick.Stop()
			seen := false
			var stoppedSince, endedAt time.Time
			// Looked at once straight away, then at each tick: a container
			// that stops within the first tick has been seen running.
			for first := true; ; first = false {
				// Asked before every look, the first too: a tick that came
				// while the look before was answered is not taken after the
				// stream is over.
				select {
				case <-done:
					return
				case <-sctx.Done():
					return
				default:
				}
				if !first {
					select {
					case <-done:
						return
					case <-sctx.Done():
						return
					case <-tick.C:
					}
				}
				now := time.Now()
				if !endedAt.IsZero() && lw.idle(now, LogsExitDrain) {
					ended.Store(true)
					cancel()
					return
				}
				info := o.rt.Inspect(o.containerName(service))
				switch {
				case o.isRestarting(service):
					// `restart` is stopping and starting it: the gap, however
					// long, is not the end.
					stoppedSince, endedAt = time.Time{}, time.Time{}
				case info.Unknown:
				case info.Exists && info.State == "running":
					seen = true
					stoppedSince, endedAt = time.Time{}, time.Time{}
				case !seen:
				case stoppedSince.IsZero():
					stoppedSince = now
				case endedAt.IsZero() && now.Sub(stoppedSince) >= LogsExitSettle:
					endedAt = now
				}
			}
		}()
	}
	err = stream(sctx, lw)
	if ended.Load() && ctx.Err() == nil {
		return true, nil
	}
	return false, err
}

// syncWriter serializes concurrent writes from several log-follow goroutines onto
// one underlying writer.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// logPrefixes is the prefix of each target's lines, as docker compose v5.5.1
// writes it (measured): the container's name as docker compose gives it,
// `<service>-1`, padded to one more than the longest among the services
// shown, then ` | ` (`web-1  | `, and `x-1    | ` beside `web-1`). With
// noPrefix each is empty. The index is always 1: opossum runs one container
// per service.
func logPrefixes(targets []string, noPrefix bool) []string {
	prefixes := make([]string, len(targets))
	if noPrefix {
		return prefixes
	}
	width := 0
	for _, name := range targets {
		width = max(width, len(name)+len("-1"))
	}
	for i, name := range targets {
		prefixes[i] = fmt.Sprintf("%-*s | ", width+1, name+"-1")
	}
	return prefixes
}

// followMultiplexed follows every target concurrently, prefixing each line with
// its prefix and merging into o.out. A single stream ending doesn't stop the
// others; the cancel of ctx stops them all.
func (o *Orchestrator) followMultiplexed(ctx context.Context, targets []string, opts runtime.LogsOptions, prefixes []string) error {
	out := &syncWriter{w: o.out}
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	for i, name := range targets {
		wg.Add(1)
		go func(i int, name, prefix string) {
			defer wg.Done()
			exited, err := o.followUntilExit(ctx, name, out, func(c context.Context, w io.Writer) error {
				return o.rt.FollowLogs(c, o.containerName(name), opts, w, prefix)
			})
			errs[i] = err
			if exited {
				out.Write([]byte(OneLine(name+"-1") + " exited\n"))
			}
		}(i, name, prefixes[i])
	}
	wg.Wait()
	// If every stream genuinely failed, surface it (non-zero exit); a partial
	// failure still showed its diagnostic per stream.
	for _, e := range errs {
		if e == nil {
			return nil
		}
	}
	return fmt.Errorf("could not follow logs for any of the %d service(s)", len(targets))
}

// StatsOptions selects how `opossum stats` shows resource usage.
type StatsOptions struct {
	NoStream bool   // print a single snapshot instead of streaming
	Format   string // "table" (default) or "json" — json requires NoStream
}

// ServiceStat is one row of `opossum stats --no-stream --format json`: a
// service's container alongside its guest-view resource snapshot (the same
// numbers `container stats --format json` reports, with the service name
// joined on).
type ServiceStat struct {
	Service          string `json:"Service"`
	Container        string `json:"Container"`
	CPUUsageUsec     int64  `json:"CPUUsageUsec"`
	MemoryUsageBytes int64  `json:"MemoryUsageBytes"`
	MemoryLimitBytes int64  `json:"MemoryLimitBytes"`
	NetworkRxBytes   int64  `json:"NetworkRxBytes"`
	NetworkTxBytes   int64  `json:"NetworkTxBytes"`
	BlockReadBytes   int64  `json:"BlockReadBytes"`
	BlockWriteBytes  int64  `json:"BlockWriteBytes"`
	NumProcesses     int    `json:"NumProcesses"`
}

// Stats streams live resource usage (CPU / memory / net / block I/O / pids) for
// the requested services, or the whole project when none are named. With
// NoStream it prints a single snapshot, and with Format "json" that snapshot
// as a JSON array of ServiceStat. Mirrors `docker stats`.
func (o *Orchestrator) Stats(services []string, opts StatsOptions) error {
	if opts.Format == "json" && !opts.NoStream {
		return fmt.Errorf("--format json requires --no-stream: a streaming JSON array isn't well-formed line by line")
	}
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	// Only this project's containers are measured (see ownContainers): with
	// every container asked for someone else's there is nothing to measure
	// and nothing is printed, exit zero, as docker compose's — not the "no
	// container found" below, which is about this project's own services
	// and offers `opossum up`, which would refuse a container that is there
	// and someone else's. The ones the runtime gave no readable answer about
	// make the exit non-zero once the rest are measured, as `down` exits.
	own, names, unanswered := o.ownContainers(targets, "not measured")
	left := unansweredOwners(unanswered, "opossum stats")
	if len(own) == 0 {
		return left
	}
	if len(names) == 0 {
		return withOwners(fmt.Errorf("no container found for any of the %d service(s) — if they were never started, `opossum up` creates them", len(own)), left)
	}
	if opts.Format != "json" {
		if err := o.rt.Stats(names, opts.NoStream); err != nil {
			return withOwners(err, left)
		}
		return left
	}
	stats, err := o.rt.StatsSnapshot(names)
	if err != nil {
		return withOwners(err, left)
	}
	byID := make(map[string]int, len(stats))
	for i, s := range stats {
		byID[s.ID] = i
	}
	// Rows follow the services, in the order ps and images use: the runtime
	// answers in an order of its own (by id on container 1.4.1) and leaves a
	// stopped container out altogether, so a service without a reading has no
	// row, and a reading for a container nobody asked about is not printed.
	rows := make([]ServiceStat, 0, len(stats))
	for _, name := range targets {
		i, ok := byID[o.containerName(name)]
		if !ok {
			continue
		}
		s := stats[i]
		rows = append(rows, ServiceStat{
			Service:          name,
			Container:        s.ID,
			CPUUsageUsec:     s.CPUUsageUsec,
			MemoryUsageBytes: s.MemoryUsageBytes,
			MemoryLimitBytes: s.MemoryLimitBytes,
			NetworkRxBytes:   s.NetworkRxBytes,
			NetworkTxBytes:   s.NetworkTxBytes,
			BlockReadBytes:   s.BlockReadBytes,
			BlockWriteBytes:  s.BlockWriteBytes,
			NumProcesses:     s.NumProcesses,
		})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	fmt.Fprintln(o.out, string(b))
	return left
}

// ownContainers is the services whose container of the name is this
// project's to read or measure — or is not there, which a caller reads as
// "no container" — and, of those, the container names that are there
// (created), from one inspect per service. Only created ones go to
// `container stats`: 1.3.1 refuses the whole call when any name it is handed
// does not exist — "no such container", exit 1, nothing shown for the ones
// that do — where 1.2.2 skipped the missing name, so asking for a service
// that was never started would blank the stats of every other service.
// Stopped containers are kept: both versions skip those quietly. A
// container that is another project's, carries no project label, or that the
// runtime gave no readable answer about is left out and named on stderr with
// what was not done to it (`logs` and `stats` are read as tables or streams,
// so nothing prose goes to stdout), as `ps` leaves it out of its table.
// docker compose shows nothing for such a container. The ones the runtime
// gave no readable answer about are also returned in unanswered, for the
// caller to exit non-zero over (unansweredOwners) once it has read the rest:
// "not shown" is not the same as "not there", and a runtime that answers
// nothing must not read as a project with nothing to show.
func (o *Orchestrator) ownContainers(services []string, notDone string) (own, created, unanswered []string) {
	for _, name := range services {
		cname := o.containerName(name)
		info := o.rt.Inspect(cname)
		if why := o.notThisProjects(info); why != "" {
			fmt.Fprintf(os.Stderr, "%s: container %s %s — %s\n", name, cname, why, notDone)
			if info.Unknown {
				unanswered = append(unanswered, cname)
			}
			continue
		}
		own = append(own, name)
		if info.Exists {
			created = append(created, cname)
		}
	}
	return own, created, unanswered
}

// Copy copies files between a service's container and the host, like
// `docker compose cp`, delegating to `container cp`. Each of src/dst is a host
// path or `<service>:<path>`; a `<service>:` prefix naming a project service is
// rewritten to that service's running container name.
//
// A container that is not this project's — another project's with the same
// name, or one the runtime gives no readable answer about — is refused, as
// `exec` refuses it: `cp` reads and writes files inside it.
func (o *Orchestrator) Copy(src, dst string) error {
	for _, arg := range []string{src, dst} {
		if service, _, ok := o.copyService(arg); ok {
			if err := o.ensureNotForeign(o.containerName(service), "opossum cp"); err != nil {
				return err
			}
		}
	}
	return o.rt.Copy(o.resolveCopyArg(src), o.resolveCopyArg(dst))
}

// copyService reads a `<service>:<path>` argument: the service and the rest
// from the `:` on, when the prefix before the first `:` names a project
// service. The check and the rewrite both read it here, so the container
// that is checked is the one the copy goes to.
func (o *Orchestrator) copyService(arg string) (service, rest string, ok bool) {
	if i := strings.IndexByte(arg, ':'); i > 0 {
		if _, known := o.Project.Services[arg[:i]]; known {
			return arg[:i], arg[i:], true
		}
	}
	return "", "", false
}

// resolveCopyArg rewrites a `<service>:<path>` argument to
// `<container-name>:<path>` when the prefix names a project service; other
// arguments (host paths, or a prefix that isn't a service) pass through.
func (o *Orchestrator) resolveCopyArg(arg string) string {
	if service, rest, ok := o.copyService(arg); ok {
		return o.containerName(service) + rest
	}
	return arg
}

// resolveServices resolves the requested service names (or all, in startup
// order) and rejects any that the project doesn't define. Shared by logs, stop,
// restart, and stats.
func (o *Orchestrator) resolveServices(services []string) ([]string, error) {
	if len(services) == 0 {
		return o.startupOrder()
	}
	// A service named twice is one service, in the position it was first
	// named: docker compose v5.5.0 prints `logs web web` once, and a command
	// that reads, counts or names containers must not read, count or name
	// one twice.
	var once []string
	for _, s := range services {
		if _, ok := o.Project.Services[s]; !ok {
			return nil, o.unknownServiceErr(s)
		}
		if !slices.Contains(once, s) {
			once = append(once, s)
		}
	}
	return once, nil
}

// Import brings each build service's Docker-built image into container's store,
// so `up` starts it without rebuilding in Apple's builder. Handy for onboarding
// (reuse images an existing `docker compose` already built) or as a fallback
// when Apple's builder can't handle a Dockerfile. With no services all build
// services are imported; otherwise the named ones. docker compose and opossum
// name a built image the same way (the service's `image:` if it has one,
// `<project>-<service>:latest` if not — see serviceImage), so it lands under
// the name `up` looks for.
func (o *Orchestrator) Import(services ...string) error {
	order, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	imported := 0
	for _, name := range order {
		svc := o.Project.Services[name]
		target, built := o.serviceImage(name, svc)
		if !built {
			if len(services) > 0 {
				o.logf("Skipping %s: no build to import (uses image %s)\n", name, target)
			}
			continue
		}
		// docker compose tags a build service by its `image:` if set, otherwise by
		// `<project>-<service>`, and so does opossum: the name to bring over is
		// the name `up` looks for.
		dockerRef := target
		o.logf("Importing %s from Docker (%s)\n", name, dockerRef)
		if err := o.rt.ImportFromDocker(dockerRef, target); err != nil {
			return fmt.Errorf("importing service %q: %w", name, err)
		}
		imported++
	}
	if imported == 0 {
		o.logf("No build services to import.\n")
	}
	return nil
}

// buildFailed wraps a build error with a pointer to the Docker-import fallback,
// so a builder that can't handle a Dockerfile (or is misbehaving) isn't a dead
// end.
//
// Not over a registry that refused an image the Dockerfile names: that is not
// the builder failing to cope, and Docker is refused the same image (measured
// with nothing logged in and the image not held locally: `docker build` over
// the same Dockerfile fails on the same name). The build error already carries
// what to check. The way round does work where Docker has the image and the
// runtime does not — held locally, or behind a login only Docker has. The hint
// does not say so: a login is better answered by the runtime logging in
// ("registry auth" there), and docs/compatibility.md names both cases where the
// way round itself is described.
func buildFailed(service string, err error) error {
	if errors.Is(err, runtime.ErrBuildImageRefused) {
		return fmt.Errorf("building service %q: %w", service, err)
	}
	return fmt.Errorf("building service %q: %w\n"+
		"  (if Apple's builder can't handle this Dockerfile, build it with Docker and import it: opossum import %s)", service, err, service)
}

// startFailure is a generic (non-decoded) container-start failure. It is a type
// rather than a formatted string because the way out depends on something that
// has not happened yet when the failure is made: the rollback runs afterwards
// and usually removes the container, and `opossum logs` can only be read while
// the container is there. The rollback marks it (removed), and the wording is
// chosen when the message is finally read.
type startFailure struct {
	service string
	err     error
	// noContainer is set by the rollback when it confirmed this service has no
	// container left. Confirmed, not assumed: a rollback that could not delete
	// it, or could not ask the runtime, leaves the container standing and its
	// logs readable.
	noContainer bool
}

func (e *startFailure) Error() string {
	way := fmt.Sprintf("check why with `opossum logs %s`, or verify the image, command, and mounts in the compose file", e.service)
	if e.noContainer {
		// Pointing at the logs here sends the reader to a container that is not
		// there: `opossum logs` answers `container … not found`, and the guidance
		// reads as advice that was never written. Said as "none is left" rather
		// than "the rollback removed it", because the two cases the teardown
		// cannot tell apart — a container it deleted, and one that was never made
		// (an image that would not come down) — are the same for a reader who
		// wants to read logs. What is left is what the runtime already printed
		// above: the stderr is streamed live, before any teardown.
		way = "there is no container left to read logs from — " +
			"the failure above is what there is; verify the image, command, and mounts in the compose file"
	}
	return fmt.Sprintf("starting service %q: %v\n  %s", e.service, e.err, way)
}

func (e *startFailure) Unwrap() error { return e.err }

// disownLast takes the container this iteration just put on the rollback list
// back off it: the runtime refused to start it because something else holds
// the name, so it is not this up's to stop or remove. The list's last entry is
// that container (nothing is appended between the append and the run); the
// name is checked rather than assumed, so a change to that ordering cannot
// quietly take someone else's entry off instead.
func disownLast(started []string, createdSvc map[string]bool, cname, svc string) ([]string, map[string]bool) {
	if n := len(started); n > 0 && started[n-1] == cname {
		started = started[:n-1]
	}
	delete(createdSvc, svc)
	return started, createdSvc
}

// nameTakenError is the refusal for a container name that something else took
// between this up freeing it and starting it. It says what this up knows — the
// name is held — and not who holds it, which it cannot tell.
func nameTakenError(svc, cname, why string) error {
	return fmt.Errorf("starting service %q: something else now holds the container name %q — it appeared after this `up` made sure the name was free, so this `up` did not create it and will not remove it; %s\n"+
		"  see it with `container ls -a`; once it is gone, or is the one you want, run `opossum up` again", svc, cname, why)
}

// imageFetchFailed reports whether a run failed while the registry was being
// asked for the image, and the runtime then said there is no container of this
// service's name. container 1.4.1 says the first in a line of its own — `Error: HTTP request to <url> failed with response:
// …` — naming the request it made: a read of the image's manifest or of one of
// its blobs. The answers differ (a registry that needs a login and an image
// that is not there both answer 401, because Docker Hub answers 401 for a
// repository that does not exist; a tag that is not there answers 404; a blob
// that is not there answers 404 with the registry's own JSON — measured, five
// shapes asked of the runtime directly — the two blob shapes against a
// registry on this machine over `--scheme http`, a flag opossum passes
// nowhere, so what they are evidence of is the wording and not the path
// opossum takes to it), so what is read is the request. Read the answer
// instead and an image name typed wrong would be called a login problem.
//
// Two things keep this from answering for a container's own output: the line
// has to be the runtime's (it begins the line, and carries the whole shape),
// and the runtime has to say there is no container of the name — a foreground
// `up` hands this function the container's stderr as well as the runtime's,
// and a service that talks to a registry prints lines that look like these. A
// runtime that will not say is not a runtime that said no.
//
// One measured shape is not told apart: a blob whose download ends early says
// `Error: stream ended at an unexpected time` and names no request at all.
// Nothing in it says the image was what failed, so it keeps the answer every
// other start failure gets. Widening to the progress lines would reach it —
// and would also reach a container that started after its image came down
// fine, which has not been measured.
func (o *Orchestrator) imageFetchFailed(service string, err error) bool {
	var re *runtime.RunError
	if !errors.As(err, &re) {
		return false
	}
	said := false
	for _, line := range strings.Split(re.Stderr, "\n") {
		if !strings.HasPrefix(line, "Error: HTTP request to ") || !strings.Contains(line, " failed with response: ") {
			continue
		}
		if strings.Contains(line, "/manifests/") || strings.Contains(line, "/blobs/") {
			said = true
		}
	}
	if !said {
		return false
	}
	// And nothing was made: a container that printed the line itself is still
	// there to be asked about, and its logs are where its trouble is. A
	// runtime that would not say is not the same as one that said no — read
	// as "gone", an apiserver that stopped answering would quietly take this
	// guard off (#957) — so an answer that did not come keeps the guidance
	// this failure had.
	info := o.rt.Inspect(o.containerName(service))
	return !info.Exists && !info.Unknown
}

// imageUnreachable is the way out for an image the runtime could not get, in
// the words `pull` uses for it: one sentence, two things to check, because the
// runtime's answer does not always tell them apart.
func imageUnreachable(image string) string {
	return fmt.Sprintf("check the image name %q and that it's reachable (registry auth / network)", image)
}

func startFailed(service string, err error) error {
	return &startFailure{service: service, err: err}
}

// RunOneOffOptions configures a one-off `run`.
type RunOneOffOptions struct {
	Rm     bool // remove the container after it exits
	NoDeps bool // don't start the service's dependencies first
	TTY    bool // allocate a TTY (the CLI sets this when its own stdin is a terminal)
	SSH    bool // forward the host SSH agent (--ssh), on top of the service's own `ssh:`
	// Audit keeps the one-off's stdout on stderr (instead of the real stdout) so the
	// audit report — the deliverable of `run --audit` — owns stdout cleanly. Set by
	// RunAudited, not the CLI directly.
	Audit bool
}

// RunOneOff starts a single throwaway container for a service in the foreground,
// like `docker compose run`: a distinct name (so it never collides with the
// service's `up` container), the command overridden when given, and no published
// ports. Dependencies are started first unless NoDeps.
func (o *Orchestrator) RunOneOff(service string, command []string, opts RunOneOffOptions) (err error) {
	svc, ok := o.Project.Services[service]
	if !ok {
		return o.unknownServiceErr(service)
	}
	if !o.rt.Available() {
		return ErrRuntimeAbsent()
	}
	// Ask for the environment here, at the top, and not where it is first
	// needed. Between there and here this starts dependencies, creates the
	// network, builds images and deletes a stale container of the same name —
	// and a refusal afterwards leaves the started dependencies running, which
	// nothing takes back. `up` asks in its own pre-flight for the same reason.
	env, err := svc.ResolvedEnv()
	if err != nil {
		return err
	}
	// What reading the project refuses — a dependency behind an inactive
	// profile, then a cycle — once the environment is read and before anything
	// about the one-off itself, as docker compose v5.5.1 refuses those first
	// and wherever in the file they sit (measured; it refuses them before a
	// missing env_file too, which this does not follow). `--no-deps` does not
	// change this: the project is read the same way either way.
	if err := o.checkProjectLoads(map[string]bool{service: true}, false); err != nil {
		return err
	}
	// The stale one-off deleted below is found by name, and a container of
	// another project can carry that name (with `--dns-domain ""`, names are bare
	// service names): refuse here, before anything starts, as `up` does for its
	// own containers.
	if err := o.ensureNotForeign(o.containerName(service+"-run"), "opossum run"); err != nil {
		return err
	}
	// The names of the networks the one-off joins, before the dependencies
	// start: their `up` checks the names of the networks it creates, but not
	// one only this service joins.
	if err := o.checkNetworkNames(o.runNetworks(svc)); err != nil {
		return err
	}
	if err := o.checkContainerName(service, o.containerName(service+"-run")); err != nil {
		return err
	}
	// The one-off and the services it depends on, in the order docker compose
	// v5.5.1 refuses them (measured, #1072): what the runtime must already have
	// (an external network, then an external volume), then the names it would
	// have to create, then what the engine would refuse when the container is
	// made. `--no-deps` changes only the last of those: a dependency's named
	// volume and the external things it names are still checked, because docker
	// creates and looks for them either way, while its container — and so its
	// tmpfs — is never made.
	chain := o.withDependencies(service)
	made := chain
	if opts.NoDeps {
		made = []string{service}
	}
	if err := o.checkExternalNetworks(chain); err != nil {
		return err
	}
	if err := o.checkExternalVolumes(chain); err != nil {
		return err
	}
	if err := o.checkVolumeNames(chain, made...); err != nil {
		return err
	}
	if err := o.checkTmpfsOptions(made); err != nil {
		return err
	}
	if err := o.checkGroupAdd(made); err != nil {
		return err
	}

	// Keep the one-off's own stdout clean (e.g. an MCP server's JSON-RPC over
	// stdio): dependency startup, build, and volume-seeding progress all go to
	// stderr; only the one-off body (below) writes to the real stdout.
	o.rt.Out = os.Stderr
	defer func() { o.rt.Out = nil }()

	// Tell the caller if the run target carries compose fields opossum ignores, so
	// an invalid field doesn't look like it took effect. When dependencies will
	// start, their Up reports the project-wide top-level fields, so skip them here
	// to avoid counting them twice in one `run`.
	willStartDeps := !opts.NoDeps && len(svc.DependsOn.Names()) > 0
	o.reportIgnoredFields([]string{service}, !willStartDeps)

	if !opts.NoDeps {
		if deps := svc.DependsOn.Names(); len(deps) > 0 {
			if err := o.Up(true, deps...); err != nil {
				return fmt.Errorf("starting dependencies: %w", err)
			}
		}
	}

	// Create the network(s) the one-off joins (its declared networks, or the
	// default project net). Isolated (`network_mode: none`) and external networks
	// are not created by opossum.
	if svc.NetworkMode != compose.NetworkModeNone {
		for _, rn := range o.networksFor(svc) {
			if rn.external {
				continue
			}
			if _, err := o.ensureNetwork(rn); err != nil {
				var changed *subnetChangedError
				if errors.As(err, &changed) {
					return err
				}
				if o.interrupted() != nil {
					return runInterrupted("the network for " + service + " was not created")
				}
				return fmt.Errorf("couldn't create network %q for the run: %w\n"+
					"  check the runtime is healthy (`opossum doctor`); if a stale network with that name exists, remove it with `container network delete %s`", rn.name, err, rn.name)
			}
			if rn.internal {
				o.warnInternalNetwork(rn.name)
			}
		}
	}

	image, _ := o.serviceImage(service, svc)
	if svc.Build != nil {
		o.logf("Building %s\n", service)
		if err := o.rt.Build(o.buildOptions(image, svc.Build, "opossum run")); err != nil {
			// A Ctrl-C mid-build is not a Dockerfile the builder cannot handle.
			// Nothing has been started yet, so there is nothing to roll back — say
			// only what was abandoned.
			if o.interrupted() != nil {
				return runInterrupted("the build of " + service + " was abandoned")
			}
			return buildFailed(service, err)
		}
	}

	cmd := []string(svc.Command)
	if len(command) > 0 {
		cmd = command
	}

	// A distinct name so the one-off never clobbers the service's up container.
	cname := o.containerName(service + "-run")
	o.rt.Delete(cname) // clear a stale one-off of the same name

	if err := o.refuseSymlinkedSocketMounts(service, svc.Volumes); err != nil {
		return err
	}
	if err := o.ensureBindDirs(service, svc.Volumes, "`opossum run`"); err != nil {
		return err
	}
	if err := o.seedVolumes(service, svc, image); err != nil {
		if o.interrupted() != nil {
			return runInterrupted("the fill of a new volume for " + service + " was taken back")
		}
		return err
	}
	// Pre-flight the exclusive-attach conflict for the one-off too (as `up` does):
	// if a running container — including this service's own `up` container — already
	// holds a volume the one-off needs, it will fail to attach. Exclude only our
	// just-deleted run container.
	o.warnBusyVolumesFor([]string{service}, map[string]bool{cname: true})
	o.logf("Running one-off %s\n", service)
	// The one-off body's stdout is the real stdout (e.g. MCP JSON-RPC) — except under
	// --audit, where the audit report owns stdout, so the container output stays on
	// stderr with the rest of the progress.
	o.rt.Out = os.Stdout
	if opts.Audit {
		o.rt.Out = os.Stderr
	}
	mem, cpu, _ := svc.Resources() // validated at load
	svcNets, dnsDomain, dnsSearch := o.serviceNetworks(svc)
	vols := append(o.resolveVolumes(service, svc.Volumes), o.secretMounts(svc)...)
	cfgMounts, err := o.configMounts(service, svc, true)
	if err != nil {
		return err
	}
	vols = append(vols, cfgMounts...)
	mcpMount, err := o.mcpConfigMount(service, svc, true)
	if err != nil {
		return err
	}
	if mcpMount != "" {
		vols = append(vols, mcpMount)
		env = append(append([]string(nil), env...), "OPOSSUM_MCP_CONFIG="+mcpMountTarget)
	}
	runErr := o.rt.Run(runtime.RunOptions{
		Name:       cname,
		Image:      image,
		Platform:   svc.Platform,
		Networks:   svcNets,
		DNSDomain:  dnsDomain,
		DNSSearch:  dnsSearch,
		MacAddress: svc.MacAddress,
		Env:        env,
		Volumes:    vols,
		Tmpfs:      tmpfsMounts(svc.Tmpfs),
		Command:    cmd,
		Entrypoint: svc.Entrypoint,
		Labels:     append(append([]string(nil), svc.Labels...), projectLabel+"="+o.Project.Name),
		Memory:     mem,
		CPUs:       cpu,
		Detach:     false, // foreground / attached
		// Keep stdin connected (docker compose run parity): piped input must
		// reach the process, so stdin-driven tools (e.g. MCP servers speaking
		// JSON-RPC over stdio) work as one-offs.
		Interactive: true,
		TTY:         opts.TTY,
		Attached:    opts.TTY,
		// Forward the SSH agent if the service asks for it or the caller passed --ssh.
		SSH:        svc.SSH || opts.SSH,
		User:       svc.User,
		WorkingDir: svc.WorkingDir,
		Init:       svc.Init,
		ReadOnly:   svc.ReadOnly,
		GID:        gidOf(svc),
		ShmSize:    string(svc.ShmSize),
		Ulimits:    svc.Ulimits.Args(),
		CapAdd:     svc.CapAdd,
		CapDrop:    svc.CapDrop,
		// No published ports for a one-off (matches docker-compose run).
	})
	// A Ctrl-C kills the attached `run`, and the container it started keeps
	// running unless something stops it — measured on container 1.4.1, where
	// the process died of the signal with nothing said and the one-off stayed
	// up. Stop it here (and remove it when --rm asked for that), on a live
	// context: the cancelled one would kill the teardown commands too. Then say
	// what happened; the run's own error is the kill, not a result.
	if ierr := o.interrupted(); ierr != nil {
		o.rt.Ctx = context.Background()
		o.rt.Stop(cname)
		// "Stopped" is checked, not assumed — as the rollback of an `up` checks
		// its removals. `stop`'s exit code says only whether the name exists
		// (0 for running, stopped and exited alike; measured on container 1.4.1),
		// so the runtime is asked what became of the container. Gone (the CLI
		// says "not found") counts as stopped: nothing is left running either
		// way; a runtime that could not be asked at all does not. On 1.4.1 `stop` is
		// synchronous (a container ignoring SIGTERM is stopped when it returns,
		// after a ~5 s grace), so the "still running" branch has no known way
		// to be reached today; it stands so that a runtime whose stop becomes
		// asynchronous is reported rather than trusted.
		if opts.Rm {
			o.rt.Delete(cname)
			switch info := o.rt.Inspect(cname); {
			case info.Unknown:
				return fmt.Errorf("interrupted — tried to stop and remove %s, but the runtime could not be asked whether it is gone; `container ls -a` shows it, `container delete --force %s` removes it", cname, cname)
			case info.Exists:
				return fmt.Errorf("interrupted — tried to stop and remove %s, but it is still there; `container delete --force %s` removes it", cname, cname)
			}
			return fmt.Errorf("interrupted — stopped and removed %s", cname)
		}
		info := o.rt.Inspect(cname)
		switch {
		case info.Unknown:
			// Not "stopped": nothing answered. Saying so is the whole point of
			// asking — the stop reached the same runtime, so it may not have either.
			return fmt.Errorf("interrupted — tried to stop %s, but the runtime could not be asked whether it stopped; `container ls -a` shows it, `container stop %s` stops it", cname, cname)
		case info.Exists && info.State == "running":
			return fmt.Errorf("interrupted — tried to stop %s, but it is still running; `container stop %s` stops it (it is kept, as without --rm)", cname, cname)
		case !info.Exists:
			// Nothing is running, which is what was wanted — but this run did not
			// remove it (no --rm), so saying "kept" would be false, and saying
			// nothing would report someone else's doing as this run's result.
			return fmt.Errorf("interrupted — stopped %s; it is no longer there (something else removed it)", cname)
		}
		return fmt.Errorf("interrupted — stopped %s (it is kept, as without --rm; `opossum run --rm` removes it)", cname)
	}
	if opts.Rm {
		o.rt.Delete(cname)
	}
	// Decode ONLY the exclusive-attach VZError (OPSM-103); every other failure — most
	// commonly the command's own non-zero exit — passes through untouched so `run`
	// keeps propagating exit codes.
	if runErr != nil {
		if decoded, ok := o.decodeVolumeAttachError(service, cname, runErr); ok {
			return decoded
		}
	}
	return attachedExit(runErr)
}

// Exec runs a command in a service's running container, streaming stdio.
func (o *Orchestrator) Exec(service string, command []string, opts runtime.ExecOptions) error {
	if _, ok := o.Project.Services[service]; !ok {
		return o.unknownServiceErr(service)
	}
	if len(command) == 0 {
		return fmt.Errorf("exec requires a command to run")
	}
	// The command would run inside whatever container carries the name, so
	// another project's — or one nobody can say the owner of — is refused.
	if err := o.ensureNotForeign(o.containerName(service), "opossum exec"); err != nil {
		return err
	}
	return attachedExit(o.rt.ExecStream(o.containerName(service), command, opts))
}

// Build builds the images for services that declare `build:` (all, or the named
// ones). Services without a build are skipped.
func (o *Orchestrator) Build(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	named := namedSet(services)
	for _, name := range targets {
		// A service gated behind a profile that is not active is not built,
		// as docker compose v5.5.1 leaves it out (measured: `build` with no
		// service warns that none was selected). Naming it enables it, as
		// naming it does for `up`.
		if !o.enabled(name, named) {
			continue
		}
		svc := o.Project.Services[name]
		if svc.Build == nil {
			continue
		}
		image, _ := o.serviceImage(name, svc)
		o.logf("Building %s\n", name)
		if err := o.rt.Build(o.buildOptions(image, svc.Build, "opossum build")); err != nil {
			return buildFailed(name, err)
		}
	}
	return nil
}

// Pull fetches the images for services that use `image:` (all, or the named
// ones). Build-only services have nothing to pull and are skipped.
func (o *Orchestrator) Pull(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	named := namedSet(services)
	for _, name := range targets {
		// Gated behind a profile that is not active: not pulled, as docker
		// compose v5.5.1 pulls only the active ones (measured). Naming it
		// enables it, as there.
		if !o.enabled(name, named) {
			continue
		}
		svc := o.Project.Services[name]
		if svc.Image == "" {
			continue
		}
		o.logf("Pulling %s (%s)\n", name, svc.Image)
		if err := o.rt.Pull(svc.Image); err != nil {
			return fmt.Errorf("pulling service %q: %w\n  %s", name, err, imageUnreachable(svc.Image))
		}
	}
	return nil
}

// Start starts already-created (stopped) containers in dependency order (all, or
// the named ones), without recreating them.
func (o *Orchestrator) Start(services []string) error {
	whole := len(services) == 0
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	targets, err = o.inStartupOrder(targets)
	if err != nil {
		return err
	}
	// One service that will not start does not stop the others: each failure
	// is kept and said at the end, beside the containers nobody would say
	// the owner of — returning at the first one left every later service
	// unstarted and those containers unmentioned. What it does stop is the
	// services that depend on it, as docker compose's start does: they would
	// come up without what they were declared to need. Targets are in startup
	// order, so a dependency is decided before anything that depends on it.
	var unanswered []string
	var failed []error
	notStarted := map[string]bool{}
	for _, name := range targets {
		if dep := o.firstNotStartedDependency(name, notStarted); dep != "" {
			notStarted[name] = true
			failed = append(failed, fmt.Errorf("not starting service %q: it depends on %q, which did not start", name, dep))
			continue
		}
		if !o.worksOn(name, whole, &unanswered) {
			continue
		}
		o.logf("Starting %s\n", name)
		// Starting it by hand undoes an earlier stop, so supervision resumes.
		o.ClearStopped(name)
		if err := o.rt.Start(o.containerName(name)); err != nil {
			notStarted[name] = true
			failed = append(failed, fmt.Errorf("starting service %q: %w\n  `start` only (re)starts an already-created container — if it doesn't exist yet, run `opossum up %s` first", name, err, name))
		}
	}
	return withOwners(startFailures("start", failed), unansweredOwners(unanswered, "opossum start"))
}

// startFailures is the failures of one start or restart. More than one is
// headed by their count and listed one entry per failure, each entry's further
// lines (the runtime's own, the next step) indented under it: every failure
// carries several lines, and without the marks the second reads as more of the
// first.
func startFailures(verb string, failed []error) error {
	return listedFailures(fmt.Sprintf("%d services did not %s:", len(failed), verb), failed)
}

// listedFailures is several failures under a heading, one entry each, as
// startFailures lays them out; one failure alone is itself.
func listedFailures(heading string, failed []error) error {
	if len(failed) < 2 {
		return errors.Join(failed...)
	}
	var b strings.Builder
	b.WriteString(heading)
	for _, f := range failed {
		lines := strings.Split(f.Error(), "\n")
		b.WriteString("\n- " + lines[0])
		for _, l := range lines[1:] {
			// The runtime's lines come unindented and the next step two in;
			// under an entry they all sit one level in.
			b.WriteString("\n    " + strings.TrimLeft(l, " "))
		}
	}
	return errorList{msg: b.String(), errs: failed}
}

// withOwners puts the containers nobody would say the owner of after the
// failures, a blank line apart: they are not among the counted failures.
func withOwners(failures, owners error) error {
	if failures == nil || owners == nil {
		return errors.Join(failures, owners)
	}
	return errorList{msg: failures.Error() + "\n\n" + owners.Error(), errs: []error{failures, owners}}
}

// errorList is several errors under a message of its own, still unwrapping to
// each of them.
type errorList struct {
	msg  string
	errs []error
}

func (l errorList) Error() string   { return l.msg }
func (l errorList) Unwrap() []error { return l.errs }

// inStartupOrder puts named services in the order `up` starts them, so that a
// dependency named after its dependent is still started, or found failing,
// first.
func (o *Orchestrator) inStartupOrder(targets []string) ([]string, error) {
	order, err := o.startupOrder()
	if err != nil {
		return nil, err
	}
	named := make(map[string]bool, len(targets))
	for _, t := range targets {
		named[t] = true
	}
	out := make([]string, 0, len(targets))
	for _, name := range order {
		if named[name] {
			out = append(out, name)
		}
	}
	return out, nil
}

// firstNotStartedDependency is the first of name's dependencies that failed to
// start in this run, or was itself held back, or "" when none was. A dependency
// not among the services asked for, or left because the runtime would not say
// its owner or because another project owns its name, does not hold back its
// dependents.
func (o *Orchestrator) firstNotStartedDependency(name string, notStarted map[string]bool) string {
	for _, dep := range o.Project.Services[name].DependsOn {
		if notStarted[dep.Name] {
			return dep.Name
		}
	}
	return ""
}

// Kill signals running containers (all, or the named ones) in reverse dependency
// order. An empty signal defaults to KILL.
//
// A killed service is recorded as stopped by the user, as `stop` records it, so
// the restart supervisor does not bring it back: docker compose v5.5.0 does not
// restart a container `kill` stopped, whatever the signal, and does restart one
// that exits on its own (measured, `restart: always`). The record is written
// before the signal, and whether or not the signal ends the container: after a
// `kill -s HUP` the container keeps running, and docker does not restart it
// when it later exits either (measured). `up`, `start` and `restart` clear it
// — where docker compose, for a container still running after the kill, lets
// only `restart` bring supervision back (measured; see docs/compatibility.md).
//
// A kill the runtime refuses leaves no record it did not find: the record this
// kill wrote is taken back (one written before — by `stop`, or by a kill the
// container outlived — stays). A container found not running afterwards was
// not killed, as docker compose kills only running ones, and that is not an
// error; one found running refused the signal (`kill -s BOGUS`), and one the
// runtime gives no readable answer about may have: both are errors, as docker
// compose exits 1 for a refused signal and for a daemon that does not answer.
// Every service is signalled whatever an earlier one did, and more than one
// failure is listed one entry each, as `start` lists its failures.
func (o *Orchestrator) Kill(services []string, signal string) error {
	whole := len(services) == 0
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	var unanswered []string
	var failed []error
	for i := len(targets) - 1; i >= 0; i-- {
		name, cname := targets[i], o.containerName(targets[i])
		if !o.worksOn(name, whole, &unanswered) {
			continue
		}
		o.logf("Killing %s\n", name)
		// Before the signal: a poll landing between the container ending and
		// the record being written would read the kill as a crash.
		recorded := o.wasStoppedByUs(name)
		o.MarkStopped(name)
		if err := o.rt.Kill(cname, signal); err != nil {
			if !recorded {
				o.ClearStopped(name)
			}
			switch info := o.rt.Inspect(cname); {
			case info.Unknown:
				failed = append(failed, fmt.Errorf("killing service %q: %w\n  the runtime gave no readable answer about the container afterwards, so whether it took the signal is not known", name, err))
			case info.Exists && info.State == "running":
				failed = append(failed, fmt.Errorf("killing service %q: %w", name, err))
			}
		}
	}
	// The heading says only that the kill failed: an entry may be one the
	// runtime gave no answer about, which may have taken the signal.
	return withOwners(listedFailures(fmt.Sprintf("the kill failed for %d services:", len(failed)), failed), unansweredOwners(unanswered, "opossum kill"))
}

// Stop stops services without removing them (unlike Down). With no names it stops
// the whole project in reverse dependency order; otherwise just the named ones.
func (o *Orchestrator) Stop(services []string) error {
	whole := len(services) == 0
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	var unanswered []string
	for i := len(targets) - 1; i >= 0; i-- {
		if !o.worksOn(targets[i], whole, &unanswered) {
			continue
		}
		o.logf("Stopping %s\n", targets[i])
		// Record it before stopping: the supervisor polls, and a stop it sees before
		// the marker exists would be read as a crash and undone.
		o.MarkStopped(targets[i])
		o.rt.Stop(o.containerName(targets[i]))
	}
	return unansweredOwners(unanswered, "opossum stop")
}

// Restart stops then starts services in place, keeping their existing config
// (containers and network are not recreated). With no names it restarts the
// whole project; otherwise just the named ones.
func (o *Orchestrator) Restart(services []string) error {
	whole := len(services) == 0
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	// Asked once per name, before anything is stopped: an answer that changed
	// between the stop and the start would leave a container stopped and not
	// started again.
	var unanswered, mine []string
	for _, name := range targets {
		if o.worksOn(name, whole, &unanswered) {
			mine = append(mine, name)
		}
	}
	// Restarting says "bring this back", so an earlier `stop` no longer stands;
	// and while it is under way a `logs --follow` is told the gap is not the
	// container's end (followUntilExit), however long the stops and starts take.
	// The marks first: while this waits for another restart of the same
	// services, an earlier `stop` still stands — it is this restart that
	// undoes it, and only once it is under way.
	_, releaseMarks := o.markRestartingAll(mine)
	defer releaseMarks()
	for _, name := range mine {
		o.ClearStopped(name)
	}
	for i := len(mine) - 1; i >= 0; i-- {
		o.rt.Stop(o.containerName(mine[i]))
	}
	// Every service stopped above is started again, whatever happened to the
	// one before it: returning at the first failure left the later ones
	// stopped by a command that was asked to bring them back.
	var failed []error
	for _, name := range mine {
		o.logf("Restarting %s\n", name)
		if err := o.rt.Start(o.containerName(name)); err != nil {
			failed = append(failed, fmt.Errorf("restarting service %q: %w\n  `restart` needs an already-created container — if it doesn't exist yet, run `opossum up %s` first", name, err, name))
		}
	}
	return withOwners(startFailures("restart", failed), unansweredOwners(unanswered, "opossum restart"))
}

// redo is the opossum command that asked for this build — "opossum up",
// "opossum run", or "opossum build" — echoed in build-failure hints so the
// retry advice names what the reader actually typed. A parameter, not a field:
// every new call site has to say which command it serves.
func (o *Orchestrator) buildOptions(tag string, b *compose.Build, redo string) runtime.BuildOptions {
	ctx := b.Context
	if ctx == "" {
		ctx = "."
	}
	resolved := o.resolvePath(ctx)
	return runtime.BuildOptions{
		Tag:        tag,
		Context:    resolved,
		Dockerfile: b.Dockerfile,
		// A bare `NAME` takes the shell's value here, and is left out when
		// the shell has none: Apple's builder does not read the shell itself
		// (`container build --build-arg A` gives the Dockerfile an empty A,
		// over its own `ARG A=default`; measured, container 1.3.1), where
		// `container run -e A` does.
		Args:   compose.ResolveBareNames(b.Args, os.LookupEnv, false),
		Target: b.Target,
		// Whose build this is, kept on the image: a built image can carry a name
		// the compose file chose (`image:`), and a name alone does not say that
		// this project made what is under it. Nothing reads it yet (#1126).
		Labels: []string{projectLabel + "=" + o.Project.Name},
		Redo:   redo,
	}
}

// subnetChangedError is the OPSM-207 refusal, its own type so the callers
// that wrap a failure to create a network can let it through unwrapped
// (`errors.As` matches the type itself; nothing unwraps it further).
type subnetChangedError struct{ error }

// volumeMount is a service's resolved volume mount. Arg is the runtime `-v`
// value. When the mount is backed by a project-owned volume (named or
// anonymous), Volume is that volume's name and Target its in-container path, so
// one classification drives resolveVolumes (startup), seedVolumes, and
// namedVolumes (`down -v`) alike — keeping them symmetric. Bind mounts and
// external volumes leave Volume empty (opossum neither seeds nor removes them).
type volumeMount struct {
	Arg    string
	Volume string
	Target string
}

// classifyVolume resolves one compose volume entry for a service into a mount.
func (o *Orchestrator) classifyVolume(svcName, entry string) volumeMount {
	// What the entry refers to is the loader's judgement (compose.ClassifyMount),
	// so what needed a declaration there is what is mounted by name here.
	kind, src := compose.ClassifyMount(entry)
	parts := strings.SplitN(entry, ":", 2)
	if kind == compose.MountAnonymous {
		// A single path (or an omitted source) is an anonymous volume at that path
		// (compose semantics), not a bind mount. Give it a deterministic
		// per-service name so re-up reuses (and `down -v` removes) the same volume.
		target := parts[len(parts)-1]
		name := o.anonVolumeName(svcName, target)
		return volumeMount{Arg: name + ":" + target, Volume: name, Target: target}
	}
	rest := parts[1]
	target := strings.SplitN(rest, ":", 2)[0] // container path, minus any :ro/:rw
	switch {
	case kind == compose.MountBind:
		// Bind mount: make the host side absolute relative to the compose dir.
		return volumeMount{Arg: o.resolvePath(src) + ":" + rest}
	case o.isExternalVolume(src):
		// External volumes are user-managed: real name, never namespaced/removed/
		// seeded (#64).
		return volumeMount{Arg: o.externalRealName(src) + ":" + rest}
	default:
		// Named volume: namespaced per project so concurrent projects don't share
		// one global volume (#63), unless its declaration names it (#937).
		name := o.volumeName(src)
		return volumeMount{Arg: name + ":" + rest, Volume: name, Target: target}
	}
}

// serviceMounts classifies every volume of a service.
func (o *Orchestrator) serviceMounts(svcName string, vols []string) []volumeMount {
	out := make([]volumeMount, 0, len(vols))
	for _, v := range vols {
		out = append(out, o.classifyVolume(svcName, v))
	}
	return out
}

// resolveVolumes returns the `-v` args for a service's volumes.
func (o *Orchestrator) resolveVolumes(svcName string, vols []string) []string {
	mounts := o.serviceMounts(svcName, vols)
	out := make([]string, len(mounts))
	for i, m := range mounts {
		out[i] = m.Arg
	}
	return out
}

// anonVolumeName is the project-namespaced name for an anonymous volume, derived
// from the service and in-container path so it stays stable across re-ups.
//
// The path's characters other than letters, digits, `_` and `-` are written
// `_`: container 1.4.1 creates only volume names matching
// `^[A-Za-z0-9][A-Za-z0-9_.-]*$`, and a path holding `+`, `@` or `é` used to
// give a name `up` failed on (`invalid volume name`). A path of letters, digits,
// `_`, `-`, `/`, `.` and spaces gets the name it always had (those three were
// already written `_`), so an existing volume is still found.
//
// A name longer than runtime.MaxVolumeNameLen keeps the start of the path and
// loses the rest: container 1.4.1 refuses a longer name, so `up` failed on it
// (docker names an anonymous volume itself and runs the same file). The hash
// is of the whole path, so two paths that differ only past the cut still get
// different names. A name that fits is left as it was.
func (o *Orchestrator) anonVolumeName(svcName, target string) string {
	san := anonVolumePathOutside.ReplaceAllString(strings.Trim(target, "/"), "_")
	// A hash of the exact path makes the name deterministic (re-up reuses the same
	// volume) yet collision-proof: paths that sanitize alike (`/a/b` vs `/a.b`)
	// stay distinct, and the suffix keeps anonymous names from ever coinciding
	// with a project-namespaced named volume (#123).
	h := fnv.New32a()
	h.Write([]byte(target))
	prefix := o.Project.Name + "_" + svcName + "_"
	suffix := fmt.Sprintf("_%08x", h.Sum32())
	if over := len(prefix) + len(san) + len(suffix) - runtime.MaxVolumeNameLen; over > 0 {
		san = san[:max(len(san)-over, 0)]
	}
	return prefix + san + suffix
}

// anonVolumePathOutside is what anonVolumeName writes `_` for.
var anonVolumePathOutside = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// tmpfsDefaults are the options docker puts on every tmpfs mount before the
// ones the file writes.
const tmpfsDefaults = "nosuid,nodev,noexec"

// tmpfsMounts gives each tmpfs mount docker's default options: `/t` becomes
// `/t:nosuid,nodev,noexec`, and `/t:exec,mode=700` becomes
// `/t:nosuid,nodev,noexec,exec,mode=700`. Put first, the defaults give way to an
// `exec`, `suid` or `dev` the file writes, since the later of two such options
// wins — in the docker engine and in container 1.4.1 alike. Measured with the
// options spelled these ways (none, `ro`, `rw`, each of `exec`/`suid`/`dev`,
// `exec,suid,dev`, `mode=`, `size=`, `noexec,exec` and `exec,noexec`): the mount
// options, whether a file in the mount runs and whether a device node in it
// opens were the same on the docker engine 29.7.2 with the options as written
// and on container 1.4.1 with the defaults put first. The options are split
// from the target at the first `:`, as an option may hold one (`mpol=bind:0`). Without them a file in a tmpfs mount ran under
// opossum that docker refuses to run.
//
// A `defaults` option is dropped: the docker engine 29.7.2 reads it as nothing
// wherever it stands (`/t:defaults,exec` mounts as `/t:exec`), where container
// 1.4.1 fails the mount with errno 22 (`data=defaults`). Only the lower-case
// word: the engine refuses `DEFAULTS` as an invalid tmpfs option.
func tmpfsMounts(mounts []string) []string {
	var out []string
	for _, m := range mounts {
		target, opts, _ := strings.Cut(m, ":")
		kept := []string{tmpfsDefaults}
		if opts != "" {
			for _, o := range strings.Split(opts, ",") {
				if o != "defaults" {
					kept = append(kept, o)
				}
			}
		}
		out = append(out, target+":"+strings.Join(kept, ","))
	}
	return out
}

// warnForeignPostgresDataVolume warns, before a Postgres service starts, that its
// data directory is a volume opossum did not prepare and that still holds
// `lost+found` — which is exactly what initdb refuses to initialise into.
//
// opossum clears `lost+found` out of the volumes it creates, so this cannot be
// predicted from the shape of the mount any more: the same compose line is the
// working case on a fresh volume and the failing case on an old one. The
// difference is inside the volume, so that is where this looks. Nothing is said
// unless the two facts are seen — the directory is there, and no cluster is
// (`PG_VERSION`), so initdb has yet to run and will refuse when it does.
//
// It reads and never writes. Clearing an existing volume would be opossum
// reaching into data it did not create, on a guess about what the user wants
// there; the two remedies are the user's to choose between.
func (o *Orchestrator) warnForeignPostgresDataVolume(svcName string, svc *compose.Service, image string) {
	if svc == nil || hasPGDATASubdir(svc) {
		return
	}
	for _, m := range o.serviceMounts(svcName, svc.Volumes) {
		// m.Volume is empty for bind mounts and for `external: true` volumes — the
		// ones opossum does not manage, and does not tell the user what to do with.
		// Today that half is belt and braces: classifyVolume leaves Target empty for
		// those too, so the data-directory test already excludes them. It is kept
		// because the reason they are excluded is ownership, not a gap in another
		// field, and the day Target is filled in for them this line is what still
		// says so.
		if m.Volume == "" || strings.TrimRight(m.Target, "/") != postgresDataDir {
			continue
		}
		if !o.rt.VolumeExists(m.Volume) {
			continue // opossum is about to create this one, and creates it empty
		}
		if o.up.dryRun && !o.rt.ImageExists(image) {
			// The look runs the image, and running an image that is not here yet
			// fetches it. A dry-run that pulls gigabytes to answer a question is not
			// a dry run, so it goes without the answer — not knowing reads the same
			// as not being able to look.
			//
			// Only under dry-run. A real `up` builds a `build:` service before this
			// point but does not pre-pull a plain `image:` one — the run itself does
			// that — so refusing to look at a missing image would go quiet on exactly
			// the first up after an image was pruned, which is a case where the
			// volume is old and this warning is most likely to be true. There the
			// pull is not a cost the look imposed: the container start seconds later
			// would have done it anyway.
			continue
		}
		entries, err := o.rt.VolumeEntries(m.Volume, image)
		if err != nil {
			// Could not look: an image with no shell, a volume another container
			// holds. Saying nothing is the only honest option — and if it does fail,
			// initdb's own refusal is decoded then (crashHint).
			continue
		}
		lostFound, initialised := false, false
		for _, e := range entries {
			switch e {
			case "lost+found":
				lostFound = true
			case "PG_VERSION":
				initialised = true // a cluster is already here; initdb won't run again
			}
		}
		if !lostFound || initialised {
			continue
		}
		o.warnf(codePGDATADatadir, "service %q won't start as written: the volume %q was not created by this "+
			"opossum and still holds `lost+found`, the directory ext4 puts in every filesystem it makes — "+
			"Postgres refuses to initialise a data directory that isn't empty. Volumes opossum creates are "+
			"cleared of it, so either let it make this one (`opossum down -v` removes this project's volumes, "+
			"then `opossum up` — nothing has initialised here, but check nothing else put anything in it), or "+
			"keep the data below the mount point by adding `environment: PGDATA=%s/pgdata` to the service. "+
			"opossum does not clear a volume it did not create.\n", svcName, m.Volume, postgresDataDir)
	}
}

// hasPGDATASubdir reports whether the service sets PGDATA to a path below the
// Postgres data directory — the arrangement that sidesteps the empty-directory
// requirement altogether, and so has nothing to be warned about.
func hasPGDATASubdir(svc *compose.Service) bool {
	for _, e := range svc.Environment {
		if v, ok := strings.CutPrefix(e, "PGDATA="); ok {
			// Must be a real subdirectory under the datadir: `.../data/<name>`,
			// not the datadir itself (`.../data` or a bare `.../data/`).
			sub, ok := strings.CutPrefix(v, postgresDataDir+"/")
			return ok && strings.Trim(sub, "/") != ""
		}
	}
	return false
}

// seedVolumes fills each of a service's project-owned volumes from the image's
// contents at the mount path the FIRST time that volume is created, mirroring
// Docker (Apple `container` mounts a fresh volume empty). Existing volumes are
// left untouched, so user data and prior state are preserved.
//
// It returns an error only for an interruption: a Ctrl-C while the throwaway
// seeding container runs kills that run from outside, which `--rm` does not
// survive — the container keeps running with the half-filled volume attached
// (measured on container 1.4.1). The seed is then taken back here, by name:
// the container stopped and removed and the volume deleted, so the next `up`
// starts from nothing rather than from half of the image's content. The
// caller returns that error, and its rollback removes what it had started.
func (o *Orchestrator) seedVolumes(svcName string, svc *compose.Service, image string) error {
	if svc == nil {
		return nil
	}
	// `volume: {nocopy: true}` is the compose file saying "mount this empty" —
	// typically because the image's copy is stale or huge and the real content
	// arrives another way. Seeding it anyway would override an explicit
	// instruction, which docker does not do.
	//
	// The service is passed in rather than looked up: the mounts and the option
	// that switches them off have to come from the same place, or a caller could
	// hand over one service's volumes while this read another's.
	vols := svc.Volumes
	skip := map[string]bool{}
	for _, t := range svc.NoCopy {
		skip[strings.TrimRight(t, "/")] = true
	}
	for _, m := range o.serviceMounts(svcName, vols) {
		// m.Volume is empty for bind mounts and `external: true` volumes: the ones
		// opossum does not own. Neither is ever created here, so neither is ever
		// touched by what follows.
		if m.Volume == "" || o.rt.VolumeExists(m.Volume) {
			continue
		}
		if skip[strings.TrimRight(m.Target, "/")] {
			// Asked to be mounted empty. That is still a volume opossum is creating,
			// and "empty" has to mean the same thing it means everywhere else — so it
			// gets the same clearing, and only the copy is skipped. Leaving it alone
			// would make `nocopy` the one way to end up with ext4's `lost+found` in a
			// fresh volume, which is the opposite of what it asks for.
			if err := o.rt.PrepareVolume(m.Volume, image); err != nil {
				if ierr := o.interrupted(); ierr != nil {
					o.takeBackSeed(m.Volume)
					return ierr
				}
				o.warnf(codeVolumeNotSeeded, "couldn't prepare the new volume %q with %s: %v\n"+
					"         nothing was copied into it (the compose file asked for that), but it also\n"+
					"         keeps `lost+found`, the directory ext4 puts in every filesystem it makes.\n"+
					"         opossum clears that with `sh` inside the image, so the usual cause is an\n"+
					"         image without one (distroless and scratch builds). A program that checks\n"+
					"         whether its data directory is empty — Postgres's initdb — will refuse it.\n",
					m.Volume, image, err)
			}
			continue
		}
		if err := o.rt.SeedVolume(m.Volume, image, m.Target); err != nil {
			if ierr := o.interrupted(); ierr != nil {
				o.takeBackSeed(m.Volume)
				return ierr
			}
			// The volume mounts empty, which looks exactly like a service that lost its
			// data — and nothing later can tell the two apart, so this is the only
			// place to say it. The runtime's own words lead, verbatim: the usual cause
			// is an image with no `sh` to run the copy with, but a failed pull or a
			// full disk land here too, and only the runtime knows which.
			o.warnf(codeVolumeNotSeeded, "couldn't fill the new volume %q from %s at %s: %v\n"+
				"         it will mount empty. opossum copies with `cp -a` run by `sh` inside the image,\n"+
				"         so the usual cause is an image without one (distroless and scratch builds) —\n"+
				"         but the reason above is the runtime's own, so read that first.\n"+
				"         Put the content there another way (an init container, or a bind mount), or\n"+
				"         add `volume: {nocopy: true}` to record that an empty mount is intended.\n",
				m.Volume, image, m.Target, err)
		}
	}
	return nil
}

// runInterrupted is the verdict for a Ctrl-C before a one-off's own container
// started: it names what was abandoned and says the one-off never ran. It says
// nothing about dependencies on purpose — those `run` started are left up, as
// docker compose run leaves them, and a sentence claiming "nothing was
// started" was false whenever there were any.
func runInterrupted(what string) error {
	return fmt.Errorf("interrupted — %s; the one-off did not start", what)
}

// takeBackSeed removes what an interrupted seeding left behind: the throwaway
// container, still running because the kill came from outside, and the volume
// it was filling. On a live context — the cancelled one would kill these too.
//
// Deleting a volume can destroy data, so what makes this one safe is spelled
// out: the caller reaches the seed only for a volume that did not exist when
// it looked (VolumeExists, just above), so this one was made by the seed run
// itself. The window between that look and this delete is closed, under
// `up`, by two things together: the name is namespaced to the project
// (`<project>_<volume>`), and the project lock keeps a second `up` of the same
// project out — someone removing that lock takes this guarantee with it. A
// one-off `run` seeds outside that lock and relies on the namespace and the
// shortness of the window alone.
func (o *Orchestrator) takeBackSeed(volume string) {
	o.rt.Ctx = context.Background()
	name := runtime.SeedContainerName(volume)
	o.rt.Stop(name)
	o.rt.Delete(name)
	o.rt.DeleteVolume(volume)
}

// isHostPath is the loader's reading of a mount source (compose.IsHostPath):
// one function on both sides, so a source that loads as a bind runs as one.
func isHostPath(s string) bool {
	return compose.IsHostPath(s)
}

func (o *Orchestrator) resolvePath(p string) string {
	if p == "~" {
		p = "~/"
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		// The runtime doesn't expand ~, so resolve it to the home dir here.
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(o.Project.BaseDir, p)
}

// ensureBindDirs creates the host source directory of any bind mount that
// doesn't exist yet, matching docker compose (Apple `container` errors on a
// missing bind source instead of creating it). Only bind mounts are touched;
// named/anonymous volumes and external volumes are left to the runtime.
//
// redo is the command the advice below tells the reader to run again —
// "`opossum up`" from Up, "`opossum run`" from a one-off. Both callers reach
// this, and the advice used to say `opossum up` to both: the person who typed
// `run` was told to redo a command they never ran, right after a mkdir line
// that was worth copying exactly.
func (o *Orchestrator) ensureBindDirs(service string, vols []string, redo string) error {
	for _, v := range vols {
		mount, target, _, ok := splitMount(v)
		if !ok || !isHostPath(mount) {
			continue // single path = anonymous volume; non-path = named/external
		}
		src := o.resolvePath(mount)
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			continue
		}
		if mkErr := os.MkdirAll(src, 0o755); mkErr != nil {
			// Only a source that was already missing reaches here — the stat above
			// lets an existing file or directory through untouched, which is what
			// makes a `./nginx.conf:/etc/nginx/nginx.conf` mount work. So nothing is
			// there and opossum could not put it there, and the runtime
			// will refuse the mount — measured: `path '…' does not exist`. Saying so
			// here and stopping is the whole point; the previous version warned and
			// started the service anyway, and the user got this warning followed by
			// an unrelated-looking runtime error a second later.
			//
			// Two `up`s racing could in principle create the directory between the
			// stat and the mkdir, and this would then refuse an up that would have
			// worked. Re-checking would cover it, but nothing could drive that
			// branch in an eval — it needs the race — and an unguarded branch is
			// worth less than the honest note that running again succeeds.
			// A symlink whose target is gone is its own case: `stat` says the path
			// is not there (it follows the link), `mkdir` says it is (it does not),
			// and telling someone to `mkdir -p` it sends them to the same
			// contradiction — the command fails with `file exists` on a path
			// nothing can see.
			if fi, lerr := os.Lstat(src); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
				target, _ := os.Readlink(src)
				return fmt.Errorf("[%s] service %q needs %s for a bind mount, but it is a symlink to "+
					"%q, and there is nothing there\n"+
					"  the container cannot start without it — point the link at something that "+
					"exists, or remove the link and let opossum create the directory, then run "+
					"%s again",
					codeBindDirCreate, service, src, target, redo)
			}
			return fmt.Errorf("[%s] service %q needs the host directory %s for a bind mount, and it "+
				"could not be created: %v\n"+
				"  the container cannot start without it — create it yourself (`mkdir -p %s`) or fix "+
				"the parent directory's permissions, then run %s again",
				codeBindDirCreate, service, src, mkErr, src, redo)
		}
		o.logf("Created host directory %s for a bind mount\n", src)
		// A directory is all this can create, and for a mount that names a file
		// that is the wrong thing. The container starts either way — an init script
		// that isn't there simply doesn't run, a config that isn't there falls back
		// to a default — so the failure arrives later, as something else, with
		// nothing connecting it back to here. Say it now.
		//
		// The name is all there is to go on, so this cannot be certain: a directory
		// legitimately named `conf.d` or `.ssh` looks the same. The instruction is
		// therefore conditional — opossum says what it did and what to check, and
		// does not tell anyone to delete something it may have been right to create.
		//
		// No `%[n]` indices in this format: warnf puts the code in front of the
		// arguments, so an explicit index here points one place to the left of what
		// it reads like. That is how the first version of this told the user to
		// `rmdir` the *service name*.
		if handsThroughAFile(mount, target) {
			o.warnf(codeBindFilePlaceholder, "service %q mounts %s at %s, which names a file — but nothing "+
				"was there, and a bind mount needs its source to exist, so opossum created a directory "+
				"(docker compose does the same). If that path is meant to be a file, the service will "+
				"find a directory where it expects one and carry on without it — an init script "+
				"won't run, a config won't be read — so remove the empty directory (`rmdir %s`), put "+
				"the real file there, and run %s again. If it is meant to be a directory "+
				"(`conf.d`, `.ssh`), there is nothing to do.\n",
				service, src, target, src, redo)
		}
	}
	return nil
}

// Started returns the services this project had up when the last Up finished —
// created by that call or left standing by it — with profile filtering and any
// named services applied. It is nil if Up hasn't run, and nil for a failure that
// returned before the startup order was even worked out (a missing runtime, an
// unresolvable compose file): nothing was measured, so nothing is claimed.
//
// Callers use it to decide what to supervise, so it answers "what is up", not
// "what did this call create".
//
// It is meaningful when Up returned an error. A failed bring-up is rolled back,
// and the services this call created are excluded from the answer even if their
// removal failed — supervising a container the teardown was trying to delete
// would have the supervisor fight it. Anything else still running is listed:
// a service left alone because it was already up to date, or one from an earlier
// Up. A post-start crash (OPSM-407) is not rolled back, so its containers are
// listed too.
//
// After a rollback the answer is measured from the runtime: a container that
// exists counts, running or not, because a service that crashed unwatched is what
// `restart:` is for. Elsewhere it is the startup order, which is why a foreground
// up — whose container has already exited by the time Up returns — still lists it.
//
// The answer is not accumulated: a second call on the same Orchestrator replaces
// it, and a container that disappeared in between is not reported.
// Run-to-completion services can appear — the supervisor filters them out itself,
// since they are meant to exit.
func (o *Orchestrator) Started() []string { return o.started }
