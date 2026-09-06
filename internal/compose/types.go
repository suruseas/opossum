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
	Secrets  map[string]Secret      // top-level file-based secrets, mounted at /run/secrets/<name> (#76)
	Volumes  map[string]VolumeDecl  // top-level volume declarations; only `external` is acted on (#64)
	Networks map[string]NetworkDecl // top-level network declarations; opossum acts on `internal`/`external`/`name`

	// Unsupported holds top-level compose keys opossum doesn't act on (e.g.
	// networks, volumes), collected so it can warn rather than silently ignore.
	Unsupported []string
}

// NetworkModeNone is the only network_mode opossum acts on: it isolates a
// service from all networking (mapped to `container run --network none`), the
// full-egress-block floor for sandboxing an untrusted workload.
const NetworkModeNone = "none"

// Service is a single service definition.
type Service struct {
	Name        string      `yaml:"-"`
	Image       string      `yaml:"image"`
	Platform    string      `yaml:"platform"` // e.g. linux/amd64; runs via Rosetta on Apple silicon
	Build       *Build      `yaml:"build"`
	Command     Command     `yaml:"command"`
	Entrypoint  Command     `yaml:"entrypoint"`
	Environment Environment `yaml:"environment"`
	EnvFile     EnvFiles    `yaml:"env_file"`
	// envFileErr holds why this service's env_file entries could not be read,
	// when they could not. Loading records it here instead of failing, because
	// a project is not broken by a service nobody is running: docker resolves
	// env_file when it needs a service's rendered environment, so `ps` and
	// `logs` and `config --services` all succeed against a compose file whose
	// unused service points at an env file that does not parse (measured on
	// Docker Compose v5.4.0; that run, and the missing-file shape beside it, are
	// in testdata/docker-compose-env-file.md, commands and all, so they can be
	// taken again). Environment is left holding the declared entries
	// only — nothing from the files is folded in — so a caller that renders or
	// starts this service has to ask through ResolvedEnv.
	envFileErr error
	Ports      Ports `yaml:"ports"`
	// Restart is the compose restart policy. opossum honours it with a small
	// per-project supervisor started by `up`; see internal/orchestrator/supervisor.go.
	Restart string `yaml:"restart"`
	// AutoHostPort marks the entries of Ports whose HOST port opossum chose,
	// because the compose file named only a container port (`ports: ["3000"]`).
	// Compose leaves the host port to the engine for those, so opossum is free to
	// move one that turns out to be taken — unlike an explicit `"3000:3000"`,
	// which is the user's declared contract and is never moved. Set during load,
	// not read from YAML.
	AutoHostPort map[string]bool `yaml:"-"`
	Volumes      Volumes         `yaml:"volumes"`
	Tmpfs        StringOrSlice   `yaml:"tmpfs"` // service-level tmpfs targets (#93); volume-form `type: tmpfs` folds in (#79)
	// NoCopy are the container paths whose volume must NOT be filled from the
	// image. Docker seeds a fresh volume from the image at the mount point, and
	// `volume: {nocopy: true}` turns that off; opossum emulates the seeding, so it
	// has to honour the same switch. Lifted out of Volumes during parsing, like
	// tmpfs, so the marker never escapes.
	NoCopy      []string        `yaml:"-"`
	Secrets     SecretRefs      `yaml:"secrets"`
	DependsOn   DependsOn       `yaml:"depends_on"`
	Healthcheck *Healthcheck    `yaml:"healthcheck"`
	Profiles    []string        `yaml:"profiles"`     // service starts only when one of these profiles is active (empty = always)
	MemLimit    scalarStr       `yaml:"mem_limit"`    // legacy memory limit ("512m", "2g", …)
	CPUs        scalarStr       `yaml:"cpus"`         // legacy CPU limit (may be fractional)
	SSH         bool            `yaml:"ssh"`          // forward the host SSH agent (--ssh) for private git over SSH
	User        string          `yaml:"user"`         // --user (name|uid[:gid]) the process runs as
	WorkingDir  string          `yaml:"working_dir"`  // --workdir the process starts in
	Init        bool            `yaml:"init"`         // --init: run a tini-like init as PID 1 to reap zombies
	ReadOnly    bool            `yaml:"read_only"`    // --read-only root filesystem
	CapAdd      StringOrSlice   `yaml:"cap_add"`      // --cap-add Linux capabilities
	CapDrop     StringOrSlice   `yaml:"cap_drop"`     // --cap-drop Linux capabilities
	NetworkMode string          `yaml:"network_mode"` // only "none" acted on: full network isolation (--network none)
	Networks    ServiceNetworks `yaml:"networks"`     // declared networks this service joins (one --network each; aliases/static IPs not applied)

	Deploy  *Deploy  `yaml:"deploy"`  // only deploy.resources.limits.{memory,cpus} is acted on
	Develop *Develop `yaml:"develop"` // develop.watch drives `opossum watch` (file-change sync)

	// MCPTools declares the MCP (Model Context Protocol) servers to wire up for an
	// agent in this service: opossum generates a `.mcp.json` and mounts it in. It's
	// a compose `x-` extension (other tools ignore it). Each entry is either another
	// service reachable by name (`svc`, `svc:port`, `svc:port/path`) or an explicit
	// `name=url`. See internal/orchestrator/mcp.go. (#258)
	MCPTools []string `yaml:"x-opossum-mcp-tools"`

	// Unsupported holds any compose keys opossum doesn't act on (e.g.
	// container_name, restart), collected during parsing so it can warn rather
	// than silently ignore them.
	Unsupported []string `yaml:"-"`
	// Extends is set when the service carries an `extends:` key — a service
	// that pulls its settings from another (in this file or another). opossum
	// does not read it; the load refuses the service by name rather than
	// reporting the missing image the reader did not forget (#415).
	Extends *ExtendsRef `yaml:"-"`
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

// Develop is the compose `develop:` block; opossum acts on its `watch:` rules
// (via `opossum watch`), which mirror host file changes into running containers.
type Develop struct {
	Watch []WatchRule `yaml:"watch"`
}

// WatchRule is one `develop.watch` entry: when files under Path change, opossum
// takes Action. `sync` copies the changed file to Target inside the container;
// other actions are recognized but not yet acted on. Ignore holds path globs
// (matched against the path relative to Path) that are skipped.
type WatchRule struct {
	Action string   `yaml:"action"` // sync | rebuild | sync+restart (only sync acted on)
	Path   string   `yaml:"path"`   // host path watched (relative to the compose dir)
	Target string   `yaml:"target"` // container path files sync to (for action: sync)
	Ignore []string `yaml:"ignore"` // globs (relative to Path) to skip
}

// rawWatchRule is a watch rule as written: `ignore` takes one glob or a
// list of them (docker compose reads both).
type rawWatchRule struct {
	Action string        `yaml:"action"`
	Path   string        `yaml:"path"`
	Target string        `yaml:"target"`
	Ignore StringOrSlice `yaml:"ignore"`
}

// UnmarshalYAML reads a watch rule; that the rule has a path is checked
// where the rule's number is known (Service.UnmarshalYAML's second pass).
// `action` left out is taken as `sync` by the watcher (docker compose
// requires it; the leniency predates this and is kept).
func (w *WatchRule) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("develop.watch: a rule must be a mapping (path, action, target), got %s", kindName(value.Kind))
	}
	var raw rawWatchRule
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*w = WatchRule{Action: raw.Action, Path: raw.Path, Target: raw.Target, Ignore: []string(raw.Ignore)}
	return nil
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
			// The key, because the message below will not repeat the value: these
			// are read from the compose file, where a `${...}` reference can put a
			// password in, and an error goes to the terminal and the CI log. Two
			// keys can hold this number, so which one is being complained about is
			// what the reader needs.
			return 0, fmt.Errorf("%s: %w", legacyKey, err)
		}
		v = p
	}
	if deploy != "" {
		p, err := parse(deploy)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", deployKey, err)
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
		return 0, fmt.Errorf("not a memory size — use a number with an optional unit, e.g. \"512m\" or \"2g\"")
	}
	unit := strings.ToLower(strings.TrimSpace(s[end:]))
	unit = strings.TrimSuffix(unit, "ib") // mib -> m
	unit = strings.TrimSuffix(unit, "b")  // mb -> m, b -> ""
	mult := map[string]float64{"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40, "p": 1 << 50}
	f, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("not a memory unit — use k, m, g, or t (e.g. \"512m\")")
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
		return 0, fmt.Errorf("not a number of CPUs — use a number, e.g. \"1.5\" or \"2\"")
	}
	if f < 0 {
		return 0, fmt.Errorf("must not be negative")
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

// stringListFields are the service fields whose value is a string or a list
// of strings and is read as such with no decoder of its own to check the
// items: docker compose refuses a number, a boolean or a date in any of them
// (measured on v5.5.0; `expose` is the one that takes bare numbers, and is
// not here).
var stringListFields = map[string]bool{"command": true, "entrypoint": true, "tmpfs": true, "profiles": true, "cap_add": true, "cap_drop": true}

// refuseNonStringInList applies refuseNonString to a field written as a list
// (each item, aliases followed) or as one scalar.
func refuseNonStringInList(field string, n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		for i, item := range n.Content {
			item = unalias(item)
			// An item with nothing in it is not a name either (docker compose:
			// `unexpected type <nil>`); these fields have no decoder of their
			// own to say so.
			if item.Kind == yaml.ScalarNode && item.ShortTag() == "!!null" {
				return fmt.Errorf("%s entry %d of %d is empty — write the value or remove the `- `", field, i+1, len(n.Content))
			}
			if err := refuseNonString(field, i, len(n.Content), item); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if word := nonStringWord(n); word != "" {
			return fmt.Errorf("%s must be a string, got %s — quote it (`\"%s\"`) if it is meant literally", field, word, n.Value)
		}
	}
	return nil
}

// stringFields are the service fields that take one string, checked for a
// number, a boolean or a date written in their place (measured on docker
// compose v5.5.0: `must be a string` for a number or a boolean; a date it
// fails on differently). `restart` is not here: its own validation names
// the policies. `mem_limit` and `cpus` take numbers.
var stringFields = map[string]bool{"image": true, "user": true, "working_dir": true, "platform": true, "network_mode": true}

// listOnlyFields are the fields docker compose takes only as a list (`must
// be a array` for a bare name), unlike tmpfs/command/entrypoint, which take
// one string as well. `profiles` is such a field too, but its own decoding
// ([]string) already refuses a bare name as the wrong shape.
var listOnlyFields = map[string]bool{"cap_add": true, "cap_drop": true}

// nestedShapes are the nested fields opossum reads (or hands to the runtime)
// whose shape docker compose validates (measured on v5.5.0, `must be a
// string` / `must be a mapping` / `must be a number or string`), keyed by the
// service field they sit under. A path's last element is the field, and a
// `[]` element steps into each item of a list; what the field takes is one
// of "string", "mapping", "list", "string or list" or "number or string".
// `reservations` is read by nothing in opossum, so the struct decode says
// nothing about it and the mapping rows are checked here in full.
var nestedShapes = map[string][]nestedShape{
	"build": {
		{[]string{"context"}, "string", "a path, as in `.`"},
		{[]string{"dockerfile"}, "string", "a file name, as in `Dockerfile`"},
		{[]string{"target"}, "string", "a stage name"},
		{[]string{"args"}, "mapping", ""},
	},
	"healthcheck": {
		{[]string{"test"}, "string or list", "the command, as in `[\"CMD\", \"curl\", \"-f\", \"http://localhost\"]`"},
		{[]string{"interval"}, "string", "a duration, as in `10s`"},
		{[]string{"timeout"}, "string", "a duration, as in `5s`"},
		{[]string{"start_period"}, "string", "a duration, as in `30s`"},
		{[]string{"retries"}, "number or string", "a count, as in `3`"},
	},
	"develop": {
		{[]string{"watch"}, "list", ""},
		{[]string{"watch", "[]", "path"}, "string", "a path, as in `./src`"},
		{[]string{"watch", "[]", "action"}, "string", "`sync`, `rebuild`, `sync+restart`, `restart` or `sync+exec`"},
		{[]string{"watch", "[]", "target"}, "string", "a path in the container, as in `/app`"},
		{[]string{"watch", "[]", "ignore"}, "string or list", "a glob or a list of them, as in `[node_modules]`"},
	},
	"deploy": {
		{[]string{"resources"}, "mapping", ""},
		{[]string{"resources", "limits"}, "mapping", ""},
		{[]string{"resources", "reservations"}, "mapping", ""},
		{[]string{"resources", "limits", "memory"}, "string", "a size with a unit, as in `\"512m\"`"},
		{[]string{"resources", "reservations", "memory"}, "string", "a size with a unit, as in `\"512m\"`"},
		{[]string{"resources", "limits", "cpus"}, "number or string", "a count, as in `0.5`"},
		{[]string{"resources", "reservations", "cpus"}, "number or string", "a count, as in `0.5`"},
	},
}

type nestedShape struct {
	path  []string
	takes string
	hint  string // what to write instead, "" when the shape word says it
}

// refuseNestedShapes reads the nested fields under a service field's node
// and refuses the shapes docker compose refuses. `build:` itself takes a
// string or a mapping; a number there is caught first.
func refuseNestedShapes(field string, n yaml.Node) error {
	if field == "build" && n.Kind == yaml.ScalarNode {
		if word := nonStringWord(&n); word != "" {
			return fmt.Errorf("build must be a string or a mapping, got %s — write the context path, as in `build: .`", word)
		}
	}
	for _, shape := range nestedShapes[field] {
		if err := walkShape(n, field, shape.path, shape); err != nil {
			return err
		}
	}
	return nil
}

// walkShape follows shape's path from n, stepping into every item where the
// path says `[]`, and checks the node at the end. name accumulates the
// dotted path the refusal prints, with `entry N` for a list item.
func walkShape(n yaml.Node, name string, path []string, shape nestedShape) error {
	for i, key := range path {
		if key == "[]" {
			// Not a list: the struct decode refused that already (a `[]WatchRule`
			// takes nothing else), so there is nothing to step into.
			if n.Kind != yaml.SequenceNode {
				return nil
			}
			for j, item := range n.Content {
				item = unalias(item)
				// A `- ` with nothing after it is an empty rule, and would be
				// read as one (an empty path watches the whole project).
				if item.Kind == yaml.ScalarNode && item.Tag == "!!null" {
					return fmt.Errorf("%s entry %d of %d is empty — write the rule or remove the `- `", name, j+1, len(n.Content))
				}
				if err := walkShape(*item, fmt.Sprintf("%s entry %d", name, j+1), path[i+1:], shape); err != nil {
					return err
				}
			}
			return nil
		}
		node, ok := nestedNode(n, key)
		if !ok {
			return nil
		}
		n = *node
		name += "." + key
	}
	hint := ""
	if shape.hint != "" {
		hint = " — write " + shape.hint
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return fmt.Errorf("%s must be %s, got nothing%s", name, aOrAn(shape.takes), hint)
	}
	switch shape.takes {
	case "string":
		if n.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s must be a string, got %s%s", name, kindName(n.Kind), hint)
		}
		if word := nonStringWord(&n); word != "" {
			return fmt.Errorf("%s must be a string, got %s%s", name, word, hint)
		}
	case "mapping":
		// Where the struct decode read the field (`limits`) it refused a
		// non-mapping already; where it did not (`reservations`) this is the
		// only check.
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be a mapping, got %s", name, kindName(n.Kind))
		}
	// "list" and "string or list": a value of another kind is refused before
	// this runs — a `[]WatchRule` takes nothing but a list, and the item check
	// on `healthcheck.test` (refuseNonStringInList) refuses a number or a
	// boolean there — so those rows are read for the bare key only, whose
	// message names what they take.
	case "number or string":
		if n.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s must be a number or a string, got %s%s", name, kindName(n.Kind), hint)
		}
		if word := nonStringWord(&n); word != "" && word != "a number" {
			return fmt.Errorf("%s must be a number or a string, got %s%s", name, word, hint)
		}
	}
	return nil
}

// aOrAn puts the article on a shape word ("a string", "a mapping", "a number
// or string").
func aOrAn(takes string) string { return "a " + takes }

// watchRuleKeys are the keys a watch rule has; any other is listed as ignored.
var watchRuleKeys = map[string]bool{"action": true, "path": true, "target": true, "ignore": true}

// watchActions are the actions docker compose takes (opossum automates
// `sync`, `rebuild` and `sync+restart`; the other two are named at change
// time as not automated yet), and watchActionCopies the ones that copy
// files into the container and so need a `target`.
var (
	watchActions      = map[string]bool{"sync": true, "rebuild": true, "sync+restart": true, "restart": true, "sync+exec": true}
	watchActionCopies = map[string]bool{"sync": true, "sync+restart": true, "sync+exec": true}
)

// retriesCount reads `healthcheck.retries`: 0 when it was left out or null,
// the count otherwise, or an error for a value that is not a count. The
// tag decides how the text is read, as docker compose's decoder does — a
// bare number may be a decimal (truncated), a quoted one must be whole.
func retriesCount(n *yaml.Node) (int, error) {
	notACount := func() (int, error) {
		return 0, fmt.Errorf("healthcheck retries: not a count — use a whole number, as in 3 (got %q)", n.Value)
	}
	switch {
	case n.Kind == 0, n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		return 0, nil
	case n.Kind != yaml.ScalarNode:
		return 0, fmt.Errorf("healthcheck retries: not a count — use a whole number, as in 3 (got %s)", kindName(n.Kind))
	}
	var count int
	switch n.Tag {
	case "!!int":
		v, err := strconv.ParseInt(n.Value, 0, 64)
		if err != nil {
			return notACount()
		}
		count = int(v)
	case "!!float":
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f > math.MaxInt32 {
			return notACount()
		}
		count = int(f)
	case "!!str":
		v, err := strconv.Atoi(n.Value)
		if err != nil {
			return notACount()
		}
		count = v
	default:
		return notACount()
	}
	if count < 0 {
		return notACount()
	}
	return count, nil
}

// nestedNode follows mapping keys down from n (aliases and merge keys
// resolved by decoding each level into a map), returning the node at the end
// of the path, if every key is there.
func nestedNode(n yaml.Node, path ...string) (*yaml.Node, bool) {
	cur := n
	for _, key := range path {
		if cur.Kind != yaml.MappingNode {
			return nil, false
		}
		var m map[string]yaml.Node
		if err := cur.Decode(&m); err != nil {
			return nil, false
		}
		next, ok := m[key]
		if !ok {
			return nil, false
		}
		cur = *unalias(&next)
	}
	return &cur, true
}

// bareKeyIsAllowed are the service fields docker compose accepts with nothing
// after them (measured on v5.5.0, every field the Service struct reads):
// `command:` and `entrypoint:` mean "no command" (and the -f merge reads them
// so, see nullIsAValue); `deploy:` and `develop:` are mappings it lets be
// empty; the `x-` extension is the project's own and is never validated.
var bareKeyIsAllowed = map[string]bool{"command": true, "entrypoint": true, "deploy": true, "develop": true, "x-opossum-mcp-tools": true}

// fieldShape says, for the refusal of a bare key, what the field takes — in
// the words docker compose uses for the same file ("must be a array" and so
// on) for the fields it was measured on; any other field gets the neutral
// word from shapeOf.
var fieldShape = map[string]string{
	"volumes": "a list", "ports": "a list", "networks": "a list or a mapping", "secrets": "a list",
	"depends_on": "a list or a mapping", "env_file": "a string or a list",
	"environment": "a mapping or a list", "healthcheck": "a mapping",
	"build": "a string or a mapping",
	"image": "a string", "working_dir": "a string", "user": "a string", "restart": "a string",
	"platform": "a string", "network_mode": "a string",
	"cap_add": "a list", "cap_drop": "a list", "tmpfs": "a string or a list", "expose": "a list", "profiles": "a list",
}

func shapeOf(k string) string {
	if s, ok := fieldShape[k]; ok {
		return s
	}
	return "a value"
}

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
				continue
			}
			if mount, ok := strings.CutPrefix(m, nocopyMarker); ok {
				// The flag is about the target, which is what seeding is keyed on.
				s.NoCopy = append(s.NoCopy, mountTarget(mount))
				m = mount
			}
			mounts = append(mounts, m)
		}
		s.Volumes = mounts
	}

	var keys map[string]yaml.Node
	if err := value.Decode(&keys); err != nil {
		return err
	}
	// A field written with nothing after it (`volumes:` alone) decodes to
	// nothing: the list decoders above are not even called for a null, so the
	// service loaded as if the key were absent — and a `${VOLS}` that expanded
	// to nothing read the same way. docker compose refuses the shape
	// (`services.web.volumes must be a array`), except where it accepts the
	// bare key (bareKeyIsAllowed). Same here, in key order so the one named
	// does not depend on map iteration. Across -f
	// files this sees the merged document, where a later file's bare key has
	// already kept the earlier file's value (#732), so only a key no file gave
	// a value to arrives here.
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		// Decoded into a yaml.Node, an alias stays an alias (`volumes: *nada`),
		// so it is followed before the tag is read.
		n := *unalias(ptr(keys[k]))
		// The fields read straight into strings — a list of names, or one name
		// — take whatever YAML made of an item: `profiles: [42]` became the
		// profile "42" (and the service vanished from `config`, since no such
		// profile is ever active), `cap_add: [42]` the capability "42". docker
		// compose refuses these (`services.web.profiles.[0]: unexpected type
		// int`, `services.web.command.0 must be a string`); so does this —
		// the decoders have already run above and made text of the number,
		// so the raw node is what is read here.
		// The fields that take only a list: docker compose refuses `cap_add:
		// NET_ADMIN` (`must be a array`) where a bare name was read as a
		// one-item list here. Before the item check below, so that a bare
		// `cap_add: 42` is told "a list", not "a string" and then "a list".
		// A bare key is left to the bare-key check further down.
		if listOnlyFields[k] && n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
			return fmt.Errorf("%s must be a list, got a single value — write it as `- %s`", k, n.Value)
		}
		if stringListFields[k] {
			if err := refuseNonStringInList(k, &n); err != nil {
				return err
			}
		}
		// The fields that take one string: `image: 42` was read as the text
		// "42", where docker compose refuses it (`services.web.image must be
		// a string`).
		if stringFields[k] && n.Kind == yaml.ScalarNode {
			if word := nonStringWord(&n); word != "" {
				return fmt.Errorf("%s must be a string, got %s — quote it (`\"%s\"`) if it is meant literally", k, word, n.Value)
			}
		}
		// One level down — `build.context`, `deploy.resources.limits.memory` and
		// their neighbours — the same three readings apply: a bare key, a
		// number where a string belongs, a value where a mapping belongs.
		if err := refuseNestedShapes(k, n); err != nil {
			return err
		}
		// A watch rule's `ignore` items are strings, and a key the rule does
		// not have is listed with the other ignored fields rather than read
		// past (docker compose refuses it outright).
		if k == "develop" {
			if watch, ok := nestedNode(n, "watch"); ok && watch.Kind == yaml.SequenceNode {
				for j, item := range watch.Content {
					item = unalias(item)
					if item.Kind != yaml.MappingNode {
						continue
					}
					var rule map[string]yaml.Node
					if err := item.Decode(&rule); err != nil {
						return err
					}
					entry := fmt.Sprintf("develop.watch entry %d", j+1)
					// A rule needs a path: without one, or with an empty one,
					// it would watch the whole project directory. A bare
					// `path:` was named by the shape table above.
					if path, ok := rule["path"]; !ok {
						return fmt.Errorf("%s has no path — write the directory to watch, as in `path: ./src`", entry)
					} else if p := unalias(&path); p.Kind == yaml.ScalarNode && p.Tag == "!!str" && p.Value == "" {
						return fmt.Errorf("%s has an empty path — write the directory to watch, as in `path: ./src`", entry)
					}
					// The action is one of docker compose's five (it refuses any
					// other word, `SYNC` and `""` included); left out, the
					// watcher takes `sync`. The three that copy files need a
					// target — docker compose refuses those without one, and
					// `rebuild` and `restart` need none.
					action := "sync"
					if a, ok := rule["action"]; ok {
						if p := unalias(&a); p.Kind == yaml.ScalarNode && p.Tag == "!!str" {
							if !watchActions[p.Value] {
								// The word is not read back: the entry and the key locate it, and a
								// `${VAR}` there may hold anything.
								return fmt.Errorf("%s has an action that is not one — write `sync`, `rebuild`, `sync+restart`, `restart` or `sync+exec`", entry)
							}
							action = p.Value
						}
					}
					if watchActionCopies[action] {
						if tgt, ok := rule["target"]; !ok {
							return fmt.Errorf("%s has no target — action `%s` copies files into the container; write where, as in `target: /app`", entry, action)
						} else if p := unalias(&tgt); p.Kind == yaml.ScalarNode && p.Tag == "!!str" && p.Value == "" {
							return fmt.Errorf("%s has an empty target — action `%s` copies files into the container; write where, as in `target: /app`", entry, action)
						}
					}
					if ig, ok := rule["ignore"]; ok {
						if err := refuseNonStringInList(entry+".ignore", unalias(&ig)); err != nil {
							return err
						}
					}
					for key := range rule {
						if !watchRuleKeys[key] {
							s.Unsupported = append(s.Unsupported, fmt.Sprintf("%s.%s", entry, key))
						}
					}
				}
			}
		}
		// `healthcheck.test` is the same kind of field one level down. Decoded
		// into a map the way the service's own keys are, so a `<<:` merge key
		// and an aliased key are read the same way there.
		if k == "healthcheck" && n.Kind == yaml.MappingNode {
			var hc map[string]yaml.Node
			if err := n.Decode(&hc); err != nil {
				return err
			}
			if test, ok := hc["test"]; ok {
				if err := refuseNonStringInList("healthcheck.test", unalias(&test)); err != nil {
					return err
				}
			}
		}
		if serviceKnownKeys[k] && !bareKeyIsAllowed[k] && n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
			// A TypeError, so the load's shape-error wording frames it — and,
			// with several -f files, the note that the line counts in the
			// merged document.
			return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: %s: expected %s, got nothing — write the value or remove the key", n.Line, k, shapeOf(k))}}
		}
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
	// `networks` in its map form carries per-network settings (aliases,
	// ipv4_address, …) that opossum reads past — ServiceNetworks keeps only the
	// names. Each dropped key is named here as `networks.<net>.<key>`, so an
	// alias that never resolves was announced rather than discovered.
	if nets, ok := keys["networks"]; ok {
		s.Unsupported = append(s.Unsupported, ignoredServiceNetworkFields(nets)...)
	}
	if ext, ok := keys["extends"]; ok {
		s.Extends = decodeExtends(ext)
	}
	sort.Strings(s.Unsupported)
	return nil
}

// ExtendsRef is what a service's `extends:` names: the service to copy from,
// and the file it lives in when it is not this one.
type ExtendsRef struct {
	Service string
	File    string
}

// decodeExtends reads `extends:` in either of its forms — a bare service name,
// or a mapping with `service` and an optional `file`. A value of neither shape
// still marks the service as extending (that is what the key means), with
// nothing to name.
func decodeExtends(n yaml.Node) *ExtendsRef {
	// Decoding into a yaml.Node keeps an alias as an alias (`extends: *x`),
	// so follow it here — the decoder only resolves aliases for values it
	// decodes into Go types.
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = *n.Alias
	}
	ref := &ExtendsRef{}
	switch n.Kind {
	case yaml.ScalarNode:
		ref.Service = n.Value
	case yaml.MappingNode:
		var m struct {
			Service string `yaml:"service"`
			File    string `yaml:"file"`
		}
		if n.Decode(&m) == nil {
			ref.Service, ref.File = m.Service, m.File
		}
	}
	return ref
}

// ignoredServiceNetworkFields lists the keys under a service's map-form
// `networks:` entries, all of which are dropped (the entry's name is the only
// thing acted on). A list-form `networks:` carries no keys and yields nothing.
func ignoredServiceNetworkFields(n yaml.Node) []string {
	// Decoding into a yaml.Node keeps an alias as an alias, so `networks:
	// *nets` arrives here unresolved and would be reported as nothing.
	n = *unalias(&n)
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var decls map[string]map[string]yaml.Node
	if n.Decode(&decls) != nil {
		return nil
	}
	var out []string
	for net, fields := range decls {
		for k := range fields {
			if strings.HasPrefix(k, "x-") {
				continue
			}
			out = append(out, fmt.Sprintf("networks.%s.%s", net, k))
		}
	}
	return out
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

// nocopyMarker tags a mount whose `volume: {nocopy: true}` asked for seeding to
// be skipped. Same trick as tmpfsMarker: the flag belongs to the mount, but the
// parsed form of a mount is a single string, so it rides along as a prefix and
// Service.UnmarshalYAML lifts it off.
const nocopyMarker = "\x00nocopy\x00"

// cutMountOption removes one comma-separated option from a short-form mount's
// mode field, reporting whether it was there. `deps:/app:ro,nocopy` keeps the
// `ro` and loses the `nocopy`, because only the first is a mount mode — the
// runtime would reject or misread the other.
func cutMountOption(mount, opt string) (string, bool) {
	parts := strings.SplitN(mount, ":", 3)
	if len(parts) < 3 {
		return mount, false
	}
	var kept []string
	found := false
	for _, m := range strings.Split(parts[2], ",") {
		if strings.TrimSpace(m) == opt {
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return mount, false
	}
	out := parts[0] + ":" + parts[1]
	if len(kept) > 0 {
		out += ":" + strings.Join(kept, ",")
	}
	return out, true
}

// Ports is a service's published ports. Each entry is normalized to the short
// `[host_ip:]host:container[/proto]` string opossum's runtime understands, so
// both compose forms are accepted: the short scalar (`"8080:80"`, `"3000"`, or a
// bare number `3000`) and the long mapping (`{target, published, protocol,
// host_ip}`) that real compose files commonly use. A bare container port still
// gets a host port later (normalizePort), so the long and short forms converge.
type Ports []string

func (p *Ports) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("ports must be a list, got %s", kindName(value.Kind))
	}
	out := make(Ports, 0, len(value.Content))
	for i, item := range value.Content {
		item = unalias(item)
		// Short form: a string ("8080:80", "3000") or a bare number (3000) — the
		// scalar's text is the spec, normalized later.
		if item.Kind == yaml.ScalarNode {
			// Same as volumes: an empty item is not a port, and normalizing it
			// later would publish "" (or "null:null" after a merge).
			if item.ShortTag() == "!!null" || item.Value == "" {
				return fmt.Errorf("ports entry %d of %d is empty — write the port (`host:container`, or a mapping with `target:`) or remove the `- `", i+1, len(value.Content))
			}
			out = append(out, item.Value)
			continue
		}
		// Long form: {target, published, protocol, host_ip}. target/published are
		// scalarStr so a numeric `target: 80` (not quoted) decodes cleanly.
		var lf struct {
			Target    scalarStr `yaml:"target"`
			Published scalarStr `yaml:"published"`
			Protocol  string    `yaml:"protocol"`
			HostIP    string    `yaml:"host_ip"`
		}
		if err := item.Decode(&lf); err != nil {
			return err
		}
		target := string(lf.Target)
		if target == "" {
			return fmt.Errorf("port entry is missing a target")
		}
		pub := string(lf.Published)
		spec := target
		switch {
		case lf.HostIP != "":
			// A host_ip with no published port leaves the host port to the engine,
			// exactly like the short `ip::80`. Emit that form rather than mirroring
			// here, so the one place that decides host ports (normalizePort) also
			// records that opossum chose it — otherwise it would look user-written
			// and could never be moved off a busy port.
			host := lf.HostIP
			if strings.Contains(host, ":") {
				host = "[" + host + "]" // bracket an IPv6 host (e.g. ::1 -> [::1])
			}
			spec = host + ":" + pub + ":" + target // pub may be "" -> "ip::target"
		case pub != "":
			spec = pub + ":" + target
		}
		if lf.Protocol != "" {
			spec += "/" + lf.Protocol
		}
		out = append(out, spec)
	}
	*p = out
	return nil
}

// Volumes is a service's volume mounts. Each entry is normalized to the short
// `source:target[:ro]` string opossum's orchestrator already understands, so
// both compose forms are accepted: the short string (`./src:/dst`, `name:/dst`)
// and the long mapping (`{type, source, target, read_only}`) that real
// docker-compose files commonly use (#74). `type: tmpfs` entries are tagged with
// tmpfsMarker and later moved to Service.Tmpfs (#79).
type Volumes []string

func (v *Volumes) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("volumes must be a list, got %s", kindName(value.Kind))
	}
	out := make(Volumes, 0, len(value.Content))
	for i, item := range value.Content {
		item = unalias(item)
		if item.Kind == yaml.ScalarNode {
			// A dash with nothing after it (`- `, or `- null`, or a `${...}` that
			// expanded to nothing) is not a mount. Left in, it reaches the runtime as
			// a mount named "" or "null" — docker refuses it at validation, and so
			// does this. The count is in the reader's own numbering, from 1.
			if item.ShortTag() == "!!null" || item.Value == "" {
				return fmt.Errorf("volumes entry %d of %d is empty — write the mount (`./src:/target`, or a mapping with `target:`) or remove the `- `", i+1, len(value.Content))
			}
			if err := refuseNonString("volumes", i, len(value.Content), item); err != nil {
				return err
			}
			// `src:target:nocopy` is the short spelling of the same switch, and
			// docker accepts it. Left in place it would reach the runtime as a mount
			// mode, which is not what it is.
			if mount, ok := cutMountOption(item.Value, "nocopy"); ok {
				out = append(out, nocopyMarker+mount)
				continue
			}
			out = append(out, item.Value)
			continue
		}
		var lf struct {
			Type     string `yaml:"type"`
			Source   string `yaml:"source"`
			Target   string `yaml:"target"`
			ReadOnly bool   `yaml:"read_only"`
			Volume   struct {
				NoCopy bool `yaml:"nocopy"`
			} `yaml:"volume"`
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
			// The target, not the type: the type is what is wrong, but it can have
			// come from a `${...}` reference and the mount is what the reader has to
			// find. There are only three types, so naming them is the whole of what
			// they need to know about the value.
			return fmt.Errorf("the mount at %s has an unsupported type (only bind, volume, tmpfs)", lf.Target)
		}
		// No source is an anonymous volume (short form is just the target path).
		s := lf.Target
		if lf.Source != "" {
			s = lf.Source + ":" + lf.Target
		}
		if lf.ReadOnly {
			s += ":ro"
		}
		if lf.Volume.NoCopy {
			s = nocopyMarker + s
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

// UnmarshalYAML accepts `external` as a bool or as the older map form
// (`external: {name: x}`), the way docker compose still does (with a
// deprecation notice; measured on v5.4.0, which canonicalizes it to
// `name: x` + `external: true`). Older compose files carry the map form
// often, and reading it as a type error sent people to the wrong place.
func (d *VolumeDecl) UnmarshalYAML(value *yaml.Node) error {
	if err := wantMapping(value, "VolumeDecl"); err != nil {
		return err
	}
	var raw rawVolumeDecl
	if err := value.Decode(&raw); err != nil {
		return err
	}
	ext, name, err := decodeExternal(&raw.External)
	if err != nil {
		return err
	}
	d.External = ext
	d.Name, err = externalName(raw.Name, name, &raw.External)
	return err
}

// wantMapping reproduces the decoder's own refusal of a value that is not a
// mapping, naming the declaration the way the default decoder did — the raw
// shapes below are internal names, and a reader acts on "VolumeDecl", not on
// "rawVolumeDecl".
func wantMapping(value *yaml.Node, decl string) error {
	if value.Kind == yaml.MappingNode || value.Kind == 0 || (value.Kind == yaml.ScalarNode && value.Tag == "!!null") {
		return nil
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: cannot unmarshal %s into compose.%s", value.Line, value.ShortTag(), decl)}}
}

// externalName settles the name between an explicit `name:` and the map
// form's `external.name`. docker compose accepts both when they agree and
// refuses the file when they differ (measured on v5.4.0: "name and
// external.name conflict; only use name"), and so does this.
func externalName(explicit, fromExternal string, at *yaml.Node) (string, error) {
	switch {
	case fromExternal == "":
		return explicit, nil
	case explicit == "" || explicit == fromExternal:
		return fromExternal, nil
	}
	return "", &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: name %q and external.name %q conflict; only use name", at.Line, explicit, fromExternal)}}
}

// decodeExternal reads an `external:` value: absent or a bool, or the map form
// whose `name` names the pre-existing resource. Anything else is refused by
// shape, naming what was found, rather than left to the decoder's
// "cannot unmarshal !!map into bool".
func decodeExternal(n *yaml.Node) (external bool, name string, err error) {
	// The field was decoded into a yaml.Node, in which an alias stays an alias
	// (`external: *shared`), so it is followed here. A refusal of the value's
	// shape names the line the reader wrote the alias on; a refusal of a key
	// inside the mapping it stands for names that key's own line, since that
	// is where the key is written and fixed.
	at := n
	n = unalias(n)
	shape := func(got string) error {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: external: expected true/false or a mapping with name, got %s", at.Line, got)}}
	}
	switch n.Kind {
	case 0:
		return false, "", nil
	case yaml.ScalarNode:
		// `external:` with nothing after it is not "not external": docker
		// compose refuses it (`must be a boolean, mapping or string`), and a
		// bool decode of a null quietly says false, so it is caught first.
		if n.Tag == "!!null" {
			return false, "", shape("nothing")
		}
		var b bool
		if err := n.Decode(&b); err == nil {
			return b, "", nil
		}
		// A quoted "true"/"false" is a string to YAML and a bool to docker
		// compose (measured on v5.4.0); read it the same way.
		if b, perr := strconv.ParseBool(n.Value); perr == nil && n.Tag == "!!str" {
			return b, "", nil
		}
		return false, "", shape(fmt.Sprintf("%q", n.Value))
	case yaml.MappingNode:
		// Only `name` lives here. docker compose refuses any other key
		// ("additional properties … not allowed"), and so does this — a typo
		// for `name` would otherwise turn into a nameless external volume.
		// A key can be an alias too (`*k: x`), and `<<` brings a mapping's keys
		// in; the decode below resolves both, so the check reads the key as
		// decoded and leaves `<<` to it.
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := unalias(n.Content[i])
			if k := key.Value; k != "name" && key.Tag != "!!merge" {
				return false, "", &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: external: unknown key %q (only name is allowed here)", key.Line, k)}}
			}
		}
		var m struct {
			Name string `yaml:"name"`
		}
		if err := n.Decode(&m); err != nil {
			return false, "", err
		}
		return true, m.Name, nil
	}
	return false, "", shape(kindName(n.Kind))
}

// The declarations as they are read, with `external` kept as a node until
// decodeExternal has looked at its shape. Named — not anonymous structs —
// because a decode error names the type it failed to fill, and the reader
// acts on "VolumeDecl" where "struct { … }" says nothing.
type (
	rawVolumeDecl struct {
		External yaml.Node `yaml:"external"`
		Name     string    `yaml:"name"`
	}
	rawNetworkDecl struct {
		Internal bool      `yaml:"internal"`
		External yaml.Node `yaml:"external"`
		Name     string    `yaml:"name"`
	}
	rawSecret struct {
		File     string    `yaml:"file"`
		External yaml.Node `yaml:"external"`
	}
)

// NetworkDecl is a top-level `networks:` entry. opossum namespaces and creates a
// declared network per project (like the default), acting on:
//   - `internal: true` — a host-only network created with `container network
//     create --internal`: no internet egress, though the host stays reachable.
//     Put an untrusted workload here and force its egress through a host proxy
//     reachable via ${OPOSSUM_HOST_GATEWAY}. (Peers on an internal network can't
//     resolve each other by name — the DNS resolver is unreachable — so use IPs.)
//   - `external: true` / `name` — a pre-existing network, used by its real name
//     and never namespaced, created, or removed by opossum.
type NetworkDecl struct {
	Internal bool   `yaml:"internal"`
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

// UnmarshalYAML: see VolumeDecl — the map form of `external` is read the
// same way here.
func (d *NetworkDecl) UnmarshalYAML(value *yaml.Node) error {
	if err := wantMapping(value, "NetworkDecl"); err != nil {
		return err
	}
	var raw rawNetworkDecl
	if err := value.Decode(&raw); err != nil {
		return err
	}
	ext, name, err := decodeExternal(&raw.External)
	if err != nil {
		return err
	}
	d.Internal, d.External = raw.Internal, ext
	d.Name, err = externalName(raw.Name, name, &raw.External)
	return err
}

// ServiceNetworks is a service's `networks:` — the declared networks it joins.
// Both the list form (`[backend]`) and the map form (`{backend: {aliases: […]}}`,
// keys only — aliases aren't acted on) decode to the network names.
type ServiceNetworks []string

func (n *ServiceNetworks) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// A null/empty `networks:` value means no networks (not an error).
		if value.Tag == "!!null" || value.Value == "" {
			*n = nil
			return nil
		}
	case yaml.SequenceNode:
		// docker compose validates the list form: an item with nothing in it is
		// not a name, and a name listed twice is an error (`items at 0 and 1 are
		// equal`), not two `--network` flags. Items are decoded one by one —
		// decoding the whole list into []string drops a null item, which is
		// how a bare `- ` used to vanish — and the decoded values are compared,
		// not the nodes: an alias (`*n`) carries its anchor's name in the node
		// and the network's name only once decoded. Entries are counted from
		// 1, as the reader counts them.
		//
		// This sees a list only when exactly one -f file wrote the service's
		// `networks:`. As soon as two files write it — the same names or
		// different ones — the merge reads both sides as maps keyed by name and
		// joins them (#720), so a duplicate inside either file is absorbed
		// there and never reaches this check; docker compose validates each
		// file first and refuses it. That divergence is pinned in the tests.
		out := make([]string, 0, len(value.Content))
		for i, item := range value.Content {
			if err := refuseNonString("networks", i, len(value.Content), unalias(item)); err != nil {
				return err
			}
			var name string
			if item.ShortTag() != "!!null" {
				if err := item.Decode(&name); err != nil {
					return err
				}
			}
			out = append(out, name)
		}
		if err := refuseEmptyOrRepeatedNetwork(out); err != nil {
			return err
		}
		*n = out
		return nil
	case yaml.MappingNode:
		// Keys are the network names; the values (aliases, ipv4_address, …) aren't
		// acted on. Sort so the parse is deterministic. The YAML decoder never
		// sees these keys (the node is walked by hand), so a key written twice
		// is caught here, as it is in the list form.
		out := make([]string, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			key := unalias(value.Content[i])
			if word := nonStringWord(key); word != "" {
				return fmt.Errorf("networks entry %d of %d must be a string, got %s — quote the name (`\"%s\"`) if it is meant literally", i/2+1, len(value.Content)/2, word, key.Value)
			}
			var name string
			if err := key.Decode(&name); err != nil {
				return err
			}
			out = append(out, name)
		}
		if err := refuseEmptyOrRepeatedNetwork(out); err != nil {
			return err
		}
		sort.Strings(out)
		*n = out
		return nil
	}
	return fmt.Errorf("expected a list or a mapping for networks, got %s", kindName(value.Kind))
}

// refuseNonString is the check docker compose applies to the items of a
// service's volumes, networks, env_file and environment lists: an item that
// YAML read as something other than a string (`- 42`, `- true`, `- 1.5`) is
// refused (`services.web.volumes.0 must be a string`), where reading it as
// the text it was written as would quietly make a mount, a network or a
// variable out of a number. Quoting it makes it a string. Ports keep taking
// bare numbers, and secrets bare names of any spelling, as docker does.
func refuseNonString(field string, i, total int, item *yaml.Node) error {
	if word := nonStringWord(item); word != "" {
		return fmt.Errorf("%s entry %d of %d must be a string, got %s — quote it (`\"%s\"`) if it is meant literally", field, i+1, total, word, item.Value)
	}
	return nil
}

// nonStringWord says, for a scalar YAML read as one of the types docker
// compose refuses where a string belongs — a number, true/false, a date —
// what it read it as; "" for a string, for the empty item (refused as such
// by the callers that have a word for it), for a mapping (read by its own
// decoder), and for a tag docker compose takes as text (`!!binary`, a
// custom `!tag`).
func nonStringWord(item *yaml.Node) string {
	if item.Kind != yaml.ScalarNode {
		return ""
	}
	switch item.ShortTag() {
	case "!!int", "!!float":
		return "a number"
	case "!!bool":
		return "true/false"
	case "!!timestamp":
		return "a date"
	}
	return ""
}

// unalias follows a YAML alias (`*name`) to the node it stands for. The
// decoder resolves an alias before it hands a value to UnmarshalYAML, and
// again inside item.Decode — but the items of a list it hands over are the
// raw nodes, so a decoder that walks value.Content and branches on an
// item's Kind sees `*name` as an AliasNode: not the scalar it stands for,
// so an alias to a string fell through to the mapping branch and failed
// there (#745). An alias to a mapping already reached Decode and worked.
func unalias(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// refuseEmptyOrRepeatedNetwork is the validation docker compose applies to a
// service's networks, in either form: no empty name, no name twice.
func refuseEmptyOrRepeatedNetwork(names []string) error {
	seen := map[string]int{}
	for i, name := range names {
		if name == "" {
			return fmt.Errorf("networks entry %d of %d is empty — write the network's name or remove the `- `", i+1, len(names))
		}
		if first, dup := seen[name]; dup {
			return fmt.Errorf("networks lists %q twice (entries %d and %d) — list each network once", name, first, i+1)
		}
		seen[name] = i + 1
	}
	return nil
}

// Secret is a top-level compose secret. opossum supports only file-based
// secrets (the common `_FILE` pattern of official images); `external` secrets
// are not resolved (#76).
type Secret struct {
	File     string `yaml:"file"`
	External bool   `yaml:"external"`
}

// UnmarshalYAML: the map form of `external` marks the secret external, so the
// load refuses it as an external secret (which it is) rather than as a type
// error.
func (s *Secret) UnmarshalYAML(value *yaml.Node) error {
	if err := wantMapping(value, "Secret"); err != nil {
		return err
	}
	var raw rawSecret
	if err := value.Decode(&raw); err != nil {
		return err
	}
	ext, _, err := decodeExternal(&raw.External)
	if err != nil {
		return err
	}
	s.File, s.External = raw.File, ext
	return nil
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
		return fmt.Errorf("secrets must be a list, got %s", kindName(value.Kind))
	}
	out := make(SecretRefs, 0, len(value.Content))
	for i, item := range value.Content {
		item = unalias(item)
		if item.Kind == yaml.ScalarNode {
			if err := refuseNonString("secrets", i, len(value.Content), item); err != nil {
				return err
			}
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
			// Both `command:` and `entrypoint:` are read by this, and which one is
			// being read is not passed in — the message used to say "command" for
			// either, which sent anyone with a bad entrypoint to look at the wrong
			// line. Naming both is true and narrows it to two.
			// No name here: this is handed the value without being told which key it
			// came from, and the one place that can tell — after the decode has failed,
			// by re-reading each key on its own — puts it back on.
			return err
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
	// Both fields again: this is the same code reading either of them, six lines
	// below the other place that used to name only one. Naming one there and not
	// here would have left half the fix in.
	// Same: named from the outside, or not at all.
	return fmt.Errorf("expected a string or a list, got %s", kindName(value.Kind))
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
		if word := nonStringWord(value); word != "" {
			return fmt.Errorf("env_file must be a string, got %s — quote it (`\"%s\"`) if it is meant literally", word, value.Value)
		}
		*e = EnvFiles{{Path: value.Value, Required: true}}
		return nil
	case yaml.SequenceNode:
		out := make(EnvFiles, 0, len(value.Content))
		for i, item := range value.Content {
			item = unalias(item)
			if item.Kind == yaml.ScalarNode {
				if err := refuseNonString("env_file", i, len(value.Content), item); err != nil {
					return err
				}
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
	return fmt.Errorf("expected a string or a list for env_file, got %s", kindName(value.Kind))
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
	return fmt.Errorf("expected a string or a list, got %s", kindName(value.Kind))
}

// ResolvedEnv is the service's environment with its env_file entries folded
// in, or the reason they could not be. Everything that renders a service or
// starts one goes through here rather than reading Environment directly: on a
// failure Environment holds the declared entries alone, and handing those to a
// container would start it with an environment nobody wrote.
//
// What this does not do is make that impossible. A path that reads Environment
// without coming through here sees the declared entries and no error. Five
// places ask here today — rendering (`config`), the two that start a container
// (`Up`, `RunOneOff`), the audit that wraps one (`RunAudited`), and the
// overlay planner, which starts nothing but writes a file out of what it
// concludes. Two of those were found by review rather than by looking, which
// is the measure of how much this list is worth.
//
// Making the field unreachable instead — so that no answer is possible without
// the error — is the change that would make it structural. Environment appears
// on 57 lines (60 occurrences: the two counts differ, and mixing them is how
// the first version of this sentence got its number wrong). 43 of those lines
// are in this package, 40 of them in its tests; outside the package there are
// 14 lines and 5 non-test reads. So the work is not the 57, and it is not the
// 5 either: it is unexporting a field this package's own tests read on 40
// lines. Still a different change than this one.
func (s *Service) ResolvedEnv() (Environment, error) {
	if s.envFileErr != nil {
		return nil, s.envFileErr
	}
	return s.Environment, nil
}

// Environment normalizes both the list form (KEY=value) and the map form
// (KEY: value) into a sorted []string of KEY=value entries. A null map value
// becomes a bare KEY (pass-through from the host environment).
type Environment []string

func (e *Environment) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		// Item by item: decoding the list into []string would turn `- 42`
		// into the variable "42" and drop a `- ` altogether, where docker
		// compose refuses both.
		out := make([]string, 0, len(value.Content))
		for i, item := range value.Content {
			item = unalias(item)
			if item.ShortTag() == "!!null" {
				return fmt.Errorf("environment entry %d of %d is empty — write the variable (`KEY=value`, or `KEY` to take it from the shell) or remove the `- `", i+1, len(value.Content))
			}
			if err := refuseNonString("environment", i, len(value.Content), item); err != nil {
				return err
			}
			var s string
			if err := item.Decode(&s); err != nil {
				return err
			}
			out = append(out, s)
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
	return fmt.Errorf("expected a list or a mapping for environment, got %s", kindName(value.Kind))
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
	return fmt.Errorf("expected a list or a mapping for depends_on, got %s", kindName(value.Kind))
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
	Disabled    bool          // switched off: `test: ["NONE"]` or `disable: true`
}

// UnmarshalYAML parses the compose healthcheck schema, normalizing `test` into
// an argv suitable for `container exec` and applying compose's defaults.
func (h *Healthcheck) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Test        StringOrSlice `yaml:"test"`
		Interval    yaml.Node     `yaml:"interval"`
		Timeout     yaml.Node     `yaml:"timeout"`
		Retries     yaml.Node     `yaml:"retries"`
		StartPeriod yaml.Node     `yaml:"start_period"`
		Disable     bool          `yaml:"disable"`
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

	// `disable: true` is the spec's other spelling of `test: ["NONE"]`, and it wins
	// over any test given alongside it — that is what asking for the check to be off
	// means. Reading only the `NONE` spelling left the other one doing nothing at all:
	// the check stayed live, a `service_healthy` dependant waited on it, and nothing
	// said why the line had no effect.
	if raw.Disable {
		h.Disabled = true
		h.Test = nil
	}

	var err error
	if h.Interval, err = durationOf(&raw.Interval, 30*time.Second); err != nil {
		return fmt.Errorf("healthcheck interval: %w", err)
	}
	if h.Timeout, err = durationOf(&raw.Timeout, 30*time.Second); err != nil {
		return fmt.Errorf("healthcheck timeout: %w", err)
	}
	if h.StartPeriod, err = durationOf(&raw.StartPeriod, 0); err != nil {
		return fmt.Errorf("healthcheck start_period: %w", err)
	}
	// A count, the way docker compose reads one: a bare number (`3`, or
	// `2.5`, which it truncates) or a quoted whole number (`"3"`); a quoted
	// decimal, a word, a blank, a boolean, a negative — not a count. Left
	// out (or null, or 0) it is the default.
	if n, err := retriesCount(unalias(&raw.Retries)); err != nil {
		return err
	} else if n > 0 {
		h.Retries = n
	}
	if h.Retries <= 0 {
		h.Retries = 3
	}
	return nil
}

// durationOf reads a healthcheck duration: the default when the key was
// left out (or written bare — the shape check names that one), the text
// as a duration otherwise. A blank (`""`, or what an unset `${VAR}`
// leaves) is not a duration, as docker compose reads it (`invalid duration
// ""`); it used to be read as the default. A mapping or a list is left to
// the shape check, which names the field and what belongs there.
func durationOf(n *yaml.Node, def time.Duration) (time.Duration, error) {
	n = unalias(n)
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return def, nil
	}
	if strings.TrimSpace(n.Value) == "" {
		return 0, fmt.Errorf("blank, not a duration — use a unit, e.g. 30s, 1m, or 500ms (an unset `${VAR}` leaves a blank)")
	}
	d, err := time.ParseDuration(n.Value)
	if err != nil {
		return 0, fmt.Errorf("not a duration — use a unit, e.g. 30s, 1m, or 500ms")
	}
	return d, nil
}

// kindName says what the parser found in words, rather than by the number YAML's
// own decoder happens to use for it. A reader who wrote a mapping where a list
// belongs cannot act on "yaml kind 4".
//
// There is no case for an alias: the decoder resolves one before it hands a
// value to a Go type, so `command: *anchor` arrives as whatever the anchor
// held (measured). A value kept as a yaml.Node — a list's items, a field read
// into yaml.Node — still carries the alias: a reader that walks such a node's
// Kind, Value or Content itself unaliases first, and one that hands the node
// to Decode need not.
func kindName(k yaml.Kind) string {
	switch k {
	case yaml.ScalarNode:
		return "a single value"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.DocumentNode:
		return "a document"
	}
	return "something else"
}

// ptr hands back a pointer to a copy: the map[string]yaml.Node the second
// pass decodes into is not addressable.
func ptr(n yaml.Node) *yaml.Node { return &n }
