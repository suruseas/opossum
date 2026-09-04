package compose

import (
	"path/filepath"
	"slices"
	"testing"
)

// In an override file, a key with nothing after it — `working_dir:`,
// `ports:`, `environment:` — is "not given" to docker compose: the base's
// value stands, whatever its kind (docker compose v5.5.0, measured). Before,
// the null won and the base's value vanished: a scalar was cleared, a list
// emptied, a secret lost its file and the load failed.
func TestAKeyWithNothingAfterItLeavesTheBaseValue(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: alpine\n"+
		"    working_dir: /app\n"+
		"    ports: [\"8080:80\"]\n"+
		"    environment:\n"+
		"      A: \"1\"\n"+
		"    deploy:\n"+
		"      resources:\n"+
		"        limits:\n"+
		"          memory: 100M\n"+
		"    networks: [back]\n"+
		"networks:\n"+
		"  back:\n"+
		"    internal: true\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    working_dir:\n"+
		"    ports:\n"+
		"    environment:\n"+
		"    deploy:\n"+
		"      resources:\n"+
		"        limits:\n"+
		"          memory:\n"+
		"networks:\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if web.WorkingDir != "/app" {
		t.Errorf("scalar: working_dir should stand, got %q", web.WorkingDir)
	}
	if len(web.Ports) != 1 {
		t.Errorf("list: ports should stand, got %v", web.Ports)
	}
	if !slices.Contains(web.Environment, "A=1") {
		t.Errorf("mapping: environment should stand, got %v", web.Environment)
	}
	if web.Deploy == nil || web.Deploy.Resources == nil || web.Deploy.Resources.Limits == nil || string(web.Deploy.Resources.Limits.Memory) != "100M" {
		t.Errorf("nested: memory should stand, got %+v", web.Deploy)
	}
	if !p.Networks["back"].Internal {
		t.Errorf("a top-level `networks:` with nothing after it leaves the declarations, got %+v", p.Networks)
	}
}

// Two places read a null as a value and win with it, and docker compose does
// the same: `command:`/`entrypoint:` mean "no command", and a variable inside
// `environment:` means "unset" — the same thing a bare `A` says in the list
// form, so the merged file reads exactly as a single file written that way.
func TestCommandAndAnEnvironmentVariableReadANullAsAValue(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	single := filepath.Join(dir, "single.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: alpine\n"+
		"    command: [sleep, \"1\"]\n"+
		"    entrypoint: [/bin/sh]\n"+
		"    environment:\n"+
		"      A: \"1\"\n"+
		"      B: \"2\"\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    command:\n"+
		"    entrypoint:\n"+
		"    environment:\n"+
		"      A:\n")
	mustWriteFile(t, single, "services:\n"+
		"  web:\n"+
		"    image: alpine\n"+
		"    environment:\n"+
		"      A:\n"+
		"      B: \"2\"\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if len(web.Command) != 0 || len(web.Entrypoint) != 0 {
		t.Errorf("command:/entrypoint: with nothing after them mean no command, got %v / %v", web.Command, web.Entrypoint)
	}
	one, err := Load(single)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(web.Environment, one.Services["web"].Environment) {
		t.Errorf("A: with nothing after it inside environment should read as it does in one file: merged %v, single %v",
			web.Environment, one.Services["web"].Environment)
	}
}

// The two exceptions are fields of a service, and only there. A service that
// happens to be called `environment` or `command` is an element named by its
// author: a null under it, or a null that *is* it, is "not given" like any
// other, and the base stands (docker compose v5.5.0, measured). An earlier
// draft keyed the exceptions on the name alone — `working_dir:` under a
// service called `environment` vanished, and `services: {command: }` crashed.
func TestTheNullExceptionsDoNotApplyToAServiceNamedLikeThem(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  environment:\n"+
		"    image: alpine\n"+
		"    working_dir: /app\n"+
		"    ports: [\"8080:80\"]\n"+
		"  command:\n"+
		"    image: alpine\n"+
		"    working_dir: /srv\n")
	mustWriteFile(t, over, "services:\n"+
		"  environment:\n"+
		"    working_dir:\n"+
		"    ports:\n"+
		"  command:\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if s := p.Services["environment"]; s == nil || s.WorkingDir != "/app" || len(s.Ports) != 1 {
		t.Errorf("a service called environment keeps its working_dir and ports across a null override, got %+v", s)
	}
	if s := p.Services["command"]; s == nil || s.WorkingDir != "/srv" {
		t.Errorf("a service called command, overridden with nothing, stands as the base wrote it, got %+v", s)
	}
}

// Inside build.args a null is a value too — "take it from the shell" — as it
// is inside environment (docker compose v5.5.0: `A: ` over `A: "1"` reads A
// from the environment, and drops it when unset). The merged args carry a
// bare `A`, the same thing a single file says with `A:`; a merge that kept
// the base's `A=1` would hand the build a value docker never would.
func TestANullInsideBuildArgsReadsAsUnset(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    build:\n"+
		"      context: .\n"+
		"      args: {A: \"1\", B: \"2\"}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    build:\n"+
		"      args: {A: }\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	args := []string(p.Services["web"].Build.Args)
	if !slices.Contains(args, "A") || slices.Contains(args, "A=1") {
		t.Errorf("A: with nothing after it should read as a bare A (from the shell), got %v", args)
	}
	if !slices.Contains(args, "B=2") {
		t.Errorf("B is untouched, got %v", args)
	}
}

// A declaration under its ordinary name, too: a secret whose override writes
// `file:` with nothing after it keeps the base's file (before, the load
// failed with "must set file").
func TestASecretsFileSurvivesANullOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: alpine\n"+
		"    secrets: [s1]\n"+
		"secrets:\n"+
		"  s1:\n"+
		"    file: ./s.txt\n")
	mustWriteFile(t, over, "secrets:\n"+
		"  s1:\n"+
		"    file:\n")
	mustWriteFile(t, filepath.Join(dir, "s.txt"), "x\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if p.Secrets["s1"].File == "" {
		t.Errorf("the base's file should stand, got %+v", p.Secrets["s1"])
	}
}
