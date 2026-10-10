package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// What docker compose answers (1 refuses, 0 reads) for a key it asks nothing of in a service nothing takes, and a few more, in a service an extends takes from another file, for each of
// the twenty-two shapes of valueShape (in the order of takenAsIsShapes), in three places: the extender writes a valid value over it, nothing does, and a second `-f` file does (measured,
// v5.5.1, `config -q`, #2004). The service that results is asked after the extends and before a later file: a value that stays is refused, one the extender writes over is read.
var takenAsIsShapes = []string{"5", "1.5", "true", "abc", `""`, "[x]", "[1]", "{a: b}", "{a: 1}", "[{a: b}]", "~", "[~]", "{a: ~}", "[{a: ~}]", "2024-01-01", "[[x]]", "[true]", "[2024-01-01]", "{a: true}", "{a: 2024-01-01}", "[1.5]", "[{a: 1}]"}

var takenAsIsOverWith = map[string]string{
	"attach":              "true",
	"blkio_config":        "{weight: 100}",
	"cgroup":              "host",
	"cgroup_parent":       "v",
	"command":             "[sh]",
	"container_name":      "v",
	"cpuset":              "0-1",
	"credential_spec":     "{config: x}",
	"device_cgroup_rules": "[x]",
	"domainname":          "v",
	"entrypoint":          "[sh]",
	"external_links":      "[x]",
	"extra_hosts":         "[h:1.2.3.4]",
	"group_add":           "[x]",
	"hostname":            "v",
	"image":               "wi",
	"ipc":                 "host",
	"isolation":           "default",
	"logging":             "{driver: json-file}",
	"mac_address":         "02:42:ac:11:00:02",
	"mem_limit":           "1g",
	"mem_reservation":     "1g",
	"mem_swappiness":      "5",
	"memswap_limit":       "1g",
	"network_mode":        "host",
	"pid":                 "host",
	"platform":            "linux/amd64",
	"post_start":          "[{command: [x]}]",
	"pre_start":           "[{command: [x]}]",
	"pre_stop":            "[{command: [x]}]",
	"provider":            "{type: x}",
	"pull_policy":         "always",
	"pull_refresh_after":  "1h",
	"restart":             "always",
	"runtime":             "v",
	"security_opt":        "[x]",
	"shm_size":            "1g",
	"stop_grace_period":   "5s",
	"stop_signal":         "SIGTERM",
	"storage_opt":         "{a: b}",
	"use_api_socket":      "true",
	"user":                "v",
	"userns_mode":         "host",
	"uts":                 "host",
	"volumes_from":        "[x]",
	"working_dir":         "/w",
	"ulimits":             "{nofile: 5}",
	"volumes":             "[\"./a:/b\"]",
}

var takenAsIsExtender = map[string]string{
	"attach":              "0000011111011101111111",
	"blkio_config":        "0000011111011101111111",
	"cgroup":              "0000011111011101111111",
	"cgroup_parent":       "0000011111011101111111",
	"command":             "0000000000000000000000",
	"container_name":      "1111111111111111111111",
	"cpuset":              "0000011111011101111111",
	"credential_spec":     "0000011111011101111111",
	"device_cgroup_rules": "0000001111011101111111",
	"domainname":          "0000011111011101111111",
	"entrypoint":          "0000000000000000000000",
	"external_links":      "0000001111011101111111",
	"extra_hosts":         "0001111001011101100011",
	"group_add":           "0000000111011101111111",
	"hostname":            "0000011111011101111111",
	"image":               "1111111111111111111111",
	"ipc":                 "0000011111011101111111",
	"isolation":           "0000011111011101111111",
	"logging":             "1111111111111111111111",
	"mac_address":         "0000011111011101111111",
	"mem_limit":           "0000011111011101111111",
	"mem_reservation":     "0000011111011101111111",
	"mem_swappiness":      "0000011111011101111111",
	"memswap_limit":       "0000011111011101111111",
	"network_mode":        "0000011111011101111111",
	"pid":                 "0000011111011101111111",
	"platform":            "0000011111011101111111",
	"post_start":          "0000011111011101111111",
	"pre_start":           "0000011111011101111111",
	"pre_stop":            "0000011111011101111111",
	"provider":            "0000011111011101111111",
	"pull_policy":         "0000011111011101111111",
	"pull_refresh_after":  "0000011111011101111111",
	"restart":             "0000011111011101111111",
	"runtime":             "0000011111011101111111",
	"security_opt":        "0000001111011101111111",
	"shm_size":            "0000011111011101111111",
	"stop_grace_period":   "0000011111011101111111",
	"stop_signal":         "0000011111011101111111",
	"storage_opt":         "0000011001010101110011",
	"use_api_socket":      "0000011111011101111111",
	"user":                "0000011111011101111111",
	"userns_mode":         "0000011111011101111111",
	"uts":                 "0000011111011101111111",
	"volumes_from":        "1111111111111111111111",
	"working_dir":         "0000011111011101111111",
	"ulimits":             "0000011101011101111111",
	"volumes":             "0000001111011101111111",
}

var takenAsIsNothing = map[string]string{
	"attach":              "1101111111111111111111",
	"blkio_config":        "1111111111111111111111",
	"cgroup":              "1111111111111111111111",
	"cgroup_parent":       "1110011111111111111111",
	"command":             "1110001111011101111111",
	"container_name":      "1110111111111111111111",
	"cpuset":              "1110011111111111111111",
	"credential_spec":     "1111111111111111111111",
	"device_cgroup_rules": "1111101111111111111111",
	"domainname":          "1110011111111111111111",
	"entrypoint":          "1110001111011101111111",
	"external_links":      "1111101111111111111111",
	"extra_hosts":         "1111111011111111101111",
	"group_add":           "1111100111111111111111",
	"hostname":            "1110011111111111111111",
	"image":               "1111111111111111111111",
	"ipc":                 "1110011111111111111111",
	"isolation":           "1110011111111111111111",
	"logging":             "1111111111111111111111",
	"mac_address":         "1110011111111111111111",
	"mem_limit":           "0011111111111101111111",
	"mem_reservation":     "0111111111111101111111",
	"mem_swappiness":      "0111111111111101111111",
	"memswap_limit":       "0011111111111101111111",
	"network_mode":        "1110011111111111111111",
	"pid":                 "1110011111111111111111",
	"platform":            "1110011111111111111111",
	"post_start":          "1111111111111111111111",
	"pre_start":           "1111111111111111111111",
	"pre_stop":            "1111111111111111111111",
	"provider":            "1111111111111111111111",
	"pull_policy":         "1111111111111111111111",
	"pull_refresh_after":  "1110011111111101111111",
	"restart":             "1110011111111111111111",
	"runtime":             "1110011111111111111111",
	"security_opt":        "1111101111111111111111",
	"shm_size":            "0011111111111101111111",
	"stop_grace_period":   "1111111111111111111111",
	"stop_signal":         "1110011111111111111111",
	"storage_opt":         "1111111001110111111111",
	"use_api_socket":      "1101111111111111111111",
	"user":                "1110011111111111111111",
	"userns_mode":         "1110011111111111111111",
	"uts":                 "1110011111111111111111",
	"volumes_from":        "1111111111111111111111",
	"working_dir":         "1110011111111111111111",
	"ulimits":             "1111111101111111111111",
	"volumes":             "1111101111111111111111",
}

var takenAsIsSecond = map[string]string{
	"attach":              "1100011111111101111111",
	"blkio_config":        "1111111111111111111111",
	"cgroup":              "1111111111111111111111",
	"cgroup_parent":       "1110011111111101111111",
	"command":             "1110001111011101101111",
	"container_name":      "1111111111111111111111",
	"cpuset":              "1110011111111101111111",
	"credential_spec":     "1111111111111111111111",
	"device_cgroup_rules": "1111101111111111111111",
	"domainname":          "1110011111111101111111",
	"entrypoint":          "1110001111011101101111",
	"external_links":      "1111101111111111111111",
	"extra_hosts":         "1111111011111111101011",
	"group_add":           "1111100111111111111111",
	"hostname":            "1110011111111101111111",
	"image":               "1111111111111111111111",
	"ipc":                 "1110011111111101111111",
	"isolation":           "1110011111111101111111",
	"logging":             "1111111111111111111111",
	"mac_address":         "1110011111111101111111",
	"mem_limit":           "0010011111111101111111",
	"mem_reservation":     "0110011111111101111111",
	"mem_swappiness":      "0110011111111101111111",
	"memswap_limit":       "0010011111111101111111",
	"network_mode":        "1110011111111101111111",
	"pid":                 "1110011111011101111111",
	"platform":            "1110011111111101111111",
	"post_start":          "1111111111111111111111",
	"pre_start":           "1111111111111111111111",
	"pre_stop":            "1111111111111111111111",
	"provider":            "1111111111111111111111",
	"pull_policy":         "1111111111111111111111",
	"pull_refresh_after":  "1110011111111101111111",
	"restart":             "1110011111111101111111",
	"runtime":             "1110011111111101111111",
	"security_opt":        "1111101111111111111111",
	"shm_size":            "0010011111111101111111",
	"stop_grace_period":   "1110011111111101111111",
	"stop_signal":         "1110011111111101111111",
	"storage_opt":         "1111111001110111110011",
	"use_api_socket":      "1101111111111111111111",
	"user":                "1110011111111101111111",
	"userns_mode":         "1110011111111101111111",
	"uts":                 "1110011111111101111111",
	"volumes_from":        "1111111111111111111111",
	"working_dir":         "1110011111111101111111",
	"ulimits":             "1111111101111111111111",
	"volumes":             "1111101111111111111111",
}

// The cells where opossum answers otherwise than docker compose, as they were before (named, not made different here). The extender's list or mapping over an `extra_hosts` word is no longer one (#1945).
var takenAsIsKnownExtender = map[string]bool{"device_cgroup_rules/[2024-01-01]": true, "external_links/[2024-01-01]": true, "security_opt/[2024-01-01]": true}

var takenAsIsKnownNothing = map[string]bool{"command/2024-01-01": true, "device_cgroup_rules/[2024-01-01]": true, "entrypoint/2024-01-01": true, "external_links/[2024-01-01]": true, "extra_hosts/{a: 2024-01-01}": true, "mac_address/abc": true, "mem_limit/2024-01-01": true, "pid/~": true, "restart/abc": true, "security_opt/[2024-01-01]": true, "shm_size/2024-01-01": true, "storage_opt/{a: 2024-01-01}": true, "storage_opt/{a: true}": true}

var takenAsIsKnownSecond = map[string]bool{"attach/\"\"": true, "attach/abc": true, "command/2024-01-01": true, "command/[2024-01-01]": true, "device_cgroup_rules/[2024-01-01]": true, "entrypoint/2024-01-01": true, "entrypoint/[2024-01-01]": true, "external_links/[2024-01-01]": true, "mac_address/1.5": true, "mac_address/5": true, "mac_address/true": true, "mem_limit/\"\"": true, "mem_limit/true": true, "mem_reservation/\"\"": true, "mem_reservation/abc": true, "mem_swappiness/\"\"": true, "mem_swappiness/abc": true, "memswap_limit/\"\"": true, "memswap_limit/abc": true, "restart/1.5": true, "restart/5": true, "restart/true": true, "security_opt/[2024-01-01]": true, "shm_size/\"\"": true, "shm_size/abc": true, "stop_grace_period/\"\"": true, "stop_grace_period/abc": true}

func TestAnAsIsValueTheExtenderWritesOverIsReadAndOneThatStaysIsRefused(t *testing.T) {
	for _, place := range []struct {
		name  string
		table map[string]string
		known map[string]bool
		files func(key, value string) map[string]string
		order []string
	}{
		{"the extender writes a value over it", takenAsIsExtender, takenAsIsKnownExtender, func(k, v string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    " + k + ": " + takenAsIsOverWith[k] + "\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": " + v + "\n"}
		}, []string{"compose.yaml"}},
		{"nothing writes it over", takenAsIsNothing, takenAsIsKnownNothing, func(k, v string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": " + v + "\n"}
		}, []string{"compose.yaml"}},
		{"a second file writes a value over it", takenAsIsSecond, takenAsIsKnownSecond, func(k, v string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "o.yaml": "services:\n  web:\n    " + k + ": " + takenAsIsOverWith[k] + "\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": " + v + "\n"}
		}, []string{"compose.yaml", "o.yaml"}},
	} {
		for key, answers := range place.table {
			t.Run(place.name+"/"+key, func(t *testing.T) {
				for i, shape := range takenAsIsShapes {
					dir := t.TempDir()
					for file, body := range place.files(key, shape) {
						if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					paths := make([]string, len(place.order))
					for j, f := range place.order {
						paths[j] = filepath.Join(dir, f)
					}
					want := answers[i] == '1'
					if place.known[key+"/"+shape] {
						want = !want
					}
					if _, err := LoadFiles(paths, nil); (err != nil) != want {
						t.Errorf("%s: refused = %v, want %v (docker compose answers %c)", shape, err != nil, want, answers[i])
					}
				}
			})
		}
	}
}
