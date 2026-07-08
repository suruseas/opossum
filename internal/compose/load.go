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
	Name     string                `yaml:"name"`
	Services map[string]*Service   `yaml:"services"`
	Secrets  map[string]Secret     `yaml:"secrets"`
	Volumes  map[string]VolumeDecl `yaml:"volumes"`
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
		case k == "name" || k == "services" || k == "version" || k == "secrets":
		case strings.HasPrefix(k, "x-"):
		default:
			out = append(out, k)
		}
	}
	sort.Strings(out)
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
// port) should collapse to one, matching docker compose.
var dedupSeqKeys = map[string]bool{"ports": true, "volumes": true, "expose": true}

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
			if dedupSeqKeys[key] {
				merged = dedupSeq(merged)
			}
			return merged
		}
	}
	return over
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
	lookup, err := loadEnv(baseDir, envFiles)
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
		if data, err = interpolate(raw, lookup); err != nil {
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
			if raw, err = interpolate(raw, lookup); err != nil {
				return nil, fmt.Errorf("interpolating %s: %w", path, err)
			}
			var m map[string]any
			if err := yaml.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", path, err)
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
		return nil, fmt.Errorf("parsing %s: %w", paths[0], err)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("%s defines no services", paths[0])
	}

	p := &Project{
		Name:        f.Name,
		BaseDir:     baseDir,
		Services:    f.Services,
		Secrets:     f.Secrets,
		Volumes:     f.Volumes,
		Unsupported: ignoredTopLevel(data),
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
		// Give bare container ports a host port (Apple's `container` requires one),
		// then drop duplicates the merge couldn't see because it dedups raw text
		// (e.g. base "3000" + override "3000:3000" both normalize to "3000:3000").
		if len(svc.Ports) > 0 {
			seen := make(map[string]bool, len(svc.Ports))
			ports := make([]string, 0, len(svc.Ports))
			for _, p := range svc.Ports {
				n := normalizePort(p)
				if seen[n] {
					continue
				}
				seen[n] = true
				ports = append(ports, n)
			}
			svc.Ports = ports
		}
		// Fold env_file values into the environment (explicit `environment` wins).
		env, err := resolveEnvFiles(baseDir, svc.EnvFile, svc.Environment)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		svc.Environment = env

		// Every referenced secret must be a defined, file-based top-level secret.
		for _, ref := range svc.Secrets {
			sec, ok := f.Secrets[ref.Source]
			if !ok {
				return nil, fmt.Errorf("service %q references undefined secret %q", name, ref.Source)
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
				return fmt.Errorf("service %q depends on unknown service %q", name, dep.Name)
			}
			switch dep.Condition {
			case ConditionStarted, ConditionCompleted:
			case ConditionHealthy:
				if target.Healthcheck == nil || target.Healthcheck.Disabled || len(target.Healthcheck.Test) == 0 {
					return fmt.Errorf("service %q requires %q to be healthy, but %q defines no healthcheck", name, dep.Name, dep.Name)
				}
				if completedTargets[dep.Name] {
					return fmt.Errorf("service %q requires %q to be healthy, but %q is depended on to complete (run-to-completion services stop, so they can't stay healthy)", name, dep.Name, dep.Name)
				}
			default:
				return fmt.Errorf("service %q: unsupported depends_on condition %q for %q", name, dep.Condition, dep.Name)
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
func normalizePort(spec string) string {
	s := strings.TrimSpace(spec)
	if s == "" {
		return spec
	}
	proto := ""
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		proto = s[i:] // keep the "/tcp" or "/udp" suffix
		s = s[:i]
	}
	switch {
	case !strings.Contains(s, ":"):
		s = s + ":" + s // bare container port -> host port mirrors it
	case strings.HasPrefix(s, ":") && !strings.Contains(s[1:], ":"):
		s = s[1:] + s // ":80" (empty host = random in docker) -> "80:80"
	}
	return s + proto
}
