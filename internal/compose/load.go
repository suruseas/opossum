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
func Load(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading compose file: %w", err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(abs)

	// Expand ${VAR} references before parsing, using a `.env` file next to the
	// compose file overlaid by the process environment.
	lookup, err := loadEnv(baseDir)
	if err != nil {
		return nil, err
	}
	data, err = interpolate(data, lookup)
	if err != nil {
		return nil, fmt.Errorf("interpolating %s: %w", path, err)
	}

	var f composeFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("%s defines no services", path)
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
