package compose

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
func ignoredTopLevel(doc interpolated) []string {
	var top map[string]yaml.Node
	if err := doc.into(&top); err != nil {
		return nil
	}
	var out []string
	for k := range top {
		switch {
		// `volumes` is acted on (declarations drive external: true and a volume's
		// real `name:`), so reporting the whole key as ignored was wrong — and noisy,
		// since almost every real project declares named volumes. The fields inside a
		// declaration that opossum *doesn't* act on are reported individually below,
		// so nothing is silently dropped. `networks` goes the same way: acted on
		// for external/name/internal, with the rest reported per field.
		case k == "name" || k == "services" || k == "version" || k == "secrets" || k == "networks" || k == "volumes":
		case strings.HasPrefix(k, "x-"):
		default:
			out = append(out, k)
		}
	}
	out = append(out, ignoredVolumeFields(top["volumes"])...)
	out = append(out, ignoredNetworkFields(top["networks"])...)
	out = append(out, ignoredSecretFields(top["secrets"])...)
	sort.Strings(out)
	return out
}

// liftExternalNames rewrites, in one file's tree before the merge, the map
// form of a declaration's `external` (`external: {name: x}`) into what docker
// compose reads it as: `external: true` with the name as the declaration's
// own `name:`. Merged as written, a later file's `external: true` replaced
// the whole map and the name went with it, where docker compose keeps
// `name: x` (measured against v5.5.0; a later `external: false` keeps the
// name too, a later `name:` wins, and a later map wins only where no
// earlier file gave the declaration a name — where one did, and the names
// differ, docker compose refuses the pair as a conflict, and so does this,
// naming the file). The file has been checked on its own already, so a
// `name:` in it that conflicts with its own map has been refused, and one
// that agrees is the same value written twice. Volumes and networks;
// a secret's name is read by nothing here (an external secret is refused
// where a service uses it), so a secret's map is left as it is. The lift
// shortens the merged document by a line per map, which the line a
// merged-document refusal names counts in.
func liftExternalNames(path string, doc, earlier map[string]any) error {
	for _, kind := range []string{"volumes", "networks"} {
		decls, ok := doc[kind].(map[string]any)
		if !ok {
			continue
		}
		for declName, d := range decls {
			decl, ok := d.(map[string]any)
			if !ok {
				continue
			}
			ext, ok := decl["external"].(map[string]any)
			if !ok {
				continue
			}
			if name, ok := ext["name"].(string); ok && name != "" {
				if existing := earlierDeclName(earlier, kind, declName); existing != "" && existing != name {
					return fmt.Errorf("compose file %s: %s.%s: name %q and external.name %q conflict; only use name", path, kind, declName, existing, name)
				}
				decl["name"] = name
			}
			decl["external"] = true
		}
	}
	return nil
}

// earlierDeclName is the `name:` the earlier files' merge gave the
// declaration kind.declName, or "" when none did.
func earlierDeclName(earlier map[string]any, kind, declName string) string {
	decls, _ := earlier[kind].(map[string]any)
	decl, _ := decls[declName].(map[string]any)
	name, _ := decl["name"].(string)
	return name
}

// networkDeclFields are the per-network keys opossum acts on. Anything else in
// a declaration (ipam, driver, driver_opts, labels, attachable, enable_ipv6) is
// parsed and dropped, and used to be dropped without a word — a project that
// pins a subnet under `ipam` got a plain project network and nothing said so.
var networkDeclFields = map[string]bool{"external": true, "name": true, "internal": true}

// ignoredNetworkFields reports unacted-on keys inside top-level network
// declarations as `networks.<net>.<key>`, the way ignoredVolumeFields does for
// volumes: the same kind of silence, now with the same voice.
func ignoredNetworkFields(node yaml.Node) []string {
	var decls map[string]map[string]yaml.Node
	if node.IsZero() || node.Decode(&decls) != nil {
		return nil
	}
	var out []string
	for net, fields := range decls {
		for k := range fields {
			if networkDeclFields[k] || strings.HasPrefix(k, "x-") {
				continue
			}
			out = append(out, fmt.Sprintf("networks.%s.%s", net, k))
		}
	}
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

// secretDeclFields are the per-secret keys opossum acts on (`external` by
// refusing it). Anything else (`name`, `labels`, `driver`) is parsed and
// dropped, and used to be dropped without a word.
var secretDeclFields = map[string]bool{"file": true, "external": true}

// ignoredSecretFields reports unacted-on keys inside top-level secret
// declarations as `secrets.<secret>.<key>`, as the two above do.
func ignoredSecretFields(node yaml.Node) []string {
	var decls map[string]map[string]yaml.Node
	if node.IsZero() || node.Decode(&decls) != nil {
		return nil
	}
	var out []string
	for sec, fields := range decls {
		for k := range fields {
			if secretDeclFields[k] || strings.HasPrefix(k, "x-") {
				continue
			}
			out = append(out, fmt.Sprintf("secrets.%s.%s", sec, k))
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
func mergeMap(base, over map[string]any, path string) map[string]any {
	for k, ov := range over {
		if bv, ok := base[k]; ok {
			base[k] = mergeValue(bv, ov, k, path)
		} else {
			base[k] = ov
		}
	}
	return base
}

// collections are the top-level mappings whose keys are names the file's
// author chose — a service, a network, a volume — rather than fields. A key
// directly under one of them is an element, so a service called `networks` or
// `environment` must not be merged the way the field of that name is; the
// special cases below look at the key and the path it sits at, not the key
// alone. Docker compose branches on the full path (services.*.networks) for
// the same reason.
var collections = map[string]bool{"services": true}

// nullReadsAsUnsetIn are the mappings inside which a key with nothing after
// it is a value (unset / empty) rather than "not given": a service's
// environment and labels, and its build.args.
var nullReadsAsUnsetIn = map[string]bool{"environment": true, "labels": true, "args": true}

// nullIsAValue reports whether a null at key under path is read as a value
// that wins over the base (see mergeValue). The two shapes: a service's
// command/entrypoint (key is the field, path is the service — not a
// collection), and a variable inside one of nullReadsAsUnsetIn (path ends in
// that mapping's name, and the mapping is a field, not an element named like
// one: its own parent is not a collection).
func nullIsAValue(key, path string) bool {
	if (key == "command" || key == "entrypoint") && !collections[path] {
		return true
	}
	return insideEnvLike(path)
}

// insideEnvLike reports whether path is an environment / labels / build.args
// mapping — a place whose keys are variable names the author chose, not
// fields. A service that happens to be called `environment` is not one: it
// is an element of a collection, and its own `environment:` field sits one
// level further down.
func insideEnvLike(path string) bool {
	return nullReadsAsUnsetIn[lastSegment(path)] && !collections[parentPath(path)]
}

// parentPath is the dotted path one level up ("" at the root or one below it).
func parentPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	return ""
}

// lastSegment is the key a dotted path ends in ("" for the document root).
func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// childPath is the dotted path of key under path ("" is the document root).
func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// replaceSeqKeys are sequence fields that represent a single value, so an override
// replaces rather than appends them (docker compose parity).
var replaceSeqKeys = map[string]bool{"command": true, "entrypoint": true, "test": true}

// envLikeKeys accept either a `KEY: value` map or a `- KEY=value` list; both merge
// by key (later wins), so a base and override merge per variable regardless of form.
// `build.args` is the third: its two forms merge by variable the same way
// (measured against docker compose v5.5.0 — a list in one file and a
// mapping in the next keep every variable, the later file winning by name).
var envLikeKeys = map[string]bool{"environment": true, "labels": true, "args": true}

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
func mergeValue(base, over any, key string, path string) any {
	if over == nil {
		// A key with nothing after it (`working_dir:`, `ports:`, `internal:`)
		// is "not given" to docker compose: the base's value stands, whatever
		// its kind — scalar, list, mapping, a declaration's field (measured
		// against docker compose v5.5.0). Two places read a null as a value
		// instead, and win with it: `command:`/`entrypoint:` mean "no command",
		// and a variable inside environment or build.args means "unset" (taken
		// from the shell at run time, as a bare `A` in the list form does;
		// inside labels it means the empty string). Both are fields of a
		// service, so neither applies to a *service* that happens to be called
		// `command` or `environment` — an element of a collection is named by
		// its author, and the path says which it is (see collections).
		if nullIsAValue(key, path) {
			return over
		}
		return base
	}
	// The env-like rule must stay on for a *service* called `environment`
	// (its own `environment:` field still merges by variable — two list forms
	// with the same variable must collapse to one) and go off for a
	// *variable* called `environment` (or `labels`) inside such a field:
	// `environment: { environment: prod }` is a common naming, and both files
	// setting it would send the two scalars through toEnvMap, which reads a
	// scalar as an empty mapping — the variable came out as
	// `environment=map[]`. insideEnvLike tells the two apart by the path: the
	// `!collections[parentPath]` term in it is what keeps the rule on for the
	// service (its parent is `services`), and the lastSegment term is what
	// turns it off inside the field. Inside an env-like mapping every key is
	// a variable name, so the scalars merge as scalars there (later wins), as
	// docker compose reads them (measured against v5.5.0). The list rules
	// below (command, ports, volumes) need no guard — they apply to two
	// lists, and an element is never one.
	if envLikeKeys[key] && !insideEnvLike(path) {
		// A list item that is not a variable — a bare number, a `- ` — has
		// no name to merge by, and toEnvMap would drop it: the merged file
		// would then pass where the file alone is refused (docker compose
		// refuses both). So an unclean side is handed on as it is, for the
		// decode of the merged document to refuse; the base first, since
		// its item is the earlier one.
		if !envListIsClean(base) {
			return base
		}
		if !envListIsClean(over) {
			return over
		}
		return mergeMap(toEnvMap(base), toEnvMap(over), childPath(path, key))
	}
	// The networks rule does: its "an empty entry keeps the other side" would
	// otherwise apply to a *service* called `networks`, where a null override
	// must win as it does for any service field.
	if key == "networks" && !collections[path] {
		// A service's `networks:` comes in two forms — a list of names, or a map
		// keyed by name whose values carry aliases and addresses. docker compose
		// reads the list as a map with empty entries before merging, so a file
		// that restates the list form over one that used the map form keeps the
		// map's entries (aliases included) instead of replacing them, and a name
		// both files list is one network, not two. An empty entry in the map
		// form (`back:` with nothing under it) is read the same way (measured
		// against docker compose v5.4.0). Do the same: read both sides as maps
		// and merge by name, keeping the other side's settings where one side
		// wrote only the name.
		if b, ok := networksAsMap(base); ok {
			if o, ok := networksAsMap(over); ok {
				return mergeNetworks(b, o, childPath(path, key))
			}
		}
	}
	// A service's `depends_on:` comes in two forms — a list of names, or a
	// mapping keyed by name whose values carry the condition. docker
	// compose reads the list as `{name: {condition: service_started}}`
	// before merging (measured against v5.5.0), so a later file's mapping
	// that writes `condition:` with nothing after it keeps what the list
	// gave. Read both sides that way. Not for a *service* called
	// `depends_on`.
	if key == "depends_on" && !collections[path] {
		if b, ok := dependsOnAsMap(base); ok {
			if o, ok := dependsOnAsMap(over); ok {
				return mergeMap(b, o, childPath(path, key))
			}
		}
	}
	// A service's `build:` comes in two forms — a context path, or a mapping
	// with `context` among its keys. docker compose reads the path as
	// `{context: <path>}` before merging (measured against v5.5.0), so a
	// file that writes the short form over the long one changes only the
	// context, and one that writes the long form over the short one keeps
	// the context. Read both sides that way. Not for a *service* called
	// `build` (an element of a collection, and a scalar there is a service
	// that is not a mapping, for the decode to refuse), and not for a
	// *variable* called `build` inside environment, labels or build.args —
	// a value there is the variable's, and two strings merge as strings.
	if key == "build" && !collections[path] && !insideEnvLike(path) {
		if b, ok := buildAsMap(base); ok {
			if o, ok := buildAsMap(over); ok {
				return mergeMap(b, o, childPath(path, key))
			}
		}
	}
	switch o := over.(type) {
	case map[string]any:
		if b, ok := base.(map[string]any); ok {
			return mergeMap(b, o, childPath(path, key))
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

// dependsOnAsMap reads a `depends_on:` value in either form as the mapping
// form: a list of names becomes `{name: {condition: service_started}}` (the
// entry docker compose makes of a listed name), a mapping is returned as
// is. Anything else — a scalar, a list holding something that is not a
// name — is not a value this can read, and ok is false so the caller falls
// back to the ordinary merge (and the decode names it).
func dependsOnAsMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case []any:
		out := make(map[string]any, len(x))
		for _, item := range x {
			name, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[name] = map[string]any{"condition": ConditionStarted}
		}
		return out, true
	}
	return nil, false
}

// buildAsMap reads a `build:` value in either form as the mapping form: a
// context path becomes `{context: <path>}`, a mapping is returned as is.
// Anything else — a number, a list — is not a build value this can read,
// and ok is false so the caller falls back to the ordinary merge: written
// in the later file it reaches the shape check, which names it; written
// in the earlier one it is replaced whole, as any value is.
func buildAsMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case string:
		return map[string]any{"context": x}, true
	}
	return nil, false
}

// networksAsMap reads a `networks:` value in either form as the map form: a
// list of names becomes a map of those names to null (the entry docker compose
// makes of a listed name), a map is returned as is. Anything else — a list
// holding something that is not a name, a scalar — is not a networks value this
// can read, and ok is false so the caller falls back to the ordinary merge.
func networksAsMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case []any:
		out := make(map[string]any, len(x))
		for _, e := range x {
			name, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[name] = nil
		}
		return out, true
	}
	return nil, false
}

// mergeNetworks is mergeMap for two map-form `networks:` values, with one
// difference: an entry that is empty on one side (a name the list form gave,
// or `back:` with nothing under it) keeps whatever the other side wrote for
// that name. A plain mergeMap would let the empty side win and drop the
// aliases the other file set — the loss this road exists to avoid.
func mergeNetworks(base, over map[string]any, path string) map[string]any {
	for k, ov := range over {
		if bv, had := base[k]; had {
			// mergeValue keeps the base entry when ov is nil — a name the list
			// form gave, or `back:` with nothing under it — so the aliases the
			// other file set survive; see there.
			base[k] = mergeValue(bv, ov, k, path)
		} else {
			base[k] = ov
		}
	}
	return base
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

// envListIsClean reports whether an env-like value can be merged by name: a
// mapping always can; a list can when every item is a string (a `KEY=value`
// or a bare `KEY`). A number, a boolean or a null item has no name.
func envListIsClean(v any) bool {
	items, ok := v.([]any)
	if !ok {
		return true
	}
	for _, item := range items {
		if _, isString := item.(string); !isString {
			return false
		}
	}
	return true
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

	var doc interpolated
	// The files the project was read from: the -f paths, with the files
	// each includes before it. What a failure in the merged document names.
	loaded := paths
	if len(paths) == 1 && !hasInclude(paths[0]) {
		// Single file: read the interpolated document directly (no merge
		// round-trip), so the positions a failure names are the ones in the file.
		raw, err := os.ReadFile(paths[0])
		if err != nil {
			return nil, fmt.Errorf("reading compose file: %w", err)
		}
		if doc, err = interpolateDocument(raw, scope.lookup()); err != nil {
			return nil, fmt.Errorf("interpolating %s: %w", paths[0], err)
		}
		// The file is read as written first — every shape check names the
		// service and the line the reader wrote — and only then is a
		// service that extends another resolved, on the plain tree. The
		// resolved tree decodes as the file did, but for a key that is
		// still nothing once extends is read, which the resolver refuses.
		if err := validateOne(paths[0], doc, nil); err != nil {
			return nil, err
		}
		if resolved, err := resolveSameFileExtends(doc, paths[0], baseDir, scope.lookup()); err != nil {
			return nil, err
		} else if resolved != nil {
			doc = *resolved
		}
	} else {
		// Several files, or one with `include:`: merge their YAML trees,
		// then render the merged result.
		var merged map[string]any
		loaded = nil
		for _, path := range paths {
			// Every -f file belongs to the project whose directory is the
			// first file's: its include paths and its `extends: {file}`
			// count from there (docker compose, measured), not from its own.
			m, files, err := loadUnit(path, baseDir, scope, merged, nil)
			if err != nil {
				return nil, err
			}
			loaded = append(loaded, files...)
			if merged == nil {
				merged = m
			} else {
				merged = mergeMap(merged, m, "")
				// What the file adds is checked in what it made of the
				// earlier files too, as docker compose checks each file's
				// merge: a key this file writes with nothing after it and
				// no earlier file gave a value to — a new network's
				// `internal:`, a `build.context:` no base has — is nothing
				// in the result, and refused naming this file.
				if err := validateMerged(path, merged, asMerged); err != nil {
					return nil, err
				}
			}
		}
		// Merging builds a new tree, so there are no positions to keep: the files
		// are one document by the time this is decoded, and a failure here names a
		// line in THAT document. Which file a value came from is not recorded, so
		// the message names them all and says the line is a merged one rather than
		// picking one and being wrong. The per-file pass above is the part that
		// reads each file as the reader wrote it, and it only sees what a plain
		// map can hold.
		data, err := yaml.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("merging compose files: %w", err)
		}
		doc = interpolated{raw: data}
	}

	var f composeFile
	if err := doc.into(&f); err != nil {
		read := asWritten
		if len(loaded) > 1 {
			read = asMerged
		}
		return nil, decodeErr(mergedName(loaded), read, blameService(doc, err))
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("%s defines no services — add a top-level `services:` block with at least one service", mergedName(loaded))
	}
	// A service key with nothing under it (`web:` alone, or `web: ~`) decodes to
	// a nil service. Every reader below this line dereferences it, so it is
	// refused here, in the words docker compose uses for the same file. With
	// several files this only fires when no file gave the service a body — an
	// override that writes the bare key keeps the earlier file's service.
	names := make([]string, 0, len(f.Services))
	for name := range f.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if f.Services[name] == nil {
			return nil, fmt.Errorf("service %q must be a mapping — the key has nothing under it; give it at least `image:` or `build:`, or remove the key", name)
		}
	}

	p := &Project{
		Name:        f.Name,
		BaseDir:     baseDir,
		Services:    f.Services,
		Secrets:     f.Secrets,
		Volumes:     f.Volumes,
		Networks:    f.Networks,
		Unsupported: ignoredTopLevel(doc),
	}
	// A declared network can't be both host-only (internal) and external: an
	// external network is used as-is, so `internal` would be silently dropped —
	// and with it the egress guarantee a caller likely set `internal` to get.
	for name, decl := range f.Networks {
		if decl.Internal && decl.External {
			return nil, fmt.Errorf("network %q: internal and external cannot both be set (an external network is used as-is)", name)
		}
	}
	// In name order: with two services at fault the one reported must not
	// depend on map iteration, or the same file names a different service
	// on each run.
	for _, name := range names {
		svc := f.Services[name]
		svc.Name = name
		// Before the image check: a service that extends another usually has
		// no image of its own, and "must set image" would send the reader to
		// look for a typo that is not there. Refused rather than ignored —
		// with `extends:` dropped, the service would run with a fraction of
		// its settings, and `config` shows the key's name but not what it
		// would have brought in.
		if svc.Extends != nil {
			switch {
			case svc.Extends.Service == "":
				// Nothing, `{}`, a list, or `file:` alone (docker compose:
				// `extends.web.service is required`).
				return nil, fmt.Errorf("service %q uses extends:%s, which names no service — write the service of this file to extend (`extends: base`, or `extends: {service: base}`), or remove extends:", name, svc.Extends.describe())
			case svc.Extends.File == "":
				// A service of this file is resolved before this point, so
				// what reaches here wrote `file:` with nothing after it.
				return nil, fmt.Errorf("service %q uses extends:%s with a `file:` that names no file — remove file: to extend a service of this file, name the file, or remove extends:", name, svc.Extends.describe())
			}
			return nil, fmt.Errorf("service %q uses extends:%s, which could not be read as a service of another file — write `extends: {file: <path>, service: <name>}` with the path as text, or remove extends: (extends within the same file and from another file is read)", name, svc.Extends.describe())
		}
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
		// A failure is recorded on the service rather than failing the load: the
		// project is not broken by a service nobody is running, and whether this
		// service is one of those is not knowable here — profiles are applied a
		// layer up. Whoever renders or starts it asks through ResolvedEnv, which
		// answers with this error.
		env, err := resolveEnvFiles(baseDir, svc.EnvFile, svc.Environment, scope)
		if err != nil {
			svc.envFileErr = fmt.Errorf("service %q: %w", name, err)
		} else {
			svc.Environment = env
		}

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
				// The dependency, not the condition it was given: there are three
				// conditions and they are all named here, so the value adds nothing —
				// and it can have come from a `${...}` reference.
				return fmt.Errorf("service %q: unsupported depends_on condition for %q — use service_started, service_healthy, or service_completed_successfully", name, dep.Name)
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

// decodeErr turns a failed YAML decode into words that send the reader to the
// right place. Four different things arrive here and they need four different
// sentences.
//
// Telling them apart by the "yaml:" prefix does not work, which is how this went
// wrong: the library writes that prefix on syntax errors and on the errors it
// collects during decoding alike. The collected ones come back as a
// *yaml.TypeError — but that box holds two unrelated complaints, a value of the
// wrong shape and a key set twice, so the box alone is not the answer either.
//
// It does not cover everything. Some fields read themselves and answer in their
// own words, which fall through to the last case unchanged; which fields those
// are is not a list worth writing down here, because it is a property of each
// unmarshaler and it has already been got wrong twice by enumerating. What is
// true is the shape of the rule: only what the decoder collected is classified,
// and everything else keeps the words it arrived with.
// readAs says what document a decode failure's line numbers count in: the
// file as written, the merge of several files, or a file after its
// `extends` was read — the last two are documents nobody wrote.
type readAs int

const (
	asWritten readAs = iota
	asMerged
	asExtended
)

func decodeErr(path string, read readAs, err error) error {
	var collected *yaml.TypeError
	switch {
	case errors.As(err, &collected) && allDuplicateKeys(collected):
		// The same key twice is not a shape problem and has nothing to do with
		// variables. Saying either would send the reader looking for something
		// that is not there — which is the whole reason this exists.
		return fmt.Errorf("compose file %s sets the same key twice:\n  %w\n  "+
			"remove one of them", path, err)
	case errors.As(err, &collected):
		// What blameService put in front of the decoder's own words — the
		// service, and the field, the failure is in — would be lost with the
		// wrapper, so it is kept as a prefix.
		prefix := strings.TrimSuffix(err.Error(), collected.Error())
		err = withoutEchoedValues(collected)
		// Saying "not valid YAML" here sends the reader to look for a syntax
		// mistake in a file that has none, and "check the indentation and
		// quoting" sends them to the wrong place twice over. The file parsed;
		// what is wrong is what the value turned out to be.
		//
		// The one extra line, when several files were merged to get here, is that
		// the numbers below count in the merged document rather than in anything
		// the reader wrote. It goes in the middle of one message rather than into
		// a second copy of it: two copies is how half of a fix gets applied.
		hint := ""
		switch read {
		case asMerged:
			hint = "\n  the line above counts in the merged document, not in any of the files as " +
				"written — look for the key it names"
		case asExtended:
			hint = "\n  the line above counts in the document after extends was read, not in the " +
				"file as written — look for the key it names"
		}
		return fmt.Errorf("compose file %s parsed, but a value is not the shape "+
			"that field takes:\n  %s%w%s\n  check what that field is set to — if the value came "+
			"from a `${...}` reference, a variable that is not set leaves it empty", path, prefix, err, hint)
	case strings.HasPrefix(err.Error(), "yaml:"):
		return fmt.Errorf("compose file %s is not valid YAML: %w\n  check the indentation and quoting near the line the parser names above", path, err)
	}
	return fmt.Errorf("compose file %s: %w", path, err)
}

// allDuplicateKeys reports whether every complaint the decoder collected is about
// the same key appearing twice. They arrive in the same box as values of the
// wrong shape, and the two need opposite advice.
//
// The match is on the library's own wording, which a value cannot imitate: the
// decoder truncates any scalar it echoes to seven characters and an ellipsis,
// and this phrase is twenty-three. A file that sets a value to the phrase itself
// is still read as a shape problem, which is what it is.
func allDuplicateKeys(te *yaml.TypeError) bool {
	for _, e := range te.Errors {
		if !strings.Contains(e, "already defined at line") {
			return false
		}
	}
	return len(te.Errors) > 0
}

// withoutEchoedValues returns the same complaints with the value each one quotes
// taken out.
//
// The decoder writes the value it could not use into its message — `cannot
// unmarshal !!str `+"`sk-live...`"+` into compose.VolumeDecl` — and that value can have come
// from a `${...}` reference, which is where passwords and tokens live. Anything
// over ten characters is shortened to seven and an ellipsis — which is not much
// comfort: a short password went out whole, and the first seven characters of a
// long token still say what kind it is and which service it opens.
//
// What the reader needs is the line, the kind of thing they wrote, and the field
// it did not fit, and all three stay. This is the same rule the expander and the
// env-file reader follow: name the place, not the contents.
func withoutEchoedValues(te *yaml.TypeError) *yaml.TypeError {
	out := &yaml.TypeError{Errors: make([]string, len(te.Errors))}
	for i, e := range te.Errors {
		// Only the complaint that quotes a value. The other one names a key set
		// twice, and a key is a name the reader has to be able to find — a key with
		// two backticks in it would otherwise come out with its middle removed,
		// sending them to look for something that is not in the file.
		if !strings.Contains(e, "cannot unmarshal") {
			out.Errors[i] = e
			continue
		}
		// From the first backtick to the last: exactly the quoted value, even if
		// the value itself held a backtick. A complaint about a sequence or a
		// mapping quotes nothing and is left alone.
		lo, hi := strings.Index(e, "`"), strings.LastIndex(e, "`")
		if lo < 0 || hi <= lo {
			out.Errors[i] = e
			continue
		}
		out.Errors[i] = strings.Join(strings.Fields(e[:lo]+e[hi+1:]), " ")
	}
	return out
}

// validateOne decodes one file of several by itself, with every check the
// merged document gets, and names the file — and the line in it — in what
// it refuses. The first file is read as written: it is the base, and a key
// with nothing after it there is the same mistake it is in a single file.
// A later file is an override, and a key it writes with nothing after it
// is "not given" (the earlier value stands; measured against docker
// compose v5.5.0, for a field and for a whole service), so those are
// taken out before the decode and the bare-key check does not fire on
// them. The nodes are pruned, not re-rendered, so a failure keeps the
// line the reader wrote.
func validateOne(path string, one interpolated, earlier map[string]any) error {
	override := earlier != nil
	doc := one.node
	if doc == nil {
		var parsed yaml.Node
		if err := yaml.Unmarshal(one.raw, &parsed); err != nil {
			return decodeErr(path, asWritten, err)
		}
		doc = &parsed
	}
	if err := checkTopLevel(path, documentRoot(doc), earlier, override); err != nil {
		return err
	}
	if override {
		doc = withoutNotGiven(doc)
	} else {
		doc = withoutNotGivenInExtending(doc)
	}
	var f composeFile
	if err := doc.Decode(&f); err != nil {
		return decodeErr(path, asWritten, blameService(interpolated{node: doc, raw: one.raw}, err))
	}
	// In the first file a service with nothing under it is the mistake it
	// is in a single file (docker compose refuses it there even when a
	// later file gives the service a body); in a later file the key was
	// pruned above as "not given".
	if !override {
		names := make([]string, 0, len(f.Services))
		for name := range f.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if f.Services[name] == nil {
				return fmt.Errorf("compose file %s: service %q must be a mapping — the key has nothing under it; give it at least `image:` or `build:`, or remove the key", path, name)
			}
		}
	}
	return nil
}

// documentRoot is the document's top-level mapping, or nil.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

// resolveExtendsInTree reads `extends:` where it names a service of the
// same file, the way docker compose (v5.5.0) reads it: the named service's
// settings come first and the extending service's own settings go over
// them, merged by the rules a later -f file merges by — lists such as
// `ports`, `volumes` and `cap_add` are the other's then its own, mappings
// such as `environment` and `labels` merge by key with its own winning,
// a scalar such as `image` or `command` is its own where it has one, and
// `healthcheck` merges by sub-key (measured; the oracle fixtures are kept
// with the project's dogfood). A chain (web extends mid extends common)
// resolves the named service first; a cycle is refused (docker compose:
// `Circular reference`), and so is a service the file does not define,
// and one written with nothing under it. `extends: {file: …}` and a value
// of neither shape are left in place for the decode to refuse. With
// several -f files this runs on each file before the merge, as docker
// compose resolves it: what a file extends is what that file defines.
// Reports whether anything was resolved.
func resolveExtendsInTree(where, projectDir string, tree map[string]any, lookup varLookup) (bool, error) {
	return resolveExtends(where, projectDir, tree, lookup, nil)
}

// extendsID names one service of one file on the resolution stack, so that
// a cycle that runs through another file is seen as the cycle it is.
func extendsID(where, name string) string { return where + "#" + name }

// resolveExtends is resolveExtendsInTree with the stack of services being
// resolved, which a service of another file joins. stack entries are
// extendsIDs; the cycle message shows the names, with the file where it
// is not this one.
func resolveExtends(where, projectDir string, tree map[string]any, lookup varLookup, stack []string) (bool, error) {
	services, _ := tree["services"].(map[string]any)
	touched := false
	done := map[string]bool{}
	// The ids are absolute so that a file reached from another file (whose
	// path is made absolute there) is the same id as the one a relative -f
	// named it by; otherwise a cycle through files is seen one round late
	// and shown twice.
	absWhere := where
	if a, err := filepath.Abs(where); err == nil {
		absWhere = a
	}
	showID := func(id string) string {
		file, name, _ := strings.Cut(id, "#")
		if file == absWhere {
			return name
		}
		return name + " (" + file + ")"
	}
	var resolve func(name string, stack []string) error
	resolve = func(name string, stack []string) error {
		if done[name] {
			return nil
		}
		svc, ok := services[name].(map[string]any)
		if !ok {
			done[name] = true
			return nil
		}
		ext, has := svc["extends"]
		if !has {
			done[name] = true
			return nil
		}
		var target, file string
		otherFile := false
		switch e := ext.(type) {
		case string:
			target = e
		case map[string]any:
			target, _ = e["service"].(string)
			// A `file:` key, even one with nothing after it, names another
			// file (docker compose tries to read it and fails).
			if f, has := e["file"]; has {
				otherFile = true
				file, _ = f.(string)
			}
		}
		if target == "" || (otherFile && file == "") {
			done[name] = true // the decode refuses these in its own words
			return nil
		}
		id := extendsID(absWhere, name)
		for _, s := range stack {
			if s == id {
				shown := make([]string, 0, len(stack)+1)
				for _, s := range stack {
					shown = append(shown, showID(s))
				}
				return fmt.Errorf("%s: service %q: extends forms a cycle (%s → %s) — remove extends: from one of them", where, name, strings.Join(shown, " → "), name)
			}
		}
		var base map[string]any
		if otherFile {
			// The named file is read from the project directory of the
			// unit this file belongs to — the first -f file's directory,
			// or an include entry's — as docker compose reads it (measured:
			// `-f a.yml -f sub/b.yml` reads sub/b.yml's `file: base.yml`
			// from a.yml's directory; a file it names in turn is found from
			// the named file's own directory), and the service is taken from
			// it with that file's own extends resolved first and its
			// relative paths made absolute against that file's directory.
			path := file
			if !filepath.IsAbs(path) {
				path = filepath.Join(projectDir, path)
			}
			b, err := extendedServiceFromFile(where, name, path, target, lookup, append(stack, id))
			if err != nil {
				return err
			}
			base = b
		} else {
			raw, exists := services[target]
			if !exists {
				return fmt.Errorf("%s: service %q extends %q, which the file does not define — name a service this file defines, or remove extends:", where, name, target)
			}
			// In a later -f file a service key with nothing under it is "not
			// given" (the earlier file's stands), but extends reads the service
			// as this file defines it, and here that is nothing: refused, where
			// docker compose leaves the key unread and the service without an
			// image (measured).
			if _, ok := raw.(map[string]any); !ok {
				return fmt.Errorf("%s: service %q extends %q, which this file writes with nothing under it — extends reads the service as this file defines it; give it a body here, or remove extends:", where, name, target)
			}
			if err := resolve(target, append(stack, id)); err != nil {
				return err
			}
			base = deepCopyTree(services[target]).(map[string]any)
		}
		own := deepCopyTree(svc).(map[string]any)
		delete(own, "extends")
		services[name] = mergeMap(base, own, childPath("services", name))
		done[name] = true
		touched = true
		return nil
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := resolve(name, stack); err != nil {
			return false, err
		}
	}
	return touched, nil
}

// extendedServiceFromFile reads the file `extends: {file: …}` names and
// returns a copy of the named service as docker compose (v5.5.0) hands it
// to the extending service: the file is expanded in the same scope as the
// project's own files and checked as written, its own extends is resolved
// first (a chain may run on into a third file; a cycle through files is
// refused), and the paths the service writes relative to its file —
// `build`, a bind mount's source, `env_file`, `develop.watch` paths — are
// made absolute against that file's directory, since the project resolves
// relative paths against its first file. Only the service comes over: the
// file's top-level declarations do not (measured).
func extendedServiceFromFile(where, name, path, target string, lookup varLookup, stack []string) (map[string]any, error) {
	// A relative -f leaves `where` relative, and so this path; the paths
	// rebased below must be absolute, since the project resolves relative
	// ones against its own directory (a relative one would be doubled).
	if a, err := filepath.Abs(path); err == nil {
		path = a
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: service %q extends %q of %s, which cannot be read: %v — name the file by a path from the project directory (the first file's, or the include entry's), or remove extends:", where, name, target, path, err)
	}
	doc, err := interpolateDocument(raw, lookup)
	if err != nil {
		return nil, fmt.Errorf("interpolating %s (extended by service %q of %s): %w", path, name, where, err)
	}
	if err := validateOne(path, doc, nil); err != nil {
		return nil, err
	}
	var tree map[string]any
	if err := doc.into(&tree); err != nil {
		return nil, decodeErr(path, asWritten, err)
	}
	// A file the extends reached is read on its own terms: what it
	// extends in turn is found from its own directory (docker compose,
	// measured: a/two.yml → b/near.yml → c/far.yml reads a/b/c/far.yml,
	// where the first hop from a -f file counts from the project
	// directory).
	if _, err := resolveExtends(path, filepath.Dir(path), tree, lookup, stack); err != nil {
		return nil, err
	}
	services, _ := tree["services"].(map[string]any)
	svc, ok := services[target].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: service %q extends %q of %s, and that file does not define it — name a service %s defines, or remove extends:", where, name, target, path, path)
	}
	base := deepCopyTree(svc).(map[string]any)
	rebasePaths(base, filepath.Dir(path))
	return base, nil
}

// rebasePaths makes the host paths a service writes relative to its own
// file absolute against dir, so that a project whose first file lives
// elsewhere resolves them where docker compose does. What is rebased is
// what docker compose rebases when it extends across files: `build` (as a
// path or its `context`), a bind mount's source in the short form (only
// one written as `.`, `..`, `./…` or `../…`, the host paths the runtime
// side reads as such — a bare name is a named volume) and the long form,
// `env_file` in every form, and `develop.watch` paths.
func rebasePaths(svc map[string]any, dir string) {
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") || strings.Contains(p, "://") || strings.HasPrefix(p, "git@") {
			return p
		}
		return filepath.Join(dir, p)
	}
	switch b := svc["build"].(type) {
	case string:
		svc["build"] = abs(b)
	case map[string]any:
		if c, ok := b["context"].(string); ok {
			b["context"] = abs(c)
		}
	}
	if vols, ok := svc["volumes"].([]any); ok {
		for i, v := range vols {
			switch m := v.(type) {
			case string:
				if src, rest, found := strings.Cut(m, ":"); found && relativeHostPath(src) {
					vols[i] = abs(src) + ":" + rest
				}
			case map[string]any:
				typ, _ := m["type"].(string)
				if src, ok := m["source"].(string); ok && (typ == "bind" || typ == "") && relativeHostPath(src) {
					m["source"] = abs(src)
				}
			}
		}
	}
	switch ef := svc["env_file"].(type) {
	case string:
		svc["env_file"] = abs(ef)
	case []any:
		for i, e := range ef {
			switch x := e.(type) {
			case string:
				ef[i] = abs(x)
			case map[string]any:
				if p, ok := x["path"].(string); ok {
					x["path"] = abs(p)
				}
			}
		}
	}
	if dev, ok := svc["develop"].(map[string]any); ok {
		if watch, ok := dev["watch"].([]any); ok {
			for _, w := range watch {
				if m, ok := w.(map[string]any); ok {
					if p, ok := m["path"].(string); ok {
						m["path"] = abs(p)
					}
				}
			}
		}
	}
}

// relativeHostPath reports whether a volume source is written as a path
// relative to a file — the forms the runtime side reads as a host path
// rather than a named volume (`.`, `..`, `./…`, `../…`).
func relativeHostPath(s string) bool {
	return s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

// resolveSameFileExtends is resolveExtendsInTree for a single file: the
// tree comes back re-marshalled only when something was resolved, so a
// file with no such service keeps its positions for the failures that
// name a line (a file with one has been checked as written already).
func resolveSameFileExtends(doc interpolated, where, projectDir string, lookup varLookup) (*interpolated, error) {
	var tree map[string]any
	if err := doc.into(&tree); err != nil {
		return nil, nil // the decode below says so in its own words
	}
	touched, err := resolveExtendsInTree(where, projectDir, tree, lookup)
	if err != nil || !touched {
		return nil, err
	}
	// A key that is still nothing once extends is read — bare in the
	// extending service, absent from the named one — is refused here, as
	// docker compose refuses it (`must be a array`).
	if err := validateMerged(where, tree, asExtended); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("reading extends in %s: %w", where, err)
	}
	return &interpolated{raw: data}, nil
}

// deepCopyTree copies a decoded YAML tree (mappings, lists and scalars) so
// that merging into the copy leaves the original service as it was.
func deepCopyTree(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopyTree(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopyTree(val)
		}
		return out
	}
	return v
}

// parsedRoot is the document's root mapping, parsing the expanded bytes
// when expansion left the tree unbuilt; nil when the document is not a
// mapping (the decode says so in its own words).
func parsedRoot(one interpolated) (*yaml.Node, error) {
	doc := one.node
	if doc == nil {
		var parsed yaml.Node
		if err := yaml.Unmarshal(one.raw, &parsed); err != nil {
			return nil, err
		}
		doc = &parsed
	}
	return documentRoot(doc), nil
}

// topLevelDecls are the top-level mappings of declarations: written with
// nothing under them, docker compose refuses each (`networks must be a
// mapping`), where reading them as "none" would let the file through.
var topLevelDecls = map[string]bool{"networks": true, "volumes": true, "secrets": true, "configs": true}

// checkTopLevel reads the shapes of a file's top-level keys the way docker
// compose (v5.5.0) checks them before anything runs. A bare `services:`
// is a mistake in any file — there is no service to keep — and so is a
// bare `networks:`, `volumes:`, `secrets:`, `configs:` or `name:` in the
// first file; in a later file such a bare key is "not given" where an
// earlier file gave the key a value (pruned before this), and refused
// where none did, as docker compose refuses it — and a bare `version:`
// in a later file is refused whatever came before, as there. A
// declaration mapping written as a list or a scalar (`networks: [a]`) is
// refused naming the key, where the decode named a Go type. A `name:`
// or `version:` that is not a string — a number, a boolean, a list, a
// mapping — is refused (`name must be a string`); read as written,
// `name: 42` was the project "42" and a bare `name:` the directory's.
// `include` with something in it is refused outright:
// opossum does not read it, and listing it as ignored left the services
// the named files hold out of the project in silence — the files can be
// passed with -f instead (an empty or bare `include:` names nothing, and
// docker compose takes it). A mapping brought in by `<<:` is walked the
// same way; earlier is the earlier files' merge, nil for the first.
func checkTopLevel(path string, root *yaml.Node, earlier map[string]any, override bool) error {
	if root == nil {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := unalias(root.Content[i])
		if key.Tag == "!!merge" {
			merged := unalias(root.Content[i+1])
			maps := []*yaml.Node{merged}
			if merged.Kind == yaml.SequenceNode {
				maps = merged.Content
			}
			for _, m := range maps {
				if m = unalias(m); m.Kind == yaml.MappingNode {
					if err := checkTopLevel(path, m, earlier, override); err != nil {
						return err
					}
				}
			}
			continue
		}
		k, v := key.Value, unalias(root.Content[i+1])
		bare := isNothing(root.Content[i+1])
		givenBefore := override && earlier[k] != nil
		switch {
		case k == "services" && bare:
			return fmt.Errorf("compose file %s: services must be a mapping — the key has nothing under it; write the services or remove the key", path)
		case k == "include":
			// A list is read (see loadUnit); nothing after the key names no
			// file. docker compose: "`include` must be a list".
			if bare || v.Kind == yaml.SequenceNode {
				continue
			}
			return fmt.Errorf("compose file %s: include must be a list, got %s (line %d) — write the files to include as `- other.yml`", path, kindName(v.Kind), key.Line)
		case k == "version" && bare:
			return fmt.Errorf("compose file %s: version must be a string — the key has nothing after it; write the value or remove the key", path)
		case k == "name" && bare:
			if !givenBefore {
				return fmt.Errorf("compose file %s: name must be a string — the key has nothing after it; write the value or remove the key", path)
			}
		case k == "name" || k == "version":
			if v.Kind != yaml.ScalarNode {
				return fmt.Errorf("compose file %s: %s must be a string, got %s (line %d)", path, k, kindName(v.Kind), key.Line)
			}
			if word := nonStringWord(v); word != "" {
				return fmt.Errorf("compose file %s: %s must be a string, got %s (line %d) — quote it (`\"%s\"`) if it is meant literally", path, k, word, v.Line, v.Value)
			}
		case topLevelDecls[k] && bare:
			if !givenBefore {
				return fmt.Errorf("compose file %s: %s must be a mapping — the key has nothing under it; write the declarations or remove the key", path, k)
			}
		case topLevelDecls[k] && v.Kind != yaml.MappingNode:
			// A list or a scalar where the declarations belong: docker
			// compose says `networks must be a mapping`; the decode said it
			// in YAML's words, with the Go type standing in for the field.
			return fmt.Errorf("compose file %s: %s must be a mapping, got %s (line %d) — write the declarations as `name: {…}`", path, k, kindName(v.Kind), key.Line)
		}
	}
	return nil
}

// hasInclude reports whether the file names files to include — a
// top-level `include:` with something in it. Read cheaply, before the file
// is read for what it says: such a file is a merge of several, and takes
// the merge road even when it is the only -f.
func hasInclude(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var top struct {
		Include []any `yaml:"include"`
	}
	return yaml.Unmarshal(raw, &top) == nil && len(top.Include) > 0
}

// includeEntry is one item of a file's `include:`, in either form: a path,
// or a mapping with `path` (one, or a list), `project_directory` and
// `env_file` (one, or a list).
type includeEntry struct {
	paths      []string
	projectDir string
	envFiles   []string
}

// includeEntries reads a file's `include:` from the plain tree, as docker
// compose (v5.5.0) reads it: a string names a file; a mapping names one or
// several under `path`, with `project_directory` the directory the
// included files' relative paths count from (the first path's directory
// when not given) and `env_file` the files their variables are read from
// (that directory's `.env` when not given); a mapping with no `path` names
// nothing and is taken. Anything else is refused.
func includeEntries(path string, inc any) ([]includeEntry, error) {
	list, ok := inc.([]any)
	if !ok {
		return nil, nil // not a list: refused as written, before this
	}
	strings := func(v any, what string, i int) ([]string, error) {
		switch x := v.(type) {
		case string:
			return []string{x}, nil
		case []any:
			out := make([]string, 0, len(x))
			for _, e := range x {
				str, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("compose file %s: include entry %d: %s must be a path or a list of paths — got %s in the list", path, i+1, what, kindOf(e))
				}
				out = append(out, str)
			}
			return out, nil
		}
		return nil, fmt.Errorf("compose file %s: include entry %d: %s must be a path or a list of paths, got %s", path, i+1, what, kindOf(v))
	}
	var entries []includeEntry
	for i, item := range list {
		switch x := item.(type) {
		case string:
			entries = append(entries, includeEntry{paths: []string{x}})
		case map[string]any:
			var e includeEntry
			if p, has := x["path"]; has {
				paths, err := strings(p, "path", i)
				if err != nil {
					return nil, err
				}
				e.paths = paths
			}
			if d, has := x["project_directory"]; has {
				dir, ok := d.(string)
				if !ok {
					return nil, fmt.Errorf("compose file %s: include entry %d: project_directory must be a path, got %s", path, i+1, kindOf(d))
				}
				e.projectDir = dir
			}
			if f, has := x["env_file"]; has {
				files, err := strings(f, "env_file", i)
				if err != nil {
					return nil, err
				}
				e.envFiles = files
			}
			entries = append(entries, e)
		default:
			return nil, fmt.Errorf("compose file %s: include entry %d must be a path or a mapping with path, got %s", path, i+1, kindOf(item))
		}
	}
	return entries, nil
}

// kindOf names a plain-tree value the way the shape refusals do.
func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "a single value"
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	return "a single value"
}

// includedPart is what an included file brings into the including
// project, as docker compose takes it: its services, with their paths
// made absolute against projectDir, and its volumes, networks, secrets
// and configs declarations, a secret's or config's `file` made absolute
// the same way. Its `name`, `version` and extension fields stay behind.
func includedPart(whole map[string]any, projectDir string) map[string]any {
	part := map[string]any{}
	if services, ok := whole["services"].(map[string]any); ok {
		for _, svc := range services {
			if svc, ok := svc.(map[string]any); ok {
				rebasePaths(svc, projectDir)
			}
		}
		part["services"] = services
	}
	for _, k := range []string{"volumes", "networks", "secrets", "configs"} {
		decls, ok := whole[k]
		if !ok {
			continue
		}
		if k == "secrets" || k == "configs" {
			if m, ok := decls.(map[string]any); ok {
				for _, d := range m {
					if d, ok := d.(map[string]any); ok {
						if f, ok := d["file"].(string); ok && f != "" && !filepath.IsAbs(f) {
							d["file"] = filepath.Join(projectDir, f)
						}
					}
				}
			}
		}
		part[k] = decls
	}
	return part
}

// loadUnit reads one -f file as the project sees it: the file, with the
// files its `include:` names read before it and merged under it, as docker
// compose (v5.5.0) reads them — each included file is a project of its own
// whose relative paths count from its project directory (made absolute
// here, since this project resolves relative ones from its own directory),
// with its own `include` and `extends` resolved, and its variables read
// from that directory's `.env` (or the entry's `env_file`) under this
// project's: the shell and this project's `.env` win. Only what docker
// compose takes from an included file comes over — its services and its
// volumes, networks, secrets and configs declarations (a secret's or
// config's file counting from its project directory) — not its `name`,
// `version` or extension fields. Where an included file and this one
// define the same service, this file's settings go over the included
// (measured: not a refusal). A service of this file may extend an
// included one. An included file is checked as a project of its own (a
// bare service key is refused there, as in a first file). A file that
// includes itself, through however many files, is refused. Returns the
// merged tree and the files it was read from, included first.
//
// projectDir is the directory this file's own include entries and
// `extends: {file}` count from — the project directory of the unit the
// file belongs to, which is the first -f file's directory for a -f file
// and the entry's for an included one ("" says the file's own) (docker compose, measured: a nested include's paths count
// from the project directory, not from the file that names them).
func loadUnit(path, projectDir string, scope envScope, earlier map[string]any, stack []string) (map[string]any, []string, error) {
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	if projectDir == "" {
		projectDir = filepath.Dir(abs)
	}
	for _, s := range stack {
		if s == abs {
			shown := make([]string, 0, len(stack)+1)
			for _, s := range stack {
				shown = append(shown, filepath.Base(s))
			}
			return nil, nil, fmt.Errorf("compose file %s: include forms a cycle (%s → %s) — remove the include that closes it", path, strings.Join(shown, " → "), filepath.Base(abs))
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if len(stack) > 0 {
			return nil, nil, fmt.Errorf("compose file %s: include names %s, which cannot be read: %v — a relative path is resolved from the project directory (the first file's, or the include entry's)", stack[len(stack)-1], path, err)
		}
		return nil, nil, fmt.Errorf("reading compose file: %w", err)
	}
	one, err := interpolateDocument(raw, scope.lookup())
	if err != nil {
		return nil, nil, fmt.Errorf("interpolating %s: %w", path, err)
	}
	var m map[string]any
	if err := one.into(&m); err != nil {
		// The same words as the single-file road. A key set twice is found
		// here rather than at the final decode, and saying "not valid YAML"
		// for it was the same wrong advice by a different route — the one a
		// reader hits precisely when they have split their file up.
		return nil, nil, decodeErr(path, asWritten, err)
	}
	files := []string{path}
	// The included files come first, so that this file is checked
	// against them below.
	var group map[string]any
	var included []string
	if inc, has := m["include"]; has {
		delete(m, "include")
		entries, err := includeEntries(path, inc)
		if err != nil {
			return nil, nil, err
		}
		dir := projectDir
		for _, e := range entries {
			if len(e.paths) == 0 {
				continue
			}
			subDir := e.projectDir
			if subDir == "" {
				subDir = filepath.Dir(e.paths[0])
			}
			if !filepath.IsAbs(subDir) {
				subDir = filepath.Join(dir, subDir)
			}
			envFiles := make([]string, 0, len(e.envFiles))
			for _, f := range e.envFiles {
				if !filepath.IsAbs(f) {
					f = filepath.Join(dir, f)
				}
				envFiles = append(envFiles, f)
			}
			// The included project's variables: its directory's `.env` (or
			// the entry's env_file) at a level under this project's, whose
			// shell and `.env` win. The built-in stays last, under both.
			sub, err := loadEnv(subDir, envFiles)
			if err != nil {
				return nil, nil, fmt.Errorf("compose file %s: include: %w", path, err)
			}
			sub.outer = chainLookup(scope.outer, mapLookup(scope.level))
			sub.builtin = scope.builtin
			for _, p := range e.paths {
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				// Checked as a project of its own: no earlier file makes
				// its bare keys "not given".
				whole, subFiles, err := loadUnit(p, subDir, sub, nil, append(stack, abs))
				if err != nil {
					return nil, nil, err
				}
				tree := includedPart(whole, subDir)
				included = append(included, subFiles...)
				if group == nil {
					group = tree
				} else {
					group = mergeMap(group, tree, "")
				}
			}
		}
	}
	// Each file is checked on its own before the merge, as docker
	// compose validates each before merging: a mistake in one file
	// is refused naming that file and its line, whether or not a
	// later file writes over it, and a shape the merge would absorb
	// (a network listed twice in one file) is refused too. In a
	// later file a key with nothing after it is "not given" (the
	// earlier value stands) and is not read as a bare key — and so
	// it is in a file with includes, where an included file may have
	// given the key its value (docker compose, measured: `services:
	// {common: }` over an include that defines common is that
	// service); what is still nothing after the merge is refused.
	before := earlier
	if group != nil {
		before = group
		if earlier != nil {
			before = mergeMap(deepCopyTree(earlier).(map[string]any), group, "")
		}
	}
	if err := validateOne(path, one, before); err != nil {
		return nil, nil, err
	}
	if err := liftExternalNames(path, m, before); err != nil {
		return nil, nil, err
	}
	if group != nil {
		m = mergeMap(group, m, "")
		files = append(included, files...)
		// What is still nothing once the includes — and the earlier -f
		// files, which count as much here as they do in LoadFiles — are
		// under this file is refused naming this file: a bare service
		// key nobody defines, or a field no file gave a value to.
		whole := m
		if earlier != nil {
			whole = mergeMap(deepCopyTree(earlier).(map[string]any), m, "") // merging reads m, and writes only the copy
		}
		if services, ok := whole["services"].(map[string]any); ok {
			names := make([]string, 0, len(services))
			for name := range services {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if services[name] == nil {
					return nil, nil, fmt.Errorf("compose file %s: service %q must be a mapping — the key has nothing under it; give it at least `image:` or `build:`, or remove the key", path, name)
				}
			}
		}
		if err := validateMerged(path, whole, asMerged); err != nil {
			return nil, nil, err
		}
	}
	// `extends` within this file is read before the merge with earlier -f
	// files, as docker compose reads it: a service this file defines — or
	// includes — is what it extends, and an earlier file's version of the
	// extending service is what this file's resolved version goes over.
	if _, err := resolveExtendsInTree(path, projectDir, m, scope.lookup()); err != nil {
		return nil, nil, err
	}
	return m, files, nil
}

// validateMerged decodes what the files so far make together and names
// the file just added in what it refuses.
func validateMerged(path string, merged map[string]any, read readAs) error {
	data, err := yaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("merging compose files: %w", err)
	}
	doc := interpolated{raw: data}
	var f composeFile
	if err := doc.into(&f); err != nil {
		return decodeErr(path, read, blameService(doc, err))
	}
	return nil
}

// withoutNotGivenInExtending copies the document with the bare keys of each
// service that extends another pruned: there a key with nothing after it is
// "not given" — the named service's value stands — as it is in a later -f
// file (docker compose reads extends before it checks the shapes, measured).
// What is still nothing once extends is read is refused then.
func withoutNotGivenInExtending(doc *yaml.Node) *yaml.Node {
	root := documentRoot(doc)
	if root == nil {
		return doc
	}
	newRoot := copyMapping(root)
	for i := 0; i+1 < len(newRoot.Content); i += 2 {
		if newRoot.Content[i].Value != "services" || newRoot.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		services := copyMapping(newRoot.Content[i+1])
		for j := 0; j+1 < len(services.Content); j += 2 {
			if svc := services.Content[j+1]; svc.Kind == yaml.MappingNode && hasKey(svc, "extends") {
				services.Content[j+1] = withoutNotGiven(svc)
			}
		}
		newRoot.Content[i+1] = services
	}
	if root == doc {
		return newRoot
	}
	out := *doc
	out.Content = []*yaml.Node{newRoot}
	return &out
}

// copyMapping is a mapping node with its own copy of the entry list, so
// that replacing an entry leaves the original node as it was.
func copyMapping(n *yaml.Node) *yaml.Node {
	out := *n
	out.Content = append([]*yaml.Node(nil), n.Content...)
	return &out
}

// hasKey reports whether the mapping node has an entry under key.
func hasKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

// withoutNotGiven copies a document's tree without the mapping entries
// whose value is nothing — `services.web:` with nothing under it, and
// `services.web.ports:` with nothing after it — at any level: in an
// override, each is the file's way of saying nothing about that key.
func withoutNotGiven(n *yaml.Node) *yaml.Node {
	out := *n
	switch n.Kind {
	case yaml.DocumentNode:
		out.Content = make([]*yaml.Node, 0, len(n.Content))
		for _, c := range n.Content {
			out.Content = append(out.Content, withoutNotGiven(c))
		}
	case yaml.MappingNode:
		out.Content = make([]*yaml.Node, 0, len(n.Content))
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if isNothing(v) {
				continue
			}
			out.Content = append(out.Content, k, withoutNotGiven(v))
		}
	case yaml.AliasNode:
		// What the alias stands for is pruned the same way — a `<<: *base`
		// whose anchor carries a bare key, or `ports: *nada` — and put in
		// its place: an alias would still point at the unpruned anchor.
		if n.Alias != nil {
			pruned := withoutNotGiven(n.Alias)
			if pruned.Kind == yaml.ScalarNode && pruned.Tag == "!!null" {
				return pruned
			}
			pruned.Anchor = ""
			return pruned
		}
	}
	return &out
}

// isNothing reports a value written with nothing after the key, through an
// alias too (`ports: *nada` with `x-nada: &nada ~`).
func isNothing(v *yaml.Node) bool {
	if v.Kind == yaml.AliasNode && v.Alias != nil {
		v = v.Alias
	}
	return v.Kind == yaml.ScalarNode && v.Tag == "!!null"
}

// mergedName names the document a failure at the final decode is about.
//
// One file is itself, and the line the parser gives is a line in it. Several are
// merged into a new document before this point, and there is no honest way to
// point into any one of them: the line belongs to the merged text, and which file
// a value came from is not recorded anywhere. Naming the first file — which is
// what this used to do — sends the reader to a file that may not contain the
// problem, at a line they cannot find.
//
// Naming them all is worse to read but true, and the reader can still act on it:
// the value is in one of these, under the key the message names. The line is
// still worth printing — it orders the failures when there are several — so it is
// the claim about WHERE that has to go, not the number.
func mergedName(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return strings.Join(paths, " + ")
}

// blameService says which service a decode failure came from, when the failure
// itself does not.
//
// A service's own complaints — a duration that is not one, a restart policy that
// is not one — are raised deep inside the decode and carry no name: `services:` is
// decoded as a map, and the key never reaches the value's unmarshaler. That was
// survivable while those messages quoted the value, because you could search the
// file for it, and stopped being survivable when they stopped quoting it.
//
// The obvious fix — decode the services one at a time so the key is in hand — is
// wrong: walking the mapping by hand skips what the decoder does for a mapping,
// and duplicate service names started passing silently, last one winning. So the
// ordinary decode stays authoritative and this runs only after it has failed,
// where being wrong costs nothing: it re-reads each service on its own, and if
// exactly one of them fails the same way, it says so. Anything else is left alone.
func blameService(d interpolated, err error) error {
	// The same document the decode read, not the bytes it was built from: those
	// still carry the marks that stand where a reference expanded to nothing, and
	// a service that is fine once they are taken out can look broken with them in.
	// Reading a different document here does not produce a wrong name — a second
	// match makes it say nothing — but it makes it say nothing on files where it
	// had an answer.
	doc := d.node
	if doc == nil {
		var parsed yaml.Node
		if yaml.Unmarshal(d.raw, &parsed) != nil {
			return err
		}
		doc = &parsed
	}
	if len(doc.Content) == 0 {
		return err
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return err
	}
	var services *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "services" {
			services = root.Content[i+1]
		}
	}
	if services == nil || services.Kind != yaml.MappingNode {
		return err
	}
	name, found := "", 0
	var culprit *yaml.Node
	for i := 0; i+1 < len(services.Content); i += 2 {
		var svc Service
		if e := services.Content[i+1].Decode(&svc); e != nil && e.Error() == err.Error() {
			name, found, culprit = services.Content[i].Value, found+1, services.Content[i+1]
		}
	}
	if found != 1 {
		return err
	}
	if field := blameField(culprit, err); field != "" {
		return fmt.Errorf("service %q: %s: %w", name, field, err)
	}
	return fmt.Errorf("service %q: %w", name, err)
}

// sharedFields are the ones read by a type that serves more than one key, so the
// message they produce cannot say which key it was reading. `command:` and
// `entrypoint:` are both a Command; the parser hands the value to it without the
// name, and saying one of them is a guess that sends half the readers to the
// wrong line.
var sharedFields = []string{"command", "entrypoint"}

// blameField says which of them failed, or "" when it cannot tell.
//
// The same shape as naming the service, and for the same reason: the decode has
// already failed, so re-reading a field costs nothing and being wrong costs
// nothing either — it says nothing rather than picking. Both broken the same way
// is the case that must stay quiet, because there is no answer to give.
func blameField(svc *yaml.Node, err error) string {
	if svc == nil || svc.Kind != yaml.MappingNode {
		return ""
	}
	name, found := "", 0
	for i := 0; i+1 < len(svc.Content); i += 2 {
		key := svc.Content[i].Value
		if !slices.Contains(sharedFields, key) {
			continue
		}
		var c Command
		if e := svc.Content[i+1].Decode(&c); e != nil && strings.Contains(err.Error(), e.Error()) {
			name, found = key, found+1
		}
	}
	switch found {
	case 0:
		// Not one of these at all; whatever failed says so in its own words.
		return ""
	case 1:
		return name
	}
	// Both, failing the same way. Naming one would send half the readers to the
	// wrong line, and naming neither would leave a message with no field in it at
	// all — the reader knows it is one of two, which is what is true.
	return strings.Join(sharedFields, " or ")
}

// describe names the extended service and its file for the refusal, as far
// as `extends:` said them.
func (e *ExtendsRef) describe() string {
	switch {
	case e.Service != "" && e.File != "":
		return fmt.Sprintf(" (service %q in %s)", e.Service, e.File)
	case e.Service != "":
		return fmt.Sprintf(" (service %q)", e.Service)
	case e.File != "":
		return fmt.Sprintf(" (a service in %s)", e.File)
	}
	return ""
}
