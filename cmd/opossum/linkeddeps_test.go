package main

import (
	"strings"
	"testing"
)

// `links:` and `network_mode: service:<name>` are dependencies, as docker compose reads them (`depends_on: {b: {condition:
// service_started, required: true}}`, v5.5.1): a service that names one the file does not define, or one behind a profile that is not
// active, is refused like a `depends_on` target, and the dependencies of the service they lead to are followed — by the commands that
// take a name (`stop`, `kill`, `logs`), as by `config` (#1802). Every row is docker compose's answer (rc 0 or not) for the same file,
// the command and, where `profile` is set, `--profile g`; `volumes_from` is held to the same by its rows, and a `depends_on` the file writes
// for the same service with `required: false` is kept over the one a link makes (a gated service only its name enables is let be).
func TestLinksAndNetworkModeServiceAreDependenciesAsDockerComposeReadsThem(t *testing.T) {
	files := map[string]string{
		"a links b, b broken (both gated)":                                      "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a network_mode service:b, b broken (both gated)":                       "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a volumes_from b, b broken (both gated)":                               "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a depends_on b, b broken (both gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links nosuch (gated)":                                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [nosuch]\n",
		"a network_mode service:nosuch (gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:nosuch\n",
		"a volumes_from nosuch (gated)":                                         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [nosuch]\n",
		"a links nosuch (ungated)":                                              "services:\n  a:\n    image: x\n    links: [nosuch]\n",
		"a network_mode service:nosuch (ungated)":                               "services:\n  a:\n    image: x\n    network_mode: service:nosuch\n",
		"a links b:alias, b defined ungated":                                    "services:\n  a:\n    image: x\n    links: [\"b:alias\"]\n  b:\n    image: y\n",
		"a links b (gated, inactive), a ungated":                                "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n",
		"a network_mode service:b (gated, inactive), a ungated":                 "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n",
		"a links b, both gated and healthy":                                     "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n",
		"a links b, b has no profile, broken":                                   "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links b + optional depends_on b, b undefined (gated)":                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a network_mode service:b + optional depends_on b, b undefined (gated)": "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a volumes_from b + optional depends_on b, b undefined (gated)":         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a depends_on b, b links nosuch (both gated)":                           "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links b, b links nosuch (both gated)":                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a volumes_from container:x (gated)":                                    "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [\"container:x\"]\n",
		"a links empty name (gated)":                                            "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"\"]\n",
		"a links empty name (ungated)":                                          "services:\n  a:\n    image: x\n    links: [\"\"]\n",
		"a network_mode service: with no name (gated)":                          "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: \"service:\"\n",
		"a links b:c:d (gated)":                                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"b:c:d\"]\n  b:\n    image: y\n    profiles: [g]\n",
		"a links :c (gated)":                                                    "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\":c\"]\n",
		"a links b, b links a (cycle)":                                          "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    links: [a]\n",
		"a links a (self)":                                                      "services:\n  a:\n    image: x\n    links: [a]\n",
		"a network_mode service:b, b links a (cycle)":                           "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    links: [a]\n",
	}
	for _, tc := range []struct {
		file, command string
		profile       bool
		refused       bool
	}{
		{"a links b, b broken (both gated)", "config", false, false},
		{"a links b, b broken (both gated)", "config", true, true},
		{"a links b, b broken (both gated)", "stop a", false, true},
		{"a links b, b broken (both gated)", "stop a", true, true},
		{"a links b, b broken (both gated)", "kill a", false, true},
		{"a links b, b broken (both gated)", "kill a", true, true},
		{"a links b, b broken (both gated)", "logs a", false, true},
		{"a links b, b broken (both gated)", "logs a", true, true},
		{"a network_mode service:b, b broken (both gated)", "config", false, false},
		{"a network_mode service:b, b broken (both gated)", "config", true, true},
		{"a network_mode service:b, b broken (both gated)", "stop a", false, true},
		{"a network_mode service:b, b broken (both gated)", "stop a", true, true},
		{"a network_mode service:b, b broken (both gated)", "kill a", false, true},
		{"a network_mode service:b, b broken (both gated)", "kill a", true, true},
		{"a network_mode service:b, b broken (both gated)", "logs a", false, true},
		{"a network_mode service:b, b broken (both gated)", "logs a", true, true},
		{"a volumes_from b, b broken (both gated)", "config", false, false},
		{"a volumes_from b, b broken (both gated)", "config", true, true},
		{"a volumes_from b, b broken (both gated)", "stop a", false, true},
		{"a volumes_from b, b broken (both gated)", "stop a", true, true},
		{"a volumes_from b, b broken (both gated)", "kill a", false, true},
		{"a volumes_from b, b broken (both gated)", "kill a", true, true},
		{"a volumes_from b, b broken (both gated)", "logs a", false, true},
		{"a volumes_from b, b broken (both gated)", "logs a", true, true},
		{"a depends_on b, b broken (both gated)", "config", false, false},
		{"a depends_on b, b broken (both gated)", "config", true, true},
		{"a depends_on b, b broken (both gated)", "stop a", false, true},
		{"a depends_on b, b broken (both gated)", "stop a", true, true},
		{"a depends_on b, b broken (both gated)", "kill a", false, true},
		{"a depends_on b, b broken (both gated)", "kill a", true, true},
		{"a depends_on b, b broken (both gated)", "logs a", false, true},
		{"a depends_on b, b broken (both gated)", "logs a", true, true},
		{"a links nosuch (gated)", "config", false, false},
		{"a links nosuch (gated)", "config", true, true},
		{"a links nosuch (gated)", "stop a", false, true},
		{"a links nosuch (gated)", "stop a", true, true},
		{"a links nosuch (gated)", "kill a", false, true},
		{"a links nosuch (gated)", "kill a", true, true},
		{"a links nosuch (gated)", "logs a", false, true},
		{"a links nosuch (gated)", "logs a", true, true},
		{"a network_mode service:nosuch (gated)", "config", false, false},
		{"a network_mode service:nosuch (gated)", "config", true, true},
		{"a network_mode service:nosuch (gated)", "stop a", false, true},
		{"a network_mode service:nosuch (gated)", "stop a", true, true},
		{"a network_mode service:nosuch (gated)", "kill a", false, true},
		{"a network_mode service:nosuch (gated)", "kill a", true, true},
		{"a network_mode service:nosuch (gated)", "logs a", false, true},
		{"a network_mode service:nosuch (gated)", "logs a", true, true},
		{"a volumes_from nosuch (gated)", "config", false, false},
		{"a volumes_from nosuch (gated)", "config", true, true},
		{"a volumes_from nosuch (gated)", "stop a", false, true},
		{"a volumes_from nosuch (gated)", "stop a", true, true},
		{"a volumes_from nosuch (gated)", "kill a", false, true},
		{"a volumes_from nosuch (gated)", "kill a", true, true},
		{"a volumes_from nosuch (gated)", "logs a", false, true},
		{"a volumes_from nosuch (gated)", "logs a", true, true},
		{"a links nosuch (ungated)", "config", false, true},
		{"a links nosuch (ungated)", "config", true, true},
		{"a links nosuch (ungated)", "stop a", false, true},
		{"a links nosuch (ungated)", "stop a", true, true},
		{"a links nosuch (ungated)", "kill a", false, true},
		{"a links nosuch (ungated)", "kill a", true, true},
		{"a links nosuch (ungated)", "logs a", false, true},
		{"a links nosuch (ungated)", "logs a", true, true},
		{"a network_mode service:nosuch (ungated)", "config", false, true},
		{"a network_mode service:nosuch (ungated)", "config", true, true},
		{"a network_mode service:nosuch (ungated)", "stop a", false, true},
		{"a network_mode service:nosuch (ungated)", "stop a", true, true},
		{"a network_mode service:nosuch (ungated)", "kill a", false, true},
		{"a network_mode service:nosuch (ungated)", "kill a", true, true},
		{"a network_mode service:nosuch (ungated)", "logs a", false, true},
		{"a network_mode service:nosuch (ungated)", "logs a", true, true},
		{"a links b:alias, b defined ungated", "config", false, false},
		{"a links b:alias, b defined ungated", "config", true, false},
		{"a links b:alias, b defined ungated", "stop a", false, false},
		{"a links b:alias, b defined ungated", "stop a", true, false},
		{"a links b:alias, b defined ungated", "kill a", false, false},
		{"a links b:alias, b defined ungated", "kill a", true, false},
		{"a links b:alias, b defined ungated", "logs a", false, false},
		{"a links b:alias, b defined ungated", "logs a", true, false},
		{"a links b (gated, inactive), a ungated", "config", false, true},
		{"a links b (gated, inactive), a ungated", "config", true, false},
		{"a links b (gated, inactive), a ungated", "stop a", false, true},
		{"a links b (gated, inactive), a ungated", "stop a", true, false},
		{"a links b (gated, inactive), a ungated", "kill a", false, true},
		{"a links b (gated, inactive), a ungated", "kill a", true, false},
		{"a links b (gated, inactive), a ungated", "logs a", false, true},
		{"a links b (gated, inactive), a ungated", "logs a", true, false},
		{"a network_mode service:b (gated, inactive), a ungated", "config", false, true},
		{"a network_mode service:b (gated, inactive), a ungated", "config", true, false},
		{"a network_mode service:b (gated, inactive), a ungated", "stop a", false, true},
		{"a network_mode service:b (gated, inactive), a ungated", "stop a", true, false},
		{"a network_mode service:b (gated, inactive), a ungated", "kill a", false, true},
		{"a network_mode service:b (gated, inactive), a ungated", "kill a", true, false},
		{"a network_mode service:b (gated, inactive), a ungated", "logs a", false, true},
		{"a network_mode service:b (gated, inactive), a ungated", "logs a", true, false},
		{"a links b, both gated and healthy", "config", false, false},
		{"a links b, both gated and healthy", "config", true, false},
		{"a links b, both gated and healthy", "stop a", false, false},
		{"a links b, both gated and healthy", "stop a", true, false},
		{"a links b, both gated and healthy", "kill a", false, false},
		{"a links b, both gated and healthy", "kill a", true, false},
		{"a links b, both gated and healthy", "logs a", false, false},
		{"a links b, both gated and healthy", "logs a", true, false},
		{"a links b, b has no profile, broken", "config", false, true},
		{"a links b, b has no profile, broken", "config", true, true},
		{"a links b, b has no profile, broken", "stop a", false, true},
		{"a links b, b has no profile, broken", "stop a", true, true},
		{"a links b, b has no profile, broken", "kill a", false, true},
		{"a links b, b has no profile, broken", "kill a", true, true},
		{"a links b, b has no profile, broken", "logs a", false, true},
		{"a links b, b has no profile, broken", "logs a", true, true},
		{"a links b + optional depends_on b, b undefined (gated)", "config", false, false},
		{"a links b + optional depends_on b, b undefined (gated)", "config", true, true},
		{"a links b + optional depends_on b, b undefined (gated)", "stop a", false, false},
		{"a links b + optional depends_on b, b undefined (gated)", "stop a", true, true},
		{"a links b + optional depends_on b, b undefined (gated)", "kill a", false, false},
		{"a links b + optional depends_on b, b undefined (gated)", "kill a", true, true},
		{"a links b + optional depends_on b, b undefined (gated)", "logs a", false, false},
		{"a links b + optional depends_on b, b undefined (gated)", "logs a", true, true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "config", false, false},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "config", true, true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "stop a", false, false},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "stop a", true, true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "kill a", false, false},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "kill a", true, true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "logs a", false, false},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "logs a", true, true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "config", false, false},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "config", true, true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "stop a", false, false},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "stop a", true, true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "kill a", false, false},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "kill a", true, true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "logs a", false, false},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "logs a", true, true},
		{"a depends_on b, b links nosuch (both gated)", "config", false, false},
		{"a depends_on b, b links nosuch (both gated)", "config", true, true},
		{"a depends_on b, b links nosuch (both gated)", "stop a", false, true},
		{"a depends_on b, b links nosuch (both gated)", "stop a", true, true},
		{"a depends_on b, b links nosuch (both gated)", "kill a", false, true},
		{"a depends_on b, b links nosuch (both gated)", "kill a", true, true},
		{"a depends_on b, b links nosuch (both gated)", "logs a", false, true},
		{"a depends_on b, b links nosuch (both gated)", "logs a", true, true},
		{"a links b, b links nosuch (both gated)", "config", false, false},
		{"a links b, b links nosuch (both gated)", "config", true, true},
		{"a links b, b links nosuch (both gated)", "stop a", false, true},
		{"a links b, b links nosuch (both gated)", "stop a", true, true},
		{"a links b, b links nosuch (both gated)", "kill a", false, true},
		{"a links b, b links nosuch (both gated)", "kill a", true, true},
		{"a links b, b links nosuch (both gated)", "logs a", false, true},
		{"a links b, b links nosuch (both gated)", "logs a", true, true},
		{"a volumes_from container:x (gated)", "config", false, false},
		{"a volumes_from container:x (gated)", "stop a", false, false},
		{"a volumes_from container:x (gated)", "kill a", false, false},
		{"a volumes_from container:x (gated)", "logs a", false, false},
		{"a links empty name (gated)", "config", false, false},
		{"a links empty name (gated)", "config", true, true},
		{"a links empty name (gated)", "stop a", false, true},
		{"a links empty name (gated)", "stop a", true, true},
		{"a links empty name (gated)", "kill a", false, true},
		{"a links empty name (gated)", "kill a", true, true},
		{"a links empty name (gated)", "logs a", false, true},
		{"a links empty name (gated)", "logs a", true, true},
		{"a links empty name (ungated)", "config", false, true},
		{"a links empty name (ungated)", "config", true, true},
		{"a links empty name (ungated)", "stop a", false, true},
		{"a links empty name (ungated)", "stop a", true, true},
		{"a links empty name (ungated)", "kill a", false, true},
		{"a links empty name (ungated)", "kill a", true, true},
		{"a links empty name (ungated)", "logs a", false, true},
		{"a links empty name (ungated)", "logs a", true, true},
		{"a network_mode service: with no name (gated)", "config", false, false},
		{"a network_mode service: with no name (gated)", "config", true, true},
		{"a network_mode service: with no name (gated)", "stop a", false, true},
		{"a network_mode service: with no name (gated)", "stop a", true, true},
		{"a network_mode service: with no name (gated)", "kill a", false, true},
		{"a network_mode service: with no name (gated)", "kill a", true, true},
		{"a network_mode service: with no name (gated)", "logs a", false, true},
		{"a network_mode service: with no name (gated)", "logs a", true, true},
		{"a links b:c:d (gated)", "config", false, false},
		{"a links b:c:d (gated)", "config", true, true},
		{"a links b:c:d (gated)", "stop a", false, true},
		{"a links b:c:d (gated)", "stop a", true, true},
		{"a links b:c:d (gated)", "kill a", false, true},
		{"a links b:c:d (gated)", "kill a", true, true},
		{"a links b:c:d (gated)", "logs a", false, true},
		{"a links b:c:d (gated)", "logs a", true, true},
		{"a links :c (gated)", "config", false, false},
		{"a links :c (gated)", "config", true, true},
		{"a links :c (gated)", "stop a", false, true},
		{"a links :c (gated)", "stop a", true, true},
		{"a links :c (gated)", "kill a", false, true},
		{"a links :c (gated)", "kill a", true, true},
		{"a links :c (gated)", "logs a", false, true},
		{"a links :c (gated)", "logs a", true, true},
	} {
		profile := ""
		if tc.profile {
			profile = " with --profile g"
		}
		t.Run(tc.file+": "+tc.command+profile, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			args := []string{"-f", writeCompose(t, files[tc.file])}
			if tc.profile {
				args = append(args, "--profile", "g")
			}
			switch tc.command {
			case "config":
				args = append(args, "config")
			default:
				args = append(args, strings.Fields(tc.command)...)
			}
			out, err := run(t, args...)
			if (err != nil) != tc.refused {
				t.Errorf("%s: err %v, docker compose refuses it: %v\n%s", tc.command, err, tc.refused, out)
			}
		})
	}
}

// What docker compose refuses of the project as a whole (`config -q`, the rows of the table above that it refuses), `up` refuses too, whether the
// services it reads are all of them (no `profiles:`) or those a `--profile g` turns on (#1802).
func TestAnUpOfALinkedProjectDockerComposeRefusesIsRefused(t *testing.T) {
	files := map[string]string{
		"a links b, b broken (both gated)":                                      "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a network_mode service:b, b broken (both gated)":                       "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a volumes_from b, b broken (both gated)":                               "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a depends_on b, b broken (both gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links nosuch (gated)":                                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [nosuch]\n",
		"a network_mode service:nosuch (gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:nosuch\n",
		"a volumes_from nosuch (gated)":                                         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [nosuch]\n",
		"a links nosuch (ungated)":                                              "services:\n  a:\n    image: x\n    links: [nosuch]\n",
		"a network_mode service:nosuch (ungated)":                               "services:\n  a:\n    image: x\n    network_mode: service:nosuch\n",
		"a links b, b has no profile, broken":                                   "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links b + optional depends_on b, b undefined (gated)":                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a network_mode service:b + optional depends_on b, b undefined (gated)": "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a volumes_from b + optional depends_on b, b undefined (gated)":         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a depends_on b, b links nosuch (both gated)":                           "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links b, b links nosuch (both gated)":                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links empty name (gated)":                                            "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"\"]\n",
		"a links empty name (ungated)":                                          "services:\n  a:\n    image: x\n    links: [\"\"]\n",
		"a network_mode service: with no name (gated)":                          "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: \"service:\"\n",
		"a links b:c:d (gated)":                                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"b:c:d\"]\n  b:\n    image: y\n    profiles: [g]\n",
		"a links :c (gated)":                                                    "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\":c\"]\n",
		"a links b, b links a (cycle)":                                          "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    links: [a]\n",
		"a links a (self)":                                                      "services:\n  a:\n    image: x\n    links: [a]\n",
		"a network_mode service:b, b links a (cycle)":                           "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    links: [a]\n",
	}
	for _, tc := range []struct {
		file    string
		profile bool
	}{
		{"a links b, b broken (both gated)", true},
		{"a network_mode service:b, b broken (both gated)", true},
		{"a volumes_from b, b broken (both gated)", true},
		{"a depends_on b, b broken (both gated)", true},
		{"a links nosuch (gated)", true},
		{"a network_mode service:nosuch (gated)", true},
		{"a volumes_from nosuch (gated)", true},
		{"a links nosuch (ungated)", false},
		{"a links nosuch (ungated)", true},
		{"a network_mode service:nosuch (ungated)", false},
		{"a network_mode service:nosuch (ungated)", true},
		{"a links b, b has no profile, broken", false},
		{"a links b, b has no profile, broken", true},
		{"a links b + optional depends_on b, b undefined (gated)", true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", true},
		{"a depends_on b, b links nosuch (both gated)", true},
		{"a links b, b links nosuch (both gated)", true},
		{"a links empty name (gated)", true},
		{"a links empty name (ungated)", false},
		{"a links empty name (ungated)", true},
		{"a network_mode service: with no name (gated)", true},
		{"a links b:c:d (gated)", true},
		{"a links :c (gated)", true},
		{"a links b, b links a (cycle)", false},
		{"a links b, b links a (cycle)", true},
		{"a links a (self)", false},
		{"a links a (self)", true},
		{"a network_mode service:b, b links a (cycle)", false},
		{"a network_mode service:b, b links a (cycle)", true},
	} {
		profile := ""
		if tc.profile {
			profile = " with --profile g"
		}
		t.Run(tc.file+profile, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			args := []string{"-f", writeCompose(t, files[tc.file])}
			if tc.profile {
				args = append(args, "--profile", "g")
			}
			out, err := run(t, append(args, "up", "--dry-run")...)
			if err == nil {
				t.Errorf("up was not refused, docker compose refuses the project:\n%s", out)
			}
		})
	}
}

// And `start a`, which reads the project's dependencies the same way (docker compose answers each of these files with `no such service`,
// `depends on undefined service` or `dependency cycle detected`).
func TestAStartOfALinkedServiceDockerComposeRefusesIsRefused(t *testing.T) {
	files := map[string]string{
		"a links b, b broken (both gated)":                                      "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a network_mode service:b, b broken (both gated)":                       "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a volumes_from b, b broken (both gated)":                               "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a depends_on b, b broken (both gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links nosuch (gated)":                                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [nosuch]\n",
		"a network_mode service:nosuch (gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:nosuch\n",
		"a volumes_from nosuch (gated)":                                         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [nosuch]\n",
		"a links nosuch (ungated)":                                              "services:\n  a:\n    image: x\n    links: [nosuch]\n",
		"a network_mode service:nosuch (ungated)":                               "services:\n  a:\n    image: x\n    network_mode: service:nosuch\n",
		"a links b (gated, inactive), a ungated":                                "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n",
		"a network_mode service:b (gated, inactive), a ungated":                 "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n",
		"a links b, b has no profile, broken":                                   "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links b + optional depends_on b, b undefined (gated)":                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a network_mode service:b + optional depends_on b, b undefined (gated)": "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a volumes_from b + optional depends_on b, b undefined (gated)":         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a depends_on b, b links nosuch (both gated)":                           "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links b, b links nosuch (both gated)":                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links empty name (gated)":                                            "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"\"]\n",
		"a links empty name (ungated)":                                          "services:\n  a:\n    image: x\n    links: [\"\"]\n",
		"a network_mode service: with no name (gated)":                          "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: \"service:\"\n",
		"a links b:c:d (gated)":                                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"b:c:d\"]\n  b:\n    image: y\n    profiles: [g]\n",
		"a links :c (gated)":                                                    "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\":c\"]\n",
		"a links b, b links a (cycle)":                                          "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    links: [a]\n",
		"a links a (self)":                                                      "services:\n  a:\n    image: x\n    links: [a]\n",
		"a network_mode service:b, b links a (cycle)":                           "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    links: [a]\n",
	}
	for _, tc := range []struct {
		file    string
		profile bool
	}{
		{"a links b, b broken (both gated)", false},
		{"a links b, b broken (both gated)", true},
		{"a network_mode service:b, b broken (both gated)", false},
		{"a network_mode service:b, b broken (both gated)", true},
		{"a volumes_from b, b broken (both gated)", false},
		{"a volumes_from b, b broken (both gated)", true},
		{"a depends_on b, b broken (both gated)", false},
		{"a depends_on b, b broken (both gated)", true},
		{"a links nosuch (gated)", false},
		{"a links nosuch (gated)", true},
		{"a network_mode service:nosuch (gated)", false},
		{"a network_mode service:nosuch (gated)", true},
		{"a volumes_from nosuch (gated)", false},
		{"a volumes_from nosuch (gated)", true},
		{"a links nosuch (ungated)", false},
		{"a links nosuch (ungated)", true},
		{"a network_mode service:nosuch (ungated)", false},
		{"a network_mode service:nosuch (ungated)", true},
		{"a links b (gated, inactive), a ungated", false},
		{"a network_mode service:b (gated, inactive), a ungated", false},
		{"a links b, b has no profile, broken", false},
		{"a links b, b has no profile, broken", true},
		{"a links b + optional depends_on b, b undefined (gated)", true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", true},
		{"a depends_on b, b links nosuch (both gated)", false},
		{"a depends_on b, b links nosuch (both gated)", true},
		{"a links b, b links nosuch (both gated)", false},
		{"a links b, b links nosuch (both gated)", true},
		{"a links empty name (gated)", false},
		{"a links empty name (gated)", true},
		{"a links empty name (ungated)", false},
		{"a links empty name (ungated)", true},
		{"a network_mode service: with no name (gated)", false},
		{"a network_mode service: with no name (gated)", true},
		{"a links b:c:d (gated)", false},
		{"a links b:c:d (gated)", true},
		{"a links :c (gated)", false},
		{"a links :c (gated)", true},
		{"a links b, b links a (cycle)", false},
		{"a links b, b links a (cycle)", true},
		{"a links a (self)", false},
		{"a links a (self)", true},
		{"a network_mode service:b, b links a (cycle)", false},
		{"a network_mode service:b, b links a (cycle)", true},
	} {
		profile := ""
		if tc.profile {
			profile = " with --profile g"
		}
		t.Run(tc.file+profile, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			args := []string{"-f", writeCompose(t, files[tc.file])}
			if tc.profile {
				args = append(args, "--profile", "g")
			}
			out, err := run(t, append(args, "start", "a")...)
			if err == nil {
				t.Errorf("start was not refused, docker compose refuses it:\n%s", out)
			}
		})
	}
}

// And the commands that build, pull and run a named service, which read the dependencies of what they are asked for.
func TestBuildPullAndRunOfALinkedServiceDockerComposeRefusesAreRefused(t *testing.T) {
	files := map[string]string{
		"a links b, b broken (both gated)":                                      "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a network_mode service:b, b broken (both gated)":                       "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a volumes_from b, b broken (both gated)":                               "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a depends_on b, b broken (both gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links nosuch (gated)":                                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [nosuch]\n",
		"a network_mode service:nosuch (gated)":                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:nosuch\n",
		"a volumes_from nosuch (gated)":                                         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [nosuch]\n",
		"a links nosuch (ungated)":                                              "services:\n  a:\n    image: x\n    links: [nosuch]\n",
		"a network_mode service:nosuch (ungated)":                               "services:\n  a:\n    image: x\n    network_mode: service:nosuch\n",
		"a links b (gated, inactive), a ungated":                                "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n",
		"a network_mode service:b (gated, inactive), a ungated":                 "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    profiles: [g]\n",
		"a links b, b has no profile, broken":                                   "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    depends_on: {nosuch: {condition: service_started}}\n",
		"a links b + optional depends_on b, b undefined (gated)":                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a network_mode service:b + optional depends_on b, b undefined (gated)": "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: service:b\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a volumes_from b + optional depends_on b, b undefined (gated)":         "services:\n  a:\n    image: x\n    profiles: [g]\n    volumes_from: [b]\n    depends_on:\n      b: {condition: service_started, required: false}\n",
		"a depends_on b, b links nosuch (both gated)":                           "services:\n  a:\n    image: x\n    profiles: [g]\n    depends_on: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links b, b links nosuch (both gated)":                                "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n    links: [nosuch]\n",
		"a links empty name (gated)":                                            "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"\"]\n",
		"a links empty name (ungated)":                                          "services:\n  a:\n    image: x\n    links: [\"\"]\n",
		"a network_mode service: with no name (gated)":                          "services:\n  a:\n    image: x\n    profiles: [g]\n    network_mode: \"service:\"\n",
		"a links b:c:d (gated)":                                                 "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\"b:c:d\"]\n  b:\n    image: y\n    profiles: [g]\n",
		"a links :c (gated)":                                                    "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [\":c\"]\n",
		"a links b, b links a (cycle)":                                          "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    links: [a]\n",
		"a links a (self)":                                                      "services:\n  a:\n    image: x\n    links: [a]\n",
		"a network_mode service:b, b links a (cycle)":                           "services:\n  a:\n    image: x\n    network_mode: service:b\n  b:\n    image: y\n    links: [a]\n",
	}
	for _, tc := range []struct {
		file, command string
		profile       bool
	}{
		{"a links b, b broken (both gated)", "build a", false},
		{"a links b, b broken (both gated)", "pull a", false},
		{"a links b, b broken (both gated)", "run a true", false},
		{"a links b, b broken (both gated)", "build a", true},
		{"a links b, b broken (both gated)", "pull a", true},
		{"a links b, b broken (both gated)", "run a true", true},
		{"a network_mode service:b, b broken (both gated)", "build a", false},
		{"a network_mode service:b, b broken (both gated)", "pull a", false},
		{"a network_mode service:b, b broken (both gated)", "run a true", false},
		{"a network_mode service:b, b broken (both gated)", "build a", true},
		{"a network_mode service:b, b broken (both gated)", "pull a", true},
		{"a network_mode service:b, b broken (both gated)", "run a true", true},
		{"a volumes_from b, b broken (both gated)", "build a", false},
		{"a volumes_from b, b broken (both gated)", "pull a", false},
		{"a volumes_from b, b broken (both gated)", "run a true", false},
		{"a volumes_from b, b broken (both gated)", "build a", true},
		{"a volumes_from b, b broken (both gated)", "pull a", true},
		{"a volumes_from b, b broken (both gated)", "run a true", true},
		{"a depends_on b, b broken (both gated)", "build a", false},
		{"a depends_on b, b broken (both gated)", "pull a", false},
		{"a depends_on b, b broken (both gated)", "run a true", false},
		{"a depends_on b, b broken (both gated)", "build a", true},
		{"a depends_on b, b broken (both gated)", "pull a", true},
		{"a depends_on b, b broken (both gated)", "run a true", true},
		{"a links nosuch (gated)", "build a", false},
		{"a links nosuch (gated)", "pull a", false},
		{"a links nosuch (gated)", "run a true", false},
		{"a links nosuch (gated)", "build a", true},
		{"a links nosuch (gated)", "pull a", true},
		{"a links nosuch (gated)", "run a true", true},
		{"a network_mode service:nosuch (gated)", "build a", false},
		{"a network_mode service:nosuch (gated)", "pull a", false},
		{"a network_mode service:nosuch (gated)", "run a true", false},
		{"a network_mode service:nosuch (gated)", "build a", true},
		{"a network_mode service:nosuch (gated)", "pull a", true},
		{"a network_mode service:nosuch (gated)", "run a true", true},
		{"a volumes_from nosuch (gated)", "build a", false},
		{"a volumes_from nosuch (gated)", "pull a", false},
		{"a volumes_from nosuch (gated)", "run a true", false},
		{"a volumes_from nosuch (gated)", "build a", true},
		{"a volumes_from nosuch (gated)", "pull a", true},
		{"a volumes_from nosuch (gated)", "run a true", true},
		{"a links nosuch (ungated)", "build a", false},
		{"a links nosuch (ungated)", "pull a", false},
		{"a links nosuch (ungated)", "run a true", false},
		{"a links nosuch (ungated)", "build a", true},
		{"a links nosuch (ungated)", "pull a", true},
		{"a links nosuch (ungated)", "run a true", true},
		{"a network_mode service:nosuch (ungated)", "build a", false},
		{"a network_mode service:nosuch (ungated)", "pull a", false},
		{"a network_mode service:nosuch (ungated)", "run a true", false},
		{"a network_mode service:nosuch (ungated)", "build a", true},
		{"a network_mode service:nosuch (ungated)", "pull a", true},
		{"a network_mode service:nosuch (ungated)", "run a true", true},
		{"a links b (gated, inactive), a ungated", "build a", false},
		{"a links b (gated, inactive), a ungated", "pull a", false},
		{"a links b (gated, inactive), a ungated", "run a true", false},
		{"a network_mode service:b (gated, inactive), a ungated", "build a", false},
		{"a network_mode service:b (gated, inactive), a ungated", "pull a", false},
		{"a network_mode service:b (gated, inactive), a ungated", "run a true", false},
		{"a links b, b has no profile, broken", "build a", false},
		{"a links b, b has no profile, broken", "pull a", false},
		{"a links b, b has no profile, broken", "run a true", false},
		{"a links b, b has no profile, broken", "build a", true},
		{"a links b, b has no profile, broken", "pull a", true},
		{"a links b, b has no profile, broken", "run a true", true},
		{"a links b + optional depends_on b, b undefined (gated)", "build a", true},
		{"a links b + optional depends_on b, b undefined (gated)", "pull a", true},
		{"a links b + optional depends_on b, b undefined (gated)", "run a true", true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "build a", true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "pull a", true},
		{"a network_mode service:b + optional depends_on b, b undefined (gated)", "run a true", true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "build a", true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "pull a", true},
		{"a volumes_from b + optional depends_on b, b undefined (gated)", "run a true", true},
		{"a depends_on b, b links nosuch (both gated)", "build a", false},
		{"a depends_on b, b links nosuch (both gated)", "pull a", false},
		{"a depends_on b, b links nosuch (both gated)", "run a true", false},
		{"a depends_on b, b links nosuch (both gated)", "build a", true},
		{"a depends_on b, b links nosuch (both gated)", "pull a", true},
		{"a depends_on b, b links nosuch (both gated)", "run a true", true},
		{"a links b, b links nosuch (both gated)", "build a", false},
		{"a links b, b links nosuch (both gated)", "pull a", false},
		{"a links b, b links nosuch (both gated)", "run a true", false},
		{"a links b, b links nosuch (both gated)", "build a", true},
		{"a links b, b links nosuch (both gated)", "pull a", true},
		{"a links b, b links nosuch (both gated)", "run a true", true},
		{"a links empty name (gated)", "build a", false},
		{"a links empty name (gated)", "pull a", false},
		{"a links empty name (gated)", "run a true", false},
		{"a links empty name (gated)", "build a", true},
		{"a links empty name (gated)", "pull a", true},
		{"a links empty name (gated)", "run a true", true},
		{"a links empty name (ungated)", "build a", false},
		{"a links empty name (ungated)", "pull a", false},
		{"a links empty name (ungated)", "run a true", false},
		{"a links empty name (ungated)", "build a", true},
		{"a links empty name (ungated)", "pull a", true},
		{"a links empty name (ungated)", "run a true", true},
		{"a network_mode service: with no name (gated)", "build a", false},
		{"a network_mode service: with no name (gated)", "pull a", false},
		{"a network_mode service: with no name (gated)", "run a true", false},
		{"a network_mode service: with no name (gated)", "build a", true},
		{"a network_mode service: with no name (gated)", "pull a", true},
		{"a network_mode service: with no name (gated)", "run a true", true},
		{"a links b:c:d (gated)", "build a", false},
		{"a links b:c:d (gated)", "pull a", false},
		{"a links b:c:d (gated)", "run a true", false},
		{"a links b:c:d (gated)", "build a", true},
		{"a links b:c:d (gated)", "pull a", true},
		{"a links b:c:d (gated)", "run a true", true},
		{"a links :c (gated)", "build a", false},
		{"a links :c (gated)", "pull a", false},
		{"a links :c (gated)", "run a true", false},
		{"a links :c (gated)", "build a", true},
		{"a links :c (gated)", "pull a", true},
		{"a links :c (gated)", "run a true", true},
		{"a links b, b links a (cycle)", "build a", false},
		{"a links b, b links a (cycle)", "pull a", false},
		{"a links b, b links a (cycle)", "run a true", false},
		{"a links b, b links a (cycle)", "build a", true},
		{"a links b, b links a (cycle)", "pull a", true},
		{"a links b, b links a (cycle)", "run a true", true},
		{"a links a (self)", "build a", false},
		{"a links a (self)", "pull a", false},
		{"a links a (self)", "run a true", false},
		{"a links a (self)", "build a", true},
		{"a links a (self)", "pull a", true},
		{"a links a (self)", "run a true", true},
		{"a network_mode service:b, b links a (cycle)", "build a", false},
		{"a network_mode service:b, b links a (cycle)", "pull a", false},
		{"a network_mode service:b, b links a (cycle)", "run a true", false},
		{"a network_mode service:b, b links a (cycle)", "build a", true},
		{"a network_mode service:b, b links a (cycle)", "pull a", true},
		{"a network_mode service:b, b links a (cycle)", "run a true", true},
	} {
		profile := ""
		if tc.profile {
			profile = " with --profile g"
		}
		t.Run(tc.file+": "+tc.command+profile, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			args := []string{"-f", writeCompose(t, files[tc.file])}
			if tc.profile {
				args = append(args, "--profile", "g")
			}
			out, err := run(t, append(args, strings.Fields(tc.command)...)...)
			if err == nil {
				t.Errorf("%s was not refused, docker compose refuses it:\n%s", tc.command, out)
			}
		})
	}
}
