// Package compose parses a subset of the docker-compose schema that opossum
// understands and maps it onto Apple's `container` runtime.
package compose

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Project is a parsed compose file plus runtime metadata.
type Project struct {
	Name     string
	BaseDir  string // directory the compose file lives in; build/volume paths resolve against it
	Services map[string]*Service
	Secrets  map[string]Secret     // top-level file-based secrets, mounted at /run/secrets/<name> (#76)
	Volumes  map[string]VolumeDecl // top-level volume declarations; only `external` is acted on (#64)

	// Unsupported holds top-level compose keys opossum doesn't act on (e.g.
	// networks, volumes), collected so it can warn rather than silently ignore.
	Unsupported []string
}

// Service is a single service definition.
type Service struct {
	Name        string        `yaml:"-"`
	Image       string        `yaml:"image"`
	Platform    string        `yaml:"platform"` // e.g. linux/amd64; runs via Rosetta on Apple silicon
	Build       *Build        `yaml:"build"`
	Command     Command       `yaml:"command"`
	Entrypoint  Command       `yaml:"entrypoint"`
	Environment Environment   `yaml:"environment"`
	EnvFile     EnvFiles      `yaml:"env_file"`
	Ports       []string      `yaml:"ports"`
	Volumes     Volumes       `yaml:"volumes"`
	Tmpfs       StringOrSlice `yaml:"tmpfs"` // service-level tmpfs targets (#93); volume-form `type: tmpfs` folds in (#79)
	Secrets     SecretRefs    `yaml:"secrets"`
	DependsOn   DependsOn     `yaml:"depends_on"`
	Healthcheck *Healthcheck  `yaml:"healthcheck"`
	Profiles    []string      `yaml:"profiles"`  // service starts only when one of these profiles is active (empty = always)
	MemLimit    scalarStr     `yaml:"mem_limit"` // legacy memory limit ("512m", "2g", …)
	CPUs        scalarStr     `yaml:"cpus"`      // legacy CPU limit (may be fractional)
	Deploy      *Deploy       `yaml:"deploy"`    // only deploy.resources.limits.{memory,cpus} is acted on

	// Unsupported holds any compose keys opossum doesn't act on (e.g.
	// container_name, restart), collected during parsing so it can warn rather
	// than silently ignore them.
	Unsupported []string `yaml:"-"`
}

// scalarStr accepts a YAML scalar (number or string) as its string form, so a
// field like `cpus: 1.5` or `memory: "512m"` decodes uniformly.
type scalarStr string

func (s *scalarStr) UnmarshalYAML(n *yaml.Node) error {
	*s = scalarStr(n.Value)
	return nil
}

// Deploy carries the one part of `deploy:` opossum acts on: resource limits.
type Deploy struct {
	Resources *DeployResources `yaml:"resources"`
}

// DeployResources is deploy.resources; only limits are used (reservations ignored).
type DeployResources struct {
	Limits *DeployLimits `yaml:"limits"`
}

// DeployLimits is deploy.resources.limits.
type DeployLimits struct {
	Memory scalarStr `yaml:"memory"`
	CPUs   scalarStr `yaml:"cpus"`
}

func (s *Service) deployMemory() string {
	if s.Deploy != nil && s.Deploy.Resources != nil && s.Deploy.Resources.Limits != nil {
		return string(s.Deploy.Resources.Limits.Memory)
	}
	return ""
}

func (s *Service) deployCPUs() string {
	if s.Deploy != nil && s.Deploy.Resources != nil && s.Deploy.Resources.Limits != nil {
		return string(s.Deploy.Resources.Limits.CPUs)
	}
	return ""
}

// Resources resolves the effective `container run` -m/-c arguments from the
// legacy (mem_limit/cpus) and modern (deploy.resources.limits) fields. Both forms
// may be set only if they agree (docker compose parity). Memory is emitted in
// MiB with an uppercase suffix and CPUs as an integer (rounded up), which is what
// Apple's `container` accepts (lowercase suffixes / fractional CPUs are rejected).
func (s *Service) Resources() (mem, cpu string, err error) {
	memBytes, err := resolveScalar("mem_limit", "deploy.resources.limits.memory",
		string(s.MemLimit), s.deployMemory(), parseMemoryBytes)
	if err != nil {
		return "", "", fmt.Errorf("service %q: %w", s.Name, err)
	}
	if memBytes > 0 {
		mib := (int64(memBytes) + (1 << 20) - 1) / (1 << 20) // ceil to MiB
		mem = strconv.FormatInt(mib, 10) + "M"
	}
	cpus, err := resolveScalar("cpus", "deploy.resources.limits.cpus",
		string(s.CPUs), s.deployCPUs(), parseCPUs)
	if err != nil {
		return "", "", fmt.Errorf("service %q: %w", s.Name, err)
	}
	if cpus > 0 {
		cpu = strconv.Itoa(int(math.Ceil(cpus))) // Apple container wants a whole CPU count
	}
	return mem, cpu, nil
}

// resolveScalar picks the legacy or deploy value for one resource, erroring if
// both are set to different values (as docker compose does).
func resolveScalar(legacyKey, deployKey, legacy, deploy string, parse func(string) (float64, error)) (float64, error) {
	var v float64
	if legacy != "" {
		p, err := parse(legacy)
		if err != nil {
			return 0, err
		}
		v = p
	}
	if deploy != "" {
		p, err := parse(deploy)
		if err != nil {
			return 0, err
		}
		if v != 0 && p != 0 && v != p {
			return 0, fmt.Errorf("%s and %s are set to different values", legacyKey, deployKey)
		}
		if p != 0 {
			v = p
		}
	}
	return v, nil
}

// parseMemoryBytes parses "512m"/"2g"/"512MiB"/"512" into bytes (binary units,
// matching docker compose).
func parseMemoryBytes(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	end := 0
	for end < len(s) && (s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	num, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory value %q", s)
	}
	unit := strings.ToLower(strings.TrimSpace(s[end:]))
	unit = strings.TrimSuffix(unit, "ib") // mib -> m
	unit = strings.TrimSuffix(unit, "b")  // mb -> m, b -> ""
	mult := map[string]float64{"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40, "p": 1 << 50}
	f, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("invalid memory unit in %q", s)
	}
	return num * f, nil
}

// parseCPUs parses a CPU count ("1.5", "0.5", "2").
func parseCPUs(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpus value %q", s)
	}
	if f < 0 {
		return 0, fmt.Errorf("cpus must not be negative, got %q", s)
	}
	return f, nil
}

// serviceKnownKeys is the set of compose service keys opossum understands,
// derived from the struct tags so it can't drift.
var serviceKnownKeys = func() map[string]bool {
	m := map[string]bool{}
	t := reflect.TypeOf(Service{})
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			m[name] = true
		}
	}
	return m
}()

// UnmarshalYAML decodes the service via the struct tags and, in a second pass,
// records any keys opossum doesn't support (so callers can warn).
func (s *Service) UnmarshalYAML(value *yaml.Node) error {
	type raw Service // no UnmarshalYAML -> default struct decoding
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*s = Service(r)

	// Move any tmpfs entries (tagged by Volumes.UnmarshalYAML) out of Volumes
	// into Tmpfs, so the marker never escapes parsing (#79).
	if len(s.Volumes) > 0 {
		mounts := make(Volumes, 0, len(s.Volumes))
		for _, m := range s.Volumes {
			if target, ok := strings.CutPrefix(m, tmpfsMarker); ok {
				s.Tmpfs = append(s.Tmpfs, target)
			} else {
				mounts = append(mounts, m)
			}
		}
		s.Volumes = mounts
	}

	var keys map[string]yaml.Node
	if err := value.Decode(&keys); err != nil {
		return err
	}
	for k := range keys {
		if !serviceKnownKeys[k] {
			s.Unsupported = append(s.Unsupported, k)
		}
	}
	// `deploy` is a known key (opossum acts on resources.limits), but flag it if it
	// carries anything else opossum drops (replicas, restart_policy, reservations…),
	// so those aren't silently ignored.
	if dep, ok := keys["deploy"]; ok && deployHasExtra(dep) {
		s.Unsupported = append(s.Unsupported, "deploy")
	}
	sort.Strings(s.Unsupported)
	return nil
}

// deployHasExtra reports whether a `deploy:` node contains anything beyond
// resources.limits.{memory,cpus} — the only part opossum applies.
func deployHasExtra(n yaml.Node) bool {
	var top map[string]yaml.Node
	if n.Decode(&top) != nil {
		return true
	}
	for k, v := range top {
		if k != "resources" {
			return true
		}
		var res map[string]yaml.Node
		if v.Decode(&res) != nil {
			return true
		}
		for rk, rv := range res {
			if rk != "limits" {
				return true // e.g. reservations
			}
			var lim map[string]yaml.Node
			if rv.Decode(&lim) != nil {
				return true
			}
			for lk := range lim {
				if lk != "memory" && lk != "cpus" {
					return true
				}
			}
		}
	}
	return false
}

// tmpfsMarker tags a `type: tmpfs` entry inside the parsed Volumes list so
// Service.UnmarshalYAML can split it out into Service.Tmpfs. The NUL bytes can't
// appear in a real volume spec, so it never collides with a bind/named mount and
// never escapes parsing (#79).
const tmpfsMarker = "\x00tmpfs\x00"

// Volumes is a service's volume mounts. Each entry is normalized to the short
// `source:target[:ro]` string opossum's orchestrator already understands, so
// both compose forms are accepted: the short string (`./src:/dst`, `name:/dst`)
// and the long mapping (`{type, source, target, read_only}`) that real
// docker-compose files commonly use (#74). `type: tmpfs` entries are tagged with
// tmpfsMarker and later moved to Service.Tmpfs (#79).
type Volumes []string

func (v *Volumes) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("volumes must be a list, got %v", value.Tag)
	}
	out := make(Volumes, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
			continue
		}
		var lf struct {
			Type     string `yaml:"type"`
			Source   string `yaml:"source"`
			Target   string `yaml:"target"`
			ReadOnly bool   `yaml:"read_only"`
		}
		if err := item.Decode(&lf); err != nil {
			return err
		}
		if lf.Target == "" {
			return fmt.Errorf("volume entry is missing a target")
		}
		// tmpfs isn't a `source:target` mount (it becomes `container run --tmpfs
		// <target>`), so tag it with a marker here and let Service.UnmarshalYAML
		// split it into Service.Tmpfs. Reject any other non-bind/volume type
		// rather than silently turning it into a host bind (#79).
		switch lf.Type {
		case "", "bind", "volume":
		case "tmpfs":
			out = append(out, tmpfsMarker+lf.Target)
			continue
		default:
			return fmt.Errorf("unsupported volume type %q (only bind, volume, tmpfs)", lf.Type)
		}
		// No source is an anonymous volume (short form is just the target path).
		s := lf.Target
		if lf.Source != "" {
			s = lf.Source + ":" + lf.Target
		}
		if lf.ReadOnly {
			s += ":ro"
		}
		out = append(out, s)
	}
	*v = out
	return nil
}

// VolumeDecl is a top-level `volumes:` entry. opossum auto-creates named volumes
// on use, so it only acts on `external` (an external volume is used by its real
// name and never namespaced or removed by `down -v`) (#64).
type VolumeDecl struct {
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

// Secret is a top-level compose secret. opossum supports only file-based
// secrets (the common `_FILE` pattern of official images); `external` secrets
// are not resolved (#76).
type Secret struct {
	File     string `yaml:"file"`
	External bool   `yaml:"external"`
}

// SecretRef is a service's reference to a top-level secret. The short form is
// just the secret name (mounted at /run/secrets/<name>); the long form
// (`{source, target}`) mounts it at /run/secrets/<target>.
type SecretRef struct {
	Source string
	Target string
}

// SecretRefs accepts the short (name) and long (`{source, target}`) entry forms.
type SecretRefs []SecretRef

func (s *SecretRefs) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("secrets must be a list, got %v", value.Tag)
	}
	out := make(SecretRefs, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, SecretRef{Source: item.Value, Target: item.Value})
			continue
		}
		var lf struct {
			Source string `yaml:"source"`
			Target string `yaml:"target"`
		}
		if err := item.Decode(&lf); err != nil {
			return err
		}
		if lf.Source == "" {
			return fmt.Errorf("secret entry is missing a source")
		}
		if lf.Target == "" {
			lf.Target = lf.Source
		}
		out = append(out, SecretRef{Source: lf.Source, Target: lf.Target})
	}
	*s = out
	return nil
}

// Build describes how to build an image for a service.
type Build struct {
	Context    string      `yaml:"context"`
	Dockerfile string      `yaml:"dockerfile"`
	Args       Environment `yaml:"args"`
	Target     string      `yaml:"target"` // multi-stage build target (#75)
}

// UnmarshalYAML accepts either a bare string (treated as the build context) or
// a full mapping.
func (b *Build) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		b.Context = value.Value
		return nil
	}
	type raw Build
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*b = Build(r)
	return nil
}

// Command is a service command or entrypoint. Compose's string form (`sh -c
// "echo hi"`) is shell-word-split so it maps onto the runtime's argv, while the
// list form (`["sh", "-c", "echo hi"]`) is taken verbatim.
type Command []string

func (c *Command) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		parts, err := shellSplit(value.Value)
		if err != nil {
			return fmt.Errorf("command: %w", err)
		}
		*c = parts
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := value.Decode(&out); err != nil {
			return err
		}
		*c = out
		return nil
	}
	return fmt.Errorf("expected a string or list for command, got yaml kind %d", value.Kind)
}

// EnvFileRef is one env_file entry. Required defaults to true (a missing file is
// an error, matching docker compose); the long form `{path, required: false}`
// makes an absent file be skipped instead (#85).
type EnvFileRef struct {
	Path     string
	Required bool
}

// EnvFiles accepts a scalar path, a list of paths, and the long-form list of
// `{path, required}` mappings (mixable).
type EnvFiles []EnvFileRef

func (e *EnvFiles) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*e = EnvFiles{{Path: value.Value, Required: true}}
		return nil
	case yaml.SequenceNode:
		out := make(EnvFiles, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, EnvFileRef{Path: item.Value, Required: true})
				continue
			}
			var lf struct {
				Path     string `yaml:"path"`
				Required *bool  `yaml:"required"`
			}
			if err := item.Decode(&lf); err != nil {
				return err
			}
			if lf.Path == "" {
				return fmt.Errorf("env_file entry is missing a path")
			}
			req := true
			if lf.Required != nil {
				req = *lf.Required
			}
			out = append(out, EnvFileRef{Path: lf.Path, Required: req})
		}
		*e = out
		return nil
	}
	return fmt.Errorf("expected a string or list for env_file, got yaml kind %d", value.Kind)
}

// StringOrSlice accepts a scalar (taken as one element) or a list. Used by
// healthcheck `test`, where a bare string means "run through a shell" — so it is
// deliberately NOT shell-split here (see Healthcheck.UnmarshalYAML).
type StringOrSlice []string

func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := value.Decode(&out); err != nil {
			return err
		}
		*s = out
		return nil
	}
	return fmt.Errorf("expected a string or list, got yaml kind %d", value.Kind)
}

// Environment normalizes both the list form (KEY=value) and the map form
// (KEY: value) into a sorted []string of KEY=value entries. A null map value
// becomes a bare KEY (pass-through from the host environment).
type Environment []string

func (e *Environment) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var out []string
		if err := value.Decode(&out); err != nil {
			return err
		}
		*e = out
		return nil
	case yaml.MappingNode:
		var m map[string]interface{}
		if err := value.Decode(&m); err != nil {
			return err
		}
		out := make([]string, 0, len(m))
		for k, v := range m {
			if v == nil {
				out = append(out, k)
			} else {
				out = append(out, fmt.Sprintf("%s=%v", k, v))
			}
		}
		sort.Strings(out)
		*e = out
		return nil
	}
	return fmt.Errorf("expected a list or map for environment, got yaml kind %d", value.Kind)
}

// depends_on condition values.
const (
	ConditionStarted   = "service_started"                // default: dependency has been started
	ConditionHealthy   = "service_healthy"                // dependency's healthcheck passes
	ConditionCompleted = "service_completed_successfully" // dependency runs to completion with exit 0
)

// Dependency is one depends_on entry: the target service plus the condition that
// must hold before the dependent starts.
type Dependency struct {
	Name      string
	Condition string
}

// DependsOn accepts the short list form (names, implying service_started) and
// the long map form (per-target condition). Ordering uses the names (see
// Names); the condition gates startup in the orchestrator.
type DependsOn []Dependency

func (d *DependsOn) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := value.Decode(&names); err != nil {
			return err
		}
		out := make(DependsOn, 0, len(names))
		for _, n := range names {
			out = append(out, Dependency{Name: n, Condition: ConditionStarted})
		}
		*d = out
		return nil
	case yaml.MappingNode:
		var m map[string]struct {
			Condition string `yaml:"condition"`
		}
		if err := value.Decode(&m); err != nil {
			return err
		}
		out := make(DependsOn, 0, len(m))
		for name, v := range m {
			cond := v.Condition
			if cond == "" {
				cond = ConditionStarted
			}
			out = append(out, Dependency{Name: name, Condition: cond})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		*d = out
		return nil
	}
	return fmt.Errorf("expected a list or map for depends_on, got yaml kind %d", value.Kind)
}

// Names returns the dependency service names in order.
func (d DependsOn) Names() []string {
	out := make([]string, len(d))
	for i, dep := range d {
		out[i] = dep.Name
	}
	return out
}

// Healthcheck describes how to probe a service for readiness. Apple's container
// runtime has no native healthcheck, so opossum runs Test via `container exec`
// and polls until it succeeds, gating dependents that require service_healthy.
type Healthcheck struct {
	Test        []string      // argv for `container exec` (shell forms wrapped in `sh -c`)
	Interval    time.Duration // wait between attempts (default 30s)
	Timeout     time.Duration // enforced per-attempt timeout (default 30s)
	Retries     int           // attempts before giving up (default 3)
	StartPeriod time.Duration // grace period before the first attempt (default 0)
	Disabled    bool          // test: ["NONE"]
}

// UnmarshalYAML parses the compose healthcheck schema, normalizing `test` into
// an argv suitable for `container exec` and applying compose's defaults.
func (h *Healthcheck) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Test        StringOrSlice `yaml:"test"`
		Interval    string        `yaml:"interval"`
		Timeout     string        `yaml:"timeout"`
		Retries     int           `yaml:"retries"`
		StartPeriod string        `yaml:"start_period"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}

	switch test := []string(raw.Test); {
	case len(test) == 0:
		// no test — load-time validation reports this for service_healthy deps
	case test[0] == "NONE":
		h.Disabled = true
	case test[0] == "CMD":
		h.Test = append([]string(nil), test[1:]...)
	case test[0] == "CMD-SHELL":
		h.Test = []string{"sh", "-c", strings.Join(test[1:], " ")}
	case len(test) == 1:
		// bare string form (`test: some command`) runs through a shell
		h.Test = []string{"sh", "-c", test[0]}
	default:
		// a list without a directive is taken as a direct argv
		h.Test = append([]string(nil), test...)
	}

	var err error
	if h.Interval, err = parseDuration(raw.Interval, 30*time.Second); err != nil {
		return fmt.Errorf("healthcheck interval: %w", err)
	}
	if h.Timeout, err = parseDuration(raw.Timeout, 30*time.Second); err != nil {
		return fmt.Errorf("healthcheck timeout: %w", err)
	}
	if h.StartPeriod, err = parseDuration(raw.StartPeriod, 0); err != nil {
		return fmt.Errorf("healthcheck start_period: %w", err)
	}
	h.Retries = raw.Retries
	if h.Retries <= 0 {
		h.Retries = 3
	}
	return nil
}

func parseDuration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}
