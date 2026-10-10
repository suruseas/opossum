package compose

import "gopkg.in/yaml.v3"

// takenReadsWhenWrittenOver says, for the keys docker compose asks nothing of in a service nothing takes (readAsItIsWhereNotTaken) and a few more, which shapes of value (see
// valueShape) it reads in a service an extends takes from another file where the extender writes the key over (measured, v5.5.1, `config -q`, every key with each of the twenty-two
// shapes, #2004). The shape stays when nothing writes it over, or a later `-f` file does: the service that results is asked after the extends (typesThatStay, and the type decode
// of the first file) and refuses it there.
var takenReadsWhenWrittenOver = map[string][]string{
	"attach":              {"5", "1.5", "abc", `""`},
	"blkio_config":        {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"cgroup":              {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"cgroup_parent":       {"5", "1.5", "true"},
	"command":             {"[~]", "[{a: ~}]"},
	"cpuset":              {"5", "1.5", "true"},
	"credential_spec":     {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"device_cgroup_rules": {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"domainname":          {"5", "1.5", "true"},
	"entrypoint":          {"[~]", "[{a: ~}]"},
	"external_links":      {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"group_add":           {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"hostname":            {"5", "1.5", "true"},
	"ipc":                 {"5", "1.5", "true"},
	"isolation":           {"5", "1.5", "true"},
	"mem_limit":           {`""`},
	"mem_reservation":     {"1.5", "true", "abc", `""`},
	"mem_swappiness":      {"1.5", "true", "abc", `""`},
	"memswap_limit":       {"true", "abc", `""`},
	"network_mode":        {"5", "1.5", "true", "2024-01-01"},
	"pid":                 {"5", "1.5", "true"},
	"platform":            {"5", "1.5", "true", "2024-01-01"},
	"post_start":          {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"pre_start":           {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"pre_stop":            {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"provider":            {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"pull_policy":         {"5", "1.5", "true", "abc", `""`},
	"pull_refresh_after":  {"5", "1.5", "true"},
	"runtime":             {"5", "1.5", "true"},
	"security_opt":        {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"stop_signal":         {"5", "1.5", "true"},
	"storage_opt":         {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"ulimits":             {"5", "1.5", "true", "abc", `""`, "2024-01-01"},
	"use_api_socket":      {"5", "1.5", "abc", `""`, "2024-01-01"},
	"user":                {"5", "1.5", "true", "2024-01-01"},
	"userns_mode":         {"5", "1.5", "true"},
	"uts":                 {"5", "1.5", "true"},
	"working_dir":         {"5", "1.5", "true", "2024-01-01"},
}

// readsWhenWrittenOver reports a value of a key of a service an extends takes, of a shape docker compose reads where the extender writes the key over.
func readsWhenWrittenOver(name string, v *yaml.Node) bool {
	shapes, ok := takenReadsWhenWrittenOver[name]
	if !ok {
		return false
	}
	shape := valueShape(v)
	for _, s := range shapes {
		if s == shape {
			return true
		}
	}
	return false
}
