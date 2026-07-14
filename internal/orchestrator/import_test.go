package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// Import brings in a Docker-built image for each build service and skips
// image-only services; an unknown service errors.
func TestImportBuildServicesOnly(t *testing.T) {
	rt, calls := fakeShim(t)
	// A fake docker that succeeds but writes nothing (the shared fake container
	// shim doesn't drain stdin; the real save→load byte flow is covered in the
	// runtime test). This test checks the orchestration: which services, and the
	// docker ref chosen for each.
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.DockerBin = docker

	p := project("pj", map[string]*compose.Service{
		"web": {Build: &compose.Build{Context: "."}},
		"api": {Build: &compose.Build{Context: "."}, Image: "myco/api:9"}, // docker tags it myco/api:9
		"db":  {Image: "postgres:16"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Import(); err != nil {
		t.Fatalf("Import: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "Importing web from Docker (pj-web:latest)") {
		t.Errorf("build service web should be imported by its built tag; got: %s", s)
	}
	if !strings.Contains(s, "Importing api from Docker (myco/api:9)") {
		t.Errorf("build+image service api should be pulled by its image: tag; got: %s", s)
	}
	if strings.Contains(s, "Importing db") {
		t.Errorf("image-only db should be skipped; got: %s", s)
	}
	if got := strings.Join(calls(), "\n"); !strings.Contains(got, "image load") {
		t.Errorf("expected a `container image load`; calls: %s", got)
	}
	if err := o.Import("nope"); err == nil {
		t.Error("importing an unknown service should error")
	}
}

// `up --from-docker` imports a build service's image instead of building it.
func TestUpFromDockerImportsInsteadOfBuilding(t *testing.T) {
	rt, calls := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=pj-web:latest") // not present, so up would otherwise build
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.DockerBin = docker

	p := project("pj", map[string]*compose.Service{"web": {Build: &compose.Build{Context: "."}}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetUpOptions(false, false, false, false, true) // --from-docker
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "Importing web from Docker") {
		t.Errorf("--from-docker should import; got: %s", s)
	}
	if strings.Contains(s, "Building web") {
		t.Errorf("--from-docker should not build; got: %s", s)
	}
	if got := strings.Join(calls(), "\n"); strings.Contains(got, "build --progress") {
		t.Errorf("--from-docker should not invoke `container build`; calls: %s", got)
	}
}

// --from-docker doesn't import (or build) a service whose image is already
// present — nothing to bring over.
func TestUpFromDockerSkipsWhenImagePresent(t *testing.T) {
	rt, calls := fakeShim(t) // no IMAGE_ABSENT: the built image is present
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { // fails if invoked
		t.Fatal(err)
	}
	rt.DockerBin = docker

	p := project("pj2", map[string]*compose.Service{"web": {Build: &compose.Build{Context: "."}}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetUpOptions(false, false, false, false, true) // --from-docker
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if strings.Contains(out.String(), "Importing") {
		t.Errorf("a present image should not be re-imported; got: %s", out.String())
	}
	if got := strings.Join(calls(), "\n"); strings.Contains(got, "build --progress") {
		t.Errorf("a present image should not be built; calls: %s", got)
	}
}
