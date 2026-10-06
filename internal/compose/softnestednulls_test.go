package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A key with nothing after it in a nested field, written by a later `-f` file where no earlier file gave it a value, is refused by
// the load as docker compose refuses it (`config -q`, every row measured: `refused` is its rc 1), and the read that takes a project
// down goes on past it with the refusal kept (CheckValueFaults) and the rest of the service read (#1582): an earlier opossum read
// the file, so a project may be running on it.
func TestASoftReadGoesOnPastANestedNullTheMergeLeavesNothingUnder(t *testing.T) {
	for _, tc := range []struct {
		name, field, base, over string
		refused                 bool
	}{
		{"healthcheck.test: ~ (base gives interval only)", "healthcheck", `services:
  a:
    image: x
    healthcheck:
      interval: 1s
`, `services:
  a:
    healthcheck:
      test: ~
`, true},
		{"healthcheck.timeout: ~ (base gives interval only)", "healthcheck", `services:
  a:
    image: x
    healthcheck:
      interval: 1s
`, `services:
  a:
    healthcheck:
      timeout: ~
`, true},
		{"healthcheck.start_period: ~ (base gives interval only)", "healthcheck", `services:
  a:
    image: x
    healthcheck:
      interval: 1s
`, `services:
  a:
    healthcheck:
      start_period: ~
`, true},
		{"healthcheck.retries: ~ (base gives interval only)", "healthcheck", `services:
  a:
    image: x
    healthcheck:
      interval: 1s
`, `services:
  a:
    healthcheck:
      retries: ~
`, true},
		{"healthcheck.interval: ~ (base gives interval only)", "healthcheck", `services:
  a:
    image: x
    healthcheck:
      interval: 1s
`, `services:
  a:
    healthcheck:
      interval: ~
`, false},
		{"build.context: ~ (base gives build: .)", "build", `services:
  a:
    build: .
`, `services:
  a:
    build:
      context: ~
`, false},
		{"build.dockerfile: ~ (base gives build: .)", "build", `services:
  a:
    build: .
`, `services:
  a:
    build:
      dockerfile: ~
`, true},
		{"build.dockerfile_inline: ~ (base gives build: .)", "build", `services:
  a:
    build: .
`, `services:
  a:
    build:
      dockerfile_inline: ~
`, true},
		{"build.target: ~ (base gives build: .)", "build", `services:
  a:
    build: .
`, `services:
  a:
    build:
      target: ~
`, true},
		{"build.args: ~ (base gives build: .)", "build", `services:
  a:
    build: .
`, `services:
  a:
    build:
      args: ~
`, true},
		{"deploy.resources.limits.memory: ~", "deploy", `services:
  a:
    image: x
    deploy:
      resources:
        limits:
          cpus: '1'
`, `services:
  a:
    deploy:
      resources:
        limits:
          memory: ~
`, true},
		{"deploy.resources.limits.cpus: ~", "deploy", `services:
  a:
    image: x
    deploy:
      resources:
        limits:
          memory: 1g
`, `services:
  a:
    deploy:
      resources:
        limits:
          cpus: ~
`, true},
		{"build.dockerfile: ~ under !override of the mapping", "build", "services:\n  a:\n    build: {context: ., dockerfile: D}\n", "services:\n  a:\n    build: !override {context: ., dockerfile: ~}\n", true},
		{"healthcheck.interval: ~ under !override of the mapping", "healthcheck", "services:\n  a:\n    image: x\n    healthcheck: {interval: 2s}\n", "services:\n  a:\n    healthcheck: !override {interval: ~}\n", true},
		{"build.dockerfile: ~ with an environment null in the same file", "build", "services:\n  a:\n    build: .\n    environment: {E: ~}\n", "services:\n  a:\n    build: {dockerfile: ~}\n", true},
		{"build.dockerfile: ~ with an args null", "build", "services:\n  a:\n    build: {context: ., args: {A: ~}}\n", "services:\n  a:\n    build: {dockerfile: ~}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, body := range []string{tc.base, tc.over} {
				p := filepath.Join(dir, []string{"f1.yaml", "f2.yaml"}[i])
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			_, strictErr := LoadFiles(paths, nil)
			if (strictErr != nil) != tc.refused {
				t.Errorf("the strict load: err %v, docker compose refuses it: %v", strictErr, tc.refused)
			}
			p, err := LoadFilesEnvDirSoft(paths, nil, "")
			if err != nil {
				t.Fatalf("the soft load stopped: %v", err)
			}
			fault := p.CheckValueFaults()
			if (fault != nil) != tc.refused {
				t.Errorf("the soft load kept the refusal: %v, docker compose refuses it: %v", fault, tc.refused)
			}
			if fault != nil && !strings.Contains(fault.Error(), "got nothing") {
				t.Errorf("the refusal kept is not the one for the empty key: %v", fault)
			}
			if fault != nil && !strings.Contains(fault.Error(), "f2.yaml") {
				t.Errorf("the refusal kept does not name the file that wrote the empty key: %v", fault)
			}
			svc := p.Services["a"]
			if svc == nil {
				t.Fatalf("service a was not read")
			}
			if strings.Contains(tc.name, "environment null") && len(svc.Environment) != 1 {
				t.Errorf("the environment variable written with nothing after it was dropped with the nested null: %v", svc.Environment)
			}
			if strings.Contains(tc.name, "args null") && (svc.Build == nil || len(svc.Build.Args) != 1) {
				t.Errorf("the build argument written with nothing after it was dropped with the nested null: %v", svc.Build)
			}
			switch tc.field {
			case "healthcheck":
				if svc.Healthcheck == nil {
					t.Errorf("the healthcheck the files gave was dropped with the key that holds nothing")
				} else if strings.Contains(tc.base, "interval: 1s") && svc.Healthcheck.Interval != time.Second {
					t.Errorf("the interval the first file gave is %v, want 1s", svc.Healthcheck.Interval)
				}
			case "build":
				if svc.Build == nil {
					t.Errorf("the build the files gave was dropped with the key that holds nothing")
				} else if svc.Build.Context == "" {
					t.Errorf("the context the first file gave was dropped")
				}
			}
		})
	}
}
