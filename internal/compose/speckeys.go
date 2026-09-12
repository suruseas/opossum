package compose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// specKnows reports whether docker compose takes key at the place path
// names in a compose file — a service (`services.*`), a block under it
// (`services.*.build`), an item of a list written in its long form
// (`services.*.ports[]`), a top-level declaration (`volumes.*`), or the
// top level itself (""). An `x-` key is taken anywhere. A place the table
// does not name is not checked, so nothing is refused there.
//
// This is what docker compose validates a file against before anything
// runs: a key it does not know is refused (`services.web additional
// properties 'foo' not allowed`), so a typo — `enviroment:` — stops the
// file rather than starting the service without its variables. The keys
// come from the compose specification's schema (see testdata/compose-spec
// .version for the copy they were taken from), not from what opossum
// reads: a key docker compose takes that opossum does not act on is
// listed among the ignored fields, as before, and only a key docker
// compose would refuse is refused here.
func specKnows(path, key string) bool {
	if strings.HasPrefix(key, "x-") {
		return true
	}
	keys, ok := specKeys[path]
	if !ok {
		return true
	}
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// opossumOwnServiceKeys are service keys opossum reads that docker compose
// has no such key for (it would refuse them): `ssh: true`, which forwards
// the host's SSH agent (docker compose takes `ssh` under `build` only).
// They are read before the refusal is reached, so the table above never
// sees them; this names them so the test that checks opossum's keys
// against the schema knows which ones are opossum's own.
var opossumOwnServiceKeys = map[string]bool{"ssh": true}

// unknownKeyErr is the refusal for a key docker compose does not take at
// the place `where` names ("" for the top level).
func unknownKeyErr(where, key string) error {
	if where == "" {
		return fmt.Errorf("%q is not a key docker compose takes — check the spelling, or write it as `x-%s` to keep it as a note", key, key)
	}
	return fmt.Errorf("%s: %q is not a key docker compose takes — check the spelling, or write it as `x-%s` to keep it as a note", where, key, key)
}

// specKeysFromSchema reads the compose specification's JSON schema and
// returns, for each place the table names, the keys it takes there, sorted.
// The test compares its result with specKeys, so the table cannot drift
// from the schema copy it was generated from.
func specKeysFromSchema(data []byte) (map[string][]string, error) {
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	defs, _ := schema["$defs"].(map[string]any)
	if defs == nil {
		return nil, fmt.Errorf("schema has no $defs")
	}
	// resolve follows a $ref to its definition.
	resolve := func(node map[string]any) map[string]any {
		if ref, ok := node["$ref"].(string); ok {
			name := ref[strings.LastIndex(ref, "/")+1:]
			d, _ := defs[name].(map[string]any)
			return d
		}
		return node
	}
	// props returns the property names of an object node, looking through
	// a $ref, a oneOf (the object alternative), and a list's items.
	var props func(node map[string]any) []string
	props = func(node map[string]any) []string {
		node = resolve(node)
		if node == nil {
			return nil
		}
		if p, ok := node["properties"].(map[string]any); ok {
			out := make([]string, 0, len(p))
			for k := range p {
				out = append(out, k)
			}
			sort.Strings(out)
			return out
		}
		if alts, ok := node["oneOf"].([]any); ok {
			for _, a := range alts {
				if m, ok := a.(map[string]any); ok {
					if out := props(m); out != nil {
						return out
					}
				}
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			return props(items)
		}
		return nil
	}
	// patternProps returns the property names of the single-pattern object
	// under an object's patternProperties (a dependency's mapping, an ipam
	// config's), when there is one.
	patternProps := func(node map[string]any) []string {
		node = resolve(node)
		if alts, ok := node["oneOf"].([]any); ok {
			for _, a := range alts {
				if m, ok := a.(map[string]any); ok {
					if pp, ok := m["patternProperties"].(map[string]any); ok {
						for pat, v := range pp {
							if pat == "^x-" {
								continue
							}
							if vm, ok := v.(map[string]any); ok {
								return props(vm)
							}
						}
					}
				}
			}
		}
		return nil
	}
	svc, _ := defs["service"].(map[string]any)
	svcProps, _ := svc["properties"].(map[string]any)
	get := func(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
	out := map[string][]string{}
	out[""] = props(schema)
	out["services.*"] = props(svc)
	for _, k := range []string{"build", "healthcheck", "deploy", "develop", "logging", "blkio_config", "extends"} {
		if p := props(get(svcProps, k)); p != nil {
			out["services.*."+k] = p
		}
	}
	dep := resolve(get(svcProps, "deploy"))
	res := get(get(dep, "properties"), "resources")
	out["services.*.deploy.resources"] = props(res)
	for _, k := range []string{"limits", "reservations"} {
		out["services.*.deploy.resources."+k] = props(get(get(res, "properties"), k))
	}
	dev := resolve(get(svcProps, "develop"))
	out["services.*.develop.watch[]"] = props(get(get(dev, "properties"), "watch"))
	for _, k := range []string{"ports", "volumes", "secrets", "configs", "env_file"} {
		if p := props(get(svcProps, k)); p != nil {
			out["services.*."+k+"[]"] = p
		}
	}
	// A mount's own option blocks (`bind`, `volume`, `tmpfs`): the list's
	// item is a string or an object, and the blocks are the object's.
	mount := get(svcProps, "volumes")
	if items, ok := mount["items"].(map[string]any); ok {
		if alts, ok := items["oneOf"].([]any); ok {
			for _, a := range alts {
				obj, _ := a.(map[string]any)
				objProps := get(obj, "properties")
				if objProps == nil {
					continue
				}
				for _, alt := range []string{"bind", "volume", "tmpfs"} {
					if p := props(get(objProps, alt)); p != nil {
						out["services.*.volumes[]."+alt] = p
					}
				}
			}
		}
	}
	out["services.*.depends_on.*"] = patternProps(get(svcProps, "depends_on"))
	out["services.*.networks.*"] = patternProps(get(svcProps, "networks"))
	for _, k := range []string{"volume", "network", "secret", "config"} {
		out[k+"s.*"] = props(get(defs, k))
	}
	net, _ := defs["network"].(map[string]any)
	ipam := get(get(net, "properties"), "ipam")
	out["networks.*.ipam"] = props(ipam)
	out["networks.*.ipam.config[]"] = props(get(get(ipam, "properties"), "config"))
	out["include[]"] = props(get(defs, "include"))
	for k, v := range out {
		if v == nil {
			return nil, fmt.Errorf("no keys found for %q", k)
		}
	}
	return out, nil
}
