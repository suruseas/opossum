package compose

// The resource limits — `cpus`, `mem_limit`, and `deploy.resources` limits
// and reservations — written as a list, a mapping or a blank (#784's first
// group; a service key × shape sweep of 432 fixtures). docker compose
// (v5.5.0), exit codes and full output read: `cpus: [1]`, `{a: b}`, `[]`,
// `{}` are `must be a number or string`; `cpus: ""` and `" "` are
// `strconv.ParseFloat: parsing "": invalid syntax`; `mem_limit: ""` is
// `invalid size: ''`; the deploy limits the same. opossum read a list or a
// mapping as no text and a blank as left out, so each set no limit, in
// silence (`config` showed nothing). A padded limit (`" 512m "`, `" 0.5 "`)
// docker refuses and this still takes (trimmed) — named here, not changed.

import (
	"strings"
	"testing"
)

func TestALimitWrittenAsAListAMappingOrABlankIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n"
	cpus := "cpus must be a number or a string, got %s — write a count of CPUs, as in `0.5`"
	mem := "mem_limit must be a number or a string, got %s — write a size with a unit, as in `\"512m\"`"
	for _, tc := range []struct{ name, body, want string }{
		{"cpus as a list", svc + "    cpus: [1]\n", strings.Replace(cpus, "%s", "a list", 1)},
		{"cpus as a mapping", svc + "    cpus: {a: b}\n", strings.Replace(cpus, "%s", "a mapping", 1)},
		{"cpus as an empty list", svc + "    cpus: []\n", strings.Replace(cpus, "%s", "a list", 1)},
		{"cpus as an empty mapping", svc + "    cpus: {}\n", strings.Replace(cpus, "%s", "a mapping", 1)},
		{"cpus blank", svc + "    cpus: \"\"\n", "cpus is blank — write a count of CPUs, as in `0.5`, or remove the key"},
		{"cpus of spaces", svc + "    cpus: \" \"\n", "cpus is blank — write a count of CPUs"},
		{"cpus aliased to a blank", "x-b: &b \"\"\n" + svc + "    cpus: *b\n", "cpus is blank"},
		{"cpus from an unset reference", svc + "    cpus: \"${OPOSSUM_TEST_LIMIT_UNSET}\"\n", "cpus is blank — write a count of CPUs, as in `0.5`, or remove the key (an unset `${VAR}` leaves a blank)"},
		{"mem_limit as a list", svc + "    mem_limit: [512m]\n", strings.Replace(mem, "%s", "a list", 1)},
		{"mem_limit as a mapping", svc + "    mem_limit: {a: b}\n", strings.Replace(mem, "%s", "a mapping", 1)},
		{"mem_limit as an empty list", svc + "    mem_limit: []\n", strings.Replace(mem, "%s", "a list", 1)},
		{"mem_limit as an empty mapping", svc + "    mem_limit: {}\n", strings.Replace(mem, "%s", "a mapping", 1)},
		{"mem_limit blank", svc + "    mem_limit: \"\"\n", "mem_limit is blank — write a size with a unit, as in `\"512m\"`, or remove the key"},
		{"mem_limit of spaces", svc + "    mem_limit: \" \"\n", "mem_limit is blank"},
		// The same limits one level down.
		{"limits.memory blank", svc + "    deploy:\n      resources:\n        limits:\n          memory: \"\"\n", "deploy.resources.limits.memory is blank — write a size with a unit, as in `\"512m\"`, or remove the key"},
		{"limits.cpus blank", svc + "    deploy:\n      resources:\n        limits:\n          cpus: \"\"\n", "deploy.resources.limits.cpus is blank — write a count, as in `0.5`, or remove the key"},
		{"reservations.memory blank", svc + "    deploy:\n      resources:\n        reservations:\n          memory: \" \"\n", "deploy.resources.reservations.memory is blank"},
		{"reservations.cpus blank", svc + "    deploy:\n      resources:\n        reservations:\n          cpus: \"\"\n", "deploy.resources.reservations.cpus is blank"},
		// Bare, the bare-key check names it, as before.
		{"cpus bare", svc + "    cpus:\n", "cpus: expected a number or a string, got nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPOSSUM_TEST_LIMIT_UNSET", "")
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Taken, as docker takes them, and resolved to the limit written.
	for _, tc := range []struct{ name, body, mem, cpu string }{
		{"a bare number of CPUs", svc + "    cpus: 0.5\n", "", "1"},
		{"a quoted number of CPUs", svc + "    cpus: \"1.5\"\n", "", "2"},
		{"an exponent", svc + "    cpus: 1e1\n", "", "10"},
		{"zero CPUs is no limit", svc + "    cpus: 0\n", "", ""},
		{"a size with a unit", svc + "    mem_limit: 512m\n", "512M", ""},
		{"a quoted size", svc + "    mem_limit: \"1.5g\"\n", "1536M", ""},
		{"a bare number of bytes", svc + "    mem_limit: 1048576\n", "1M", ""},
		{"zero bytes is no limit", svc + "    mem_limit: \"0\"\n", "", ""},
		{"a padded size (docker refuses; taken here, trimmed)", svc + "    mem_limit: \" 512m \"\n", "512M", ""},
		{"a padded count (docker refuses; taken here, trimmed)", svc + "    cpus: \" 0.5 \"\n", "", "1"},
		{"the deploy limits", svc + "    deploy:\n      resources:\n        limits:\n          memory: 256m\n          cpus: \"0.5\"\n", "256M", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			mem, cpu, err := p.Services["web"].Resources()
			if err != nil {
				t.Fatalf("resources: %v", err)
			}
			if mem != tc.mem || cpu != tc.cpu {
				t.Errorf("mem=%q cpu=%q, want mem=%q cpu=%q", mem, cpu, tc.mem, tc.cpu)
			}
		})
	}
}
