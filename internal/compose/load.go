package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Name     string                 `yaml:"name"`
	Services map[string]*Service    `yaml:"services"`
	Secrets  map[string]Secret      `yaml:"secrets"`
	Volumes  map[string]VolumeDecl  `yaml:"volumes"`
	Networks map[string]NetworkDecl `yaml:"networks"`
}

// DefaultFileNames are the compose file names opossum looks for when none is
// given, in docker-compose's precedence order.
var DefaultFileNames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

// overrideFileNames are auto-merged on top of a discovered base compose file.
var overrideFileNames = []string{
	"compose.override.yaml",
	"compose.override.yml",
	"docker-compose.override.yaml",
	"docker-compose.override.yml",
}

// DiscoverOverride returns the path of an override file in dir (merged on top of
// the base compose file), or "" if none exists.
func DiscoverOverride(dir string) string {
	for _, name := range overrideFileNames {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// opossumOverlayFileNames are opossum-specific overlays, auto-merged at the
// highest precedence — on top of the base compose file AND any standard override.
// docker compose doesn't know these names, so it ignores them: the same directory
// works with both tools and the original compose file stays untouched, while
// opossum can carry adjustments that make a project run on Apple `container`.
var opossumOverlayFileNames = []string{
	"compose.opossum.yaml",
	"compose.opossum.yml",
}

// DiscoverOpossumOverlay returns the path of an opossum overlay file in dir
// (merged last, at the highest precedence), or "" if none exists.
func DiscoverOpossumOverlay(dir string) string {
	for _, name := range opossumOverlayFileNames {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// ignoredTopLevel lists top-level compose keys opossum doesn't act on. `version`
// (legacy no-op) and `x-` extension keys are intentionally not flagged.
func ignoredTopLevel(data []byte) []string {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil
	}
	var out []string
	for k := range top {
		switch {
		// `volumes` is acted on (declarations drive external: true and a volume's
		// real `name:`), so reporting the whole key as ignored was wrong — and noisy,
		// since almost every real project declares named volumes. The fields inside a
		// declaration that opossum *doesn't* act on are reported individually below,
		// so nothing is silently dropped.
		case k == "name" || k == "services" || k == "version" || k == "secrets" || k == "networks" || k == "volumes":
		case strings.HasPrefix(k, "x-"):
		default:
			out = append(out, k)
		}
	}
	out = append(out, ignoredVolumeFields(top["volumes"])...)
	sort.Strings(out)
	return out
}

// volumeDeclFields are the per-volume keys opossum acts on. Anything else in a
// declaration (driver, driver_opts, labels) is parsed and dropped — a project that
// asks for an NFS driver would otherwise silently get a plain local volume, with
// nothing said about it.
var volumeDeclFields = map[string]bool{"external": true, "name": true}

// ignoredVolumeFields reports unacted-on keys inside top-level volume declarations
// as `volumes.<vol>.<key>`, so the signal lost by not flagging `volumes` wholesale
// comes back sharper than before.
func ignoredVolumeFields(node yaml.Node) []string {
	var decls map[string]map[string]yaml.Node
	if node.IsZero() || node.Decode(&decls) != nil {
		return nil
	}
	var out []string
	for vol, fields := range decls {
		for k := range fields {
			if volumeDeclFields[k] || strings.HasPrefix(k, "x-") {
				continue
			}
			out = append(out, fmt.Sprintf("volumes.%s.%s", vol, k))
		}
	}
	return out
}

// Discover returns the path to the first standard compose file present in dir,
// following docker-compose precedence, so `opossum up` works in a directory that
// has a `docker-compose.yml` (or any of the standard names) without `-f`.
func Discover(dir string) (string, error) {
	for _, name := range DefaultFileNames {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %q (looked for %s)", dir, strings.Join(DefaultFileNames, ", "))
}

// Load reads and validates a compose file.
// Load parses a single compose file. envFiles, when given, supply the ${VAR}
// interpolation values in place of the default `.env` (docker compose's
// --env-file; later files win, the shell still overrides all).
func Load(path string, envFiles ...string) (*Project, error) {
	return LoadFiles([]string{path}, envFiles)
}

// mergeMap deep-merges override onto base per the compose spec: keys in both
// recurse; keys only in override are added.
func mergeMap(base, over map[string]any) map[string]any {
	for k, ov := range over {
		if bv, ok := base[k]; ok {
			base[k] = mergeValue(bv, ov, k)
		} else {
			base[k] = ov
		}
	}
	return base
}

// replaceSeqKeys are sequence fields that represent a single value, so an override
// replaces rather than appends them (docker compose parity).
var replaceSeqKeys = map[string]bool{"command": true, "entrypoint": true, "test": true}

// envLikeKeys accept either a `KEY: value` map or a `- KEY=value` list; both merge
// by key (later wins), so a base and override merge per variable regardless of form.
var envLikeKeys = map[string]bool{"environment": true, "labels": true}

// dedupSeqKeys are list fields where a repeated entry (e.g. an override restating a
// port) should collapse to one, matching docker compose. `volumes` is deliberately
// absent: mergeByTargetKeys handles it with a stricter rule (same mount point, not
// just same text) that subsumes plain dedup.
var dedupSeqKeys = map[string]bool{"ports": true, "expose": true}

// mergeByTargetKeys are list fields docker compose merges by *mount point* rather
// than by whole entry: a later file's mount at a path an earlier file already
// mounts replaces it. Appending both instead (what a plain dedup does, since the
// two entries differ as strings) would mount two sources at one path — the
// container gets whichever the runtime picks, which is not what either file asked
// for. This is what lets an override swap a bind mount for a named volume.
var mergeByTargetKeys = map[string]bool{"volumes": true}

// mergeValue merges one value: env-like fields merge by key (list or map form),
// nested mappings merge by key, most sequences append (deduping known list
// fields), and replaceSeqKeys sequences / scalars are overridden.
func mergeValue(base, over any, key string) any {
	if envLikeKeys[key] {
		return mergeMap(toEnvMap(base), toEnvMap(over))
	}
	switch o := over.(type) {
	case map[string]any:
		if b, ok := base.(map[string]any); ok {
			return mergeMap(b, o)
		}
	case []any:
		if b, ok := base.([]any); ok && !replaceSeqKeys[key] {
			merged := append(append([]any{}, b...), o...)
			if mergeByTargetKeys[key] {
				return mergeSeqByTarget(merged)
			}
			if dedupSeqKeys[key] {
				merged = dedupSeq(merged)
			}
			return merged
		}
	}
	return over
}

// mergeSeqByTarget collapses mount entries that share a target path, keeping the
// LAST one (the higher-precedence file wins) at the FIRST one's position, so an
// override swaps a mount in place instead of adding a second mount at the same
// path. Entries whose target can't be read are left alone.
func mergeSeqByTarget(xs []any) []any {
	pos := map[string]int{} // target -> index in out
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		t := mountTarget(x)
		if t == "" {
			out = append(out, x)
			continue
		}
		if i, seen := pos[t]; seen {
			out[i] = x // later file wins, in the earlier file's slot
			continue
		}
		pos[t] = len(out)
		out = append(out, x)
	}
	return out
}

// collapseMountsByTarget is mergeSeqByTarget for an already-parsed (string) mount
// list, applied after load so a single file gets the same treatment as merged ones.
func collapseMountsByTarget(vs []string) []string {
	pos := map[string]int{}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		t := mountTarget(v)
		if t == "" {
			out = append(out, v)
			continue
		}
		if i, seen := pos[t]; seen {
			out[i] = v
			continue
		}
		pos[t] = len(out)
		out = append(out, v)
	}
	return out
}

// mountTarget returns the container path a volumes entry mounts at, for both the
// short string form ("src:target", "src:target:ro", or a bare "target" for an
// anonymous volume) and the long mapping form ({type, source, target, …}).
// Returns "" when the entry has no readable target — such an entry is left alone
// rather than guessed at, which is the pre-merge-by-target behaviour (append both)
// and never a wrong merge.
func mountTarget(v any) string {
	switch x := v.(type) {
	case string:
		// Split never yields an empty slice: Split("", ":") is [""], which falls to
		// the anonymous-volume case and correctly reports "" (unkeyable).
		parts := strings.Split(x, ":")
		if len(parts) == 1 {
			return strings.TrimRight(parts[0], "/") // anonymous volume: the target itself
		}
		return strings.TrimRight(parts[1], "/")
	case map[string]any:
		if t, ok := x["target"].(string); ok {
			return strings.TrimRight(t, "/")
		}
	}
	return ""
}

// toEnvMap normalizes an env-like value (a `KEY: value` map or a `- KEY=value`
// list) to a map, so the two forms merge by key.
func toEnvMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case []any:
		m := map[string]any{}
		for _, item := range x {
			if s, ok := item.(string); ok {
				k, val, found := strings.Cut(s, "=")
				if found {
					m[k] = val
				} else {
					m[k] = nil
				}
			}
		}
		return m
	}
	return map[string]any{}
}

// dedupSeq drops repeated string entries (keeping the first), leaving non-string
// entries untouched.
func dedupSeq(xs []any) []any {
	seen := map[string]bool{}
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		if s, ok := x.(string); ok {
			if seen[s] {
				continue
			}
			seen[s] = true
		}
		out = append(out, x)
	}
	return out
}

// LoadFiles parses and merges one or more compose files, applying docker compose's
// multiple-`-f` semantics: later files override earlier ones (mappings merge by
// key, most sequences append, command/entrypoint replace).
func LoadFiles(paths []string, envFiles []string) (*Project, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no compose file given")
	}
	abs, err := filepath.Abs(paths[0])
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(abs)

	// Expand ${VAR} references before parsing, using a `.env` file next to the
	// first compose file (or the given --env-file paths) overlaid by the process env.
	scope, err := loadEnv(baseDir, envFiles)
	if err != nil {
		return nil, err
	}

	var data []byte
	if len(paths) == 1 {
		// Single file: parse the interpolated bytes directly (no merge round-trip).
		raw, err := os.ReadFile(paths[0])
		if err != nil {
			return nil, fmt.Errorf("reading compose file: %w", err)
		}
		if data, err = interpolate(raw, scope.lookup()); err != nil {
			return nil, fmt.Errorf("interpolating %s: %w", paths[0], err)
		}
	} else {
		// Multiple files: merge their YAML trees, then render the merged result.
		var merged map[string]any
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading compose file: %w", err)
			}
			if raw, err = interpolate(raw, scope.lookup()); err != nil {
				return nil, fmt.Errorf("interpolating %s: %w", path, err)
			}
			var m map[string]any
			if err := yaml.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("compose file %s is not valid YAML: %w\n  check the indentation and quoting near the line the parser names above", path, err)
			}
			if merged == nil {
				merged = m
			} else {
				merged = mergeMap(merged, m)
			}
		}
		if data, err = yaml.Marshal(merged); err != nil {
			return nil, fmt.Errorf("merging compose files: %w", err)
		}
	}

	var f composeFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		// This decode runs the services' custom unmarshalers, so the error may be a
		// real YAML syntax problem OR a semantic one (a bad duration/memory/cpus
		// value, which already carries its own fix). Only frame the former as invalid
		// YAML — the library prefixes those with "yaml:".
		if strings.HasPrefix(err.Error(), "yaml:") {
			return nil, fmt.Errorf("compose file %s is not valid YAML: %w\n  check the indentation and quoting near the line the parser names above", paths[0], err)
		}
		return nil, fmt.Errorf("compose file %s: %w", paths[0], err)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("%s defines no services — add a top-level `services:` block with at least one service", paths[0])
	}

	p := &Project{
		Name:        f.Name,
		BaseDir:     baseDir,
		Services:    f.Services,
		Secrets:     f.Secrets,
		Volumes:     f.Volumes,
		Networks:    f.Networks,
		Unsupported: ignoredTopLevel(data),
	}
	// A declared network can't be both host-only (internal) and external: an
	// external network is used as-is, so `internal` would be silently dropped —
	// and with it the egress guarantee a caller likely set `internal` to get.
	for name, decl := range f.Networks {
		if decl.Internal && decl.External {
			return nil, fmt.Errorf("network %q: internal and external cannot both be set (an external network is used as-is)", name)
		}
	}
	for name, svc := range f.Services {
		svc.Name = name
		if svc.Image == "" && svc.Build == nil {
			return nil, fmt.Errorf("service %q must set either image or build", name)
		}
		// Validate resource limits early (conflict / bad units), like docker compose.
		if _, _, err := svc.Resources(); err != nil {
			return nil, err
		}
		// A misspelled restart policy would otherwise be accepted and then quietly
		// ignored — the service simply never gets supervised, with nothing to explain
		// why. Fail at load, where the typo is.
		if _, err := svc.RestartPolicy(); err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		// network_mode: only "none" (full isolation) is acted on. Any other value
		// (host, bridge, service:x, …) has no faithful mapping on Apple `container`,
		// so ignore it — the service joins the project network — and report it as an
		// ignored field rather than failing the whole file. Rejecting it outright
		// would break real-world compose files (e.g. a `network_mode: host` service)
		// that otherwise run fine; "run a docker-compose.yml without surprises" wins.
		if svc.NetworkMode != "" && svc.NetworkMode != NetworkModeNone {
			svc.NetworkMode = "" // don't let an unsupported value reach the orchestrator
			svc.Unsupported = append(svc.Unsupported, "network_mode")
			sort.Strings(svc.Unsupported)
		}
		// networks: a service joins the declared networks it names. Each must be
		// declared top-level, and `networks:` can't combine with full isolation.
		if len(svc.Networks) > 0 {
			if svc.NetworkMode == NetworkModeNone {
				return nil, fmt.Errorf("service %q: network_mode: none and networks: cannot both be set", name)
			}
			for _, netName := range svc.Networks {
				if _, ok := f.Networks[netName]; !ok {
					return nil, fmt.Errorf("service %q references undefined network %q (declare it under top-level networks:)", name, netName)
				}
			}
		}
		// Give bare container ports a host port (Apple's `container` requires one),
		// then drop duplicates the merge couldn't see because it dedups raw text
		// (e.g. base "3000" + override "3000:3000" both normalize to "3000:3000").
		if len(svc.Ports) > 0 {
			seen := make(map[string]bool, len(svc.Ports))
			ports := make([]string, 0, len(svc.Ports))
			auto := map[string]bool{}
			for _, p := range svc.Ports {
				n, mirrored := normalizePort(p)
				if seen[n] {
					// A spec is only opossum's to move if EVERY declaration of it was
					// bare: `["3000", "3000:3000"]` names the host port explicitly in
					// one of them, so the user did choose it.
					auto[n] = auto[n] && mirrored
					continue
				}
				seen[n] = true
				auto[n] = mirrored
				ports = append(ports, n)
			}
			svc.Ports = ports
			for spec, isAuto := range auto {
				if !isAuto {
					delete(auto, spec)
				}
			}
			if len(auto) > 0 {
				svc.AutoHostPort = auto
			}
		}
		// Collapse mounts sharing a target, for the same reason ports are re-deduped
		// above: the merge only sees files being combined, so a single file — or one
		// whose override doesn't restate `volumes` — never reaches it. docker compose
		// collapses unconditionally, keeping the last entry.
		if len(svc.Volumes) > 1 {
			svc.Volumes = collapseMountsByTarget(svc.Volumes)
		}
		// Fold env_file values into the environment (explicit `environment` wins).
		env, err := resolveEnvFiles(baseDir, svc.EnvFile, svc.Environment, scope)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		svc.Environment = env

		// Every referenced secret must be a defined, file-based top-level secret.
		for _, ref := range svc.Secrets {
			sec, ok := f.Secrets[ref.Source]
			if !ok {
				return nil, fmt.Errorf("service %q references undefined secret %q — declare it under top-level secrets: with a file:, or remove the reference", name, ref.Source)
			}
			if sec.External {
				return nil, fmt.Errorf("service %q: external secret %q is not supported (only file-based secrets)", name, ref.Source)
			}
			if sec.File == "" {
				return nil, fmt.Errorf("secret %q must set `file` (only file-based secrets are supported)", ref.Source)
			}
			// The target names a file directly under /run/secrets; reject a path
			// that would nest under or escape it.
			if strings.ContainsAny(ref.Target, "/") || strings.Contains(ref.Target, "..") {
				return nil, fmt.Errorf("service %q: secret target %q must be a bare name (no path separators)", name, ref.Target)
			}
		}
	}
	if err := p.validateDeps(); err != nil {
		return nil, err
	}
	return p, nil
}

// validateDeps ensures every depends_on target exists, uses a known condition,
// and — for service_healthy — actually defines a (non-disabled) healthcheck.
func (p *Project) validateDeps() error {
	// Services that some dependent needs to run to completion (exit 0). opossum
	// runs these in the foreground, so they finish and stop; nobody may also
	// require them to stay running (service_healthy).
	completedTargets := map[string]bool{}
	for _, svc := range p.Services {
		for _, dep := range svc.DependsOn {
			if dep.Condition == ConditionCompleted {
				completedTargets[dep.Name] = true
			}
		}
	}

	for name, svc := range p.Services {
		for _, dep := range svc.DependsOn {
			target, ok := p.Services[dep.Name]
			if !ok {
				return fmt.Errorf("service %q depends on unknown service %q — define %q under services: or remove it from depends_on", name, dep.Name, dep.Name)
			}
			switch dep.Condition {
			case ConditionStarted, ConditionCompleted:
			case ConditionHealthy:
				// Disabled is redundant today — both spellings of "off" clear Test, so the
				// length check fires first — but it is the clause that states the intent.
				// Keep it: the day a healthcheck keeps its test while disabled (round-tripping
				// `config`, say), the length check alone would silently start accepting this.
				if target.Healthcheck == nil || target.Healthcheck.Disabled || len(target.Healthcheck.Test) == 0 {
					return fmt.Errorf("service %q requires %q to be healthy, but %q defines no healthcheck", name, dep.Name, dep.Name)
				}
				if completedTargets[dep.Name] {
					return fmt.Errorf("service %q requires %q to be healthy, but %q is depended on to complete (run-to-completion services stop, so they can't stay healthy)", name, dep.Name, dep.Name)
				}
			default:
				return fmt.Errorf("service %q: unsupported depends_on condition %q for %q — use service_started, service_healthy, or service_completed_successfully", name, dep.Condition, dep.Name)
			}
		}
	}
	return nil
}

var projectNameSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// SanitizeName lowercases and strips characters not allowed in project/container
// names so a directory like "My App" becomes "my-app".
func SanitizeName(s string) string {
	s = strings.ToLower(s)
	s = projectNameSanitizer.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "opossum"
	}
	return s
}

// normalizePort maps a bare container-port ports entry ("3000", "3000/udp",
// "3000-3005") to the host:container form Apple's `container` requires
// ("3000:3000", …) — it has no random-host-port option, so the host port
// mirrors the container port. Specs that already name a host port
// ("8080:80", "127.0.0.1:8080:80", "8080:80/udp") pass through unchanged.
// mirrored is true when opossum supplied the host port itself (the compose file
// named only a container port), which is what lets `up` move it if the mirrored
// port turns out to be taken.
func normalizePort(spec string) (norm string, mirrored bool) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return spec, false
	}
	proto := ""
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		proto = s[i:] // keep the "/tcp" or "/udp" suffix
		s = s[:i]
	}
	switch {
	case !strings.Contains(s, ":"):
		s = s + ":" + s // bare container port -> host port mirrors it
		mirrored = true
	case strings.HasPrefix(s, ":") && !strings.Contains(s[1:], ":"):
		s = s[1:] + s // ":80" (empty host = random in docker) -> "80:80"
		mirrored = true
	default:
		// "ip::80" — a host IP with the host port left to the engine. Same deal as
		// ":80", just bound to one interface; without this it reached the runtime
		// with an empty host port.
		if i := strings.LastIndexByte(s, ':'); i > 0 && s[i-1] == ':' {
			target := s[i+1:]
			s = s[:i] + target + ":" + target
			mirrored = true
		}
	}
	return s + proto, mirrored
}
