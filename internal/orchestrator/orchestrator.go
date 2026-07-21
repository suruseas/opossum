// Package orchestrator turns a parsed compose Project into calls against the
// container runtime: it starts services in dependency order on a shared
// network so they can resolve each other by name.
package orchestrator

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	Project   *compose.Project
	DNSDomain string // local DNS domain enabling bare-name service discovery
	rt        *runtime.Runtime
	out       interface{ Write([]byte) (int, error) }
	sleep     func(time.Duration) // overridable so tests don't wait in real time
	ctx       context.Context     // cancelled on Ctrl-C so a partial `up` rolls back
	profiles  map[string]bool     // active compose profiles (--profile / COMPOSE_PROFILES)
	up        upOptions           // per-invocation `up` flags
	// HostFP supplies per-service host memory footprints for `stats --host`. Left
	// nil in production (the real macOS introspector is used); tests inject a fake.
	HostFP HostFootprinter
}

// upOptions holds the `up` recreate/build flags.
type upOptions struct {
	forceRecreate bool // --force-recreate: recreate even if unchanged
	build         bool // --build: (re)build images even if present
	noBuild       bool // --no-build: never build (error if an image is missing)
	removeOrphans bool // --remove-orphans: remove containers for services no longer in the compose
	fromDocker    bool // --from-docker: import a build service's image from Docker instead of building it
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
	return &Orchestrator{Project: p, DNSDomain: dnsDomain, rt: rt, out: w, sleep: time.Sleep, ctx: context.Background()}
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
	if o.SSH {
		write("ssh")
	}
	if o.Init {
		write("init")
	}
	if o.ReadOnly {
		write("read_only")
	}
	if o.User != "" {
		write("user", o.User)
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

// EnableProfiles marks compose profiles active (from --profile flags and the
// COMPOSE_PROFILES env var), so services gated behind them start.
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

// enabled reports whether a service is active under the current profiles: a
// service with no profiles is always enabled; otherwise one of its profiles must
// be active, or it must be named explicitly (docker compose: naming a profiled
// service enables it). named holds the services requested on the command line.
func (o *Orchestrator) enabled(name string, named map[string]bool) bool {
	svc := o.Project.Services[name]
	if len(svc.Profiles) == 0 || named[name] {
		return true
	}
	for _, p := range svc.Profiles {
		if o.profiles[p] {
			return true
		}
	}
	return false
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
	for _, name := range names {
		for _, dep := range o.Project.Services[name].DependsOn {
			if !o.enabled(dep.Name, named) {
				return fmt.Errorf("service %q depends on %q, whose profile is not active — enable it with --profile or COMPOSE_PROFILES, or name it explicitly", name, dep.Name)
			}
		}
	}
	return nil
}

// ValidateProfiles errors if any enabled service depends on a gated-inactive one,
// so `config` rejects the same projects `up` does.
func (o *Orchestrator) ValidateProfiles() error {
	var names []string
	for name := range o.EnabledServices() {
		names = append(names, name)
	}
	return o.validateProfileDeps(names, nil)
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
	fmt.Fprintf(o.out, format, a...)
}

// networkName is the default per-project network services share when they don't
// name a network of their own.
func (o *Orchestrator) networkName() string {
	return o.Project.Name + "-net"
}

// resolvedNetwork is the runtime network a service joins, plus how opossum
// manages it (whether it's host-only, and whether opossum creates/deletes it).
type resolvedNetwork struct {
	name     string // the actual `container` network name (namespaced unless external)
	internal bool   // created with --internal (host-only): no internet egress
	external bool   // pre-existing; opossum never creates or deletes it
}

// resolveNetwork maps one declared network key to its runtime network. External
// networks use their real name verbatim; others are namespaced `<project>-<key>`
// and carry the decl's internal flag.
func (o *Orchestrator) resolveNetwork(key string) resolvedNetwork {
	decl := o.Project.Networks[key]
	if decl.External {
		real := decl.Name
		if real == "" {
			real = key
		}
		return resolvedNetwork{name: real, external: true}
	}
	return resolvedNetwork{name: o.Project.Name + "-" + key, internal: decl.Internal}
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
	if !o.rt.Available() {
		return ErrRuntimeAbsent()
	}

	order, err := o.Project.StartupOrder()
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
	named := map[string]bool{}
	for _, s := range services {
		named[s] = true
	}
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

	// Compose fields we don't act on don't affect startup, and a warning for each
	// one is more alarming than useful — surface them only under --verbose. Fields
	// that DO change behavior (e.g. a Postgres datadir on a named volume) still
	// warn unconditionally below.
	if o.rt.Verbose {
		if u := o.Project.Unsupported; len(u) > 0 {
			o.warnf(codeIgnoredTopField, "ignoring unsupported top-level field(s): %s\n", strings.Join(u, ", "))
		}
		for _, name := range order {
			if u := o.Project.Services[name].Unsupported; len(u) > 0 {
				o.warnf(codeIgnoredField, "service %q: ignoring unsupported field(s): %s\n", name, strings.Join(u, ", "))
			}
		}
	}
	for _, name := range order {
		o.warnPostgresDatadir(name, o.Project.Services[name])
		o.warnDockerSocket(name, o.Project.Services[name])
	}
	o.warnSharedNamedVolumes(order)

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

	// Pre-flight: fail before starting anything if a published host port is already
	// taken, with a clearer message than the runtime's raw bind error.
	if err := o.checkHostPorts(order); err != nil {
		return err
	}

	// Pre-flight: bail out before creating the network (or anything else) if a
	// target container name is already owned by a different project.
	for _, name := range order {
		if err := o.ensureNotForeign(o.containerName(name)); err != nil {
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
		created, nerr := o.rt.EnsureNetwork(rn.name, rn.internal)
		if nerr != nil {
			return nerr
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
	defer func() {
		if err == nil {
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
	}()

	if o.DNSDomain != "" && !o.rt.DNSDomainExists(o.DNSDomain) {
		o.warnf(codeDNSDomainAbsent, "DNS domain %q not found — services won't resolve each other by name.\n"+
			"         Create it once with:  sudo container system dns create %s\n",
			o.DNSDomain, o.DNSDomain)
	}

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
		image := svc.Image
		rebuilt := false
		if svc.Build != nil {
			image = o.Project.Name + "-" + name + ":latest"
			have := o.rt.ImageExists(image)
			need := o.up.build || !have
			switch {
			case o.up.fromDocker && need:
				// Bring the image over from Docker instead of building it here.
				dockerRef := image
				if svc.Image != "" {
					dockerRef = svc.Image // docker tags a build+image service by its image:
				}
				o.logf("Importing %s from Docker (%s)\n", name, dockerRef)
				if err := o.rt.ImportFromDocker(dockerRef, image); err != nil {
					return fmt.Errorf("importing service %q: %w", name, err)
				}
				rebuilt = true
			case o.up.noBuild && !have:
				return fmt.Errorf("service %q: image %q is not built and --no-build was given", name, image)
			case need:
				o.logf("Building %s\n", name)
				if err := o.rt.Build(o.buildOptions(image, svc.Build)); err != nil {
					return buildFailed(name, err)
				}
				rebuilt = true
			}
		}

		mem, cpu, _ := svc.Resources() // validated at load
		svcNets, dnsDomain, dnsSearch := o.serviceNetworks(svc)
		runOpts := runtime.RunOptions{
			Name:       cname,
			Image:      image,
			Platform:   svc.Platform,
			Networks:   svcNets,
			DNSDomain:  dnsDomain,
			DNSSearch:  dnsSearch,
			Env:        svc.Environment,
			Ports:      svc.Ports,
			Volumes:    append(o.resolveVolumes(name, svc.Volumes), o.secretMounts(svc)...),
			Tmpfs:      svc.Tmpfs,
			Command:    svc.Command,
			Entrypoint: svc.Entrypoint,
			Labels:     []string{projectLabel + "=" + o.Project.Name},
			Memory:     mem,
			CPUs:       cpu,
			Detach:     detach,
			SSH:        svc.SSH,
			User:       svc.User,
			WorkingDir: svc.WorkingDir,
			Init:       svc.Init,
			ReadOnly:   svc.ReadOnly,
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
			o.ensureBindDirs(svc.Volumes)
		}
		// Seed fresh named/anonymous volumes from the image before the container
		// mounts them (Apple `container` mounts them empty, unlike Docker). This runs
		// through the runtime, so under dry-run its `run --rm` is recorded (not
		// executed) and appears in the plan.
		o.seedVolumes(name, image, svc.Volumes)

		// Replace any stale container left by a previous run of THIS project (the
		// pre-flight above already ruled out foreign owners).
		o.rt.Delete(cname)

		// Track before running so rollback also removes a container whose run
		// failed (it may have been created before erroring).
		started = append(started, cname)
		if oneShot[name] {
			// Run to completion in the foreground so a non-zero exit surfaces as a
			// run error; a dependent's service_completed_successfully gate is then
			// satisfied structurally, since deps precede dependents in the order.
			runOpts.Detach = false
			o.logf("Running %s to completion (%s)\n", name, image)
			if err := o.rt.Run(runOpts); err != nil {
				return fmt.Errorf("service %q did not complete successfully: %w", name, err)
			}
			continue
		}
		o.logf("Starting %s (%s)\n", name, image)
		if err := o.rt.Run(runOpts); err != nil {
			return fmt.Errorf("starting service %q: %w", name, err)
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
	}
	return nil
}

// printPlan lists the exact `container` invocations a dry-run recorded but did
// not run — the argv `up` would have issued, in order. Read-only queries (the
// inspects that resolve recreate/skip) aren't listed; only the mutating commands
// (network create, delete, run, build, …) that a real up would execute.
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
func hostPortInUse(network, address string) bool {
	if network == "udp" {
		c, err := net.ListenPacket("udp", address)
		if err != nil {
			return true
		}
		c.Close()
		return false
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return true
	}
	l.Close()
	return false
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
			return nil, fmt.Errorf("unknown service %q", r)
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
			// StartupOrder already rejected cycles, so this recursion terminates.
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
// silently destroy the other project's container. An unlabeled or missing
// container is treated as safe to (re)use.
func (o *Orchestrator) ensureNotForeign(cname string) error {
	if proj, exists := o.rt.InspectLabel(cname, projectLabel); exists && proj != "" && proj != o.Project.Name {
		return fmt.Errorf("container %q is already in use by project %q; give this project its own DNS domain so names don't collide "+
			"(e.g. --dns-domain %s, created once with `sudo container system dns create %s`) — see README (multi-project)",
			cname, proj, o.Project.Name, o.Project.Name)
	}
	return nil
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
		if hc == nil || hc.Disabled || len(hc.Test) == 0 {
			// Load-time validation rejects this; guard defensively anyway.
			o.warnf(codeDepNoHealth, "%s wants %s healthy but it has no healthcheck — not waiting\n", name, dep.Name)
			continue
		}
		o.logf("Waiting for %s to be healthy\n", dep.Name)
		if err := o.waitHealthy(dep.Name, hc); err != nil {
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
				return fmt.Errorf("[OPSM-401] container is not running (state %q); its last log lines:\n%s", info.State, indentLines(logs))
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
	order, err := o.Project.StartupOrder()
	if err != nil {
		return err
	}
	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]
		cname := o.containerName(name)
		o.logf("Stopping %s\n", name)
		o.rt.Stop(cname)
		o.rt.Delete(cname)
		// Also clear any leftover one-off container from `run` (no --rm).
		o.rt.Delete(o.containerName(name + "-run"))
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
	for key, decl := range o.Project.Networks {
		if decl.External {
			continue
		}
		o.rt.DeleteNetwork(o.Project.Name + "-" + key)
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
	return nil
}

// removeImages deletes the services' images after teardown. "local" removes only
// images opossum built (`<project>-<service>:latest`); "all" also removes the
// pulled images services reference. Deduped and best-effort.
func (o *Orchestrator) removeImages(order []string, all bool) {
	seen := map[string]bool{}
	for _, name := range order {
		ref, built := o.serviceImage(name, o.Project.Services[name])
		if ref == "" || seen[ref] || (!built && !all) {
			continue
		}
		seen[ref] = true
		o.logf("Removing image %s\n", ref)
		o.rt.DeleteImage(ref)
	}
}

// serviceImage is the image reference opossum uses for a service: the image it
// builds (`<project>-<service>:latest`) when the service has a build, else the
// pulled `image:` reference. built reports which.
func (o *Orchestrator) serviceImage(name string, svc *compose.Service) (ref string, built bool) {
	if svc.Build != nil {
		return o.Project.Name + "-" + name + ":latest", true
	}
	return svc.Image, false
}

// Images lists each service's image, whether opossum builds it, and whether it's
// present locally — the image-side counterpart to Ps.
func (o *Orchestrator) Images() error {
	// Same reasoning as Ps: a stopped daemon makes every `image inspect` fail, which
	// would print a confident `PRESENT=no` for images that may well be present. Probe
	// first so `PRESENT` reflects reality rather than "couldn't ask the runtime".
	if !o.rt.SystemRunning() {
		return ErrRuntimeStopped()
	}
	order, err := o.Project.StartupOrder()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(o.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tIMAGE\tSOURCE\tPRESENT")
	for _, name := range order {
		ref, built := o.serviceImage(name, o.Project.Services[name])
		source := "pulled"
		if built {
			source = "built"
		}
		present := "no"
		if ref != "" && o.rt.ImageExists(ref) {
			present = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, dash(ref), source, present)
	}
	return tw.Flush()
}

// volumeName namespaces a named volume by project, matching docker compose's
// `<project>_<volume>` convention. This keeps concurrent projects that share a
// volume name from colliding on a single global volume — the same isolation the
// `<service>.<project>.<domain>` container naming already gives (see #9/#63).
func (o *Orchestrator) volumeName(src string) string {
	return o.Project.Name + "_" + src
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

// isNamedVolume reports whether a volume mount's source is a named volume (not a
// bind-mount host path and not empty). resolveVolumes (startup) and namedVolumes
// (down -v) share this predicate so the name opossum creates and the name it
// removes stay symmetric.
func isNamedVolume(src string) bool {
	return src != "" && !isHostPath(src)
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
	sort.Strings(shared)
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
			strings.Join(quoted, ", "), src)
	}
}

// postgresDataDir is Postgres's default data directory. A named volume mounted
// there fails `initdb` because the mount point isn't empty (contains lost+found),
// unless PGDATA points at a subdirectory. This is the single most common snag in
// real self-hosted app composes (gitea, nextcloud, …). MySQL/MariaDB tolerate the
// mount point, so this is Postgres-specific (#57/#103).
const postgresDataDir = "/var/lib/postgresql/data"

// warnPostgresDatadir warns when a service mounts a named volume directly at
// Postgres's data directory without redirecting PGDATA to a subdirectory.
func (o *Orchestrator) warnPostgresDatadir(name string, svc *compose.Service) {
	for _, v := range svc.Volumes {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) < 2 {
			continue
		}
		src := parts[0]
		// parts[1] is "target[:mode]" (mode = ro/rw/cached/…); a path has no colon.
		target := strings.TrimRight(strings.SplitN(parts[1], ":", 2)[0], "/")
		if target != postgresDataDir || !isNamedVolume(src) || o.isExternalVolume(src) {
			continue
		}
		if hasPGDATASubdir(svc) {
			continue // the workaround is already in place
		}
		o.warnf(codePGDATADatadir, "service %q won't start as written: a named volume mounted at %s "+
			"makes Postgres initdb fail (the mount point isn't empty). To fix it, keep the "+
			"data in a subdirectory — add `environment: PGDATA=%s/pgdata` to the service — "+
			"then run `opossum up` again.\n", name, postgresDataDir, postgresDataDir)
	}
}

// warnDockerSocket warns when a service mounts the Docker daemon socket. Apple
// `container` has no Docker socket to expose, so the mount fails at runtime with
// an opaque virtiofs error — surface the real reason up front. (Tools like
// Portainer that talk to Docker over the socket can't work here.)
func (o *Orchestrator) warnDockerSocket(name string, svc *compose.Service) {
	for _, v := range svc.Volumes {
		if strings.Contains(v, "docker.sock") {
			o.warnf(codeDockerSocket, "service %q mounts the Docker socket (%s), but Apple `container` "+
				"has no Docker daemon socket to share — the container can't reach Docker, and "+
				"the mount will fail. Tools that drive Docker over its socket don't work here.\n", name, v)
			return
		}
	}
}

// indentLines prefixes each line of s with two spaces, for embedding a captured
// block (e.g. container logs) inside an error message.
func indentLines(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// hasPGDATASubdir reports whether the service sets PGDATA to a path below the
// Postgres data directory (the recommended workaround).
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

// isExternalVolume reports whether a named volume is declared `external: true`
// at the top level. External volumes are used by their real name (not
// namespaced) and never removed by `down -v` — the user manages them (#64).
func (o *Orchestrator) isExternalVolume(src string) bool {
	return o.Project.Volumes[src].External
}

// externalRealName is the real volume name to mount for an external volume: its
// declared `name:` if set (compose lets an external volume have a real name
// different from its key), otherwise the compose key (#64).
func (o *Orchestrator) externalRealName(src string) string {
	if n := o.Project.Volumes[src].Name; n != "" {
		return n
	}
	return src
}

// namedVolumes lists the distinct, project-namespaced named volumes referenced
// by services (the source of a `name:/path` mount that isn't a host path).
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

// Ps prints each service, its container name, status, and resolved IP.
func (o *Orchestrator) Ps() error {
	// A dead daemon makes every per-service inspect look like "container absent",
	// which would render as an empty table — a lie ("nothing is running") when the
	// truth is the runtime is unreachable. Probe the system once up front so an
	// empty `ps` means genuinely empty, not "couldn't ask". (The CLI-absent case is
	// caught earlier by the root preflight; here the CLI is present but stopped.)
	if !o.rt.SystemRunning() {
		return ErrRuntimeStopped()
	}
	order, err := o.Project.StartupOrder()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(o.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tCONTAINER\tIMAGE\tIP\tPORTS\tSTATUS")
	for _, name := range order {
		svc := o.Project.Services[name]
		cname := o.containerName(name)
		image := svc.Image
		if svc.Build != nil {
			image = o.Project.Name + "-" + name + ":latest"
		}
		info := o.rt.Inspect(cname)
		// Only list containers that actually exist. A service that was never
		// created, or was removed by `down`, is skipped — so after a teardown `ps`
		// is empty (matching docker compose) rather than a wall of dead rows.
		if !info.Exists {
			continue
		}
		status := "stopped"
		if info.State != "" {
			status = info.State
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			name, cname, image, dash(info.IP), dash(formatPorts(info.Ports)), status)
	}
	return tw.Flush()
}

// formatPorts renders published ports docker-ps style: "0.0.0.0:8080->8080/tcp".
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

// Logs streams container logs. With no service names it shows every service in
// dependency order; otherwise just the named ones (validated against the
// project). Following (-f) blocks on a single stream, so it requires exactly one
// target. When more than one service is shown non-follow, each is prefixed with
// a header.
func (o *Orchestrator) Logs(services []string, opts runtime.LogsOptions) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	// Following several services multiplexes their streams into one output, each
	// line prefixed with the service name (docker compose style); Ctrl-C stops all.
	if opts.Follow && len(targets) > 1 {
		return o.followMultiplexed(targets, opts)
	}
	for _, name := range targets {
		if len(targets) > 1 {
			o.logf("==> %s <==\n", name)
		}
		if err := o.rt.Logs(o.containerName(name), opts); err != nil {
			return fmt.Errorf("logs for service %q: %w", name, err)
		}
	}
	return nil
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

// followMultiplexed follows every target concurrently, prefixing each line with
// the (padded) service name and merging into o.out. A single stream ending
// doesn't stop the others; Ctrl-C (SIGINT/SIGTERM) cancels them all.
func (o *Orchestrator) followMultiplexed(targets []string, opts runtime.LogsOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	width := 0
	for _, name := range targets {
		if len(name) > width {
			width = len(name)
		}
	}
	out := &syncWriter{w: o.out}
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	for i, name := range targets {
		wg.Add(1)
		prefix := fmt.Sprintf("%-*s | ", width, name)
		go func(i int, name, prefix string) {
			defer wg.Done()
			errs[i] = o.rt.FollowLogs(ctx, o.containerName(name), opts, out, prefix)
		}(i, name, prefix)
	}
	wg.Wait()
	// A clean Ctrl-C leaves all errs nil. If every stream genuinely failed, surface
	// it (non-zero exit); a partial failure still showed its diagnostic per stream.
	for _, e := range errs {
		if e == nil {
			return nil
		}
	}
	return fmt.Errorf("could not follow logs for any of the %d service(s)", len(targets))
}

// Stats streams live resource usage (CPU / memory / net / block I/O / pids) for
// the requested services, or the whole project when none are named. With
// noStream it prints a single snapshot. Mirrors `docker stats`.
func (o *Orchestrator) Stats(services []string, noStream bool) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	names := make([]string, len(targets))
	for i, name := range targets {
		names[i] = o.containerName(name)
	}
	return o.rt.Stats(names, noStream)
}

// Copy copies files between a service's container and the host, like
// `docker compose cp`, delegating to `container cp`. Each of src/dst is a host
// path or `<service>:<path>`; a `<service>:` prefix naming a project service is
// rewritten to that service's running container name.
func (o *Orchestrator) Copy(src, dst string) error {
	return o.rt.Copy(o.resolveCopyArg(src), o.resolveCopyArg(dst))
}

// resolveCopyArg rewrites a `<service>:<path>` argument to
// `<container-name>:<path>` when the prefix names a project service; other
// arguments (host paths, or a prefix that isn't a service) pass through.
func (o *Orchestrator) resolveCopyArg(arg string) string {
	if i := strings.IndexByte(arg, ':'); i > 0 {
		if _, ok := o.Project.Services[arg[:i]]; ok {
			return o.containerName(arg[:i]) + arg[i:]
		}
	}
	return arg
}

// resolveServices resolves the requested service names (or all, in startup
// order) and rejects any that the project doesn't define. Shared by logs, stop,
// restart, and stats.
func (o *Orchestrator) resolveServices(services []string) ([]string, error) {
	if len(services) == 0 {
		return o.Project.StartupOrder()
	}
	for _, s := range services {
		if _, ok := o.Project.Services[s]; !ok {
			return nil, fmt.Errorf("unknown service %q", s)
		}
	}
	return services, nil
}

// Import brings each build service's Docker-built image into container's store,
// so `up` starts it without rebuilding in Apple's builder. Handy for onboarding
// (reuse images an existing `docker compose` already built) or as a fallback
// when Apple's builder can't handle a Dockerfile. With no services all build
// services are imported; otherwise the named ones. docker compose and opossum
// name a built image the same way (`<project>-<service>:latest`), so it lands
// under the tag `up` looks for.
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
		// `<project>-<service>`. opossum's `up` always looks for the latter, so pull
		// from whatever Docker named it and retag to what `up` expects.
		dockerRef := target
		if svc.Image != "" {
			dockerRef = svc.Image
		}
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
func buildFailed(service string, err error) error {
	return fmt.Errorf("building service %q: %w\n"+
		"  (if Apple's builder can't handle this Dockerfile, build it with Docker and import it: opossum import %s)", service, err, service)
}

// RunOneOffOptions configures a one-off `run`.
type RunOneOffOptions struct {
	Rm     bool // remove the container after it exits
	NoDeps bool // don't start the service's dependencies first
	TTY    bool // allocate a TTY (the CLI sets this when its own stdin is a terminal)
	SSH    bool // forward the host SSH agent (--ssh), on top of the service's own `ssh:`
}

// RunOneOff starts a single throwaway container for a service in the foreground,
// like `docker compose run`: a distinct name (so it never collides with the
// service's `up` container), the command overridden when given, and no published
// ports. Dependencies are started first unless NoDeps.
func (o *Orchestrator) RunOneOff(service string, command []string, opts RunOneOffOptions) (err error) {
	svc, ok := o.Project.Services[service]
	if !ok {
		return fmt.Errorf("unknown service %q", service)
	}
	if !o.rt.Available() {
		return ErrRuntimeAbsent()
	}

	// Keep the one-off's own stdout clean (e.g. an MCP server's JSON-RPC over
	// stdio): dependency startup, build, and volume-seeding progress all go to
	// stderr; only the one-off body (below) writes to the real stdout.
	o.rt.Out = os.Stderr
	defer func() { o.rt.Out = nil }()

	if !opts.NoDeps {
		if deps := svc.DependsOn.Names(); len(deps) > 0 {
			// A gated-inactive dependency is an error here too (as in `up`) rather
			// than being silently force-started. The run target itself is "named",
			// but its dependencies must be enabled on their own.
			named := map[string]bool{service: true}
			for _, d := range deps {
				if !o.enabled(d, named) {
					return fmt.Errorf("service %q depends on %q, whose profile is not active — enable it with --profile or COMPOSE_PROFILES, or name it explicitly", service, d)
				}
			}
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
			if _, err := o.rt.EnsureNetwork(rn.name, rn.internal); err != nil {
				return err
			}
			if rn.internal {
				o.warnInternalNetwork(rn.name)
			}
		}
	}

	image := svc.Image
	if svc.Build != nil {
		image = o.Project.Name + "-" + service + ":latest"
		o.logf("Building %s\n", service)
		if err := o.rt.Build(o.buildOptions(image, svc.Build)); err != nil {
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

	o.ensureBindDirs(svc.Volumes)
	o.seedVolumes(service, image, svc.Volumes)
	o.logf("Running one-off %s\n", service)
	o.rt.Out = os.Stdout           // the one-off body's stdout is the real stdout (e.g. MCP JSON-RPC)
	mem, cpu, _ := svc.Resources() // validated at load
	svcNets, dnsDomain, dnsSearch := o.serviceNetworks(svc)
	runErr := o.rt.Run(runtime.RunOptions{
		Name:       cname,
		Image:      image,
		Platform:   svc.Platform,
		Networks:   svcNets,
		DNSDomain:  dnsDomain,
		DNSSearch:  dnsSearch,
		Env:        svc.Environment,
		Volumes:    append(o.resolveVolumes(service, svc.Volumes), o.secretMounts(svc)...),
		Tmpfs:      svc.Tmpfs,
		Command:    cmd,
		Entrypoint: svc.Entrypoint,
		Labels:     []string{projectLabel + "=" + o.Project.Name},
		Memory:     mem,
		CPUs:       cpu,
		Detach:     false, // foreground / attached
		// Keep stdin connected (docker compose run parity): piped input must
		// reach the process, so stdin-driven tools (e.g. MCP servers speaking
		// JSON-RPC over stdio) work as one-offs.
		Interactive: true,
		TTY:         opts.TTY,
		// Forward the SSH agent if the service asks for it or the caller passed --ssh.
		SSH:        svc.SSH || opts.SSH,
		User:       svc.User,
		WorkingDir: svc.WorkingDir,
		Init:       svc.Init,
		ReadOnly:   svc.ReadOnly,
		CapAdd:     svc.CapAdd,
		CapDrop:    svc.CapDrop,
		// No published ports for a one-off (matches docker-compose run).
	})
	if opts.Rm {
		o.rt.Delete(cname)
	}
	return runErr
}

// Exec runs a command in a service's running container, streaming stdio.
func (o *Orchestrator) Exec(service string, command []string, opts runtime.ExecOptions) error {
	if _, ok := o.Project.Services[service]; !ok {
		return fmt.Errorf("unknown service %q", service)
	}
	if len(command) == 0 {
		return fmt.Errorf("exec requires a command to run")
	}
	return o.rt.ExecStream(o.containerName(service), command, opts)
}

// Build builds the images for services that declare `build:` (all, or the named
// ones). Services without a build are skipped.
func (o *Orchestrator) Build(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	for _, name := range targets {
		svc := o.Project.Services[name]
		if svc.Build == nil {
			continue
		}
		image := o.Project.Name + "-" + name + ":latest"
		o.logf("Building %s\n", name)
		if err := o.rt.Build(o.buildOptions(image, svc.Build)); err != nil {
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
	for _, name := range targets {
		svc := o.Project.Services[name]
		if svc.Image == "" {
			continue
		}
		o.logf("Pulling %s (%s)\n", name, svc.Image)
		if err := o.rt.Pull(svc.Image); err != nil {
			return fmt.Errorf("pulling service %q: %w", name, err)
		}
	}
	return nil
}

// Start starts already-created (stopped) containers in dependency order (all, or
// the named ones), without recreating them.
func (o *Orchestrator) Start(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	for _, name := range targets {
		o.logf("Starting %s\n", name)
		if err := o.rt.Start(o.containerName(name)); err != nil {
			return fmt.Errorf("starting service %q: %w", name, err)
		}
	}
	return nil
}

// Kill signals running containers (all, or the named ones) in reverse dependency
// order. An empty signal defaults to KILL.
func (o *Orchestrator) Kill(services []string, signal string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	for i := len(targets) - 1; i >= 0; i-- {
		o.logf("Killing %s\n", targets[i])
		o.rt.Kill(o.containerName(targets[i]), signal)
	}
	return nil
}

// Stop stops services without removing them (unlike Down). With no names it stops
// the whole project in reverse dependency order; otherwise just the named ones.
func (o *Orchestrator) Stop(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	for i := len(targets) - 1; i >= 0; i-- {
		o.logf("Stopping %s\n", targets[i])
		o.rt.Stop(o.containerName(targets[i]))
	}
	return nil
}

// Restart stops then starts services in place, keeping their existing config
// (containers and network are not recreated). With no names it restarts the
// whole project; otherwise just the named ones.
func (o *Orchestrator) Restart(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	for i := len(targets) - 1; i >= 0; i-- {
		o.rt.Stop(o.containerName(targets[i]))
	}
	for _, name := range targets {
		o.logf("Restarting %s\n", name)
		if err := o.rt.Start(o.containerName(name)); err != nil {
			return fmt.Errorf("restarting service %q: %w", name, err)
		}
	}
	return nil
}

func (o *Orchestrator) buildOptions(tag string, b *compose.Build) runtime.BuildOptions {
	ctx := b.Context
	if ctx == "" {
		ctx = "."
	}
	resolved := o.resolvePath(ctx)
	o.warnUnreadableContext(resolved)
	return runtime.BuildOptions{
		Tag:        tag,
		Context:    resolved,
		Dockerfile: b.Dockerfile,
		Args:       b.Args,
		Target:     b.Target,
	}
}

// warnUnreadableContext hints when the build context is somewhere Apple's
// container builder can't read: a directory under /private/tmp (not mounted into
// the builder VM) or a symlinked context directory (rejected as "not a
// directory"). Auto-resolving symlinks would be unsafe — `/tmp/x` (which the
// builder reads) resolves to `/private/tmp/x` (which it can't) — so opossum only
// warns and leaves the path unchanged (#83).
func (o *Orchestrator) warnUnreadableContext(ctx string) {
	switch {
	case ctx == "/private/tmp" || strings.HasPrefix(ctx, "/private/tmp/"):
		o.warnf(codeBuildTmpContext, "build context %q is under /private/tmp, which the container builder can't read — run from a real path under your home directory (or /tmp)\n", ctx)
	default:
		if fi, err := os.Lstat(ctx); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			o.warnf(codeBuildSymlink, "build context %q is a symlink; the container builder may reject it — use its real path\n", ctx)
		}
	}
}

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
	parts := strings.SplitN(entry, ":", 2)
	if len(parts) == 1 || parts[0] == "" {
		// A single path (or an omitted source) is an anonymous volume at that path
		// (compose semantics), not a bind mount. Give it a deterministic
		// per-service name so re-up reuses (and `down -v` removes) the same volume.
		target := parts[len(parts)-1]
		name := o.anonVolumeName(svcName, target)
		return volumeMount{Arg: name + ":" + target, Volume: name, Target: target}
	}
	src, rest := parts[0], parts[1]
	target := strings.SplitN(rest, ":", 2)[0] // container path, minus any :ro/:rw
	switch {
	case isHostPath(src):
		// Bind mount: make the host side absolute relative to the compose dir.
		return volumeMount{Arg: o.resolvePath(src) + ":" + rest}
	case o.isExternalVolume(src):
		// External volumes are user-managed: real name, never namespaced/removed/
		// seeded (#64).
		return volumeMount{Arg: o.externalRealName(src) + ":" + rest}
	default:
		// Named volume: namespaced per project so concurrent projects don't share
		// one global volume (#63).
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
func (o *Orchestrator) anonVolumeName(svcName, target string) string {
	san := strings.NewReplacer("/", "_", ".", "_", " ", "_").Replace(strings.Trim(target, "/"))
	// A hash of the exact path makes the name deterministic (re-up reuses the same
	// volume) yet collision-proof: paths that sanitize alike (`/a/b` vs `/a.b`)
	// stay distinct, and the suffix keeps anonymous names from ever coinciding
	// with a project-namespaced named volume (#123).
	h := fnv.New32a()
	h.Write([]byte(target))
	return fmt.Sprintf("%s_%s_%s_%08x", o.Project.Name, svcName, san, h.Sum32())
}

// seedVolumes fills each of a service's project-owned volumes from the image's
// contents at the mount path the FIRST time that volume is created, mirroring
// Docker (Apple `container` mounts a fresh volume empty). Existing volumes are
// left untouched, so user data and prior state are preserved.
func (o *Orchestrator) seedVolumes(svcName, image string, vols []string) {
	for _, m := range o.serviceMounts(svcName, vols) {
		if m.Volume == "" || o.rt.VolumeExists(m.Volume) {
			continue
		}
		o.rt.SeedVolume(m.Volume, image, m.Target)
	}
}

func isHostPath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "~/") ||
		s == "." || s == ".."
}

func (o *Orchestrator) resolvePath(p string) string {
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
func (o *Orchestrator) ensureBindDirs(vols []string) {
	for _, v := range vols {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) < 2 || !isHostPath(parts[0]) {
			continue // single path = anonymous volume; non-path = named/external
		}
		src := o.resolvePath(parts[0])
		if _, err := os.Stat(src); os.IsNotExist(err) {
			if mkErr := os.MkdirAll(src, 0o755); mkErr == nil {
				o.logf("Created host directory %s for a bind mount\n", src)
			}
		}
	}
}
