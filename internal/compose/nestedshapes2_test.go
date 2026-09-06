package compose

// The rest of the nested fields opossum reads (#735's family, after #763):
// `healthcheck` fields written bare, `develop.watch` and its items, and a
// list or a mapping where `memory` takes a string. docker compose (v5.5.0)
// refuses each — `services.web.healthcheck.interval must be a string`,
// `…healthcheck.retries must be a number or string`, `…develop.watch must be
// a array`, `…develop.watch.0.path must be a string`, `…limits.memory must be
// a string` (exit codes and full output read). A number for `interval` was
// already refused by the duration parser; a bare `interval:` was not.

import (
	"strings"
	"testing"
)

func TestTheRestOfTheNestedFieldsOfTheWrongShapeAreRefused(t *testing.T) {
	hc := "services:\n  web:\n    image: alpine\n    healthcheck:\n      test: [CMD, \"true\"]\n"
	watch := "services:\n  web:\n    build: .\n    develop:\n      watch:\n"
	for _, tc := range []struct{ name, body, want string }{
		{"healthcheck.test bare", "services:\n  web:\n    image: alpine\n    healthcheck:\n      test:\n", "healthcheck.test must be a string or list, got nothing — write the command"},
		{"healthcheck.interval bare", hc + "      interval:\n", "healthcheck.interval must be a string, got nothing — write a duration, as in `10s`"},
		// A list where a duration belongs is named by the shape check (the
		// duration reader leaves a non-scalar to it).
		{"healthcheck.timeout as a list", hc + "      timeout: [5s]\n", "healthcheck.timeout must be a string, got a list — write a duration, as in `5s`"},
		{"healthcheck.timeout bare", hc + "      timeout:\n", "healthcheck.timeout must be a string, got nothing — write a duration, as in `5s`"},
		{"healthcheck.start_period bare", hc + "      start_period:\n", "healthcheck.start_period must be a string, got nothing"},
		{"healthcheck.retries bare", hc + "      retries:\n", "healthcheck.retries must be a number or string, got nothing — write a count, as in `3`"},
		{"develop.watch bare", watch, "develop.watch must be a list, got nothing"},
		{"develop.watch as a mapping", "services:\n  web:\n    build: .\n    develop:\n      watch: {path: ./src}\n", "cannot unmarshal !!map into []compose.WatchRule"},
		{"develop.watch as one path", "services:\n  web:\n    build: .\n    develop:\n      watch: ./src\n", "cannot unmarshal !!str into []compose.WatchRule"},
		{"healthcheck.test as a mapping", "services:\n  web:\n    image: alpine\n    healthcheck:\n      test: {a: b}\n", "expected a string or a list, got a mapping"},
		{"watch item path as a number", watch + "        - path: 42\n          action: sync\n          target: /app\n", "develop.watch entry 1.path must be a string, got a number — write a path, as in `./src`"},
		{"watch item action bare", watch + "        - path: ./src\n          action:\n          target: /app\n", "develop.watch entry 1.action must be a string, got nothing"},
		{"second watch item target as a boolean", watch + "        - path: ./a\n          action: sync\n          target: /a\n        - path: ./b\n          action: sync\n          target: true\n", "develop.watch entry 2.target must be a string, got true/false"},
		{"memory as a list", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          memory: [512m]\n", "deploy.resources.limits.memory must be a string, got a list"},
		{"memory as a mapping", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          memory: {a: b}\n", "deploy.resources.limits.memory must be a string, got a mapping"},
		// The neighbouring branch of the same switch: cpus takes a number or
		// a string, and a list there used to make the limit vanish.
		{"limits.cpus as a list", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          cpus: [0.5]\n", "deploy.resources.limits.cpus must be a number or a string, got a list"},
		{"reservations.cpus as a mapping", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          cpus: {a: b}\n", "deploy.resources.reservations.cpus must be a number or a string, got a mapping"},
		// A `- ` with nothing after it is an empty rule, not a rule to skip.
		{"empty watch item", watch + "        - \n", "develop.watch entry 1 of 1 is empty — write the rule or remove the `- `"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}

// A watch rule reached through an alias is walked the same way.
func TestAWatchItemThroughAnAliasIsChecked(t *testing.T) {
	got := loadErr(t, "x-r: &r\n  path: 42\n  action: sync\n  target: /app\nservices:\n  web:\n    build: .\n    develop:\n      watch:\n        - *r\n")
	if !strings.Contains(got, "develop.watch entry 1.path must be a string, got a number") {
		t.Errorf("want the refusal through the alias, got:\n%s", got)
	}
}

// What docker compose takes there, and so does this.
func TestTheRestOfTheNestedFieldsOfTheRightShapeStillLoad(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    build: .\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: 10s\n      timeout: \"5s\"\n      start_period: 30s\n      retries: 3\n    develop:\n      watch:\n        - path: ./src\n          action: sync\n          target: /app\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if web.Healthcheck == nil || web.Healthcheck.Retries != 3 || web.Develop == nil || len(web.Develop.Watch) != 1 || web.Develop.Watch[0].Path != "./src" {
		t.Errorf("right shapes should load as written: healthcheck=%+v develop=%+v", web.Healthcheck, web.Develop)
	}
}
