package compose

// The nested fields opossum reads under `build:` and `deploy:` (#735's family,
// after #760): `build: 42` was read as the context "42", `build.context: 42`
// too, `deploy.resources.limits:` alone as no limit, and a number for
// `reservations.memory` as "512". docker compose (v5.5.0) refuses each —
// `services.web.build must be a string`, `…build.context must be a string`,
// `…deploy.resources.limits must be a mapping`, `…reservations.memory must be
// a string`, `…limits.cpus must be a number or string` (exit codes and full
// output read).

import (
	"strings"
	"testing"
)

func TestANestedFieldOfTheWrongShapeIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"build as a number", "services:\n  web:\n    build: 42\n", "build must be a string or a mapping, got a number"},
		{"build.context as a number", "services:\n  web:\n    build:\n      context: 42\n", "build.context must be a string, got a number — write a path, as in `.`"},
		{"build.dockerfile as a boolean", "services:\n  web:\n    build:\n      context: .\n      dockerfile: true\n", "build.dockerfile must be a string, got true/false"},
		{"build.context bare", "services:\n  web:\n    build:\n      context:\n", "build.context must be a string, got nothing"},
		{"deploy.resources bare", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n", "deploy.resources must be a mapping, got nothing"},
		{"deploy.resources.limits bare", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n", "deploy.resources.limits must be a mapping, got nothing"},
		// A number where the mapping belongs is caught by the field's own
		// decoder first (the struct cannot take it); pinned so that stays so.
		{"deploy.resources.limits as a number", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits: 42\n", "not the shape that field takes"},
		{"limits.memory bare", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          memory:\n", "deploy.resources.limits.memory must be a string, got nothing"},
		{"reservations.memory as a number", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          memory: 512\n", "deploy.resources.reservations.memory must be a string, got a number — write a size with a unit, as in `\"512m\"`"},
		{"limits.cpus bare", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          cpus:\n", "deploy.resources.limits.cpus must be a number or string, got nothing — write a count, as in `0.5`"},
		// `reservations` is read by nothing in opossum, so no struct decode
		// stands in front of these: the table is the only check.
		{"reservations bare", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n", "deploy.resources.reservations must be a mapping, got nothing"},
		{"reservations as a number", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations: 42\n", "deploy.resources.reservations must be a mapping, got a single value"},
		{"reservations.cpus bare", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          cpus:\n", "deploy.resources.reservations.cpus must be a number or string, got nothing"},
		{"reservations.cpus as a boolean", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          cpus: true\n", "deploy.resources.reservations.cpus must be a number or a string, got true/false"},
		{"build.target as a number", "services:\n  web:\n    build:\n      context: .\n      target: 42\n", "build.target must be a string, got a number — write a stage name"},
		{"build.args bare", "services:\n  web:\n    build:\n      context: .\n      args:\n", "build.args must be a mapping or list, got nothing — write the variables, as in `{A: 1}` or `[A=1]`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
			if !strings.Contains(got, `service "web"`) {
				t.Errorf("the refusal should name the service, got:\n%s", got)
			}
		})
	}
}

// What docker compose takes there, and so does this.
func TestNestedFieldsOfTheRightShapeStillLoad(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    build:\n      context: \"42\"\n      dockerfile: Dockerfile.dev\n    deploy:\n      resources:\n        limits:\n          memory: 512m\n          cpus: 0.5\n        reservations:\n          memory: \"256m\"\n          cpus: \"0.25\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if web := p.Services["web"]; web.Build == nil || web.Build.Context != "42" {
		t.Errorf("build.context = %+v, want \"42\" as written", web.Build)
	}
}
