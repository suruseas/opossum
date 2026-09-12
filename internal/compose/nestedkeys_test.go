package compose

// A key opossum does not read under `build`, `healthcheck`, `deploy` or
// `develop` (#784's second group). docker compose (v5.5.0) refuses one it
// does not know (`services.web.build additional properties 'bogus' not
// allowed`) and reads the ones it knows that opossum does not act on
// (`build.labels`, `healthcheck.start_interval`, `deploy.replicas`,
// `deploy.resources.limits.pids`). opossum names both among the ignored
// fields — the way a watch rule's extra key was already named — where it
// used to drop a key under `build`, `healthcheck` and `develop` in
// silence, and to say only `deploy` for anything extra under `deploy`.

import (
	"slices"
	"strings"
	"testing"
)

func TestAKeyOpossumDoesNotReadUnderAMappingIsNamedAmongTheIgnoredFields(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"build.labels", "build:\n  context: .\n  labels: {a: b}\n", "build.labels"},
		{"build.cache_from, which docker compose takes", "build:\n  context: .\n  cache_from: [a]\n", "build.cache_from"},
		{"healthcheck.start_interval", "healthcheck:\n  test: [CMD, \"true\"]\n  start_interval: 5s\n", "healthcheck.start_interval"},
		{"deploy.replicas", "deploy:\n  replicas: 3\n", "deploy.replicas"},
		{"deploy.resources.reservations", "deploy:\n  resources:\n    reservations:\n      memory: 1g\n", "deploy.resources.reservations"},
		{"deploy.resources.limits.pids", "deploy:\n  resources:\n    limits:\n      memory: 1g\n      pids: 100\n", "deploy.resources.limits.pids"},
		{"through an alias", "build: *b\n", "build.labels"},
		{"through a merge key", "build:\n  <<: *b\n  dockerfile: Dockerfile\n", "build.labels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "x-b: &b {context: ., labels: {a: b}}\nservices:\n  web:\n    image: alpine\n    "+strings.ReplaceAll(tc.body, "\n", "\n    ")+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := p.Services["web"].Unsupported
			if !slices.Contains(got, tc.want) {
				t.Errorf("ignored fields = %v, want %q among them", got, tc.want)
			}
			// By its full name, not by the top key alone (which is what `deploy`
			// used to be reported as).
			for _, top := range []string{"build", "healthcheck", "deploy", "develop"} {
				if slices.Contains(got, top) {
					t.Errorf("the top key %q is listed bare; the key under it should be named: %v", top, got)
				}
			}
		})
	}
	// Nothing is named when every key is one opossum reads — the deploy
	// limits, a full build, a full healthcheck, a watch rule — nor for an
	// `x-` extension under any of them (the file's own note; docker compose
	// takes one anywhere).
	p, err := Load(writeTemp(t, "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile\n      args: {A: 1}\n      target: dev\n"+
		"    healthcheck:\n      test: [CMD, \"true\"]\n      interval: 10s\n      timeout: 5s\n      start_period: 1s\n      retries: 3\n      disable: false\n      x-note: 1\n"+
		"    deploy:\n      x-note: 1\n      resources:\n        limits:\n          memory: 512m\n          cpus: \"0.5\"\n"+
		"    develop:\n      watch:\n        - path: ./src\n          action: sync\n          target: /app\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["web"].Unsupported; len(got) != 0 {
		t.Errorf("every key here is read, yet these are listed as ignored: %v", got)
	}
}
