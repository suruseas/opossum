package compose

// A watch rule's action, its target, and a blank healthcheck duration
// (#735's neighbours seen in #766's review). docker compose (v5.5.0), exit
// codes and full output read: `action: bogus`, `action: ""` and
// `action: SYNC` are `value must be one of 'rebuild', 'sync', 'restart',
// 'sync+restart', 'sync+exec'`; `sync`, `sync+restart` and `sync+exec`
// without a `target` (or with `target: ""`) are `target is required for
// non-rebuild actions`, while `rebuild` and `restart` load without one;
// `interval: ""`, `timeout: ""` and `start_period: ""` are `invalid
// duration ""`, an alias to `""` the same. opossum used to take every one
// of these: the action reached the watcher, which named it as not
// automated at the first change; the missing target made `sync` copy to
// the container's root; the blank duration became the default.

import (
	"strings"
	"testing"
	"time"
)

func TestAWatchActionIsOneOfDockerComposesAndACopyingOneHasATarget(t *testing.T) {
	watch := "services:\n  web:\n    build: .\n    develop:\n      watch:\n"
	actions := "write `sync`, `rebuild`, `sync+restart`, `restart` or `sync+exec`"
	target := "copies files into the container; write where, as in `target: /app`"
	for _, tc := range []struct{ name, body, want string }{
		{"a word that is not an action", watch + "        - path: ./src\n          action: bogus\n          target: /app\n", "develop.watch entry 1 has an action that is not one — " + actions},
		{"an empty action", watch + "        - path: ./src\n          action: \"\"\n          target: /app\n", "develop.watch entry 1 has an action that is not one — " + actions},
		{"an action in capitals", watch + "        - path: ./src\n          action: SYNC\n          target: /app\n", "develop.watch entry 1 has an action that is not one — " + actions},
		{"an aliased action", "x-a: &a bogus\n" + watch + "        - path: ./src\n          action: *a\n          target: /app\n", "develop.watch entry 1 has an action that is not one"},
		{"the second rule's action", watch + "        - path: ./src\n          action: sync\n          target: /app\n        - path: ./x\n          action: sync-restart\n          target: /x\n", "develop.watch entry 2 has an action that is not one"},
		{"sync without a target", watch + "        - path: ./src\n          action: sync\n", "develop.watch entry 1 has no target — action `sync` " + target},
		{"sync+restart without a target", watch + "        - path: ./src\n          action: sync+restart\n", "develop.watch entry 1 has no target — action `sync+restart` " + target},
		{"sync+exec without a target", watch + "        - path: ./src\n          action: sync+exec\n", "develop.watch entry 1 has no target — action `sync+exec` " + target},
		{"action left out is sync, so a target is needed", watch + "        - path: ./src\n", "develop.watch entry 1 has no target — action `sync` " + target},
		{"an empty target", watch + "        - path: ./src\n          action: sync\n          target: \"\"\n", "develop.watch entry 1 has an empty target — action `sync` " + target},
		{"an aliased empty target", "x-e: &e \"\"\n" + watch + "        - path: ./src\n          action: sync\n          target: *e\n", "develop.watch entry 1 has an empty target"},
		// The path is checked first: a rule with nothing in it is named for
		// its path, as before.
		{"an empty rule", watch + "        - {}\n", "develop.watch entry 1 has no path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
			for _, word := range []string{"bogus", "SYNC", "sync-restart"} {
				if strings.Contains(got, word) {
					t.Errorf("the message reads the action back (%q):\n%s", word, got)
				}
			}
		})
	}
	// Taken, as docker takes them, with the action kept as written: the
	// two actions that copy nothing need no target, a target alongside
	// `rebuild` is not refused, and the other three load with one.
	for _, tc := range []struct{ name, rule, action string }{
		{"restart without a target", "        - path: ./src\n          action: restart\n", "restart"},
		{"rebuild without a target", "        - path: ./src\n          action: rebuild\n", "rebuild"},
		{"rebuild with a target", "        - path: ./src\n          action: rebuild\n          target: /app\n", "rebuild"},
		{"sync with a target", "        - path: ./src\n          action: sync\n          target: /app\n", "sync"},
		{"sync+restart with a target", "        - path: ./src\n          action: sync+restart\n          target: /app\n", "sync+restart"},
		{"sync+exec with a target", "        - path: ./src\n          action: sync+exec\n          target: /app\n", "sync+exec"},
		{"action left out with a target", "        - path: ./src\n          target: /app\n", ""},
		{"an aliased action", "        - path: ./src\n          action: *a\n          target: /app\n", "sync+exec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "x-a: &a sync+exec\n"+watch+tc.rule))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Develop.Watch[0].Action; got != tc.action {
				t.Errorf("action = %q, want %q", got, tc.action)
			}
		})
	}
}

func TestABlankHealthcheckDurationIsNotTheDefault(t *testing.T) {
	hc := "services:\n  web:\n    image: alpine\n    healthcheck:\n      test: [CMD, \"true\"]\n"
	blank := "blank, not a duration — use a unit, e.g. 30s, 1m, or 500ms (an unset `${VAR}` leaves a blank)"
	for _, tc := range []struct{ name, body, want string }{
		{"interval empty", hc + "      interval: \"\"\n", "healthcheck interval: " + blank},
		{"timeout empty", hc + "      timeout: \"\"\n", "healthcheck timeout: " + blank},
		{"start_period empty", hc + "      start_period: \"\"\n", "healthcheck start_period: " + blank},
		{"interval of spaces", hc + "      interval: \" \"\n", "healthcheck interval: " + blank},
		{"timeout aliased to a blank", "x-i: &i \"\"\n" + hc + "      timeout: *i\n", "healthcheck timeout: " + blank},
		{"an unset reference", hc + "      interval: \"${OPOSSUM_TEST_INTERVAL_UNSET}\"\n", "healthcheck interval: " + blank},
		// A word keeps its own refusal — and, as every load message, does not
		// read the value back.
		{"a word", hc + "      interval: soon\n", "healthcheck interval: not a duration — use a unit, e.g. 30s, 1m, or 500ms"},
		// Bare, the shape check names it (docker: `must be a string`).
		{"interval bare", hc + "      interval:\n", "healthcheck.interval must be a string, got nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPOSSUM_TEST_INTERVAL_UNSET", "")
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
			if strings.Contains(got, "soon") {
				t.Errorf("the message reads the value back:\n%s", got)
			}
		})
	}
	// Left out, each is its default; written, each is what was written,
	// through an alias too.
	p, err := Load(writeTemp(t, hc))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h := p.Services["web"].Healthcheck; h.Interval != 30*time.Second || h.Timeout != 30*time.Second || h.StartPeriod != 0 {
		t.Errorf("defaults: interval=%v timeout=%v start_period=%v", h.Interval, h.Timeout, h.StartPeriod)
	}
	p, err = Load(writeTemp(t, hc+"      interval: &i 10s\n      timeout: *i\n      start_period: 1m\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if h := p.Services["web"].Healthcheck; h.Interval != 10*time.Second || h.Timeout != 10*time.Second || h.StartPeriod != time.Minute {
		t.Errorf("written: interval=%v timeout=%v start_period=%v", h.Interval, h.Timeout, h.StartPeriod)
	}
}
