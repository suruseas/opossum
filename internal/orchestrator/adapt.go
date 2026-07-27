package orchestrator

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
)

// Adapting a compose project
//
// Some compose files that run fine on Docker cannot start as written on Apple
// `container`, for reasons that are properties of the runtime rather than
// mistakes in the file (see the two patterns below). opossum already warns about
// these; `up --from-docker-compose` goes one step further and writes the fix into
// a `compose.opossum.yaml` overlay, so bringing an existing project over is one
// command instead of a read-warn-edit loop.
//
// Two rules keep this honest:
//   - The overlay is a separate file. The user's own compose files are never
//     modified, docker compose never reads the overlay, and deleting it undoes
//     everything.
//   - Only changes that preserve intent are automated (see adaptations below).
//     Anything that would alter what the project *means* — sharing semantics,
//     published ports, app-specific seeding — stays a warning.
//
// The generated YAML is written from templates rather than marshalled, because
// the comments are the point: the reader is often an agent that must decide what
// to do when the fix doesn't work, so every entry carries what changed, why (with
// the diagnostic code, to cross-reference AGENTS.md), how to verify it, what to do
// if it still fails, and how to undo it.

// overlayMarker prefixes every generated entry. It is a stable string on purpose:
// an agent can grep for it to tell opossum-generated entries from hand-written
// ones. Never change it without a migration.
const overlayMarker = "[opossum --from-docker-compose]"

// OverlayFileName is the overlay `up --from-docker-compose` generates.
const OverlayFileName = "compose.opossum.yaml"

// mysqlDataDir is MySQL/MariaDB's default data directory. Like Postgres's, a
// bind mount here fails because the image chowns it at startup.
const mysqlDataDir = "/var/lib/mysql"

// dbDataDir pairs a database's data directory with the images that own it. Both
// halves are required to adapt a mount: the path alone is not evidence, because
// plenty of services legitimately mount a database's directory without being that
// database (a backup sidecar, an inspector, a migration tool). Rewriting one of
// those would swap its real data for an empty volume — silently, and in the user's
// favourite direction to not notice. So an adaptation only fires when the service
// is plausibly the database that chowns the path at startup.
type dbDataDir struct {
	path string
	// images are the image names of databases that own path, matched against the
	// LAST path segment of the image reference (registry, namespace, tag and digest
	// stripped). Matching the whole reference by substring would be far too loose:
	// `postgres-exporter`, `mysqldump-cron` and `mysql-backup-tool` all contain a
	// database's name while being exactly the sidecars this must not touch. An
	// unrecognized fork is only a missed fix; a wrong match swaps a service's real
	// data for an empty volume, so the list errs toward missing.
	images []string
}

var dbDataDirs = []dbDataDir{
	{postgresDataDir, []string{"postgres", "postgresql", "postgis", "timescaledb", "timescaledb-ha"}},
	{mysqlDataDir, []string{"mysql", "mysql-server", "mariadb", "percona", "percona-server", "percona-xtradb-cluster"}},
}

// imageName reduces an image reference to its bare name: the last path segment,
// with any registry, namespace, tag and digest removed. `docker.io/library/
// postgres:16` and `bitnami/postgresql` become `postgres` and `postgresql`.
func imageName(ref string) string {
	s := strings.ToLower(strings.TrimSpace(ref))
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[:i] // digest
	}
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s = s[:i] // tag (a colon before the last slash is a registry port)
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// ownsDataDir reports whether svc looks like the database that owns target — i.e.
// whether adapting that mount is safe. A service with a `build:` and no `image:`
// can't be identified, so it is left alone.
func ownsDataDir(svc *compose.Service, target string) bool {
	name := imageName(svc.Image)
	if name == "" {
		return false
	}
	for _, d := range dbDataDirs {
		if d.path != target {
			continue
		}
		for _, img := range d.images {
			if name == img {
				return true
			}
		}
	}
	return false
}

// Adaptation is one change the overlay makes, for reporting to the user.
type Adaptation struct {
	Service string
	Code    string // the diagnostic code this fixes, e.g. "OPSM-101"
	Summary string // one line: what changed, in the user's terms
}

// serviceAdaptation is the internal, renderable form: the YAML fragment for one
// service plus the comment block that explains it.
type serviceAdaptation struct {
	Adaptation
	comment string   // the full comment block (already prefixed with "# ")
	keys    []string // the service's YAML body lines, already indented
	volume  string   // a top-level named volume to declare, if any
}

// PlanOverlay inspects the resolved project (base + any override already merged)
// and returns the text of a compose.opossum.yaml that adapts it to run on Apple
// `container`, plus a description of each change. It returns ("", nil) when
// nothing needs adapting.
//
// Detection runs on the *resolved* project on purpose: an override may already
// have applied the fix by hand, and adapting on top of that would be noise (or
// wrong).
func (o *Orchestrator) PlanOverlay() (string, []Adaptation) {
	names := make([]string, 0, len(o.Project.Services))
	for name := range o.Project.Services {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic output

	claimed := map[string]bool{} // volume names this plan has already taken
	var plans []serviceAdaptation
	for _, name := range names {
		plans = append(plans, o.adaptService(name, o.Project.Services[name], claimed)...)
	}
	if len(plans) == 0 {
		return "", nil
	}
	return renderOverlay(plans), summarize(plans)
}

func summarize(plans []serviceAdaptation) []Adaptation {
	out := make([]Adaptation, 0, len(plans))
	for _, p := range plans {
		out = append(out, p.Adaptation)
	}
	return out
}

// adaptService returns the adaptations for one service. A Postgres service whose
// data directory is a bind mount needs both fixes: the mount has to become a named
// volume (bind mounts can't be chowned), and PGDATA then has to point at a
// subdirectory of it (a named volume mount point isn't empty). Handling only one
// would move the failure rather than remove it.
func (o *Orchestrator) adaptService(name string, svc *compose.Service, claimed map[string]bool) []serviceAdaptation {
	if svc == nil {
		return nil
	}
	var out []serviceAdaptation
	a, swapped := o.adaptBindMountedDataDir(name, svc, claimed)
	if swapped {
		out = append(out, a)
	}
	if p, ok := o.adaptPGDATA(name, svc, swapped); ok {
		out = append(out, p)
	}
	return out
}

// adaptBindMountedDataDir swaps a bind-mounted database data directory for a named
// volume. This MOVES WHERE THE DATA LIVES (out of the host directory, into a
// volume the runtime manages), so the generated comment says so plainly — it's the
// one automated change a user could be surprised by.
func (o *Orchestrator) adaptBindMountedDataDir(name string, svc *compose.Service, claimed map[string]bool) (serviceAdaptation, bool) {
	for _, v := range svc.Volumes {
		src, target, mode, ok := splitMount(v)
		if !ok || !isHostPath(src) {
			continue
		}
		// Both must hold: the path is a database data directory AND this service
		// is the database that owns it. A read-only mount rules it out — a database
		// can't run on one, so the service is something else looking at the files.
		if !ownsDataDir(svc, target) || readOnlyMount(mode) {
			continue
		}
		// Another service on the same host directory means deliberate sharing;
		// splitting it is the user's call, not ours.
		if o.sharesHostDataDir(name, src) {
			continue
		}
		vol := o.freeVolumeName(compose.SanitizeName(name)+"-data", claimed)
		return serviceAdaptation{
			Adaptation: Adaptation{
				Service: name,
				Code:    string(codeBindDataDirChown),
				Summary: fmt.Sprintf("service %q: data directory %s moved from the host path %s to a named volume %q", name, target, src, vol),
			},
			comment: commentBlock(
				fmt.Sprintf("%s service %q: %s now uses the named volume %q instead of the host path %q.", overlayMarker, esc(name), esc(target), vol, esc(src)),
				[]string{
					"Apple container bind-mounts host directories read-write but host-owned,",
					"and they cannot be chowned from inside the container. Official database",
					"images chown their data directory at startup, so they fail on a bind",
					fmt.Sprintf("mount. A named volume is chownable. Diagnostic: %s.", codeBindDataDirChown),
					"NOTE: this changes where the data lives. The database now writes into",
					fmt.Sprintf("the volume, not into %s. Existing data in that directory is", esc(src)),
					"not copied — it is left untouched on the host.",
				},
				[]string{
					fmt.Sprintf("after `opossum up`, `opossum logs %s` should show the database", name),
					"initializing and accepting connections, with no chown error.",
				},
				[]string{
					fmt.Sprintf("run `opossum logs %s` and `opossum doctor`, then match the error", name),
					"against the failure-signatures table in AGENTS.md.",
				},
			),
			keys: []string{
				"    volumes:",
				fmt.Sprintf("      - %s:%s", esc(vol), esc(target)),
			},
			volume: vol,
		}, true
	}
	return serviceAdaptation{}, false
}

// adaptPGDATA points PGDATA at a subdirectory when Postgres's data directory is a
// named volume. Purely additive (an environment variable), and the data stays
// inside the same volume — the safest fix in the set, and the most common need.
//
// It fires when the data dir is already a named volume, and also when
// adaptBindMountedDataDir is about to make it one.
func (o *Orchestrator) adaptPGDATA(name string, svc *compose.Service, willBeNamedVolume bool) (serviceAdaptation, bool) {
	// Any PGDATA the user set is theirs. Checking only for "a subdirectory of the
	// default datadir" would treat a deliberate PGDATA elsewhere (a second volume,
	// say) as unfixed and overwrite it — pointing Postgres at an empty directory
	// and initdb'ing a new cluster, while their real one sits untouched and
	// unreachable. Silence beats a fix here.
	if hasPGDATA(svc) {
		return serviceAdaptation{}, false
	}
	// A mount below the data directory would be stranded above the new PGDATA.
	if hasNestedDataDirMount(svc) {
		return serviceAdaptation{}, false
	}
	for _, v := range svc.Volumes {
		src, target, mode, ok := splitMount(v)
		if !ok || target != postgresDataDir {
			continue
		}
		// Only the Postgres that owns the directory, and never a read-only mount
		// (a database can't run on one) — same reasoning as the bind-mount fix.
		if !ownsDataDir(svc, target) || readOnlyMount(mode) {
			continue
		}
		// The redirection exists because a NAMED VOLUME's mount point isn't empty.
		// It must fire for a named volume today, or for a host path this overlay is
		// converting into one — but never for a bind mount that stays a bind mount
		// (its directory is the user's, initdb is happy with it, and moving PGDATA
		// would just relocate their data a level down for no reason).
		if isHostPath(src) && !willBeNamedVolume {
			continue
		}
		if !isNamedVolume(src) && !isHostPath(src) {
			continue
		}
		if isNamedVolume(src) && o.isExternalVolume(src) {
			continue // an external volume is the user's to manage
		}
		sub := postgresDataDir + "/pgdata"
		return serviceAdaptation{
			Adaptation: Adaptation{
				Service: name,
				Code:    string(codePGDATADatadir),
				Summary: fmt.Sprintf("service %q: PGDATA pointed at %s (a subdirectory of the volume)", name, sub),
			},
			comment: commentBlock(
				fmt.Sprintf("%s service %q: PGDATA moved to a subdirectory of the data volume.", overlayMarker, esc(name)),
				[]string{
					"Apple container attaches a named volume as a filesystem mount point, so",
					"the directory is not empty (it contains lost+found). Postgres initdb",
					fmt.Sprintf("refuses to initialize a non-empty data directory. Diagnostic: %s.", codePGDATADatadir),
					"The data still lives inside the same volume, one level down.",
				},
				[]string{
					fmt.Sprintf("after `opossum up`, `opossum logs %s` should show initdb completing", name),
					"and the database accepting connections.",
				},
				[]string{
					fmt.Sprintf("run `opossum logs %s` and `opossum doctor`, then match the error", name),
					"against the failure-signatures table in AGENTS.md.",
				},
			),
			keys: []string{
				"    environment:",
				fmt.Sprintf("      PGDATA: %s", sub),
			},
		}, true
	}
	return serviceAdaptation{}, false
}

// commentBlock renders the five-part contract every generated entry carries. The
// shape is fixed so a reader (often an agent) can rely on it: what changed, why it
// was needed, how to confirm it worked, what to do if it didn't, and how to undo
// it. Wrapped lines are indented so the block stays readable.
func commentBlock(what string, why, verify, ifFails []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", what)
	writeSection := func(label string, lines []string) {
		for i, l := range lines {
			if i == 0 {
				fmt.Fprintf(&b, "# %s: %s\n", label, l)
			} else {
				fmt.Fprintf(&b, "#   %s\n", l)
			}
		}
	}
	writeSection("Why", why)
	writeSection("Verify", verify)
	writeSection("If this still fails", ifFails)
	writeSection("To undo", []string{
		"delete this entry, or this whole file. Your original compose file was",
		"not modified, and docker compose never reads this file.",
	})
	return b.String()
}

// renderOverlay assembles the overlay file: a header explaining what the file is,
// then one commented entry per adaptation, grouped under each service, then any
// named volumes the adaptations introduced.
func renderOverlay(plans []serviceAdaptation) string {
	var b strings.Builder
	b.WriteString("# Generated by `opossum up --from-docker-compose`.\n" +
		"#\n" +
		"# This file adapts the compose project to Apple's `container` runtime. opossum\n" +
		"# merges it last, at the highest precedence, on top of your compose file and any\n" +
		"# compose.override.yaml. docker compose does not read this file, so the same\n" +
		"# directory still works with both tools and your own files are unchanged.\n" +
		"#\n" +
		"# Every entry below says what changed, why, how to check it, and how to undo it.\n" +
		"# Edit or delete anything here — opossum will not overwrite this file.\n" +
		"\n" +
		"services:\n")

	// Group entries by service so the file reads as compose, not as a changelog.
	type group struct {
		name     string
		comments []string
		keys     []string
	}
	var groups []group
	index := map[string]int{}
	var volumes []string
	for _, p := range plans {
		i, ok := index[p.Service]
		if !ok {
			index[p.Service] = len(groups)
			groups = append(groups, group{name: p.Service})
			i = len(groups) - 1
		}
		groups[i].comments = append(groups[i].comments, p.comment)
		groups[i].keys = append(groups[i].keys, p.keys...)
		if p.volume != "" {
			volumes = append(volumes, p.volume)
		}
	}
	for _, g := range groups {
		for _, c := range g.comments {
			// Indent the comment under the service it belongs to.
			for _, line := range strings.Split(strings.TrimRight(c, "\n"), "\n") {
				b.WriteString("  " + line + "\n")
			}
		}
		fmt.Fprintf(&b, "  %s:\n", esc(yamlKey(g.name)))
		for _, k := range g.keys {
			b.WriteString(k + "\n")
		}
	}
	if len(volumes) > 0 {
		sort.Strings(volumes)
		b.WriteString("\nvolumes:\n")
		for _, v := range volumes {
			fmt.Fprintf(&b, "  %s: {}\n", esc(yamlKey(v)))
		}
	}
	return b.String()
}

// splitMount splits a short-form volumes entry into source, target and mode.
// Entries with no source (an anonymous volume) return ok=false: there's no host
// path or named volume to reason about.
func splitMount(v string) (src, target, mode string, ok bool) {
	parts := strings.SplitN(v, ":", 3)
	if len(parts) < 2 {
		return "", "", "", false
	}
	if len(parts) == 3 {
		mode = parts[2]
	}
	return parts[0], strings.TrimRight(parts[1], "/"), mode, true
}

// readOnlyMount reports whether a mount is read-only. A read-only mount is strong
// evidence the service is NOT the database that owns the directory — a database
// can't run on one — so those are never adapted.
func readOnlyMount(mode string) bool {
	for _, opt := range strings.Split(mode, ",") {
		if strings.TrimSpace(opt) == "ro" {
			return true
		}
	}
	return false
}

// esc escapes a user-derived string for the generated file. opossum interpolates
// ${VAR} over the raw text before parsing — comments included — so an unescaped
// `$` from a host path would be expanded on the next load, or fail the load
// outright ("unterminated variable reference") and leave the project unusable.
func esc(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// plainYAMLKey matches names safe to emit as a bare YAML key.
var plainYAMLKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// yamlBareUnsafe are names that look like a bare scalar but parse as something
// else (a bool, a null). A service legitimately named `on` or `no` would silently
// become a different key — or vanish — when the merged tree round-trips through
// YAML, so they must be quoted.
var yamlBareUnsafe = map[string]bool{
	"y": true, "n": true, "yes": true, "no": true, "true": true, "false": true,
	"on": true, "off": true, "null": true, "nil": true, "~": true,
}

// yamlKey renders a name as a YAML mapping key, quoting whatever wouldn't survive
// as a bare scalar. Compose allows service names (digits, dots, reserved words)
// that a bare key would mangle — writing those unquoted produces a file that
// silently renames or drops the service.
func yamlKey(s string) string {
	if plainYAMLKey.MatchString(s) && !yamlBareUnsafe[strings.ToLower(s)] {
		return s
	}
	return strconv.Quote(s)
}

// hasPGDATA reports whether the service sets PGDATA at all — including the
// pass-through forms (`- PGDATA` in a list, `PGDATA:` with no value in a map),
// which are stored as a bare name with no `=` and take their value from the host
// environment. Missing those would overwrite a PGDATA the user is supplying at run
// time, which is the exact damage this guard exists to prevent.
func hasPGDATA(svc *compose.Service) bool {
	for _, e := range svc.Environment {
		if e == "PGDATA" || strings.HasPrefix(e, "PGDATA=") {
			return true
		}
	}
	return false
}

// sharesHostDataDir reports whether another service mounts the same host path.
// Two services on one host directory are sharing it deliberately — a database and
// a backup/inspection sidecar, typically. Giving each its own named volume would
// sever that silently: the sidecar would keep working against an empty volume and
// look healthy forever. Adapting only one is no better (the other would read a
// directory the database no longer writes), so this is a decision for the user.
func (o *Orchestrator) sharesHostDataDir(self, src string) bool {
	for name, svc := range o.Project.Services {
		if name == self || svc == nil {
			continue
		}
		for _, v := range svc.Volumes {
			if s, _, _, ok := splitMount(v); ok && s == src {
				return true
			}
		}
	}
	return false
}

// hasNestedDataDirMount reports whether the service mounts anything at a path
// below Postgres's data directory — an injected `postgresql.conf`, a dedicated
// `pg_wal`. Moving PGDATA down a level would leave those mounts one level above
// the new data directory, so the config would stop being read and the WAL
// directory would go unused, both without a word of complaint.
func hasNestedDataDirMount(svc *compose.Service) bool {
	for _, v := range svc.Volumes {
		_, target, _, ok := splitMount(v)
		if ok && strings.HasPrefix(target, postgresDataDir+"/") {
			return true
		}
	}
	return false
}

// freeVolumeName returns a volume name that no other adaptation has claimed, that
// the project doesn't declare, and that no service already mounts. Reusing a
// declared name would merge into that declaration — including an `external: true`
// one, pointing a freshly-initialized database at data the user manages elsewhere.
// Undeclared-but-mounted names matter just as much: compose doesn't require a
// declaration, so taking one would silently hand another service's volume to a
// database (and trip the exclusive-attach failure, OPSM-102).
func (o *Orchestrator) freeVolumeName(base string, claimed map[string]bool) string {
	used := map[string]bool{}
	for _, svc := range o.Project.Services {
		for _, v := range svc.Volumes {
			if src, _, _, ok := splitMount(v); ok && isNamedVolume(src) {
				used[src] = true
			}
		}
	}
	taken := func(n string) bool {
		if claimed[n] || used[n] {
			return true
		}
		_, declared := o.Project.Volumes[n]
		return declared
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	claimed[name] = true
	return name
}
