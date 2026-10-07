package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Name     string                 `yaml:"name"`
	Services map[string]*Service    `yaml:"services"`
	Secrets  map[string]Secret      `yaml:"secrets"`
	Configs  map[string]Config      `yaml:"configs"`
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
		case k == "name" || k == "services" || k == "version" || k == "secrets" || k == "configs" || k == "networks" || k == "volumes":
		case strings.HasPrefix(k, "x-"):
		default:
			out = append(out, k)
		}
	}
	out = append(out, ignoredVolumeFields(top["volumes"])...)
	out = append(out, ignoredNetworkFields(top["networks"])...)
	out = append(out, ignoredSecretFields(top["secrets"])...)
	out = append(out, ignoredConfigFields(top["configs"])...)
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
var networkDeclFields = map[string]bool{"external": true, "name": true, "internal": true, "labels": true, "ipam": true}

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

// configDeclFields are the per-config keys opossum acts on (`external` by
// refusing it). Anything else (`name`, `labels`) is parsed and dropped,
// and named here so it is not dropped in silence.
var configDeclFields = map[string]bool{"file": true, "content": true, "environment": true, "external": true}

// ignoredConfigFields reports unacted-on keys inside top-level config
// declarations as `configs.<config>.<key>`, as the secrets one does.
func ignoredConfigFields(node yaml.Node) []string {
	var decls map[string]map[string]yaml.Node
	if node.IsZero() || node.Decode(&decls) != nil {
		return nil
	}
	var out []string
	for cfg, fields := range decls {
		for k := range fields {
			if configDeclFields[k] || strings.HasPrefix(k, "x-") {
				continue
			}
			out = append(out, fmt.Sprintf("configs.%s.%s", cfg, k))
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
// `config` is a network's `ipam.config`: a later file's list replaces the
// earlier one (docker compose v5.5.0 gives the merged network the later
// file's entries only), where appending would read as a second subnet of the
// same family and be refused. No other list in a compose file is keyed
// `config` (the spec's tables have it under `networks.*.ipam` alone).
var replaceSeqKeys = map[string]bool{"command": true, "entrypoint": true, "test": true, "config": true}

// scalarListKeys are the service fields written as a string or a list that
// docker compose merges as lists (a string is a one-item list there). Not
// `command`/`entrypoint` (replaced whole), and not `cap_add`, `cap_drop` or
// `profiles`, whose string form docker compose refuses outright.
var scalarListKeys = map[string]bool{"tmpfs": true, "env_file": true}

// asList reads a string as the one-item list it stands for; anything else
// is returned as it is.
func asList(v any) any {
	if s, ok := v.(string); ok {
		return []any{s}
	}
	return v
}

// envLikeKeys accept either a `KEY: value` map or a `- KEY=value` list; both merge
// by key (later wins), so a base and override merge per variable regardless of form.
// `build.args` is the third: its two forms merge by variable the same way
// (measured against docker compose v5.5.0 — a list in one file and a
// mapping in the next keep every variable, the later file winning by name).
var envLikeKeys = map[string]bool{"environment": true, "labels": true, "args": true}

// dedupSeqKeys are list fields where a repeated entry (e.g. an override restating an
// exposed port, or a `volumes_from` a second file lists again) should collapse to
// one, as docker compose collapses it. What a file repeats within its own list is
// left alone, which is where the two part company (see appendNew). `volumes` is deliberately
// absent: mergeByTargetKeys handles it with a stricter rule (same mount point, not
// just same text) that subsumes plain dedup.
//
// `ports` is absent too, and not for want of the same rule: it is folded after the
// load, by what it normalizes to — `3000` and `3000:3000` are one entry there, and a
// file's own repeats go with them — which takes everything the entry-by-entry fold
// here would (an entry written the same way twice is the same entry once
// normalized). Kept here it changed no answer: every ordered pair of twenty
// spellings (400), laid out as two files and as one file, came out the same with
// and without it, in `config` and in the ports `up --dry-run` builds; a second
// review's 3,072 runs over twelve ways of reaching the merge agreed. A second fold that nothing can tell from the
// first is one that a test would only hold in place.
var dedupSeqKeys = map[string]bool{"expose": true, "volumes_from": true, "group_add": true}

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
		if b, ok := dependsOnAsMap(base, true); ok {
			if o, ok := dependsOnAsMap(over, false); ok {
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
	// A service's `tmpfs:` and `env_file:` take a string as well as a list:
	// docker compose reads the string as a one-item list before merging
	// (measured against v5.5.0: `/t` in one file and `[/u]` or `/u` in the
	// next mount both), so a later file's `tmpfs:` never replaces the earlier
	// one's whichever form each wrote. Read both sides that way. Not for a
	// *service* of that name (an element of a collection), nor for a
	// *variable* of that name inside environment, labels or build.args.
	if scalarListKeys[key] && !collections[path] && !insideEnvLike(path) {
		base, over = asList(base), asList(over)
	}
	switch o := over.(type) {
	case map[string]any:
		if b, ok := base.(map[string]any); ok {
			return mergeMap(b, o, childPath(path, key))
		}
	case []any:
		if b, ok := base.([]any); ok && !replaceSeqKeys[key] {
			// This one is handed the two lists rather than one appended list:
			// what a file wrote on its own is that file's, and only what the
			// later file adds is compared against the earlier one. The fold
			// by mount point below is handed the appended list, being about
			// where each entry lands and not about which file wrote it.
			if dedupSeqKeys[key] {
				return appendNew(b, o)
			}
			merged := append(append([]any{}, b...), o...)
			if mergeByTargetKeys[key] {
				return mergeSeqByTarget(merged)
			}
			// A service's `secrets` and `configs` entries merge by the place they are mounted at: a later
			// file's entry for the same target replaces the earlier one whole (its `mode` with it), where an
			// entry for another target is added (#1544; measured, v5.5.1, -f files and extends alike).
			if (key == "secrets" || key == "configs") && collections[parentPath(path)] {
				return mergeSeqBySecretTarget(merged, key)
			}
			return merged
		}
	}
	return over
}

// dependsOnAsMap reads a `depends_on:` value in either form as the mapping
// form: a list of names becomes `{name: {condition: service_started, required: true}}`
// (the entry docker compose makes of a listed name, in the later file too), a mapping is
// returned as is — with the `required` an earlier file's entry has by default put in
// (earlier says it is the earlier side of a merge). Anything else — a scalar, a list holding something that is not a
// name — is not a value this can read, and ok is false so the caller falls
// back to the ordinary merge (and the decode names it).
func dependsOnAsMap(v any, earlier bool) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		if !earlier {
			return x, true
		}
		// An entry of an EARLIER file that names a condition has a `required` as well, true unless written so —
		// as the entry of a listed name does, below: a later file's `required: ~` is then "not given" over it,
		// where over no key at all it would stay a null the decode refuses (#1585; measured, v5.5.1). Not for the
		// later file's own map: docker compose gives `required` its default after the merge, so a later
		// `{condition: service_healthy}` that writes no `required` leaves an earlier `required: false` as it is. A
		// copy: the map is the file's own.
		out := make(map[string]any, len(x))
		for name, entry := range x {
			if m, ok := entry.(map[string]any); ok {
				if _, hasCond := m["condition"]; hasCond {
					if _, hasReq := m["required"]; !hasReq {
						withRequired := make(map[string]any, len(m)+1)
						for key, val := range m {
							withRequired[key] = val
						}
						withRequired["required"] = true
						entry = withRequired
					}
				}
			}
			out[name] = entry
		}
		return out, true
	case []any:
		out := make(map[string]any, len(x))
		for _, item := range x {
			name, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[name] = map[string]any{"condition": ConditionStarted, "required": true}
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
// path. Entries whose target can't be read are left alone: the bare `:` is one
// docker compose accepts with no target, and two of them are two entries, which
// a fold keyed on the empty target would leave as one, dropping a mount a file
// wrote (TestTwoVolumeEntriesWithNoTargetStayTwoAfterTheMerge holds this).
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

// mergeSeqBySecretTarget collapses `secrets` or `configs` entries that are mounted at the same target, keeping the
// LAST one at the FIRST one's place. An entry that writes no target is mounted at the default of its kind
// (`/run/secrets/<source>`, `/<source>`), so the short form `- k` and `{source: k}` are one entry, and one that writes the
// default out is the same one. An entry that is neither a name nor a mapping with a name is left alone.
func mergeSeqBySecretTarget(xs []any, kind string) []any {
	dir := "/run/secrets/"
	if kind == "configs" {
		dir = "/"
	}
	keyOf := func(x any) string {
		switch e := x.(type) {
		case string:
			return dir + e
		case map[string]any:
			// A target written, even an empty one, is its own place (docker compose does not read an empty
			// one as the default); an empty one has no key and is left alone.
			if target, ok := e["target"].(string); ok {
				return target
			}
			if source, ok := e["source"].(string); ok {
				return dir + source
			}
		}
		return ""
	}
	pos := map[string]int{}
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		k := keyOf(x)
		if k == "" {
			out = append(out, x)
			continue
		}
		if i, seen := pos[k]; seen {
			out[i] = x
			continue
		}
		pos[k] = len(out)
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

// MountTarget is mountTarget, exported for orchestrator's own volumes_from
// checks (checkVolumesFromRefs), which re-run this same classification once a
// gated service turns out to be active.
func MountTarget(v any) string { return mountTarget(v) }

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

// appendNew is how two files' lists of these fields become one: the later
// file's entries are added, except the ones the earlier file already has.
// What each file wrote on its own is left as it wrote it — a list naming one
// entry twice is that file's to answer for, and the pass that reads each file
// on its own says so before the merge is asked anything.
// docker compose says so for the first file and not for the later one:
// it folds a repeat the later file wrote on its own and goes on (`[20]` over
// `[16, 16]` leaves `20 16`), where the same repeat in the first file is
// refused (measured on v5.5.1) — a difference `docs/compatibility.md` names.
//
// Before this, only entries that arrived as strings were compared, so a
// number written in both files stayed twice over and `group_add` refused the
// pair as the same entry twice — as the file was read, which stops `up`,
// `ps`, `config` and `down` alike, so a project started from those files
// could not be brought down (measured 2026-09-23 on container 1.4.1; docker
// compose folds them and goes on, measured on v5.5.1).
func appendNew(base, over []any) []any {
	// As many as the earlier file already has, and no more: a later file
	// naming one entry twice still names it twice. Dropping every match
	// would swallow that file's own repeat — which `group_add` is refused
	// for before the merge runs, and which the other fields here keep as
	// the file wrote it.
	have := map[string]int{}
	for _, x := range base {
		if key, ok := scalarKey(x); ok {
			have[key]++
		}
	}
	out := append([]any{}, base...)
	for _, x := range over {
		if key, ok := scalarKey(x); ok && have[key] > 0 {
			have[key]--
			continue
		}
		out = append(out, x)
	}
	return out
}

// scalarKey is how a list entry is compared for being the same entry twice:
// what it reads as, for the kinds a list of these fields holds — a name, a
// port, a group. A float or a bool is left out, since folding one would not
// change what a reader sees: `ports`, `volumes_from` and `group_add` refuse
// it for its kind before anything asks whether it is there twice, and nothing
// reads `expose` at all (`up` says the field is ignored). `020` is YAML's octal
// and reads as 16, where the string `"020"` reads as those characters, so the
// two are not one entry — which is what docker compose does with them too
// (measured on v5.5.1).
func scalarKey(x any) (string, bool) {
	switch v := x.(type) {
	case string:
		return v, true
	case int:
		return strconv.Itoa(v), true
	case int64:
		// Where an int is 32 bits: yaml.v3 reads a number that fits an int
		// as one and a larger positive one as a uint64, so on a 64-bit
		// build nothing arrives here.
		return strconv.FormatInt(v, 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	}
	return "", false
}

// LoadFiles parses and merges one or more compose files, applying docker compose's
// multiple-`-f` semantics: later files override earlier ones (mappings merge by
// key, most sequences append, command/entrypoint replace).
func LoadFiles(paths []string, envFiles []string) (*Project, error) {
	return LoadFilesEnvDir(paths, envFiles, "")
}

// LoadFilesEnvDir is LoadFiles with the directory whose `.env` the project is
// read with given apart from the compose files' own. "" is the first file's
// directory, which is where `-f` and a file that was looked for have it. Files
// named by COMPOSE_FILE have it where COMPOSE_FILE was read from — the working
// directory — while their relative paths stay their own directory's, and their
// own directory's `.env` is read under the working directory's, for what that
// one does not set (measured on docker compose v5.5.1; see loadEnvLayers).
func LoadFilesEnvDir(paths []string, envFiles []string, envDir string) (*Project, error) {
	return loadFilesEnvDir(paths, envFiles, envDir, false)
}

// LoadFilesEnvDirSoft is LoadFilesEnvDir for the commands that take a project
// down: a value docker compose reads as another kind (a string that is not a
// number, one outside a key's bounds, a `scale` below zero) is kept on the project
// (CheckValueFaults) and the read goes on, where LoadFilesEnvDir fails on it. An
// earlier opossum passed such a value on, so a project may be running on it, and
// the commands that take it down have to read the file (#1468).
func LoadFilesEnvDirSoft(paths []string, envFiles []string, envDir string) (*Project, error) {
	return loadFilesEnvDir(paths, envFiles, envDir, true)
}

func loadFilesEnvDir(paths []string, envFiles []string, envDir string, soft bool) (*Project, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no compose file given")
	}
	abs, err := filepath.Abs(paths[0])
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(abs)
	if envDir == "" {
		envDir = baseDir
	}

	// Expand ${VAR} references before parsing, using the `.env` file in envDir
	// (or the given --env-file paths) overlaid by the process env.
	scope, err := loadEnvLayers(envDir, baseDir, envFiles, true)
	if err != nil {
		return nil, err
	}
	if soft {
		scope.values = &[]error{}
	}

	var doc interpolated
	var mergedTree map[string]any // the merged files, for the soft read to try again with what a nested null left out
	// The files the project was read from: the -f paths, with the files
	// each includes before it. What a failure in the merged document names.
	loaded := paths
	// A file of several YAML documents is read as that many files, each merged
	// into the ones before (docker compose's reading, measured, v5.5.1): its
	// documents come after each other in the list, ahead of the next `-f` file.
	type source struct {
		path string
		raw  []byte // the text of one document; nil is the file, read by loadUnit
		doc  int    // which document of the file raw is, from 1
	}
	var sources []source
	multi := false
	for _, path := range paths {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			sources = append(sources, source{path: path}) // loadUnit says what could not be read
			continue
		}
		docs, err := splitDocuments(path, raw, scope.values)
		if err != nil {
			return nil, err
		}
		if len(docs) == 1 {
			sources = append(sources, source{path: path})
			continue
		}
		multi = true
		for i, d := range docs {
			sources = append(sources, source{path: path, raw: d, doc: i + 1})
		}
	}
	// What a release before 0.38.0 read of these files — the first document of each,
	// merged — kept beside what is read now, to tell whether the project's name is the
	// same in both (#1483). Both are the loader's own merge of what it has read, so
	// what a `${VAR}`, an alias or an empty `name:` comes to is decided as it is for
	// the project.
	var mergedFirst map[string]any
	var documentNameFault error
	if len(paths) == 1 && !multi && !hasInclude(paths[0]) {
		// Single file: read the interpolated document directly (no merge
		// round-trip), so the positions a failure names are the ones in the file.
		raw, err := os.ReadFile(paths[0])
		if err != nil {
			return nil, fmt.Errorf("reading compose file: %w", err)
		}
		if err := checkOneDocument(paths[0], raw, scope.values); err != nil {
			return nil, err
		}
		if doc, err = interpolateDocument(raw, scope.lookup()); err != nil {
			return nil, fmt.Errorf("interpolating %s: %w", paths[0], err)
		}
		// `!reset` and `!override` are read here, on the file alone: a reset
		// key is gone and an override key is its value; extends reads them
		// against the service it extends (resolveSameFileExtends).
		var tags []mergeTag
		if doc, tags, err = takeMergeTags(doc); err != nil {
			return nil, fmt.Errorf("compose file %s: %w", paths[0], err)
		}
		// The file is read as written first — every shape check names the
		// service and the line the reader wrote — and only then is a
		// service that extends another resolved, on the plain tree. The
		// resolved tree decodes as the file did, but for a key that is
		// still nothing once extends is read, which the resolver refuses.
		if err := validateOne(paths[0], doc, nil, scope.values, ""); err != nil {
			return nil, err
		}
		if resolved, err := resolveSameFileExtends(doc, tags, paths[0], baseDir, scope.lookup(), scope.values); err != nil {
			return nil, err
		} else if resolved != nil {
			doc = *resolved
		}
	} else {
		// Several files, or one with `include:`: merge their YAML trees,
		// then render the merged result.
		var merged map[string]any
		loaded = nil
		mixed := map[string]mixedKey{} // the list-or-mapping keys that two of the files merged so far wrote
		for _, src := range sources {
			path := src.path
			// Every -f file belongs to the project whose directory is the
			// first file's: its include paths and its `extends: {file}`
			// count from there (docker compose, measured), not from its own.
			var (
				m     map[string]any
				files []string
				tags  []mergeTag
				err   error
			)
			if src.raw != nil {
				m, files, tags, err = loadUnitRaw(path, src.raw, baseDir, scope, merged, nil)
			} else {
				m, files, tags, err = loadUnit(path, baseDir, scope, merged, nil)
			}
			if err != nil {
				if src.doc > 1 && strings.Contains(err.Error(), "unknown anchor") {
					// docker compose lets an alias reach an anchor of an earlier
					// document (measured, v5.5.1); here each document is read by itself.
					return nil, fmt.Errorf("compose file %s: document %d refers to an anchor written in an earlier document — opossum does not carry an anchor across `---`: write the anchor again in this document, or put the documents in files of their own and pass them with `-f`", path, src.doc)
				}
				return nil, err
			}
			for _, f := range files {
				if !slices.Contains(loaded, f) {
					loaded = append(loaded, f)
				}
			}
			if multi && src.doc <= 1 {
				first := deepCopyTree(m).(map[string]any)
				if mergedFirst == nil {
					mergedFirst = first
				} else {
					applyMergeTags(mergedFirst, tags, true)
					mergedFirst = mergeMap(mergedFirst, first, "")
				}
			}
			if merged == nil {
				merged = m
				// What the files made so far is put to the keys and the required keys of a block as each file is added (docker compose, measured: the
				// files before it merged, the extends read, then the schema, the refusal naming the file just added).
				if err := checkStrictShapesStep(path, merged, scope.values); err != nil {
					return nil, err
				}
			} else {
				// A key this file tagged `!reset` or `!override` leaves the
				// earlier files' value out of the merge.
				held := hostEntriesHeld(merged)
				applyMergeTagsOver(merged, tags, true, mixed)
				dropGone(merged, mixed)
				markMixed(merged, m, mixed)
				merged = mergeMap(merged, m, "")
				if err := checkStrictShapesStep(path, merged, scope.values); err != nil {
					return nil, err
				}
				// An `extra_hosts` (or `build.extra_hosts`) that was a mapping, that this file writes, and that holds no entry once it is merged
				// in (its `!reset` took each one out) is refused naming this file (measured, v5.5.1: `must be a mapping`), where the
				// other mappings that are left empty are read.
				if err := hostEntriesLeftNone(path, held, m, merged, tags); err != nil {
					if scope.values == nil {
						return nil, err
					}
					*scope.values = append(*scope.values, err)
				}
				// What the file adds is checked in what it made of the
				// earlier files too, as docker compose checks each file's
				// merge: a key this file writes with nothing after it and
				// no earlier file gave a value to — a new network's
				// `internal:`, a `build.context:` no base has — is nothing
				// in the result, and refused naming this file.
				if err := validateMerged(path, merged, asMerged); err != nil {
					// A nested key with nothing after it that the merged files leave nothing under: a read that goes on past refused
					// values keeps the refusal and goes on (#1582), and the decode of the merged files reads them again without the key.
					// The check of each file has not always kept it (a `!override` of the mapping reads the earlier file's value as
					// the one that stays).
					if scope.values == nil || !strings.Contains(err.Error(), "got nothing") {
						return nil, err
					}
					*scope.values = append(*scope.values, err)
				}
				// A service that has nothing under it once this file is merged in — no earlier file wrote it, and this one writes it
				// bare — is refused naming this file, a later file that gives it a body does not take that away (measured, v5.5.1).
				if err := serviceWithoutBody(path, merged); err != nil {
					if scope.values == nil {
						return nil, err
					}
					*scope.values = append(*scope.values, err)
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
		if multi && mergedFirst != nil {
			before, after := mapProjectName(mergedFirst, baseDir), mapProjectName(merged, baseDir)
			if before != after {
				documentNameFault = &DocumentNameFault{Before: before, After: after}
			}
		}
		mergedTree = merged
		data, err := yaml.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("merging compose files: %w", err)
		}
		doc = interpolated{raw: data}
	}

	var f composeFile
	if err := doc.into(&f); err != nil {
		read := asWritten
		if len(loaded) > 1 || multi {
			read = asMerged
		}
		refusal := decodeErr(mergedName(loaded), read, blameService(doc, err))
		// A key with nothing after it in a nested field (`healthcheck: {test: ~}`, `build: {dockerfile: ~}`) that no earlier file
		// gave a value is refused by the decode of the merged files. A read that goes on past refused values has kept the
		// refusal when the file that wrote it was merged in (above, where the same decode ran on the files so far), and reads
		// the merged files again with those keys left out: the commands that take a project down have to read the file (#1582).
		retry, ok := interpolated{}, false
		if scope.values != nil && mergedTree != nil && strings.Contains(err.Error(), "got nothing") {
			if data, merr := yaml.Marshal(withoutNestedNulls(mergedTree)); merr == nil {
				retry, ok = interpolated{raw: data}, true
			}
		}
		var again composeFile
		if !ok || retry.into(&again) != nil {
			return nil, refusal
		}
		doc, f = retry, again
	}
	// What docker compose checks of the model it built out of every file:
	// the merged document, with `extends` read (#1462).
	var modelDoc struct {
		Services map[string]any `yaml:"services"`
		Secrets  map[string]any `yaml:"secrets"`
	}
	if err := doc.intoMarked(&modelDoc); err == nil {
		if err := checkBuildModel("compose file "+mergedName(loaded), modelDoc.Services, modelDoc.Secrets); err != nil {
			if scope.values == nil {
				return nil, err
			}
			*scope.values = append(*scope.values, err)
		}
		if err := checkModelBounds("compose file "+mergedName(loaded), modelDoc.Services); err != nil {
			if scope.values == nil {
				return nil, err
			}
			*scope.values = append(*scope.values, err)
		}
		if err := checkSecretModes("compose file "+mergedName(loaded), modelDoc.Services); err != nil {
			if scope.values == nil {
				return nil, err
			}
			*scope.values = append(*scope.values, err)
		}
		if err := checkPortHostIPs("compose file "+mergedName(loaded), modelDoc.Services); err != nil {
			if scope.values == nil {
				return nil, err
			}
			*scope.values = append(*scope.values, err)
		}
	}
	// A declared name starting with `.` takes the spelling a `type: volume`
	// mount of it has (dotVolumeKey), so the two meet by key.
	// The names are read before any is rewritten: a key added while ranging
	// over the map may be visited, and the rewritten one carries NUL bytes.
	declared := make([]string, 0, len(f.Volumes))
	for name := range f.Volumes {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	for _, name := range declared {
		if strings.Contains(name, "\x00") {
			return nil, fmt.Errorf("%s: volume name %q contains a NUL character — remove it", mergedName(loaded), name)
		}
		if !validVolumeName(name) {
			return nil, fmt.Errorf("%s: volume name %q %s", mergedName(loaded), name, volumeNameRule)
		}
		if key := dotVolumeKey(name); key != name {
			f.Volumes[key] = f.Volumes[name]
			delete(f.Volumes, name)
		}
	}
	if err := refuseBadNames(mergedName(loaded), "service", keysOf(f.Services)); err != nil {
		return nil, err
	}
	// A secret's and a config's name is refused by the commands that start or
	// print something (CheckDeclaredNames), and not here: an earlier opossum
	// passed such a name on as the file's mount path, so a project can be
	// running on one, and the commands that take it down have to read the file.
	// A service's is refused here — an earlier opossum refused it before it
	// created anything, so no project runs on one.
	nameFault := refuseBadNames(mergedName(loaded), "secret", keysOf(f.Secrets))
	if nameFault == nil {
		nameFault = refuseBadNames(mergedName(loaded), "config", keysOf(f.Configs))
	}
	// A variable name the project's `.env` (or an `--env-file`) refuses is kept the
	// same way, for the same reason: an earlier opossum passed it on, and the
	// commands that take a project down read this file first.
	if nameFault == nil {
		nameFault = scope.firstFault()
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

	envName, _ := scope.lookup()(projectNameVar)
	envProfiles, _ := scope.lookup()(profilesVar)
	p := &Project{
		Name:         f.Name,
		EnvName:      envName,
		EnvProfiles:  envProfiles,
		BaseDir:      baseDir,
		Services:     f.Services,
		Secrets:      f.Secrets,
		Configs:      f.Configs,
		Volumes:      f.Volumes,
		Networks:     f.Networks,
		Unsupported:  ignoredTopLevel(doc),
		nameFault:    nameFault,
		nameEmptied:  f.Name == "" && nameEmptiedIn(paths, scope.lookup()),
		docNameFault: documentNameFault,
		valueFault:   firstOf(scope.values),
	}
	// `ipam` is read for its subnets; the keys under it opossum reads past
	// (`driver`, an entry's `gateway`) are named the way the other
	// declaration keys are, by their full names.
	for _, name := range sortedKeys(f.Networks) {
		for _, k := range f.Networks[name].IPAM.Ignored() {
			p.Unsupported = append(p.Unsupported, fmt.Sprintf("networks.%s.%s", name, k))
		}
	}
	sort.Strings(p.Unsupported)
	// A declared network can't be both host-only (internal) and external: an
	// external network is used as-is, so `internal` would be silently dropped —
	// and with it the egress guarantee a caller likely set `internal` to get.
	for name, decl := range f.Networks {
		if decl.Internal && decl.External {
			return nil, fmt.Errorf("network %q: internal and external cannot both be set (an external network is used as-is)", name)
		}
	}
	// What `links:` and `network_mode: service:` name is a dependency, as docker compose reads it: before anything reads the
	// dependencies, and before `volumes_from` adds its own.
	if err := addLinkedDeps(f.Services, names); err != nil {
		return nil, err
	}
	// `volumes_from` is folded before anything reads a service's mounts:
	// the entries it borrows are mounts like any other from here on.
	if err := expandVolumesFrom(f.Services, names); err != nil {
		return nil, err
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
		if len(svc.Networks) > 0 && svc.NetworkMode == NetworkModeNone {
			return nil, fmt.Errorf("service %q: network_mode: none and networks: cannot both be set", name)
		}
		// A service that lists no networks is on `default`, so its declaration
		// is read as if the service had listed it.
		joined := svc.Networks
		if len(joined) == 0 && svc.NetworkMode != NetworkModeNone {
			joined = []string{DefaultNetworkDecl}
		}
		if len(joined) > 0 {
			for _, netName := range joined {
				decl, ok := f.Networks[netName]
				if !ok && netName == DefaultNetworkDecl {
					continue // there whether or not the file declares it
				}
				if !ok {
					return nil, fmt.Errorf("service %q references undefined network %q (declare it under top-level networks:)", name, netName)
				}
				// An external network reaches the runtime by its real name as
				// written; the rest become `<project>-<key>` with the key folded
				// by NetworkRuntimeKey, which can fold a key down to nothing.
				switch {
				case decl.External && decl.Name != "" && !ValidRuntimeNetworkName(decl.Name):
					return nil, fmt.Errorf("service %q: external network %q: name %q %s", name, netName, decl.Name, runtimeNetworkNameRule)
				case decl.External && decl.Name == "" && !ValidRuntimeNetworkName(netName):
					return nil, fmt.Errorf("service %q: external network %q is used by that name, which %s; set `name:` to the network's real name", name, netName, runtimeNetworkNameRule)
				case !decl.External && NetworkRuntimeKey(netName) == "":
					return nil, fmt.Errorf("service %q: network %q keeps no character a network name can hold once folded to what the container runtime (1.4.1) takes (lower-case a-z, 0-9, and `.`, `_` or `-` before the end) — rename the key", name, netName)
				}
			}
		}
		// Give bare container ports a host port (Apple's `container` requires one),
		// then drop duplicates once entries are normalized: this is the only fold
		// `ports` has, in one file and across several (e.g. base "3000" + override
		// "3000:3000" both normalize to "3000:3000"; the merge does not fold it).
		if len(svc.Ports) > 0 {
			// Two entries are the same published port when they name the same
			// host port, container port and protocol — and a spec that names
			// no protocol names tcp, as docker compose reads it. That is a
			// question about the entries, not about what is handed to the
			// runtime, so it is asked of a key and answered there: what is
			// kept is the first of them as `ports` read it — `/tcp` and all
			// or neither, with a protocol written in the short form settled
			// in lower case (normalizePortSpec) and a host port opossum
			// supplied already there.
			seen := map[string]string{} // key -> the spec kept for it
			ports := make([]string, 0, len(svc.Ports))
			auto := map[string]bool{}
			for _, p := range svc.Ports {
				n, mirrored := normalizePort(p)
				k := portKey(n)
				if kept, ok := seen[k]; ok {
					// A spec is only opossum's to move if EVERY declaration of it was
					// bare: `["3000", "3000:3000"]` names the host port explicitly in
					// one of them, so the user did choose it.
					auto[kept] = auto[kept] && mirrored
					continue
				}
				seen[k] = n
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
		// Collapse mounts sharing a target, which the merge does only for files
		// being combined: so a single file — or one
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

		// `shm_size` is read like `mem_limit` (`1gb`, `64M`, a bare byte
		// count) and carried as the byte count docker compose normalises it
		// to (`config` shows `"1073741824"`); the runtime takes bytes.
		if s := strings.TrimSpace(string(svc.ShmSize)); s != "" {
			b, err := parseMemoryBytes(s)
			if err != nil || int64(b) <= 0 {
				return nil, fmt.Errorf("service %q: shm_size %q is not a size — write it as 64M, 1gb, or a byte count", name, s)
			}
			svc.ShmSize = bareInt(strconv.FormatInt(int64(b), 10))
		}
		// `mac_address` reaches the runtime as `--network <name>,mac=XX:XX:XX:XX:XX:XX`
		// on the service's first network. The runtime refuses any other
		// spelling (`invalid MAC address format …, expected format:
		// XX:XX:XX:XX:XX:XX`) and docker compose refuses it when the
		// container is made (`invalid MAC address`); both are said here, at
		// load, and a service with no network to carry it is refused too.
		if svc.MacAddress != "" {
			hw, err := net.ParseMAC(svc.MacAddress)
			if err != nil || len(hw) != 6 {
				return nil, fmt.Errorf("service %q: mac_address %q is not a 48-bit MAC address — write six hex pairs, as in 02:42:ac:11:00:02", name, svc.MacAddress)
			}
			// The runtime takes only the colon form (`expected format:
			// XX:XX:XX:XX:XX:XX`); a dashed or dotted spelling is a MAC
			// address too, so it is carried in the form the runtime reads.
			svc.MacAddress = hw.String()
			if svc.NetworkMode == NetworkModeNone {
				return nil, fmt.Errorf("service %q sets mac_address with network_mode: none — there is no network interface to give the address to; remove one of them", name)
			}
		}
		// An `environment:` config takes the variable's value from the
		// project's environment — the shell over `.env`, the scope every
		// `${VAR}` in the file is read from — as docker compose does
		// (measured: a value only in `.env` is placed; an unset one is
		// refused when the container is made).
		for cname, cfg := range f.Configs {
			if cfg.EnvVar != "" {
				cfg.EnvValue, cfg.EnvSet = scope.lookup()(cfg.EnvVar)
				f.Configs[cname] = cfg
			}
		}
		// Every referenced config must be a declared top-level config that
		// opossum can place: not external. docker compose refuses the
		// undefined reference at `config` time (`service "web" refers to
		// undefined config x`) and the external one when the container is
		// created (`unsupported external config x`); both are refused here.
		for _, ref := range svc.Configs {
			cfg, ok := f.Configs[ref.Source]
			if !ok {
				return nil, fmt.Errorf("service %q refers to undefined config %q — declare it under top-level configs: with a file:, content: or environment:, or remove the reference", name, ref.Source)
			}
			if cfg.External {
				return nil, fmt.Errorf("service %q: external config %q is not supported — declare it with a file:, content: or environment:", name, ref.Source)
			}
			if strings.Contains(ref.Target, "..") {
				return nil, fmt.Errorf("service %q: config target %q must not contain `..`", name, ref.Target)
			}
		}

		// Every named volume a service mounts must be declared top-level, as
		// docker compose requires (`service "db" refers to undefined volume
		// dbdata`). Read past, a misspelling on either side made `up` create
		// and mount an empty volume under the misspelt name while the declared
		// one went unused — a database initialised fresh over data that was
		// meant to be there. A host path is a bind mount and a bare target is
		// an anonymous volume; neither is declared.
		for _, m := range svc.Volumes {
			kind, src := ClassifyMount(m)
			if kind == MountBind && homeOfAnotherUser(src) {
				return nil, fmt.Errorf("service %q: the mount source %q starts with `~` but not `~/` — `~` here means your own home only; write `~/%s` for a path under it, or an absolute path", name, src, strings.TrimPrefix(src, "~"))
			}
			if kind != MountNamed {
				continue
			}
			decl, ok := f.Volumes[src]
			shown := VolumeDisplayName(src)
			if !ok {
				return nil, fmt.Errorf("service %q refers to undefined volume %q — declare it under top-level volumes:, or write a host path (`./%s`) for a bind mount", name, shown, shown)
			}
			// The names that reach the runtime as written: a `name:` of its own,
			// or the key of an external volume without one (the rest become
			// `<project>_<key>`, which starts with the project's name). Checked
			// for the volumes a service mounts only: docker compose drops a
			// declaration nothing uses, and so does what opossum runs.
			if decl.Name != "" && !runtimeVolumeNamePattern.MatchString(decl.Name) {
				return nil, fmt.Errorf("service %q: volume %q: name %q %s", name, shown, decl.Name, runtimeVolumeNameRule)
			}
			if decl.Name == "" && decl.External && !runtimeVolumeNamePattern.MatchString(shown) {
				return nil, fmt.Errorf("service %q: external volume %q is used by that name, which %s; set `name:` to the volume's real name", name, shown, runtimeVolumeNameRule)
			}
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
			// The target is a file under /run/secrets, or an absolute path; docker compose lets any string through and its engine mounts it
			// there, which a path that is no file's cannot be (#1778).
			if err := checkSecretTarget(ref.Target); err != nil {
				return nil, fmt.Errorf("service %q: secret target %q %v", name, ref.Target, err)
			}
		}
	}
	if err := p.validateDeps(); err != nil {
		return nil, err
	}
	return p, nil
}

// volumeNamePattern is the names docker compose lets a volume have: it refuses
// a declaration outside it (`volumes additional properties 'a/b' not allowed`,
// measured on v5.5.0 for every printable ASCII character). Read past, the name
// went to the runtime as written — `-v demo_a/b:/y`, `-v demo_a,b:/y`.
var volumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// refuseBadNames refuses a declared name outside volumeNamePattern, in the
// first of them in sorted order, so a file with two is refused for the same one
// every time it is read. docker compose gives a service, a secret and a config
// the same rule as a volume (`services additional properties 'a b' not allowed`,
// measured on v5.5.1 for every printable ASCII character, the empty name and
// non-ASCII letters; a network has none). Read past, the name went on to a
// container name and a mount path as written.
func refuseBadNames(file, kind string, names []string) error {
	sort.Strings(names)
	for _, name := range names {
		if !validVolumeName(name) {
			return fmt.Errorf("%s: %s name %q %s", file, kind, name, volumeNameRule)
		}
	}
	return nil
}

// nameEmptiedIn is whether the project's `name:` came to nothing by a reference, read of the files given as docker compose
// reads them (measured, v5.5.1): a file that writes a name by a reference that comes to nothing makes the project's name
// empty, one that writes a name that is something puts it right, and one that writes `name: ""` or none leaves what the files
// before it made (so `n`, empty, `""` is the refusal, and empty, `n`, `""` is not). A project whose name no `-p` or
// COMPOSE_PROJECT_NAME gives is refused for it; an included file's `name:` is not the project's.
func nameEmptiedIn(paths []string, lookup varLookup) bool {
	emptied := false
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		one, err := interpolateDocument(raw, lookup)
		if err != nil {
			continue
		}
		if one.nameEmptied {
			emptied = true
		} else if writesAName(one) {
			emptied = false
		}
	}
	return emptied
}

// writesAName is whether the document's top-level `name:` is a scalar with something in it, as it is once its references are
// expanded.
func writesAName(one interpolated) bool {
	node := one.node
	if node == nil {
		var doc yaml.Node
		if yaml.Unmarshal(one.raw, &doc) != nil {
			return false
		}
		node = &doc
	}
	root := node
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "name" {
			v := root.Content[i+1]
			for v.Kind == yaml.AliasNode && v.Alias != nil {
				v = v.Alias
			}
			return v.Kind == yaml.ScalarNode && v.ShortTag() != "!!null" && v.Value != ""
		}
	}
	return false
}

// CheckEmptyName is the refusal of a project whose `name:` came to nothing by a reference (`name: ${P:-}`, P unset or empty):
// docker compose reads that as "project name must not be empty" and not as the directory's name, so the project it would
// have started has no name here either. It is for the callers that know whether `-p` or COMPOSE_PROJECT_NAME named the
// project, which make it no fault; the commands that take a project down name it and go on, as an earlier opossum started the
// project under the directory's name.
func (p *Project) CheckEmptyName() error {
	if p.nameEmptied && p.EnvName == "" {
		return errors.New("project name must not be empty — the `name:` of the compose file is made of variables that come to nothing; set one, write a name, or give `-p`")
	}
	return nil
}

// CheckDeclaredNames refuses a secret or a config whose name is outside the rule
// a service's and a volume's are held to (refuseBadNames), the first in sorted
// order, secrets before configs — and a variable name the project's `.env`, an
// included project's `.env` or an `--env-file` refuses (the line is read past, and
// the first is kept here).
//
// It is for the commands that start containers or print the project to call, not
// the loader: an earlier opossum ran such a project, and `down` and `destroy` have
// to read the file to take it down.
func (p *Project) CheckDeclaredNames() error { return p.nameFault }

// CheckValueFaults is the first value docker compose reads as another kind that a
// LoadFilesEnvDirSoft read went on past, or nil (and always nil after a read that
// failed on it instead). The commands that take a project down name it and go on.
func (p *Project) CheckValueFaults() error { return p.valueFault }

// CheckDocumentName is a refusal for the commands that take a project down when a
// file of several YAML documents names the project differently in a later document
// than in the first (#1483): 0.38.0 and later read every document, so the name is
// the later one's, and a release before it read the first document alone, so the
// project it started is the first one's. Which one a take-down means cannot be
// told from the file, and naming the wrong one takes down another project's
// containers, so the caller asks for -p. Nil where the documents agree.
func (p *Project) CheckDocumentName() error { return p.docNameFault }

// DocumentNameFault is what CheckDocumentName returns: the name a release before
// 0.38.0 gave the project (the first document of each file, merged) and the name it
// has now (every document), sanitised as project names are.
type DocumentNameFault struct{ Before, After string }

func (e *DocumentNameFault) Error() string {
	return fmt.Sprintf("the project is named %q where only the first YAML document of each file is read (an opossum before 0.38.0 read no other), and %q where every document is (0.38.0 and later): which one a take-down means cannot be told from the file", e.Before, e.After)
}

// mapProjectName is the project name a merged document comes to: its `name:` when
// that is a non-empty string, the directory's otherwise, sanitised.
func mapProjectName(m map[string]any, dir string) string {
	if name, ok := m["name"].(string); ok && name != "" {
		return SanitizeName(name)
	}
	return SanitizeName(filepath.Base(dir))
}

// firstOf is the first error a sink recorded, or nil where there is no sink.
func firstOf(sink *[]error) error {
	if sink == nil || len(*sink) == 0 {
		return nil
	}
	return (*sink)[0]
}

// keysOf is the keys of a map, in no order.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// volumeNameRule is what a refused volume name is told, after the name.
const volumeNameRule = "can only contain letters, digits, `.`, `_` and `-` (docker compose refuses it as well)"

// runtimeVolumeNamePattern is the names a volume can be created with: container
// 1.4.1 refuses anything else (`invalid volume name … must match
// ^[A-Za-z0-9][A-Za-z0-9_.-]*$`), and the docker engine refuses the same set.
// docker compose's `config` checks neither a `name:` nor an external key, so a
// file it loads can still name a volume no runtime can make.
var runtimeVolumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// runtimeVolumeNameRule is what a name the runtime cannot create is told.
const runtimeVolumeNameRule = "is not a name a volume can be created with — it has to start with a letter or digit, followed by letters, digits, `_`, `.` or `-` (the container runtime and the docker engine both refuse it)"

// runtimeNetworkNamePattern is the names a network can be created with:
// container 1.4.1 refuses (`invalid network name`) upper case, characters
// outside a-z, 0-9, `.`, `_` and `-`, a last character that is not a letter or
// digit, and more than MaxRuntimeNetworkNameLen characters (measured with
// `container network create`).
var runtimeNetworkNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// MaxRuntimeNetworkNameLen is the longest network name container 1.4.1 creates.
const MaxRuntimeNetworkNameLen = 63

// runtimeNetworkNameRule is what a network name the runtime cannot create is told.
const runtimeNetworkNameRule = "is not a name a network can be created with — the container runtime (1.4.1) takes lower-case letters, digits, `.`, `_` and `-`, starting and ending with a letter or digit, at most 63 characters"

// ValidRuntimeNetworkName reports whether the runtime can create a network by name.
func ValidRuntimeNetworkName(name string) bool {
	return len(name) <= MaxRuntimeNetworkNameLen && runtimeNetworkNamePattern.MatchString(name)
}

var networkKeyOutsideRuntime = regexp.MustCompile(`[^a-z0-9._-]`)

// NetworkRuntimeKey is what a declared network's key contributes to its
// runtime name, `<project>-<key>`: lower case, each character the runtime
// refuses written as `-`, and the trailing `-`, `.` and `_` it refuses
// dropped. docker compose runs a key like `backEnd` or `a+b` as written
// (its engine takes them), so folding keeps such a file running; a key the
// runtime already takes is unchanged, and so is its network.
func NetworkRuntimeKey(key string) string {
	return strings.TrimRight(networkKeyOutsideRuntime.ReplaceAllString(strings.ToLower(key), "-"), "-._")
}

// DefaultNetworkKey is the key part of the network a service with no
// `networks:` joins, `<project>-net`.
const DefaultNetworkKey = "net"

// DefaultNetworkDecl is the key under which a compose file declares that
// network, and lists it in a service's `networks:`. It is there whether or not
// the file declares it: a service with no `networks:` and a service that lists
// `default` are on the one network, and the declaration, when there is one
// (`name`, `internal`, `external`, `labels`, `ipam`), is that network's (measured
// on docker compose v5.5.1).
const DefaultNetworkDecl = "default"

// NetworkOwnName is the runtime name of a declared network that carries a
// `name:` of its own, and whether it does. docker compose creates such a network
// under that name as written, with no project in front of it, and a network
// under a name can be shared with another project or another file. External
// networks are named by `name:` too, but they are not created here.
func (d NetworkDecl) NetworkOwnName() (string, bool) {
	if d.External || d.Name == "" {
		return "", false
	}
	return d.Name, true
}

// sameNetworkDecl reports whether two declarations would make the same
// network: the same host-only flag, subnets and labels — the labels as they
// read (an entry written twice under one key is the later one), as docker
// compose reads them.
func sameNetworkDecl(a, b NetworkDecl) bool {
	return a.Internal == b.Internal && a.IPAM.Subnet == b.IPAM.Subnet && a.IPAM.SubnetV6 == b.IPAM.SubnetV6 &&
		maps.Equal(labelMap(a.Labels), labelMap(b.Labels))
}

// CheckNetworkKeys refuses the networks the services join that would come to one
// runtime network although the file means them apart, or the other way round:
//
//   - two keys folding to the same runtime network — `backEnd` and `backend`,
//     or a key folding to the default network's `net` while a service is on
//     that — since docker compose gives each its own network and one shared
//     network would join services the file keeps apart;
//   - two networks that carry the same `name:` but are declared differently
//     (docker compose makes one network of them and does not say whose
//     `internal:` or labels it took), or a network whose `name:` is another's
//     derived `<project>-<key>`.
//
// Two networks with the same `name:` and the same declaration are one network,
// as in docker compose. It is for the commands that create networks to call,
// not the loader: a key `net` beside the default network ran before this check
// existed (the two services shared one network), and `down` and `destroy`
// have to read that file to clean such a project up.
func (p *Project) CheckNetworkKeys() error {
	type taker struct {
		key   string      // the declared key; "" for the default network
		named bool        // the runtime name is the declaration's own `name:`
		decl  NetworkDecl // the default network's own declaration, if the file has one, for the default network
	}
	shown := func(key string) string {
		if key == "" {
			return DefaultNetworkDecl
		}
		return key
	}
	owner := map[string]taker{} // runtime network name -> who took it first
	take := func(real string, t taker) error {
		first, taken := owner[real]
		if !taken {
			owner[real] = t
			return nil
		}
		if first.key == t.key {
			return nil
		}
		// Two keys that name one network: docker compose makes one of them.
		if (first.named || first.key == "") && (t.named || t.key == "") && sameNetworkDecl(first.decl, t.decl) {
			return nil
		}
		if first.key == "" || (t.key != "" && t.key < first.key) {
			first, t = t, first
		}
		// first is now a declared key, and t the default network or a later key.
		switch {
		case first.named && t.named:
			return fmt.Errorf("networks %q and %q both have `name: %s` and are declared differently (`internal`, `labels` or `ipam`) — docker compose makes one network of them and does not say whose declaration it took; give them one declaration, or different names", shown(first.key), shown(t.key), real)
		case first.named && t.key == "":
			return fmt.Errorf("network %q has `name: %s`, which is the network services without `networks:` join, and that network is declared differently (`internal`, `labels` or `ipam`) — give it a name of its own, or declare the same", first.key, real)
		case first.named:
			return fmt.Errorf("network %q has `name: %s`, which is the runtime name of network %q (`<project>-%s`) — rename one of them", shown(first.key), real, shown(t.key), NetworkRuntimeKey(t.key))
		case t.named:
			return fmt.Errorf("network %q has `name: %s`, which is the runtime name of network %q (`<project>-%s`) — rename one of them", shown(t.key), real, shown(first.key), NetworkRuntimeKey(first.key))
		case t.key == "":
			return fmt.Errorf("network %q becomes the runtime network `<project>-%s`, which is the network services without `networks:` join — rename the key", first.key, NetworkRuntimeKey(first.key))
		}
		return fmt.Errorf("networks %q and %q both become the runtime network `<project>-%s` (the container runtime (1.4.1) takes network names in lower case, so each character it refuses is written `-` and a trailing `-`, `.` or `_` is dropped) — rename one of them", first.key, t.key, NetworkRuntimeKey(first.key))
	}
	// The default network: the one a service with no `networks:` joins, and the one
	// `default` names. External, it is somebody else's and is not made.
	takeDefault := func() error {
		decl := p.Networks[DefaultNetworkDecl]
		if decl.External {
			return nil
		}
		if own, named := decl.NetworkOwnName(); named {
			return take(own, taker{named: true, decl: decl})
		}
		return take(p.Name+"-"+DefaultNetworkKey, taker{decl: decl})
	}
	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := p.Services[name]
		if svc.NetworkMode == NetworkModeNone {
			continue
		}
		if len(svc.Networks) == 0 {
			if err := takeDefault(); err != nil {
				return err
			}
			continue
		}
		for _, key := range svc.Networks {
			if key == DefaultNetworkDecl {
				if err := takeDefault(); err != nil {
					return err
				}
				continue
			}
			decl := p.Networks[key]
			if decl.External {
				continue
			}
			if own, named := decl.NetworkOwnName(); named {
				if err := take(own, taker{key: key, named: true, decl: decl}); err != nil {
					return err
				}
				continue
			}
			if err := take(p.Name+"-"+NetworkRuntimeKey(key), taker{key: key}); err != nil {
				return err
			}
		}
	}
	return nil
}

// validVolumeName reports whether name is one docker compose lets a volume have.
func validVolumeName(name string) bool { return volumeNamePattern.MatchString(name) }

// IsHostPath reports whether a volume mount's source is a host path — the
// forms mounted as a bind rather than a named volume. The rule is docker
// compose's: a source that starts with `/`, `.` or `~` is a path (so `.hidden`
// and `~` are paths, not volume names), everything else names a volume. The
// orchestrator classifies the same string with this one function, so what
// loads as a bind is what runs as one. A `~` followed by anything but `/` is
// a path here too, but one the loader refuses (see homeOfAnotherUser): docker
// reads `~bob/h` as `$HOME/bob/h`, which nobody means.
func IsHostPath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || relativeHostPath(s)
}

// homeOfAnotherUser reports a `~name…` source: `~` followed by something
// other than `/`. docker compose resolves it to `$HOME/name…` — not that
// user's home, not a directory called `~name` — a reading nobody intends, so
// it is refused with the two spellings that mean what they say.
func homeOfAnotherUser(s string) bool {
	return strings.HasPrefix(s, "~") && s != "~" && !strings.HasPrefix(s, "~/")
}

// mountSource is the source half of a `source:target[:mode]` mount, or "" for
// an anonymous volume written as its target alone.
func mountSource(m string) string {
	if i := strings.Index(m, ":"); i > 0 {
		return m[:i]
	}
	return ""
}

// MountKind is what a service's volume entry (already in the loader's
// `source:target[:mode]` or bare-target spelling) refers to.
type MountKind int

const (
	MountAnonymous MountKind = iota // a bare target: a volume named for the service and path
	MountBind                       // a host path at a container path
	MountNamed                      // a named volume, declared under top-level volumes:
)

// ClassifyMount says what a volume entry refers to, and for a bind or a named
// volume, its source. The loader requires a declaration for exactly the
// entries this calls named, and the orchestrator mounts an entry as what this
// calls it — one judgement on both sides, so a source that loads as a bind
// runs as one and one that needs a declaration is the one mounted by name
// (created under it, or, declared external, looked up under its real name).
func ClassifyMount(entry string) (MountKind, string) {
	src := mountSource(entry)
	switch {
	case src == "":
		return MountAnonymous, ""
	case IsHostPath(src):
		return MountBind, src
	}
	return MountNamed, src
}

// expandVolumesFrom folds each service's `volumes_from` into its Volumes, the
// way docker compose (v5.5.1, measured 2026-09-18) mounts them: the named
// service's `volumes:` entries come first and this service's own after; an
// entry at a path this service mounts itself (its `volumes:`, or its
// `tmpfs:`, whose targets are compared as written) is not borrowed, and
// where two holders mount one path the later's wins (collapseMountsByTarget
// keeps the later entry); `holder:ro` mounts as `holder` does (the suffix
// changes nothing there either); what a holder borrowed itself is borrowed
// on (A from B from C brings C's to A); and the holder becomes a dependency
// — `depends_on: {holder: {condition: service_started}}` — unless one is
// written. A service that is not there, or a `container:<name>` entry, whose
// mounts live outside the compose file, is refused in docker compose's words;
// a holder's anonymous volume (`- /data`) at a path this service does not
// mount itself, which docker compose shares and this would instead name after
// the service that mounts it — a second volume, not the one shared — is
// refused too, as a permanent limit on what this can mount, not a timing
// question. All three are refused unless the service naming the ref carries
// `profiles:`, whether it ever runs being decided per invocation rather than
// known here — deferred to command time the same way an undefined
// depends_on target is (#1094, #1156): checked again once the service turns
// out to be active (orchestrator.checkVolumesFromRefs). The anonymous-volume
// one is the exception to its own deferral: a service that lends its own
// anonymous-volume conflict on to a further service (isHolder, below) is
// refused right away regardless of its own gate — deferring it there would
// drop the fault for good once the further service asks, not just delay it.
//
// A cycle among these
// entries is left to the commands that order the services, as one among
// `depends_on` is (the holder is one). A named volume borrowed is then one two
// services share, which the runtime attaches to one container at a time —
// the shared-volume note (OPSM-102) reads the folded mounts and says so.
func expandVolumesFrom(services map[string]*Service, names []string) error {
	const visiting, done = 1, 2
	state := map[string]int{}
	// isHolder names every service some other service's volumes_from
	// borrows from — computed once, over every service's own list, not just
	// names: a holder can be lent from without itself being asked for. A
	// service in here is not a leaf consumer of what it borrows, so its own
	// anonymous-volume conflicts are never deferred (below): were one held
	// back because THIS service is gated, borrowing it further on (through
	// this same holder, to whoever borrows from it) would silently drop the
	// entry from what that borrower sees, with no later look to catch it —
	// expand's memoizing (state[name] = done) makes this service's Volumes
	// permanent once computed, so a later borrower reads the entry as never
	// having existed rather than as pending. A leaf nobody borrows from has
	// no such further borrower to mislead, so its own gating alone decides.
	isHolder := map[string]bool{}
	for _, svc := range services {
		for _, ref := range svc.VolumesFrom {
			// A container: entry names no service of this file's at all — a
			// service actually called "container" is not lent from just
			// because some other ref happens to read "container:whatever".
			if strings.HasPrefix(ref, "container:") {
				continue
			}
			holder, _, _ := strings.Cut(ref, ":")
			isHolder[holder] = true
		}
	}
	var expand func(name string) error
	expand = func(name string) error {
		svc := services[name]
		switch state[name] {
		case done:
			return nil
		case visiting:
			// A cycle is not refused here. The holder is a dependency (below), so
			// the cycle is one among `depends_on` and is read where they are: by
			// the commands that put the services in order, over the services they
			// read — a corner behind a profile that is off is not one of those, as
			// in docker compose. What is folded on the way round is whatever the walk
			// had reached, the same every time. A command that reads the cycle
			// refuses it before it uses the mounts, with one exception that holds
			// with or without a cycle: a holder behind a profile that is off, whose
			// dependency is written `required: false`, is not read and is borrowed
			// from all the same (a known difference, in the `volumes_from` row).
			return nil
		}
		state[name] = visiting
		svc.OwnVolumes = svc.Volumes
		if len(svc.VolumesFrom) == 0 {
			state[name] = done
			return nil
		}
		var borrowed []string
		// This service's `tmpfs:` at a path takes it before a borrowed mount
		// does (docker compose mounts the tmpfs; container 1.4.1 would put
		// the volume on top of both if both were passed).
		// A `tmpfs:` target is compared as written (`/x/` is not `/x` there,
		// as the tmpfs row of the compatibility table says), a mount's with
		// its trailing `/` dropped.
		ownTmpfs := map[string]bool{}
		for _, t := range svc.Tmpfs {
			ownTmpfs[strings.SplitN(t, ":", 2)[0]] = true
		}
		ownAt := map[string]bool{}
		for _, v := range svc.OwnVolumes {
			ownAt[mountTarget(v)] = true
		}
		// nocopy is the winning mount's: a holder's `nocopy` at a path this
		// service's own mount, or a later holder's, takes instead does not
		// come along.
		nocopyAt := map[string]bool{}
		// So is a mount of a part of a volume: the holder's, at a path the
		// winning mount comes from — nil where the holder mounts it whole.
		subpathAt := map[string]*VolumeSubpath{}
		for _, ref := range svc.VolumesFrom {
			holder, _, _ := strings.Cut(ref, ":")
			// A `volumes_from` naming a container outside the file, or a
			// service the file does not define, is refused here — except
			// when svc itself carries `profiles:`, since whether it ever
			// runs is decided per invocation and not known yet here: that
			// case is deferred to command time (orchestrator.checkVolumesFromRefs),
			// the same way an undefined depends_on target is (#1094, #1156).
			// A service with no `profiles:` is always active, so its
			// volumes_from is always this run's business and is still
			// checked here.
			if strings.HasPrefix(ref, "container:") {
				if len(svc.Profiles) != 0 {
					continue
				}
				return fmt.Errorf("service %q: volumes_from %q names a container outside this compose file, whose mounts cannot be read here — name the service that mounts them, or write the mounts under `volumes:`", name, ref)
			}
			h, ok := services[holder]
			if !ok || h == nil {
				if len(svc.Profiles) != 0 {
					continue
				}
				return fmt.Errorf("service %q depends on undefined service %q: invalid compose project", name, holder)
			}
			if err := expand(holder); err != nil {
				return err
			}
			hNoCopy := map[string]bool{} // NoCopy holds targets as mountTarget spells them
			for _, t := range h.NoCopy {
				hNoCopy[t] = true
			}
			hSubpath := map[string]VolumeSubpath{}
			for _, v := range h.VolumeSubpaths {
				hSubpath[mountTarget(v.Source+":"+v.Target)] = v
			}
			for _, entry := range h.Volumes {
				target := mountTarget(entry)
				// Not borrowed where this service mounts the path itself.
				if ownTmpfs[target] || ownAt[target] {
					continue
				}
				if kind, _ := ClassifyMount(entry); kind == MountAnonymous {
					// Deferred to command time when svc is gated (see the
					// deferral above, for a missing holder or a container:
					// entry) — this one entry only, not the rest of the ref:
					// the other targets borrowed from the same holder here are
					// always correct, whether or not svc turns out to run.
					// Not deferred when svc is itself a holder (isHolder):
					// svc.Volumes is about to be memoized and read by whoever
					// borrows from svc in turn, and a deferred entry does not
					// travel with it — a later borrower would see it as never
					// having existed, not as pending. Refusing here, whether
					// or not svc itself ever runs, is conservative rather
					// than silently wrong.
					if len(svc.Profiles) != 0 && !isHolder[name] {
						continue
					}
					return fmt.Errorf("service %q: volumes_from %q would share %s's anonymous volume at %s, which is named after the service that mounts it and would be a second volume here (docker compose shares the one) — declare it under `volumes:` and mount it by name in both", name, ref, holder, target)
				}
				borrowed = append(borrowed, entry)
				nocopyAt[target] = hNoCopy[target]
				if v, ok := hSubpath[target]; ok {
					v.Entry = 0 // the holder's entry, not one of this service's
					subpathAt[target] = &v
				} else {
					subpathAt[target] = nil
				}
			}
			if !slices.ContainsFunc(svc.DependsOn, func(d Dependency) bool { return d.Name == holder }) {
				svc.DependsOn = append(svc.DependsOn, Dependency{Name: holder, Condition: ConditionStarted})
			}
		}
		for _, v := range svc.OwnVolumes {
			delete(nocopyAt, mountTarget(v))
		}
		for _, target := range sortedKeys(nocopyAt) {
			if nocopyAt[target] && !slices.Contains(svc.NoCopy, target) {
				svc.NoCopy = append(svc.NoCopy, target)
			}
		}
		for _, target := range sortedKeys(subpathAt) {
			if v := subpathAt[target]; v != nil {
				svc.VolumeSubpaths = append(svc.VolumeSubpaths, *v)
			}
		}
		svc.Volumes = collapseMountsByTarget(append(borrowed, svc.Volumes...))
		state[name] = done
		return nil
	}
	for _, name := range names {
		if err := expand(name); err != nil {
			return err
		}
	}
	return nil
}

// validateDeps ensures every depends_on target exists, uses a known condition,
// and — for service_healthy — actually defines a (non-disabled) healthcheck.
//
// A dependency named by a service that itself carries `profiles:` is the one
// exception: whether that service ever runs is decided per invocation (a
// `--profile` flag, COMPOSE_PROFILES, or being named), which is not known yet
// here — docker compose does not even look at a gated-inactive service's
// depends_on (measured, #1094). Refusing it unconditionally, as this used to,
// is a divergence of opossum's own for exactly this reason: it is checked
// again once profiles are resolved and the service turns out to be active
// (orchestrator.checkProjectLoads / validateProfileDeps), in the active set's
// own words. A service with no `profiles:` is always active, so its
// dependencies are always this run's business and are still checked here.
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
				if len(svc.Profiles) != 0 {
					continue // deferred: see whether name ever activates (above)
				}
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
// mirrors the container port, as it does for a host port of `0`. Specs that already name a host port
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
	// A host port of 0 asks the engine for a free port (docker compose reads `0:80` as that: `published: "0"`), which the runtime
	// refuses (`invalid publish host port range: 0`, container 1.5.0): left to the runtime to choose it is not, so it is
	// the same as a host port left out — the container port, moved if it is taken.
	if parts := strings.Split(s, ":"); len(parts) >= 2 && hostPortIsZero(parts[len(parts)-2]) {
		parts[len(parts)-2] = parts[len(parts)-1]
		return strings.Join(parts, ":") + proto, true
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

// hostPortIsZero reports whether a host port, as written, is the port 0: `0`, and the spellings docker compose
// reads as the same number (`00`, `000`, and the range of one `0-0` or `00-00`; measured, v5.5.1: published
// "0"). A range that goes on past 0 (`0-1`) is not: it is left as written, and the runtime refuses it.
func hostPortIsZero(host string) bool {
	lo, hi, isRange := strings.Cut(host, "-")
	zero := func(n string) bool { return n != "" && strings.Trim(n, "0") == "" }
	return zero(lo) && (!isRange || zero(hi))
}

// portKey is how two published-port specs are compared for being the same
// port. A spec carries its protocol after the last `/`, and one that carries
// none is tcp — docker compose's default, and the runtime's — so `8080` and
// `8080/tcp` are one port there and here. The case of a protocol written in
// the short form is settled before this (normalizePortSpec), where docker
// compose settles it. Only the comparison is made on
// this: `8080/tcp` written alone still reaches the runtime with its `/tcp`,
// and `8080` alone still reaches it without, because a spec the file wrote is
// what is published.
func portKey(norm string) string {
	if strings.LastIndexByte(norm, '/') >= 0 {
		return norm
	}
	return norm + "/tcp"
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
func validateOne(path string, one interpolated, earlier map[string]any, values *[]error, only string) error {
	override := earlier != nil
	doc := one.node
	if doc == nil {
		var parsed yaml.Node
		if err := yaml.Unmarshal(one.raw, &parsed); err != nil {
			return decodeErr(path, asWritten, err)
		}
		doc = &parsed
	}
	spreadSequenceMerges(doc)
	// A file whose block merges itself is refused where a project is read, as docker compose refuses it. A command that takes a project down reads it all the same
	// with the merge key that brings the block in by itself taken out, and keeps the refusal (#1475), as it does for an alias that holds its own block.
	if cyc := selfMerging(doc, values != nil); cyc != nil {
		err := fmt.Errorf("compose file %s: %w", path, errSelfMerging(cyc))
		if values == nil {
			return err
		}
		*values = append(*values, err)
	}
	asRead := doc
	if err := checkTopLevel(path, documentRoot(doc), earlier, override); err != nil {
		return err
	}
	if err := checkExtensionRepeats(path, one.written); err != nil {
		return err
	}
	if override {
		doc = withoutNotGivenBacked(doc, earlier)
	} else {
		doc = withoutNotGivenInExtending(doc)
	}
	doc = withoutUntakenDeploy(doc, only)
	doc = withoutNullInUntaken(doc, only)
	doc = withoutNotGivenInTaken(doc, only)
	doc = withoutDeferredTypes(doc, only)
	doc = withoutNegativeRetries(doc)
	var f composeFile
	if err := doc.Decode(&f); err != nil {
		refusal := decodeErr(path, asWritten, blameService(interpolated{node: doc, raw: one.raw}, err))
		if values == nil || !override {
			return refusal
		}
		// Taking a project down reads what an earlier opossum started on, and that opossum
		// took a key with nothing after it in a later file as "not given" wherever it stood
		// (#1589): the refusal is kept for what reports the faults, and the file is read as
		// it was.
		*values = append(*values, refusal)
		doc = withoutNotGiven(asRead)
		f = composeFile{}
		if err := doc.Decode(&f); err != nil {
			return decodeErr(path, asWritten, blameService(interpolated{node: doc, raw: one.raw}, err))
		}
	}
	// The decode has answered for the blocks it reads into a typed shape; what it
	// took as it came is asked now, so that nothing is said twice.
	// A key written twice in a mapping the decode takes as it comes, and an alias
	// that refers to its own block: an opossum before 0.38.0 read both (#1475).
	if err := checkRepeatedKeys(path, one.written, true); err != nil {
		if values == nil {
			return err
		}
		*values = append(*values, err)
	}
	// An infinity or a NaN written as a number, anywhere in the file: docker compose
	// cannot put one in its model and refuses the file (#1507). A file that is only
	// extended from (`only` names the service) is asked in that service alone, the
	// one part of it docker compose takes (#1510).
	if err := checkNonFiniteNumbers(path, one.written, only); err != nil {
		if values == nil {
			return err
		}
		*values = append(*values, err)
	}
	// The keys the decode took as they came are asked what they hold.
	var generic struct {
		Services map[string]any `yaml:"services"`
	}
	if err := doc.Decode(&generic); err == nil {
		if err := checkServiceShapes(path, generic.Services, values, only); err != nil {
			return err
		}
	}
	// A file that names one `group_add` entry twice is refused here, where
	// the entries are the ones that file wrote: the positions are its own,
	// and a file beside it neither adds to them nor takes them away. The
	// merged document is not asked again — merging writes the tree back
	// out, so `0x10` arrives there as `16` and would read as a repeat of a
	// `"16"` the same file wrote in another entry, refusing at the load a
	// file that is refused before the service starts when it is read
	// alone. That refusal stops `down`, which left a project started from
	// those files with no way to come down.
	if err := checkGroupAddRepeats(path, &f); err != nil {
		return err
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

// checkExtensionRepeats refuses a key written twice in one mapping inside an
// `x-` extension, at any depth. docker compose refuses the file wherever a
// mapping repeats a key (`mapping key "a" already defined at line 2`); the
// decoder says the same for a block it reads into a typed shape, and never sees
// an extension, whose contents nobody interprets — so in a single file a repeat
// there was accepted, where docker compose refuses the file. It is asked before
// the decode, which answers for the typed blocks in its own words; the rest of the
// file is asked by checkRepeatedKeys once the decode has passed.
func checkExtensionRepeats(path string, written []byte) error {
	return checkRepeatedKeys(path, written, false)
}

// nonFiniteRE is what YAML 1.1 reads as an infinity or a NaN: `.inf`, `.Inf`, `.INF`,
// their signed forms, and `.nan`, `.NaN`, `.NAN`.
var nonFiniteRE = regexp.MustCompile(`^[-+]?\.(?i:inf)$|^\.(?i:nan)$`)

// checkNonFiniteNumbers refuses a `.inf`, `-.inf` or `.nan` written as a number in a
// file, in a value, a list item, a mapping key or an `x-` extension alike: docker
// compose reads it as a float and cannot write it into the model it checks
// (`json: unsupported value: +Inf`), so the whole file is refused (measured,
// v5.5.1). A string is not one — quoted, or tagged `!!str`, or a `${VAR}` that
// expands to it, since docker compose expands after it reads — and a number too big
// for a float (`1.0e999`) is read. A value tagged `!!float` that the reader cannot
// take as a float (`!!float abc`, `!!float inf`, `!!float 1.0e999`) is refused too.
//
// Asked of the file as written (`written`), as checkRepeatedKeys is, so that the line
// it names is the reader's. A text that does not parse is left to the decode.
func checkNonFiniteNumbers(path string, written []byte, only string) error {
	if len(written) == 0 {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(written, &doc); err != nil {
		return nil
	}
	// finite: also ask for an infinity or a NaN. A value tagged `!!float` that is no
	// float is a fault of the read, so docker compose finds it anywhere in a file it
	// reads; an infinity is a fault of the model it builds, so in a file that is only
	// extended from it is asked of the service taken, once its own extends is resolved
	// (nonFiniteInService), and not here (measured, v5.5.1).
	var walk func(n *yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n == nil || n.Kind == yaml.AliasNode {
			return nil // the anchor it points at is walked where it stands
		}
		if n.Kind == yaml.ScalarNode && n.Tag == "!!float" {
			if nonFiniteRE.MatchString(n.Value) {
				if only == "" {
					return fmt.Errorf("compose file %s: line %d: %s is a number docker compose cannot read — infinity and NaN cannot go into its model; quote it to keep it a string", path, n.Line, n.Value)
				}
				return nil
			}
			// A value tagged `!!float` that is not one (`!!float abc`, `!!float inf`, a
			// number past the float range) is refused by the reader the way docker
			// compose refuses it (#1509); the decode is the same reader's answer.
			var f float64
			if err := n.Decode(&f); err != nil {
				return fmt.Errorf("compose file %s: line %d: %q is tagged !!float and is not a number docker compose can read", path, n.Line, n.Value)
			}
		}
		// The same for the other tags that name a plain value: `!!int abc`, `!!bool yes`,
		// `!!null x`, `!!timestamp x`, `!!binary !!!` are refused by docker compose as
		// `!!float abc` is (#1520; measured, v5.5.1, 408 tag-and-value forms, the decode
		// agrees with docker compose on every one). A value the reader resolved by its
		// look is a value of its tag already, so only a tag that was written is asked.
		if n.Kind == yaml.ScalarNode && n.Style&yaml.TaggedStyle != 0 {
			switch n.Tag {
			case "!!int", "!!bool", "!!null", "!!timestamp", "!!binary":
				var v any
				err := n.Decode(&v)
				// `!!int -0` decodes here and is refused by docker compose, which reads it as
				// a float (#1524; `+0`, `-00`, `-0x0` and `-0_0` are read by both).
				if err == nil && n.Tag == "!!int" && n.Value == "-0" {
					err = fmt.Errorf("negative zero")
				}
				if err != nil {
					return fmt.Errorf("compose file %s: line %d: %q is tagged %s and is not a value docker compose can read as one", path, n.Line, n.Value, n.Tag)
				}
			}
		}
		if n.Kind == yaml.MappingNode {
			// A mapping key that is not a string (`1: a`, `true: a`, `!!int 1: a`, a
			// list or a mapping as a key) is refused by docker compose wherever it
			// stands (#1525; measured, v5.5.1). The `<<` merge key is a string here, and
			// an infinity in a file read whole is left to the number check below, which names it
			// as one (in a file only extended from, that check asks the service taken alone, and
			// docker compose refuses an infinity as a key anywhere in it).
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				for k.Kind == yaml.AliasNode && k.Alias != nil {
					k = k.Alias
				}
				// The `<<` merge key takes a mapping, or a list of mappings (an empty list
				// too); docker compose refuses anything else (#1529; measured, v5.5.1).
				// Asked of the key as written: a key that is an alias to a `<<` is not a merge key.
				if mk := n.Content[i]; mk.Kind == yaml.ScalarNode && mk.Tag == "!!merge" && !mergeValueOK(n.Content[i+1]) {
					return fmt.Errorf("compose file %s: line %d: the merge key `<<` takes a mapping or a list of mappings, and this is %s — docker compose refuses it", path, n.Content[i].Line, mergeValueKind(n.Content[i+1]))
				}
				// What docker compose refuses is a key its core schema reads as something
				// other than a string: a number, a bool, a null, a timestamp, a list or a
				// mapping. A key with a tag of its own (`!foo 1`, `! 1`, `!!binary YQ==`),
				// a `!!str`, and the `<<` merge key are read as strings (#1525; measured).
				refused := k.Kind != yaml.ScalarNode
				switch k.Tag {
				case "!!int", "!!bool", "!!null", "!!timestamp", "!!float":
					refused = true
					// The reader gives `! 1` the tag of `1`; the text tells them apart, and a
					// key written with a `!` of its own is a string.
					if k.Style&yaml.TaggedStyle == 0 && startsWithBang(written, k.Line, k.Column) {
						refused = false
					}
					// An infinity in a file read whole is named by the number check.
					if k.Tag == "!!float" && only == "" && nonFiniteRE.MatchString(k.Value) {
						refused = false
					}
				}
				if !refused {
					continue
				}
				return fmt.Errorf("compose file %s: line %d: the mapping key %s is not a string — docker compose refuses a key of another type; quote it to keep it a string", path, n.Content[i].Line, keyText(k))
			}
		}
		for _, c := range n.Content {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(&doc)
}

// startsWithBang reports whether the node at a line and column (1-based, in characters,
// a line ending in `\n`, `\r\n` or a lone `\r`, a byte order mark not counted) is written
// with a `!` before its text — after an anchor, if it has one: the non-specific tag
// `! 1`, which the reader does not keep.
func startsWithBang(written []byte, line, column int) bool {
	rest := bytes.TrimPrefix(written, []byte("\xef\xbb\xbf"))
	for i := 1; i < line; i++ {
		nl := bytes.IndexAny(rest, "\r\n")
		if nl < 0 {
			return false
		}
		if rest[nl] == '\r' && nl+1 < len(rest) && rest[nl+1] == '\n' {
			nl++
		}
		rest = rest[nl+1:]
	}
	for i := 1; i < column; i++ {
		_, size := utf8.DecodeRune(rest)
		if size == 0 {
			return false
		}
		rest = rest[size:]
	}
	if len(rest) > 0 && rest[0] == '&' {
		if sp := bytes.IndexAny(rest, " \t"); sp >= 0 {
			rest = bytes.TrimLeft(rest[sp:], " \t")
		}
	}
	return len(rest) > 0 && rest[0] == '!'
}

// mergeValueOK reports whether what a `<<` merge key holds is one docker compose reads:
// a mapping, or a list whose every item is one (aliases followed).
func mergeValueOK(v *yaml.Node) bool {
	// An alias to an anchor tagged `!reset` merges nothing, whatever the anchor holds (docker compose
	// v5.5.1 takes `<<: *z` over `z: &z !reset [/z]`): takeMergeTags drops the key.
	if v != nil && v.Kind == yaml.AliasNode && v.Alias != nil && v.Alias.Tag == "!reset" {
		return true
	}
	for v != nil && v.Kind == yaml.AliasNode {
		v = v.Alias
	}
	if v == nil {
		return false
	}
	switch v.Kind {
	case yaml.MappingNode:
		return true
	case yaml.SequenceNode:
		for _, item := range v.Content {
			// An alias to a `!reset` anchor merges nothing, whatever it holds (see above).
			if item != nil && item.Kind == yaml.AliasNode && item.Alias != nil && item.Alias.Tag == "!reset" {
				continue
			}
			for item != nil && item.Kind == yaml.AliasNode {
				item = item.Alias
			}
			if item == nil || item.Kind != yaml.MappingNode {
				return false
			}
		}
		return true
	}
	return false
}

// mergeValueKind names what a `<<` merge key holds when it is not a mapping or a list of them.
func mergeValueKind(v *yaml.Node) string {
	for v != nil && v.Kind == yaml.AliasNode {
		v = v.Alias
	}
	switch {
	case v == nil:
		return "nothing"
	case v.Kind == yaml.SequenceNode:
		return "a list with an item that is not a mapping"
	case v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == ""):
		return "empty"
	case v.Kind == yaml.ScalarNode:
		return "the scalar " + v.Value
	}
	return "not a mapping"
}

// keyText is how a mapping key that is not a string is named: its text when it is a
// scalar, and what it is when it is a list or a mapping.
func keyText(k *yaml.Node) string {
	switch k.Kind {
	case yaml.ScalarNode:
		if k.Value == "" {
			return "(empty)"
		}
		return k.Value
	case yaml.SequenceNode:
		return "(a list)"
	}
	return "(a mapping)"
}

// nonFiniteInService is an infinity or a NaN that a service holds after everything
// docker compose does to it before it checks its model — `<<` merges, `${…}`
// expansion, aliases, the extends it resolves in its own file — for the service an
// `extends: {file: …}` takes from another file (#1510). It returns the path of the
// value, or "".
func nonFiniteInService(v any, at string) string {
	switch x := v.(type) {
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return at
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // the same value is named on every run
		for _, k := range keys {
			if r := nonFiniteInService(x[k], at+"."+k); r != "" {
				return r
			}
		}
	case []any:
		for i, e := range x {
			if r := nonFiniteInService(e, fmt.Sprintf("%s[%d]", at, i)); r != "" {
				return r
			}
		}
	}
	return ""
}

// checkRepeatedKeys refuses a key written twice in one mapping (inside an `x-`
// extension only, or anywhere in the file), and — asked of the whole file — an
// alias that refers to the block that contains it.
//
// Asked of the whole file after the decode has passed, so that a block the
// decode reads into a typed shape has already answered in its own words and is
// not asked again to say it twice: what is left is a mapping the decode takes
// as it comes — `logging.options`, `deploy.resources.reservations`, `sysctls`,
// `ulimits`, `extra_hosts`, `driver_opts`, a gpu device, a mount's `bind:` block —
// which accepted a repeat in a single file where docker compose refuses it
// (measured, v5.5.1: 7 positions of a 31-position sweep, and
// `deploy.resources.reservations` and `bind:` by probes of their own). Several
// files were refused already, by
// the merge of the maps the later ones decode into.
//
// Asked of the file as written (`written`), not of the document after `${...}`
// was expanded: docker compose does not expand the keys of a mapping, so
// `{${A}: 1, ${B}: 2}` is two keys however A and B are set, and reading the
// expanded text would refuse a file it accepts and name a key the file never
// wrote. Its line numbers are the ones the reader counts as well. A text that
// does not parse as written (a `${...}` where a flow mapping needs a plain
// key) is left to the decode, which reads it after expansion.
//
// Keys are the same when their text is: docker compose's decoder does not
// look at the tag, so `1` and `"1"` are one key, and neither is `<<` left out —
// `<<` written twice is a repeat there as well. Only what is written in this
// mapping is compared: a key an alias or a merge brings in is not written twice
// in it. An alias is followed once, so a block reused by anchor is read where it
// is written and not again wherever it is pointed at; a block that contains
// itself is refused where the whole file is asked (docker compose: `cycle
// detected`) and ends the walk where it is not.
func checkRepeatedKeys(path string, written []byte, everywhere bool) error {
	if len(written) == 0 {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(written, &doc); err != nil {
		return nil
	}
	root := documentRoot(&doc)
	if root == nil {
		return nil
	}
	seen := map[*yaml.Node]bool{}
	onPath := map[*yaml.Node]bool{}
	var walk func(n *yaml.Node, inExtension bool) error
	walk = func(n *yaml.Node, inExtension bool) error {
		target := unalias(n)
		if everywhere && n.Kind == yaml.AliasNode && onPath[target] {
			return fmt.Errorf("compose file %s: line %d: an alias refers to the block that contains it (cycle detected)\n  remove the alias, or anchor a block that does not contain it",
				path, n.Line)
		}
		n = target
		if seen[n] {
			return nil
		}
		seen[n] = true
		onPath[n] = true
		defer delete(onPath, n)
		switch n.Kind {
		case yaml.MappingNode:
			first := map[string]int{}
			for i := 0; i+1 < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind == yaml.ScalarNode && (inExtension || everywhere) {
					if at, dup := first[key.Value]; dup {
						return fmt.Errorf("compose file %s sets the same key twice:\n  line %d: mapping key %q already defined at line %d\n  remove one of them",
							path, key.Line, key.Value, at)
					}
					first[key.Value] = key.Line
				}
				within := inExtension || key.Kind == yaml.ScalarNode && strings.HasPrefix(key.Value, "x-")
				if err := walk(n.Content[i+1], within); err != nil {
					return err
				}
			}
		case yaml.SequenceNode:
			for _, item := range n.Content {
				if err := walk(item, inExtension); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, false)
}

// checkGroupAddRepeats refuses a service that writes the same `group_add`
// entry twice in one file.
//
// The same entry twice is the spelling the file wrote together with what
// opossum hands the runtime for it: the same characters can read two ways —
// an integer `020` is YAML's octal (the group 16) where the string `"020"`
// is the digits `--gid` reads as 20 — so those are two groups and not a
// repeat. Two spellings of one group (`[0x10, "16"]`) are not a repeat
// either; the check the service is started by folds those and asks for the
// group to be listed once, which is a refusal a project can still come down
// from.
func checkGroupAddRepeats(path string, f *composeFile) error {
	names := make([]string, 0, len(f.Services))
	for name := range f.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := f.Services[name]
		// A service pruned as "not given" is nil. The length test is
		// defence rather than a case: the spellings come from the same
		// reading as the values, so a service that has one has the other,
		// and a `group_add` that arrives through a merge key carries both
		// (measured — such a file is refused here). Nothing found reaches
		// it; it is here so that a service whose spellings the loader could
		// not line up entry for entry is left to the checks that read the
		// values alone, rather than read past the end of the shorter list.
		if svc == nil || len(svc.GroupAddWritten) != len(svc.GroupAdd) {
			continue
		}
		for i := range svc.GroupAdd {
			for j := 0; j < i; j++ {
				if svc.GroupAddWritten[j] == svc.GroupAddWritten[i] && svc.GroupAdd[j] == svc.GroupAdd[i] {
					return fmt.Errorf("compose file %s: service %q: group_add items at %d and %d are equal — list the group once", path, name, j, i)
				}
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
func resolveExtendsInTree(where, projectDir string, tree map[string]any, lookup varLookup, values *[]error, earlier map[string]any) (bool, error) {
	return resolveExtends(where, projectDir, tree, lookup, nil, values, "", earlier)
}

// extendsID names one service of one file on the resolution stack, so that
// a cycle that runs through another file is seen as the cycle it is.
func extendsID(where, name string) string { return where + "#" + name }

// resolveExtends is resolveExtendsInTree with the stack of services being
// resolved, which a service of another file joins. stack entries are
// extendsIDs; the cycle message shows the names, with the file where it
// is not this one.
//
// `only`, when it is not empty, is the one service of this file that is taken (the file was reached
// by another service's `extends: {file: …, service: only}`): its extends is resolved, and the
// chain of services it extends in turn, and no other service of the file — docker compose reads
// the rest of the file for nothing but its own errors, and a service that extends a file that is
// missing, or this file again, is not its business there (#1561, #1516).
func resolveExtends(where, projectDir string, tree map[string]any, lookup varLookup, stack []string, values *[]error, only string, earlier map[string]any) (bool, error) {
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
		fromFile := ""    // the file the extended service comes from, when it is another's
		viaChain := false // and whether that service extends another in turn
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
			b, chained, err := extendedServiceFromFile(where, name, path, target, lookup, append(stack, id), values)
			if err != nil {
				return err
			}
			base = b
			fromFile = path
			viaChain = chained
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
		// A key the extending service tagged `!reset` or `!override` leaves
		// the extended service's value out.
		if tags, ok := own[serviceTagsKey].([]mergeTag); ok {
			// A list in the extended service is not read as a mapping here.
			// docker compose reads one as a mapping when the file that wrote
			// it was reached by an extends from another file, however many
			// extends within one file lie between; opossum does not follow
			// that yet (a known difference, see docs/compatibility.md).
			applyMergeTags(base, tags, false)
			delete(own, serviceTagsKey)
		}
		delete(base, serviceTagsKey)
		// A `deploy.replicas` that is a list or a mapping in the extended service and that the extender writes over with a value of another
		// kind, without `!override` or `!reset` (which took it out of the extended service above), is refused: docker compose cannot merge
		// the two (measured, v5.5.1: #1776). A list over a list, a mapping over a mapping and a null merge; one that the extender leaves
		// in place is asked once the service that extends this one is merged, or at the end (replicasReadable). Asked of what a file that is only
		// extended from holds too, where one service of the file extends another.
		if fromFile != "" || only != "" {
			if bd, ok := base["deploy"].(map[string]any); ok {
				if od, ok := own["deploy"].(map[string]any); ok {
					if ov, written := od["replicas"]; written && ov != nil && replicasKind(ov) != replicasKind(bd["replicas"]) && replicasKind(bd["replicas"]) != "count" {
						e := fmt.Errorf("compose file %s%s: services.%s.deploy.replicas is %s in the service it extends, and cannot be merged with %s written over it — write `replicas` or `deploy` with `!override` or `!reset` to replace it", where, orFile(fromFile), name, describeYAMLValue(bd["replicas"]), describeYAMLValue(ov))
						if values == nil {
							return e
						}
						*values = append(*values, e)
					}
				}
			}
		}
		// A key the extending service writes with nothing after it that docker compose refuses even over a value, over a value the
		// extended service gave: refused (the merge below reads any other null as "not given" there).
		var earlierSvc map[string]any
		if earlierServices, ok := earlier["services"].(map[string]any); ok {
			earlierSvc, _ = earlierServices[name].(map[string]any)
		}
		{
			// Where a service is only extended from (a hop), the service that extends it may still write the key over, and docker
			// compose refuses a null over a value only for the keys it refuses whatever is written over them.
			refused := refusedOverAValue
			if only != "" {
				refused = refusedWhateverIsWrittenOver
			}
			at, over := nullOverAValue(own, base, nil, refused), "the service it extends"
			if at == "" && earlierSvc != nil {
				at, over = nullOverAValue(own, earlierSvc, nil, refused), "an earlier file"
			}
			if at != "" {
				e := fmt.Errorf("compose file %s: services.%s.%s has nothing after it, over the value %s gives it — write the value, or remove the key", where, name, at, over)
				if values == nil {
					return e
				}
				*values = append(*values, e)
			}
		}
		services[name] = mergeMap(base, own, childPath("services", name))
		// An infinity or a NaN that stays in the merged service came from the other file (what this file
		// writes itself was refused where the file was read): docker compose cannot write it into its
		// model. Named by the service it came from (target), in the file it came from. Asked of the service this
		// file takes, once, and not of a hop through a file that is only extended from (only is set there): the
		// service that extends it may write over it.
		if fromFile != "" && only == "" {
			if at := nonFiniteInService(services[name], "services."+target); at != "" {
				where := fromFile
				if viaChain {
					where += " (or a file it extends)" // the value may be written in a file this one extends in turn
				}
				err := fmt.Errorf("compose file %s: %s is a number docker compose cannot read — infinity and NaN cannot go into its model; quote it to keep it a string", where, at)
				if values == nil {
					return err
				}
				*values = append(*values, err)
			}
		}
		// The blocks of `deploy` that stay in the merged service are put to the schema the same way (see
		// deployShapes), and a key with nothing after it that stays is refused: the extender writing the key over, or
		// resetting `deploy`, is what takes either away.
		if fromFile != "" && only == "" {
			merged, _ := services[name].(map[string]any)
			if d, ok := merged["deploy"].(map[string]any); ok {
				where := fromFile
				if viaChain {
					where += " (or a file it extends)"
				}
				say := func(e error) error {
					if values == nil {
						return e
					}
					*values = append(*values, e)
					return nil
				}
				if err := deployShapes(where, "services."+target+".deploy", d, true, say); err != nil {
					return err
				}
			}
		}
		// The number of replicas that stays in the merged service is read the same way: where it came from
		// the other file, whatever it holds is what docker compose casts, and the file it was read in did not
		// ask (a count the extender writes over is not asked at all).
		if fromFile != "" && only == "" {
			merged, _ := services[name].(map[string]any)
			if d, ok := merged["deploy"].(map[string]any); ok {
				if replicas, present := d["replicas"]; present && !replicasReadable(replicas) {
					where := fromFile
					if viaChain {
						where += " (or a file it extends)"
					}
					err := fmt.Errorf("compose file %s: services.%s.deploy.replicas must be a whole number, and this is %s", where, target, describeYAMLValue(replicas))
					if values == nil {
						return err
					}
					*values = append(*values, err)
				}
			}
		}
		// A number outside the bounds of its key that is still there once the services are merged (see boundsThatStay): the extender
		// writing a value over it is what takes it away. Asked of what a service takes from another file, as the null is below.
		if fromFile != "" && only == "" {
			whereFile := fmt.Sprintf("%s (or %s, the file it extends)", where, fromFile)
			if viaChain {
				whereFile = fmt.Sprintf("%s (or %s, the file it extends, or a file that one extends)", where, fromFile) // the value may be written in a file this one extends in turn
			}
			if e := typesThatStay(whereFile, name, services[name].(map[string]any)); e != nil {
				if values == nil {
					return e
				}
				*values = append(*values, e)
			}
		}
		// A key with nothing after it that is still there once the services are merged: the schema is put to it now (see nullsThatStay).
		if only == "" {
			if msg := nullsThatStay(name, withoutBackedNulls(services[name].(map[string]any), earlierSvc)); msg != "" {
				whereFile := where
				if fromFile != "" {
					// The key may be the extender's own, or the extended service's: both files are named.
					whereFile = fmt.Sprintf("%s (or %s, the file it extends)", where, fromFile)
				}
				e := fmt.Errorf("compose file %s: %s", whereFile, msg)
				if values == nil {
					return e
				}
				*values = append(*values, e)
			}
		}
		done[name] = true
		touched = true
		return nil
	}
	if only != "" {
		if err := resolve(only, stack); err != nil {
			return false, err
		}
		return touched, nil
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
func extendedServiceFromFile(where, name, path, target string, lookup varLookup, stack []string, values *[]error) (base map[string]any, chained bool, err error) {
	// A relative -f leaves `where` relative, and so this path; the paths
	// rebased below must be absolute, since the project resolves relative
	// ones against its own directory (a relative one would be doubled).
	if a, err := filepath.Abs(path); err == nil {
		path = a
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("%s: service %q extends %q of %s, which cannot be read: %v — name the file by a path from the project directory (the first file's, or the include entry's), or remove extends:", where, name, target, path, err)
	}
	if err := checkOneDocument(path, raw, values); err != nil {
		return nil, false, err
	}
	doc, err := interpolateDocument(raw, lookup)
	if err != nil {
		return nil, false, fmt.Errorf("interpolating %s (extended by service %q of %s): %w", path, name, where, err)
	}
	doc, tags, err := takeMergeTags(doc)
	if err != nil {
		return nil, false, fmt.Errorf("compose file %s: %w", path, err)
	}
	if err := validateOne(path, doc, nil, values, target); err != nil {
		return nil, false, err
	}
	var tree map[string]any
	if err := doc.intoMarked(&tree); err != nil {
		return nil, false, decodeErr(path, asWritten, err)
	}
	// Whether the service itself extends another (so what it holds may have come from a file it extends in
	// turn: the file named in a refusal of an infinity is then the one it was reached through).
	if sv, ok := tree["services"].(map[string]any); ok {
		if tsvc, ok := sv[target].(map[string]any); ok {
			_, chained = tsvc["extends"]
		}
	}
	markServiceTags(tree, tags)
	defer unmarkServiceTags(tree)
	// A file the extends reached is read on its own terms: what it
	// extends in turn is found from its own directory (docker compose,
	// measured: a/two.yml → b/near.yml → c/far.yml reads a/b/c/far.yml,
	// where the first hop from a -f file counts from the project
	// directory).
	if _, err := resolveExtends(path, filepath.Dir(path), tree, lookup, stack, values, target, nil); err != nil {
		return nil, false, err
	}
	services, _ := tree["services"].(map[string]any)
	svc, ok := services[target].(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s: service %q extends %q of %s, and that file does not define it — name a service %s defines, or remove extends:", where, name, target, path, path)
	}
	base = deepCopyTree(svc).(map[string]any)
	// An infinity or a NaN in what docker compose takes of this file is asked of the
	// service that extends it, once the two are merged (resolveExtends): a value the
	// extending service writes over or resets is not in the model docker compose checks.
	rebasePaths(base, filepath.Dir(path))
	return base, chained, nil
}

// rebasePaths makes the host paths a service writes relative to its own
// file absolute against dir, so that a project whose first file lives
// elsewhere resolves them where docker compose does. What is rebased is
// what docker compose rebases when it extends across files: `build` (as a
// path or its `context`), a bind mount's source in the short form (one
// that starts with `.` — `.`, `..`, `./…`, `../…`, `.hidden` — the
// file-relative host paths; a bare name is a named volume, `/…` and `~…`
// are not relative to the file) and the long form, `env_file` in every
// form, and `develop.watch` paths.
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
				// In the long form the type decides: a `type: bind` source that is a
				// bare name (`ldata`) is the directory beside that file, so it is
				// rebased like `./ldata` — otherwise the short spelling it becomes
				// (`./ldata`) would point beside the main file instead.
				if src, ok := m["source"].(string); ok && (typ == "bind" && (relativeHostPath(src) || !IsHostPath(src)) || typ == "" && relativeHostPath(src)) {
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
// relative to a file: anything that starts with `.` — `.`, `..`, `./…`,
// `../…`, and a hidden name such as `.hidden` or `.hidden/sub` — as docker
// compose reads it. These are the sources an included or extended file's
// directory has to be joined onto.
func relativeHostPath(s string) bool {
	return strings.HasPrefix(s, ".")
}

// resolveSameFileExtends is resolveExtendsInTree for a single file: the
// tree comes back re-marshalled only when something was resolved, so a
// file with no such service keeps its positions for the failures that
// name a line (a file with one has been checked as written already).
func resolveSameFileExtends(doc interpolated, tags []mergeTag, where, projectDir string, lookup varLookup, values *[]error) (*interpolated, error) {
	var tree map[string]any
	if err := doc.intoMarked(&tree); err != nil {
		return nil, nil // the decode below says so in its own words
	}
	markServiceTags(tree, tags)
	touched, err := resolveExtendsInTree(where, projectDir, tree, lookup, values, nil)
	unmarkServiceTags(tree)
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
		case topLevelDecls[k]:
			// Inside each declaration, a key docker compose does not take
			// there (`volumes.data.foo`) is refused as docker compose refuses
			// it; a key it takes that opossum does not act on is listed among
			// the ignored fields later, as before.
			if err := checkDeclKeys(path, k, v); err != nil {
				return err
			}
		case !specKnows("", k):
			// A top-level key docker compose does not take — `servcies:` for
			// `services:` — is refused as docker compose refuses it, naming
			// the line, rather than read past and listed as ignored.
			return fmt.Errorf("compose file %s: %q is not a top-level key docker compose takes (line %d) — check the spelling, or write it as `x-%s` to keep it as a note", path, k, key.Line, k)
		}
	}
	return nil
}

// checkDeclKeys refuses, in a top-level `volumes:`/`networks:`/`secrets:`/
// `configs:` mapping, a key inside a declaration that docker compose does
// not take there (see specKnows), naming the file and the line.
func checkDeclKeys(path, kind string, decls *yaml.Node) error {
	for i := 0; i+1 < len(decls.Content); i += 2 {
		name := unalias(decls.Content[i])
		decl := unalias(decls.Content[i+1])
		if name.Tag == "!!merge" || decl.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(decl.Content); j += 2 {
			key := unalias(decl.Content[j])
			if key.Tag == "!!merge" {
				continue
			}
			if !specKnows(kind+".*", key.Value) {
				return fmt.Errorf("compose file %s: %s.%s: %q is not a key docker compose takes (line %d) — check the spelling, or write it as `x-%s` to keep it as a note", path, kind, name.Value, key.Value, key.Line, key.Value)
			}
		}
	}
	return nil
}

// splitDocuments cuts a compose file into its YAML documents (`---` between
// them). docker compose reads them in order and merges each into the ones before,
// as it does several `-f` files (measured, v5.5.1: the later value wins, `command`
// is replaced, `ports` append, `name:` is the last one's), so a top-level file is
// read as that many files. A file of one document is returned as it is. What is
// cut keeps its lines: each document's text is padded with the newlines before it,
// so a failure names the line the file has.
//
// A document that is empty or is not a mapping (a trailing `---`, two in a row, a
// `[1]`) is refused as docker compose refuses it, whatever else the file holds
// (`top-level object must be a mapping`). A text that does not parse is left to
// the reader when it is the first document — which says so in its own words — and
// refused here, naming the document, when it is a later one: the reader would read
// the first alone and say nothing. A leading `---` or a `...` at the end is one
// document. An alias to an earlier document's anchor is not carried across: the
// document that has one is refused by the reader as an unknown anchor.
func splitDocuments(path string, raw []byte, values *[]error) ([][]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var starts []int // the line the content of each document begins on
	var notMapping []int
	n := 0
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if n == 0 {
				return [][]byte{raw}, nil
			}
			return nil, fmt.Errorf("compose file %s: document %d does not parse: %v — docker compose refuses the file", path, n+1, err)
		}
		n++
		// An empty document is a `!!null` scalar; an empty node list is not seen
		// from the decoder but keeps the index below from being one too far.
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			notMapping = append(notMapping, n)
			starts = append(starts, 0)
			continue
		}
		starts = append(starts, doc.Content[0].Line)
	}
	if n <= 1 {
		return [][]byte{raw}, nil
	}
	if len(notMapping) > 0 {
		refusal := fmt.Errorf("compose file %s: document %d (between two `---`, or after the last one) is empty or not a mapping — docker compose refuses it too (`top-level object must be a mapping`); remove the extra `---`",
			path, notMapping[0])
		// A trailing `---` after the one document the file holds is let go, and only
		// where the caller takes a project down (#1475): a release before 0.38.0 read
		// the first document and no other (measured, v0.37.0: a second document with
		// a `services:` of its own started nothing of it), so the project it started
		// from such a file is the first document's, and that is the file read here.
		// Where a second mapping follows, the documents are merged as 0.38.0 reads
		// them and the file is refused as before, as is an empty one in the middle.
		trailing := values != nil && notMapping[0] == 2 && len(notMapping) == n-1
		if !trailing {
			return nil, refusal
		}
		*values = append(*values, refusal)
		return [][]byte{raw}, nil // the one mapping document; the rest is nothing to read
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	// A document begins at the `---` line before its content (the content may be
	// on that line); the first one at the top. Where the cut falls is for tidiness:
	// a text that kept the marker of the next document at its end is read as the
	// same document, so no answer depends on it but the padding.
	begin := make([]int, n)
	for d := 1; d < n; d++ {
		l := starts[d] - 1 // 0-based index of the content's line
		for l > 0 && !bytes.HasPrefix(lines[l], []byte("---")) {
			l--
		}
		begin[d] = l
	}
	docs := make([][]byte, 0, n)
	for d := 0; d < n; d++ {
		end := len(lines)
		if d+1 < n {
			end = begin[d+1]
		}
		var b bytes.Buffer
		b.Write(bytes.Repeat([]byte("\n"), begin[d]))
		for _, line := range lines[begin[d]:end] {
			b.Write(line)
		}
		docs = append(docs, b.Bytes())
	}
	return docs, nil
}

// checkOneDocument is what an included file and the file a service extends are
// read with: they are read as one document, and a file of several is refused
// rather than read as its first alone (the top-level files are merged, see
// splitDocuments).
func checkOneDocument(path string, raw []byte, values *[]error) error {
	docs, err := splitDocuments(path, raw, values)
	if err != nil {
		return err
	}
	if len(docs) > 1 {
		return fmt.Errorf("compose file %s holds %d YAML documents (separated by `---`), and is read here as one file: docker compose merges them in order, and opossum reads only the first document of an included or extended file — it does not leave the rest out silently. Put each document in a file of its own", path, len(docs))
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
func loadUnit(path, projectDir string, scope envScope, earlier map[string]any, stack []string) (map[string]any, []string, []mergeTag, error) {
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
			return nil, nil, nil, fmt.Errorf("compose file %s: include forms a cycle (%s → %s) — remove the include that closes it", path, strings.Join(shown, " → "), filepath.Base(abs))
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if len(stack) > 0 {
			return nil, nil, nil, fmt.Errorf("compose file %s: include names %s, which cannot be read: %v — a relative path is resolved from the project directory (the first file's, or the include entry's)", stack[len(stack)-1], path, err)
		}
		return nil, nil, nil, fmt.Errorf("reading compose file: %w", err)
	}
	if err := checkOneDocument(path, raw, scope.values); err != nil {
		return nil, nil, nil, err
	}
	return loadUnitRaw(path, raw, projectDir, scope, earlier, stack)
}

// loadUnitRaw is loadUnit for the text of a file that has been read (or of one
// document of a file that holds several, which is padded so that the lines a
// failure names are the file's).
func loadUnitRaw(path string, raw []byte, projectDir string, scope envScope, earlier map[string]any, stack []string) (map[string]any, []string, []mergeTag, error) {
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	if projectDir == "" {
		projectDir = filepath.Dir(abs)
	}
	one, err := interpolateDocument(raw, scope.lookup())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("interpolating %s: %w", path, err)
	}
	one, tags, err := takeMergeTags(one)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose file %s: %w", path, err)
	}
	var m map[string]any
	if err := one.intoMarked(&m); err != nil {
		// The same words as the single-file road. A key set twice is found
		// here rather than at the final decode, and saying "not valid YAML"
		// for it was the same wrong advice by a different route — the one a
		// reader hits precisely when they have split their file up.
		return nil, nil, nil, decodeErr(path, asWritten, err)
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
			return nil, nil, nil, err
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
			sub, err := loadEnvLayers(subDir, "", envFiles, scope.faults != nil)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("compose file %s: include: %w", path, err)
			}
			sub.values = scope.values
			if scope.faults != nil && sub.faults != nil {
				// A name the included project's `.env` refuses is kept with this
				// project's own — the same commands are to be stopped or let go on —
				// and what a project it includes in turn refuses goes to the same place.
				*scope.faults = append(*scope.faults, *sub.faults...)
				sub.faults = scope.faults
			}
			sub.outer = chainLookup(scope.outer, mapLookup(scope.level))
			sub.builtin = scope.builtin
			// The paths of one entry merge as -f files do, tags and all; the
			// entries merge with each other plainly — a later entry's tags do
			// not reach an earlier entry (docker compose v5.5.0, measured).
			var entry map[string]any
			for _, p := range e.paths {
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				// Checked as a project of its own: no earlier file makes
				// its bare keys "not given".
				whole, subFiles, subTags, err := loadUnit(p, subDir, sub, nil, append(stack, abs))
				if err != nil {
					return nil, nil, nil, err
				}
				tree := includedPart(whole, subDir)
				included = append(included, subFiles...)
				if entry == nil {
					entry = tree
				} else {
					applyMergeTags(entry, subTags, true)
					entry = mergeMap(entry, tree, "")
				}
			}
			if entry == nil {
				continue
			}
			if group == nil {
				group = entry
			} else {
				group = mergeMap(group, entry, "")
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
	if err := validateOne(path, one, before, scope.values, ""); err != nil {
		return nil, nil, nil, err
	}
	if err := liftExternalNames(path, m, before); err != nil {
		return nil, nil, nil, err
	}
	if group != nil {
		// A key this file tagged `!reset` or `!override` leaves the included
		// files' value out, as it does an earlier -f file's — a field of a service, of a network, a volume,
		// a config or a secret; not the item itself (a service, or one of those by its name) nor the whole section (`networks:`, `services:`…), which docker
		// compose merges with the included one whatever it is tagged with (measured, v5.5.1: `web: !override {…}`,
		// `web: !reset null`, `n: !reset null`, an alias or a merge key to a tagged mapping).
		applyMergeTags(group, withoutItemTags(tags), true)
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
					return nil, nil, nil, fmt.Errorf("compose file %s: service %q must be a mapping — the key has nothing under it; give it at least `image:` or `build:`, or remove the key", path, name)
				}
			}
		}
		if err := validateMerged(path, whole, asMerged); err != nil {
			return nil, nil, nil, err
		}
	}
	// `extends` within this file is read before the merge with earlier -f
	// files, as docker compose reads it: a service this file defines — or
	// includes — is what it extends, and an earlier file's version of the
	// extending service is what this file's resolved version goes over.
	markServiceTags(m, tags)
	_, err = resolveExtendsInTree(path, projectDir, m, scope.lookup(), scope.values, earlier)
	unmarkServiceTags(m)
	if err != nil {
		return nil, nil, nil, err
	}
	return m, files, tags, nil
}

// validateMerged decodes what the files so far make together and names
// the file just added in what it refuses.
func validateMerged(path string, merged map[string]any, read readAs) error {
	// A negative `healthcheck.retries` is asked of the project the files make, once the last is merged in (#1774): a later file
	// may still write the count over it or reset it.
	data, err := yaml.Marshal(withoutNegativeRetriesTree(merged))
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

// refusedNullInUntaken are the keys docker compose refuses with nothing after them in a service nothing takes, as it does in one taken
// (measured, v5.5.1: a sweep of every key of the service schema). `extends` is refused there too and is read by opossum as it was (a known
// difference, left alone), and `pid` is read.
var refusedNullInUntaken = map[string]bool{"build": true, "depends_on": true, "env_file": true, "gpus": true, "ports": true}

// withoutNullInUntaken copies a file that is only extended from (`only` names the service taken) without the keys that hold nothing
// in the services docker compose does not take: it reads a key with nothing after it there (`user: ~`, `image:`, `restart: ~`;
// measured, v5.5.1: all but five keys of a service, for a service nothing takes), where it refuses the same in a service taken.
// The five it refuses there too stay (refusedNullInUntaken). A file read whole is returned as it is.
func withoutNullInUntaken(doc *yaml.Node, only string) *yaml.Node {
	if only == "" {
		return doc
	}
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return doc
	}
	var generic struct {
		Services map[string]any `yaml:"services"`
	}
	if err := doc.Decode(&generic); err != nil {
		return doc
	}
	taken := takenServices(generic.Services, only)
	newRoot := copyMapping(root)
	changed := false
	for i := 0; i+1 < len(newRoot.Content); i += 2 {
		if newRoot.Content[i].Value != "services" || newRoot.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		services := copyMapping(newRoot.Content[i+1])
		for j := 0; j+1 < len(services.Content); j += 2 {
			// The service as it comes to be read: through an alias (`other: *a`) and with its merge keys taken in (`<<: *a`), where a key
			// that holds nothing may come from.
			svc := unalias(services.Content[j+1])
			if taken[services.Content[j].Value] || svc == nil || svc.Kind != yaml.MappingNode {
				continue
			}
			pairs := svc.Content
			if resolved, err := mergedPairs(svc); err == nil {
				pairs = resolved
			}
			var kept []*yaml.Node
			dropped := false
			for k := 0; k+1 < len(pairs); k += 2 {
				if !refusedNullInUntaken[unalias(pairs[k]).Value] && isNothing(pairs[k+1]) {
					dropped = true
					continue
				}
				kept = append(kept, pairs[k], pairs[k+1])
			}
			if !dropped {
				continue
			}
			svcCopy := copyMapping(svc)
			svcCopy.Content = kept
			services.Content[j+1] = svcCopy
			changed = true
		}
		newRoot.Content[i+1] = services
	}
	if !changed {
		return doc
	}
	if root == doc {
		return newRoot
	}
	out := *doc
	out.Content = []*yaml.Node{newRoot}
	return &out
}

// withoutNotGivenInTaken is a file that is only extended from (only names the service taken from it) without the keys of that
// service, and of the ones it extends in turn in the file, that hold nothing: docker compose puts the schema to the service once
// it is merged with the one that extends it, so what the extender writes over is read, and what stays is refused by
// nullsThatStay. The keys it refuses with nothing after them whatever is written over them (refusedOverAValue) stay in, to be refused
// here as in any file.
func withoutNotGivenInTaken(doc *yaml.Node, only string) *yaml.Node {
	if only == "" {
		return doc
	}
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return doc
	}
	var generic struct {
		Services map[string]any `yaml:"services"`
	}
	if err := doc.Decode(&generic); err != nil {
		return doc
	}
	taken := takenServices(generic.Services, only)
	newRoot := copyMapping(root)
	for i := 0; i+1 < len(newRoot.Content); i += 2 {
		if newRoot.Content[i].Value != "services" || newRoot.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		services := copyMapping(newRoot.Content[i+1])
		for j := 0; j+1 < len(services.Content); j += 2 {
			if svc := services.Content[j+1]; taken[services.Content[j].Value] && svc.Kind == yaml.MappingNode {
				services.Content[j+1] = prunedExtended(svc, nil)
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

// refusedWhateverIsWrittenOver names the keys of a service that docker compose refuses with nothing after them in a file it
// extends from even where the extending service writes a value over them (measured, v5.5.1: `build`,
// `depends_on`, `gpus`, `logging`, `models`, `networks` and `ports` as a whole, and each entry of `ulimits`,
// `extra_hosts` and `depends_on`); every other key is read where something is written over it.
func refusedWhateverIsWrittenOver(path []string) bool {
	switch len(path) {
	case 1:
		switch path[0] {
		case "build", "depends_on", "gpus", "logging", "models", "networks", "ports":
			return true
		}
	case 2:
		return path[0] == "ulimits" || path[0] == "extra_hosts" || path[0] == "depends_on"
	}
	return false
}

// prunedExtended prunes the nothings of a mapping but those at keys docker compose refuses whatever is written over them.
func prunedExtended(n *yaml.Node, path []string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return withoutNotGiven(n)
	}
	out := *n
	pairs := n.Content
	if resolved, err := mergedPairs(n); err == nil {
		pairs = resolved
	}
	out.Content = make([]*yaml.Node, 0, len(pairs))
	for i := 0; i+1 < len(pairs); i += 2 {
		k, v := pairs[i], pairs[i+1]
		here := append(append([]string(nil), path...), unalias(k).Value)
		if isNothing(v) {
			if refusedWhateverIsWrittenOver(here) {
				out.Content = append(out.Content, k, v)
			}
			continue
		}
		out.Content = append(out.Content, k, prunedExtended(v, here))
	}
	return &out
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

// withoutNotGivenBacked is withoutNotGiven for a later file, with one difference
// inside a service: a key written with nothing after it is "not given" only where an
// earlier file gave that key a value. Where none did there is nothing for the key
// to fall back on, and docker compose refuses the file (measured, v5.5.1: `dns: ~`,
// `deploy: {mode: ~}` and some forty more keys, rc 1 with no earlier value and rc 0
// with one), so the key stays and is asked as it is in a single file (#1562).
func withoutNotGivenBacked(doc *yaml.Node, earlier map[string]any) *yaml.Node {
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return withoutNotGiven(doc)
	}
	services, _ := earlier["services"].(map[string]any)
	newRoot := withoutNotGiven(root)
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		written := root.Content[i+1]
		// The services the mapping comes to, a `<<` merge key at `services:` itself resolved: a service it brings is asked of what an
		// earlier file holds under its own name.
		pairs := written.Content
		if resolved, err := mergedPairs(written); err == nil {
			pairs = resolved
		}
		kept := make([]*yaml.Node, 0, len(pairs))
		for j := 0; j+1 < len(pairs); j += 2 {
			name, svc := pairs[j], pairs[j+1]
			if isNothing(svc) {
				// "Not given" over a service an earlier file wrote; one no earlier file wrote is refused once the file is merged in
				// (serviceWithoutBody).
				continue
			}
			// (`extends` read through an alias and a merge key too: `<<: *e`.)
			if _, extends := nestedNode(*svc, "extends"); extends {
				// What the extended service gives backs a key too, and is resolved later:
				// such a service is read as it always was, every nothing "not given".
				kept = append(kept, name, withoutNotGiven(svc))
				continue
			}
			before, _ := services[name.Value].(map[string]any)
			kept = append(kept, name, prunedBacked(svc, before, nil))
		}
		for k := 0; k+1 < len(newRoot.Content); k += 2 {
			if newRoot.Content[k].Value == "services" {
				m := *newRoot.Content[k+1]
				m.Content = kept
				newRoot.Content[k+1] = &m
			}
		}
	}
	if root == doc {
		return newRoot
	}
	out := *doc
	out.Content = []*yaml.Node{newRoot}
	return &out
}

// prunedBacked prunes the nothings of a mapping that an earlier value backs, and
// leaves the ones that nothing backs where they are.
func prunedBacked(n *yaml.Node, before map[string]any, path []string) *yaml.Node {
	// A mapping written as an alias (`logging: *l`, `a: *z`) is the mapping it stands for, asked key by key as one written there; the
	// anchor's own nodes are not changed, the pruned mapping being a copy.
	if n.Kind == yaml.AliasNode && n.Alias != nil && n.Alias.Kind == yaml.MappingNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return withoutNotGiven(n)
	}
	out := *n
	// The pairs the mapping comes to, with a `<<` merge key resolved (a key written in the mapping wins over a merged one): a
	// value a merge key brings is read as one written there, so a null it carries is asked of what an earlier file holds (#1594).
	// A merge source that is no mapping is left as it is, for the decode to refuse.
	pairs := n.Content
	if resolved, err := mergedPairs(n); err == nil {
		pairs = resolved
	}
	out.Content = make([]*yaml.Node, 0, len(pairs))
	for i := 0; i+1 < len(pairs); i += 2 {
		k, v := pairs[i], pairs[i+1]
		// The name a key is written by may be an alias (`*k : v`): the file reads it as the name it stands for.
		name := unalias(k).Value
		backing, backed := before[name]
		backed = backed && backing != nil
		if backed {
			backing = longFormOfShort(name, backing)
		}
		here := append(append([]string(nil), path...), name)
		switch {
		case isNothing(v):
			if backed && !refusedOverAValue(here) {
				continue
			}
			out.Content = append(out.Content, k, v)
		case backed:
			sub, _ := backing.(map[string]any)
			// A dependency an earlier file gave has a `required` whether it wrote one or not (it is
			// required unless written so), which a `required: ~` of a later file is "not given" over.
			if len(here) == 2 && here[0] == "depends_on" && sub != nil {
				if _, has := sub["required"]; !has {
					withDefault := make(map[string]any, len(sub)+1)
					for key, val := range sub {
						withDefault[key] = val
					}
					withDefault["required"] = true
					sub = withDefault
				}
			}
			out.Content = append(out.Content, k, prunedBacked(v, sub, here))
		default:
			out.Content = append(out.Content, k, prunedBacked(v, nil, here))
		}
	}
	// A limit an earlier file gave as `{soft, hard}` is merged into by a later file's part of it: a side written
	// as one number, a side that is null (dropped above), an empty mapping — docker compose merges them into the
	// earlier two (measured, v5.5.1). Each file is read on its own before the merge, and a limit with one side is
	// refused there, so the sides the later file leaves out are written into it from the earlier file.
	if len(path) == 2 && path[0] == "ulimits" && before != nil {
		for _, side := range []string{"soft", "hard"} {
			if hasKey(&out, side) {
				continue
			}
			if v, ok := before[side]; ok && v != nil {
				var val yaml.Node
				if err := val.Encode(v); err == nil {
					key := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: side}
					out.Content = append(out.Content, &key, &val)
				}
			}
		}
	}
	return &out
}

// withoutUntakenDeploy copies a file that is only extended from (`only` names the
// service taken) without the `deploy` of the services docker compose does not take: it
// reads none of it there — whatever it holds, a word, a list, a number, a key it does not
// know, and a value of the wrong kind in `resources` (`limits: abc`, `limits: {cpus: true}`,
// `reservations: {memory: ~}`) passes (measured, v5.5.1: 84 forms of `resources` and the
// forms of `deploy` itself, for a service taken and one not) — where it refuses the same in
// a service taken (#1581, #1606). The number of replicas it still casts, so that one is kept in
// the copy, for modeKinds to ask. A file read whole is returned as it is.
func withoutUntakenDeploy(doc *yaml.Node, only string) *yaml.Node {
	if only == "" {
		return doc
	}
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return doc
	}
	var generic struct {
		Services map[string]any `yaml:"services"`
	}
	if err := doc.Decode(&generic); err != nil {
		return doc
	}
	taken := takenServices(generic.Services, only)
	newRoot := copyMapping(root)
	changed := false
	for i := 0; i+1 < len(newRoot.Content); i += 2 {
		if newRoot.Content[i].Value != "services" || newRoot.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		services := copyMapping(newRoot.Content[i+1])
		for j := 0; j+1 < len(services.Content); j += 2 {
			// The service as it comes to be read: through an alias (`other: *o`) and with its merge keys taken in (`<<: *d`), where a `deploy` may come from.
			svc := unalias(services.Content[j+1])
			if taken[services.Content[j].Value] || svc == nil || svc.Kind != yaml.MappingNode {
				continue
			}
			pairs := svc.Content
			if resolved, err := mergedPairs(svc); err == nil {
				pairs = resolved
			}
			hasDeploy := false
			for k := 0; k+1 < len(pairs); k += 2 {
				if unalias(pairs[k]).Value == "deploy" {
					hasDeploy = true
				}
			}
			if !hasDeploy {
				continue
			}
			svcCopy := copyMapping(svc)
			svcCopy.Content = nil
			for k := 0; k+1 < len(pairs); k += 2 {
				if unalias(pairs[k]).Value != "deploy" {
					svcCopy.Content = append(svcCopy.Content, pairs[k], pairs[k+1])
					continue
				}
				// The number of replicas, and the few counts and labels that docker compose reads into its model
				// (untakenDeployKept), are what it still casts of `deploy` in a service not taken, and
				// modeKinds and deployCasts ask them of this copy: kept, with nothing else. Read from
				// what the file comes to, with aliases and merge keys resolved (`deploy: *d`, `deploy: {<<: *d}`),
				// and not from the node as it is written, where a `replicas` behind either of them is not to be seen.
				var keptDeploy map[string]any
				if m, ok := generic.Services[services.Content[j].Value].(map[string]any); ok {
					if d, ok := m["deploy"].(map[string]any); ok {
						keptDeploy = untakenDeployKept(d)
					}
				}
				if len(keptDeploy) == 0 {
					continue
				}
				var kept yaml.Node
				if err := kept.Encode(keptDeploy); err != nil {
					continue
				}
				svcCopy.Content = append(svcCopy.Content, pairs[k], &kept)
			}
			services.Content[j+1] = svcCopy
			changed = true
		}
		newRoot.Content[i+1] = services
	}
	if !changed {
		return doc
	}
	if root == doc {
		return newRoot
	}
	out := *doc
	out.Content = []*yaml.Node{newRoot}
	return &out
}

// withoutNegativeRetries is a file without a `healthcheck.retries` that is a negative number: docker compose refuses one where it stays in
// the project the files make, and reads it where a later file or an extending service writes the count over it or resets it, or where
// it stands in a service nothing takes (measured, v5.5.1: #1774). It is the copy each file is checked by; the merged files are decoded
// whole, where a count that stays is refused (retriesCount).
func withoutNegativeRetries(doc *yaml.Node) *yaml.Node {
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return doc
	}
	newRoot := copyMapping(root)
	changed := false
	for i := 0; i+1 < len(newRoot.Content); i += 2 {
		if newRoot.Content[i].Value != "services" || newRoot.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		services := copyMapping(newRoot.Content[i+1])
		for j := 0; j+1 < len(services.Content); j += 2 {
			svc := services.Content[j+1]
			if svc.Kind != yaml.MappingNode {
				continue
			}
			svcCopy := copyMapping(svc)
			svcCopy.Content = svcCopy.Content[:0]
			for k := 0; k+1 < len(svc.Content); k += 2 {
				key, val := svc.Content[k], unalias(svc.Content[k+1])
				if key.Value != "healthcheck" || val.Kind != yaml.MappingNode {
					svcCopy.Content = append(svcCopy.Content, svc.Content[k], svc.Content[k+1])
					continue
				}
				hc := copyMapping(val)
				hc.Content = hc.Content[:0]
				for h := 0; h+1 < len(val.Content); h += 2 {
					if val.Content[h].Value == "retries" && negativeCount(val.Content[h+1]) {
						changed = true
						continue
					}
					hc.Content = append(hc.Content, val.Content[h], val.Content[h+1])
				}
				svcCopy.Content = append(svcCopy.Content, key, hc)
			}
			services.Content[j+1] = svcCopy
		}
		newRoot.Content[i+1] = services
	}
	if !changed {
		return doc
	}
	if root == doc {
		return newRoot
	}
	out := *doc
	out.Content = []*yaml.Node{newRoot}
	return &out
}

// withoutNegativeRetriesTree is a merged tree of the files so far without a `healthcheck.retries` that is a negative number.
func withoutNegativeRetriesTree(merged map[string]any) map[string]any {
	services, ok := merged["services"].(map[string]any)
	if !ok {
		return merged
	}
	changed := false
	cleaned := make(map[string]any, len(services))
	for name, v := range services {
		svc, ok := v.(map[string]any)
		hc, hok := map[string]any(nil), false
		if ok {
			hc, hok = svc["healthcheck"].(map[string]any)
		}
		if !hok || !negativeCountValue(hc["retries"]) {
			cleaned[name] = v
			continue
		}
		changed = true
		hcCopy := make(map[string]any, len(hc))
		for k, x := range hc {
			if k != "retries" {
				hcCopy[k] = x
			}
		}
		svcCopy := make(map[string]any, len(svc))
		for k, x := range svc {
			svcCopy[k] = x
		}
		svcCopy["healthcheck"] = hcCopy
		cleaned[name] = svcCopy
	}
	if !changed {
		return merged
	}
	out := make(map[string]any, len(merged))
	for k, v := range merged {
		out[k] = v
	}
	out["services"] = cleaned
	return out
}

// negativeCountValue says that a decoded value is a number below zero, or a quoted whole number below zero.
func negativeCountValue(v any) bool {
	switch x := v.(type) {
	case int:
		return x < 0
	case int64:
		return x < 0
	case float64:
		return x < 0
	case string:
		n, err := strconv.Atoi(x)
		return err == nil && n < 0
	}
	return false
}

// negativeCount says that a node is a number below zero: bare, or quoted when it is a whole number.
func negativeCount(n *yaml.Node) bool {
	n = unalias(n)
	if n.Kind != yaml.ScalarNode {
		return false
	}
	switch n.Tag {
	case "!!int", "!!float":
		if v, err := strconv.ParseInt(n.Value, 0, 64); err == nil {
			return v < 0 // `-0x1`, `-0o7`, `-0b1`, `-1_0`
		}
		if f, err := strconv.ParseFloat(strings.ReplaceAll(n.Value, "_", ""), 64); err == nil {
			return f < 0
		}
	case "!!str":
		// A quoted count must be a whole number as it is read (retriesCount): a word, or a fraction, is refused in the file that writes it.
		if v, err := strconv.Atoi(n.Value); err == nil {
			return v < 0
		}
	}
	return false
}

// replicasKind says what a `deploy.replicas` is for merging: a `list`, a `mapping`, or a `count` (anything else).
func replicasKind(v any) string {
	switch v.(type) {
	case []any:
		return "list"
	case map[string]any:
		return "mapping"
	}
	return "count"
}

// orFile is the words that name the file a service is taken from, when it is another's.
func orFile(fromFile string) string {
	if fromFile == "" {
		return ""
	}
	return " (or " + fromFile + ", the file it extends)"
}

// uncastTarget says that a long `ports` entry has a `target` docker compose does not cast as the file is read: anything that is not a
// string, a binary or a null — a number (a fraction, a negative, one past 65535), a boolean, a timestamp, a list, a mapping. A word is cast
// (and refused) wherever it is; the rest is read where the service is merged (measured, v5.5.1: #1780).
func uncastTarget(entry *yaml.Node) bool {
	v := keyNodeThroughMerge(entry, "target")
	if v == nil {
		return false
	}
	switch v.Kind {
	case yaml.SequenceNode, yaml.MappingNode:
		return true
	case yaml.ScalarNode:
		switch v.Tag {
		case "!!int", "!!float", "!!bool", "!!timestamp":
			return true
		}
	}
	return false
}

// holdsNothing says that a node is nothing, or holds nothing in a mapping or a list inside it, at any depth.
func holdsNothing(n *yaml.Node) bool {
	if isNothing(n) {
		return true
	}
	n = unalias(n)
	for _, c := range n.Content {
		if holdsNothing(c) {
			return true
		}
	}
	return false
}

// deferredTypedKeys are the keys of a service that docker compose reads into no type while it reads a file that is only
// extended from, a service taken and one not alike: the extender may write over them, and it asks them (measured, v5.5.1,
// `config -q`, the service as it comes out of the extends) of the service that results (typesThatStay, #1771). The casts of
// numbers and booleans, `ports` as a whole and the `target` of a long entry, `build`, `depends_on`, `env_file`, and `healthcheck.retries` and `healthcheck.disable`, and the list form of `environment`, `labels` and `sysctls`, are
// asked of the file as they are everywhere, and so are not here.
var deferredTypedKeys = map[string]bool{
	"command": true, "entrypoint": true, "environment": true, "labels": true, "extra_hosts": true, "sysctls": true,
}

// deferredPortFields are the fields of a long `ports` entry that docker compose asks of the service an extends results in.
var deferredPortFields = map[string]bool{"published": true, "mode": true, "protocol": true, "host_ip": true}

// withoutDeferredTypes is a file that is only extended from (only names the service taken) without what docker compose asks only of
// the service that results from the extends: the keys of deferredTypedKeys, a `deploy` that is not a mapping, `healthcheck` but its `retries`, the long entries of `ports`
// but their `target` and the like (deferredPortFields), and in a service that is not taken every key readAsItIsWhereNotTaken lists.
// It is the copy the file is checked by; what the service holds is taken from the file as it was written. A file read whole is
// returned as it is.
func withoutDeferredTypes(doc *yaml.Node, only string) *yaml.Node {
	if only == "" {
		return doc
	}
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return doc
	}
	var generic struct {
		Services map[string]any `yaml:"services"`
	}
	if err := doc.Decode(&generic); err != nil {
		return doc
	}
	taken := takenServices(generic.Services, only)
	newRoot := copyMapping(root)
	for i := 0; i+1 < len(newRoot.Content); i += 2 {
		if newRoot.Content[i].Value != "services" || newRoot.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		services := copyMapping(newRoot.Content[i+1])
		for j := 0; j+1 < len(services.Content); j += 2 {
			svc := services.Content[j+1]
			if svc.Kind != yaml.MappingNode {
				continue
			}
			isTaken := taken[services.Content[j].Value]
			svcCopy := copyMapping(svc)
			svcCopy.Content = svcCopy.Content[:0]
			for k := 0; k+1 < len(svc.Content); k += 2 {
				key, val := svc.Content[k], svc.Content[k+1]
				// What holds nothing, or has a key or an entry that holds nothing, is asked as it has been (a key with nothing after it
				// is read by its own rules: refusedOverAValue, nullsThatStay).
				if holdsNothing(val) {
					svcCopy.Content = append(svcCopy.Content, key, val)
					continue
				}
				switch {
				case (key.Value == "environment" || key.Value == "labels" || key.Value == "sysctls") && val.Kind == yaml.SequenceNode:
					// The entries of the list form are read into strings as the file is read (`[1]`, `[true]`): asked of the file
					// (measured, v5.5.1), where the mapping form and a scalar are asked of the service that results.
					svcCopy.Content = append(svcCopy.Content, key, val)
				case deferredTypedKeys[key.Value], !isTaken && readAsItIsWhereNotTaken[key.Value]:
					continue
				case !isTaken && key.Value == "cpus" && (unalias(val).Kind == yaml.SequenceNode || unalias(val).Kind == yaml.MappingNode):
					// docker compose casts a number or a word of `cpus` before it knows the service is not taken (`abc` is refused there, #1574),
					// and reads a list or a mapping as it is (measured, v5.5.1: `[1]`, `[]` and `{a: 1}`, in a service nothing takes).
					continue
				case (key.Value == "stop_grace_period" || key.Value == "shm_size") && val.Kind == yaml.ScalarNode:
					// A number or a word (`5`, `abc`) is read by docker compose in the service the extends results in; a list is
					// asked of the file (measured, v5.5.1).
					continue
				case key.Value == "deploy":
					// The blocks of a `deploy` that is a mapping are asked as they have been (untakenDeployKept, deployShapes); one that is
					// not a mapping is the one kind of it docker compose reads into no type.
					if val.Kind == yaml.MappingNode {
						svcCopy.Content = append(svcCopy.Content, key, val)
					}
				case key.Value == "healthcheck":
					if val.Kind != yaml.MappingNode {
						continue
					}
					kept := copyMapping(val)
					kept.Content = kept.Content[:0]
					for h := 0; h+1 < len(val.Content); h += 2 {
						// `disable` is cast to a boolean as the file is read when it is a word (`abc`); a number is read.
						if v := val.Content[h].Value; v == "retries" || v == "disable" && val.Content[h+1].Tag == "!!str" {
							kept.Content = append(kept.Content, val.Content[h], val.Content[h+1])
						}
					}
					svcCopy.Content = append(svcCopy.Content, key, kept)
				case key.Value == "ports" && val.Kind == yaml.SequenceNode:
					ports := copyMapping(val)
					ports.Content = make([]*yaml.Node, 0, len(val.Content))
					for _, entry := range val.Content {
						if entry.Kind != yaml.MappingNode {
							ports.Content = append(ports.Content, entry)
							continue
						}
						// docker compose reads a long entry's `target` as it comes in a file that is only extended from: one that is a fraction, a
						// boolean, a list or a mapping is left in a service nothing takes, and in the service taken where the extender writes `ports`
						// over it, and refused where it stays (the decode of the merged files asks); a word is refused wherever it is (measured, v5.5.1: #1780).
						if uncastTarget(entry) {
							continue
						}
						e := copyMapping(entry)
						e.Content = e.Content[:0]
						for f := 0; f+1 < len(entry.Content); f += 2 {
							if !deferredPortFields[entry.Content[f].Value] {
								e.Content = append(e.Content, entry.Content[f], entry.Content[f+1])
							}
						}
						ports.Content = append(ports.Content, e)
					}
					svcCopy.Content = append(svcCopy.Content, key, ports)
				default:
					svcCopy.Content = append(svcCopy.Content, key, val)
				}
			}
			services.Content[j+1] = svcCopy
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

// refusedOverAValue names the keys of a service that docker compose refuses with
// nothing after them even where an earlier file gave the key a value (measured, v5.5.1,
// `config -q`, 119 keys of a service, nested ones included): `depends_on`, `logging`,
// `networks` and `models` as a whole, `healthcheck.test`, `logging.driver`, and each entry of
// `ulimits` and of `extra_hosts`, a build's included (#1593). Every other key measured is "not given" there (#1589).
func refusedOverAValue(path []string) bool {
	switch len(path) {
	case 1:
		return path[0] == "depends_on" || path[0] == "logging" || path[0] == "networks" || path[0] == "models"
	case 2:
		return (path[0] == "healthcheck" && path[1] == "test") || (path[0] == "logging" && path[1] == "driver") || path[0] == "ulimits" || path[0] == "extra_hosts"
	case 3:
		return path[0] == "build" && path[1] == "extra_hosts"
	}
	return false
}

// longFormOfShort is what an earlier file's short form says in the long form a later
// file may write a field of: a dependency listed by name has its condition
// (`depends_on: [db]` is `{db: {condition: service_started}}`), and a build given as
// a path has its context (`build: .` is `{context: .}`). Measured, v5.5.1: a later
// `condition: ~` or `context: ~` over either is "not given" (rc 0).
func longFormOfShort(key string, backing any) any {
	switch b := backing.(type) {
	case []any:
		if key != "depends_on" {
			return backing
		}
		long := make(map[string]any, len(b))
		for _, name := range b {
			if s, ok := name.(string); ok {
				long[s] = map[string]any{"condition": "service_started"}
			}
		}
		return long
	case string:
		if key == "build" {
			return map[string]any{"context": b}
		}
	}
	return backing
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

// serviceWithoutBody is the refusal of a service of a merged tree that holds nothing; nil when there is none.
func serviceWithoutBody(path string, merged map[string]any) error {
	services, _ := merged["services"].(map[string]any)
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if services[name] == nil {
			return fmt.Errorf("compose file %s: service %q must be a mapping — the key has nothing under it once the files so far are merged; give it at least `image:` or `build:`, or remove the key", path, name)
		}
	}
	return nil
}

// addLinkedDeps puts the services a service `links:` to, and the one its `network_mode: service:` names, among its
// dependencies, as docker compose does (`depends_on: {b: {condition: service_started, required: true}}`, measured v5.5.1): the
// order they start in, what `up a` brings with it, and what the commands that read the dependencies refuse follow from
// that. A target the file does not define is refused where the service has no `profiles:` — one that has them is asked
// once a command knows it is active (orchestrator.checkLinkedRefs), as an undefined `depends_on` target is.
func addLinkedDeps(services map[string]*Service, names []string) error {
	for _, name := range names {
		svc := services[name]
		if svc == nil {
			continue
		}
		// The `network_mode: service:` target is kept with the links: the value itself is cleared further down (only `none` is acted on).
		svc.Linked = svc.LinkedNames()
		for _, target := range svc.Linked {
			if t, ok := services[target]; !ok || t == nil {
				if len(svc.Profiles) != 0 {
					continue
				}
				return fmt.Errorf("service %q depends on undefined service %q: invalid compose project", name, target)
			}
			if !slices.ContainsFunc(svc.DependsOn, func(d Dependency) bool { return d.Name == target }) {
				svc.DependsOn = append(svc.DependsOn, Dependency{Name: target, Condition: ConditionStarted})
			}
		}
	}
	return nil
}
