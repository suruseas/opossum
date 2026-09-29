package orchestrator_test

// Evals for #1126: `down --rmi local` recognizing a build under a custom
// `image:` name as THIS project's, by label, without normalizing the label's
// own value first — see builtHere's doc comment in orchestrator.go for why
// normalizing is unsafe (the `my_app`/`my-app` collision this issue is about).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestRmiLocalRecognizesABuildByItsLabel(t *testing.T) {
	const image = "org/custom:v9"
	build := &compose.Build{Context: "."}
	for _, tc := range []struct {
		name   string
		labels string // IMAGE_LABELS entry's value half (after "ref="), or "" for none
		want   bool   // whether --rmi local removes it
	}{
		{"no labels at all (pulled, or tagged by hand)", "", false},
		{"opossum's own pair, exact match", "opossum.project=demo,opossum.service=web", true},
		{"docker compose's label, exact match", "com.docker.compose.project=demo", true},
		{"docker compose's label names an unrelated project that needs no sanitizing", "com.docker.compose.project=other", false},
		{"opossum's project label matches but its service label is missing (#1413 — a past build of some other service, inherited)", "opossum.project=demo", false},
		{"opossum's project label matches but its service label names a different service (#1413)", "opossum.project=demo,opossum.service=worker", false},
		{"opossum's project label names a different project, service label matches", "opossum.project=other,opossum.service=web", false},
		{"opossum's pair present, project label mismatched, docker compose's label would have matched — not consulted", "opossum.project=other,opossum.service=web,com.docker.compose.project=demo", false},
		{"opossum's project label matches but service label absent, docker compose's label would have matched — not consulted (#1412, the reverse: a stale inherited opossum.project blocks the compose fallback)", "opossum.project=demo,com.docker.compose.project=demo", false},
		{"opossum's pair matches, docker compose's label names another project — opossum's pair is enough regardless", "opossum.project=demo,opossum.service=web,com.docker.compose.project=other", true},
		{"docker compose's label is a superstring of this project's name, not equal", "com.docker.compose.project=demo-other", false},
		{"opossum's project label is a superstring of this project's name, not equal (service label matches)", "opossum.project=demo-other,opossum.service=web", false},
		{"docker compose's label differs only in case", "com.docker.compose.project=Demo", false},
		{"labels unreadable", "unreadable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			switch tc.labels {
			case "":
			case "unreadable":
				setShimEnv(rt, "IMAGE_ABSENT="+image)
			default:
				setShimEnv(rt, "IMAGE_LABELS="+image+"="+tc.labels)
			}
			var out bytes.Buffer
			svc := compose.Service{Build: build, Image: image}
			if err := orchestrator.New(namedProject(svc), rt, "opossum", &out).Down(false, "local", false); err != nil {
				t.Fatal(err)
			}
			removed := false
			for _, l := range linesWith(log(), "image delete ") {
				if argvNames(l, image) {
					removed = true
				}
			}
			if removed != tc.want {
				t.Errorf("--rmi local removed %v: %v, want %v\ninvocations: %v", image, removed, tc.want, log())
			}
			saidRemoved := strings.Contains(out.String(), "Removing image "+image)
			if saidRemoved != tc.want {
				t.Errorf("--rmi local said it removed %v: %v, want %v", image, saidRemoved, tc.want)
			}
		})
	}
}

// The exact scenario #1126 is about: this project's own name is ALREADY
// opossum's canonical form (needs no sanitizing), and a DIFFERENT docker
// compose project — one whose own name is `my_app`, not `my-app` — happens to
// sanitize to the very same string. TestRmiLocalRecognizesABuildByItsLabel's
// rows all use project "demo", so a label of "my_app" there never collides
// with anything; this test is the one row where it actually would, if
// builtHere normalized the label before comparing.
func TestRmiLocalDoesNotClaimAnotherProjectsBuildThatCollidesAfterSanitizing(t *testing.T) {
	if got := compose.SanitizeName("my_app"); got != "my-app" {
		t.Fatalf(`SanitizeName("my_app") = %q, want "my-app" — the collision this test (and #1126) assumes no longer holds`, got)
	}
	const image = "org/custom:v9"
	build := &compose.Build{Context: "."}
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_LABELS="+image+"=com.docker.compose.project=my_app")
	svc := &compose.Service{Build: build, Image: image}
	p := project("my-app", map[string]*compose.Service{"web": svc})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Down(false, "local", false); err != nil {
		t.Fatal(err)
	}
	for _, l := range linesWith(log(), "image delete ") {
		if argvNames(l, image) {
			t.Errorf("must not remove %v: its label names docker compose project %q, a different project that merely sanitizes to this one's name (%q) — invocations: %v",
				image, "my_app", "my-app", log())
		}
	}
	if strings.Contains(out.String(), "Removing image "+image) {
		t.Errorf("must not say it removed %v, got: %s", image, out.String())
	}
}

// #1412 and #1413 — the exact scenarios the issues reproduce on container
// 1.4.1: a `FROM` chain carries a label an ancestor build set into a
// descendant build's final image, on any key the descendant's own build never
// names. A build can then wear an opossum label that genuinely names THIS
// project — inherited from an earlier, unrelated build of a DIFFERENT
// service — alongside a docker compose label that genuinely names a
// DIFFERENT project's actual build. Checking `opossum.project` alone, or
// either label whichever is present, would read the inherited project name as
// proof; the service label not matching this call's own service is what rules
// it out, without needing to weigh the (possibly also inherited, possibly
// genuine) compose label at all.
func TestRmiLocalDoesNotClaimABuildInheritedFromAnotherServicesOrProjectsBuild(t *testing.T) {
	build := &compose.Build{Context: "."}
	for _, tc := range []struct {
		name, image, labels string
	}{
		// derived:latest is docker compose project "other"'s own build, made
		// FROM a base opossum project "demo" built for its OWN service "web".
		// Project demo's service "api" happens to name this same tag under
		// image: (already there — up never builds it itself), but demo's "api"
		// never built it.
		{"inherited from a docker-compose build of a different project, itself FROM this project's own base image for a different service",
			"org/derived:v1", "com.docker.compose.project=other,opossum.project=demo,opossum.service=web"},
		// #1413's own shape: this project's service "api" names a tag that
		// already holds a hand build FROM a base this SAME project built for
		// its OTHER service "worker" — the project label is genuinely this
		// project's, and even a (here, also matching) docker compose label does
		// not change that api's build never made this image.
		{"this project's own pair present but for a different service, alongside a matching docker compose label",
			"org/derived:v2", "opossum.project=demo,opossum.service=worker,com.docker.compose.project=demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "IMAGE_LABELS="+tc.image+"="+tc.labels)
			svc := &compose.Service{Build: build, Image: tc.image}
			p := project("demo", map[string]*compose.Service{"api": svc})
			var out bytes.Buffer
			if err := orchestrator.New(p, rt, "opossum", &out).Down(false, "local", false); err != nil {
				t.Fatal(err)
			}
			for _, l := range linesWith(log(), "image delete ") {
				if argvNames(l, tc.image) {
					t.Errorf("must not remove %v: its label names a different project, or a different service's build — invocations: %v", tc.image, log())
				}
			}
			if strings.Contains(out.String(), "Removing image "+tc.image) {
				t.Errorf("must not say it removed %v, got: %s", tc.image, out.String())
			}
		})
	}
}

// builtHere is asked with the service being checked, not any other name in
// order — a mutation reading the wrong one (say, always order's first entry)
// would still pass every row above, since each names only one service. Two
// services are needed to catch it: one whose image carries a stale
// `opossum.service` inherited from the OTHER's past build (the #1413 shape,
// so the wrong name and the label's value could coincidentally agree), and one
// whose own label genuinely is its own name. Checked with the affected
// service first in order and with it second, so a mutation that always reads
// order's first or its last entry both meet a row where that entry is not the
// one being asked about.
func TestRmiLocalAsksBuiltHereAboutTheServiceBeingCheckedNotAnotherOneInOrder(t *testing.T) {
	build := &compose.Build{Context: "."}
	const (
		workerImage = "org/worker-build:v1"
		otherImage  = "org/other-build:v1" // inherited opossum.service=worker from worker's past build under a different tag
	)
	newProject := func(otherFirst bool) *compose.Project {
		worker := &compose.Service{Build: build, Image: workerImage}
		other := &compose.Service{Build: build, Image: otherImage}
		if otherFirst {
			worker.DependsOn = compose.DependsOn{{Name: "other"}}
		} else {
			other.DependsOn = compose.DependsOn{{Name: "worker"}}
		}
		return project("demo", map[string]*compose.Service{"worker": worker, "other": other})
	}
	for _, tc := range []struct {
		name       string
		otherFirst bool
	}{
		{"the mislabeled build's service is checked second in order", false},
		{"the mislabeled build's service is checked first in order", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt,
				"IMAGE_LABELS="+workerImage+"=opossum.project=demo,opossum.service=worker "+otherImage+"=opossum.project=demo,opossum.service=worker")
			var out bytes.Buffer
			if err := orchestrator.New(newProject(tc.otherFirst), rt, "opossum", &out).Down(false, "local", false); err != nil {
				t.Fatal(err)
			}
			removed := map[string]bool{}
			for _, l := range linesWith(log(), "image delete ") {
				for _, ref := range []string{workerImage, otherImage} {
					if argvNames(l, ref) {
						removed[ref] = true
					}
				}
			}
			if !removed[workerImage] {
				t.Errorf("must remove %v: its own opossum.service label genuinely names it — invocations: %v", workerImage, log())
			}
			if removed[otherImage] {
				t.Errorf("must not remove %v: its opossum.service label names a different service (\"worker\"), not \"other\" — invocations: %v", otherImage, log())
			}
		})
	}
}

// A service with no `build:` at all is never removed by `--rmi local`, even if
// its image happens to carry a label that would otherwise match this project —
// a pulled image can carry any label its author gave it, and none of that makes
// it something THIS run built. This pins the switch's ordering in removeImages:
// the `built` check has to come before builtHere is even asked.
func TestRmiLocalNeverRemovesAnUnbuiltServicesImageEvenIfLabelled(t *testing.T) {
	const image = "postgres:16"
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_LABELS="+image+"=opossum.project=demo")
	svc := compose.Service{Image: image}
	var out bytes.Buffer
	if err := orchestrator.New(namedProject(svc), rt, "opossum", &out).Down(false, "local", false); err != nil {
		t.Fatal(err)
	}
	for _, l := range linesWith(log(), "image delete ") {
		if argvNames(l, image) {
			t.Errorf("must not remove %v: this service has no build: at all, whatever its image's labels say — invocations: %v", image, log())
		}
	}
}

// Two services, two different custom image names, two different labels — so
// the fake shim (and ImageLabels/builtHere through it) has to answer per ref,
// not the same thing for every "image inspect" it sees. Every other test here
// only ever names one image, which a shim that ignored which ref was asked
// about could still pass.
func TestRmiLocalJudgesEachCustomNamedImageByItsOwnLabels(t *testing.T) {
	const mine, other = "org/mine:v1", "org/other:v1"
	build := &compose.Build{Context: "."}
	rt, log := fakeShim(t)
	setShimEnv(rt,
		"IMAGE_LABELS="+mine+"=opossum.project=demo,opossum.service=a "+other+"=opossum.project=someone-else")
	p := project("demo", map[string]*compose.Service{
		"a": {Build: build, Image: mine},
		"b": {Build: build, Image: other},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Down(false, "local", false); err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, l := range linesWith(log(), "image delete ") {
		for _, ref := range []string{mine, other} {
			if argvNames(l, ref) {
				removed[ref] = true
			}
		}
	}
	if !removed[mine] {
		t.Errorf("must remove %v: it is labelled as this project's own build, invocations: %v", mine, log())
	}
	if removed[other] {
		t.Errorf("must not remove %v: it is labelled as a different project's build, invocations: %v", other, log())
	}
}

// --rmi all is unaffected by any of this: it removes a build under a custom
// `image:` name regardless of labels, as it already did before #1126 (see
// TestRmiLocalGoesByOpossumsOwnName's "all" rows) — this just adds the "even
// with no matching label" case, since all the other rows there are unlabeled.
func TestRmiAllIgnoresLabelsEntirely(t *testing.T) {
	const image = "org/custom:v9"
	build := &compose.Build{Context: "."}
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_LABELS="+image+"=com.docker.compose.project=someone-else")
	svc := compose.Service{Build: build, Image: image}
	if err := orchestrator.New(namedProject(svc), rt, "opossum", &bytes.Buffer{}).Down(false, "all", false); err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, l := range linesWith(log(), "image delete ") {
		if argvNames(l, image) {
			removed = true
		}
	}
	if !removed {
		t.Errorf("--rmi all must remove a service's image regardless of its labels, invocations: %v", log())
	}
}
