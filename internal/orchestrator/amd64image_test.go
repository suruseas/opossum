package orchestrator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// archShim is a `container` stand-in that knows two things about an image: what
// the registry has (the builds it can be pulled for) and what the local store
// holds (what `image inspect` lists). `image pull --platform P ref` succeeds when the
// registry has a build for P and fails, saying what the runtime says, when it does
// not; a pull of a ref ending in `:down` fails the way a network failure does. Every
// call is logged, and the log is what the tests count.
type archShim struct {
	rt  *runtime.Runtime
	log string
}

func archDoc(archs ...string) string {
	var vs []string
	for _, a := range archs {
		vs = append(vs, fmt.Sprintf(`{"platform":{"architecture":%q,"os":"linux"},"config":{"architecture":%q,"config":{"Env":["PATH=/bin"]}}}`, a, a))
	}
	return `[{"id":"x","variants":[` + strings.Join(vs, ",") + `]}]`
}

func newArchShim(t *testing.T, local, registry map[string][]string) *archShim {
	t.Helper()
	dir := t.TempDir()
	safe := func(ref string) string { return strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(ref) }
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$@" >> ` + log + `
d=` + dir + `
op="$1 $2"
ref=; plat=
shift 2
while [ $# -gt 0 ]; do
  case "$1" in
    --platform) plat="$2"; shift ;;
    --progress) shift ;;
    -*) ;;
    *) ref="$1" ;;
  esac
  shift
done
key=$(printf %s "$ref" | tr '/:@' '___')
case "$op" in
"image inspect")
  if [ -e "$d/local_$key" ]; then cat "$d/local_$key"; exit 0; fi
  echo 'Error: image not found' >&2; exit 1 ;;
"image pull")
  case "$ref" in *:down) echo 'Error: unable to reach the registry' >&2; exit 1 ;; esac
  if [ -n "$plat" ]; then
    want="${plat#linux/}"
    if ! grep -qw "$want" "$d/reg_$key" 2>/dev/null; then
      # The three wordings the runtime has said it in (container 1.1.0, 1.2.2, 1.4.1/1.5.0);
      # the ref's tag picks one. The last two are captured output of 1.5.0.
      case "$ref" in
        *:old) echo 'Error: platform linux/arm64' >&2 ;;
        *:index) echo 'Error: unsupported platform Platform(architecture: "amd64", os: "linux", _rawArch: "arm64")' >&2 ;;
        *) echo 'Error: image sha256:0000 does not support required platforms' >&2 ;;
      esac
      exit 1
    fi
    # what the runtime stores after a pull for one platform: that build alone
    printf '[{"id":"x","variants":[{"platform":{"architecture":"%s","os":"linux"},"config":{"architecture":"%s","config":{"Env":["PATH=/bin"]}}}]}]' "$want" "$want" > "$d/local_$key"
  fi
  exit 0 ;;
esac
exit 0
`
	shim := filepath.Join(dir, "c")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for ref, archs := range local {
		if err := os.WriteFile(filepath.Join(dir, "local_"+safe(ref)), []byte(archDoc(archs...)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for ref, archs := range registry {
		if err := os.WriteFile(filepath.Join(dir, "reg_"+safe(ref)), []byte(strings.Join(archs, " ")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &archShim{rt: &runtime.Runtime{Bin: shim}, log: log}
}

func (a *archShim) calls() []string {
	b, _ := os.ReadFile(a.log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (a *archShim) count(prefix string) int {
	n := 0
	for _, c := range a.calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (a *archShim) pulls() int { return a.count("image pull") }

func planAmd64(t *testing.T, a *archShim, services string) (string, []Adaptation, string, string) {
	t.Helper()
	dir := t.TempDir()
	src := "name: demo\nservices:\n" + services
	writeFile(t, dir, "compose.yaml", src)
	p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body, changes := New(p, a.rt, "opossum", io.Discard).PlanOverlay()
	return body, changes, dir, src
}

func appliedOnly(changes []Adaptation) []Adaptation {
	var out []Adaptation
	for _, c := range changes {
		if c.Kind == "applied" {
			out = append(out, c)
		}
	}
	return out
}

// docker runs an image that has no arm64 build, emulated, without a word; Apple
// container refuses it unless the service names a platform. The overlay names it
// for the services whose image has an amd64 build and no arm64 one — found by
// asking the registry for each platform in turn, not by what the local store lists.
func TestAnAmd64OnlyImageGetsItsPlatformInTheOverlay(t *testing.T) {
	const img = "vendor/app:1"
	for _, tc := range []struct {
		name            string
		local, registry []string // nil: not in the local store / the registry
		svc             string
		want            bool
	}{
		{"amd64 only, not here", nil, []string{"amd64"}, "    image: " + img + "\n", true},
		{"amd64 and others, not here", nil, []string{"amd64", "s390x"}, "    image: " + img + "\n", true},
		{"both builds, not here", nil, []string{"arm64", "amd64"}, "    image: " + img + "\n", false},
		{"arm64 only, not here", nil, []string{"arm64"}, "    image: " + img + "\n", false},
		{"neither of the two", nil, []string{"s390x"}, "    image: " + img + "\n", false},
		{"amd64 only, held locally", []string{"amd64"}, []string{"amd64"}, "    image: " + img + "\n", true},
		// The local store lists only the builds it holds: an image pulled for amd64
		// alone lists amd64 alone, and has an arm64 build all the same.
		{"held locally as amd64, but the registry has arm64", []string{"amd64"}, []string{"arm64", "amd64"}, "    image: " + img + "\n", false},
		{"held locally with arm64", []string{"arm64", "amd64"}, []string{"arm64", "amd64"}, "    image: " + img + "\n", false},
		{"a platform is already given", nil, []string{"amd64"}, "    image: " + img + "\n    platform: linux/amd64\n", false},
		{"another platform is given", nil, []string{"amd64"}, "    image: " + img + "\n    platform: linux/arm64\n", false},
		{"a service that builds", nil, []string{"amd64"}, "    image: " + img + "\n    build: .\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := map[string][]string{}
			if tc.local != nil {
				local[img] = tc.local
			}
			registry := map[string][]string{}
			if tc.registry != nil {
				registry[img] = tc.registry
			}
			a := newArchShim(t, local, registry)
			body, changes, dir, src := planAmd64(t, a, "  app:\n"+tc.svc)
			applied := appliedOnly(changes)
			if !tc.want {
				if len(applied) != 0 {
					t.Fatalf("no platform was called for, got %+v\n%s", applied, body)
				}
				return
			}
			if len(applied) != 1 || applied[0].Code != string(codeImageNoArm64) {
				t.Fatalf("want one applied OPSM-412 entry, got %+v\n%s", changes, body)
			}
			ov := writeFile(t, dir, OverlayFileName, body)
			p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
			if err != nil {
				t.Fatalf("the overlay must load: %v\n%s", err, body)
			}
			if got := p.Services["app"].Platform; got != "linux/amd64" {
				t.Errorf("after the overlay the platform is %q\n%s\n(source)\n%s", got, body, src)
			}
		})
	}
}

// The image is asked for one platform at a time. A plain pull fetches every build
// the image lists — a dozen of them, unpacked — to answer a question about two.
func TestTheBuildsOfAnImageAreAskedForOnePlatformAtATime(t *testing.T) {
	t.Run("an image with an arm64 build: one pull, for arm64", func(t *testing.T) {
		a := newArchShim(t, nil, map[string][]string{"v/a:1": {"arm64", "amd64", "s390x"}})
		planAmd64(t, a, "  app:\n    image: v/a:1\n")
		if got := a.calls(); len(got) < 2 || got[1] != "image pull --progress none --platform linux/arm64 v/a:1" {
			t.Fatalf("want a pull limited to arm64, got %v", got)
		}
		if a.count("image pull --progress none --platform linux/amd64") != 0 {
			t.Errorf("asked for amd64 though arm64 was there: %v", a.calls())
		}
		for _, c := range a.calls() {
			if strings.HasPrefix(c, "image pull") && !strings.Contains(c, "--platform") {
				t.Errorf("a pull of every build: %q", c)
			}
		}
	})
	t.Run("an image without one: arm64 refused, then amd64", func(t *testing.T) {
		a := newArchShim(t, nil, map[string][]string{"v/a:1": {"amd64"}})
		_, changes, _, _ := planAmd64(t, a, "  app:\n    image: v/a:1\n")
		if len(appliedOnly(changes)) != 1 {
			t.Fatalf("want the platform entry, got %+v", changes)
		}
		var pulls []string
		for _, c := range a.calls() {
			if strings.HasPrefix(c, "image pull") {
				pulls = append(pulls, c)
			}
		}
		want := []string{"image pull --progress none --platform linux/arm64 v/a:1", "image pull --progress none --platform linux/amd64 v/a:1"}
		if strings.Join(pulls, "|") != strings.Join(want, "|") {
			t.Errorf("pulls %v, want %v", pulls, want)
		}
	})
	t.Run("a copy here that lists an arm64 build is believed without a pull", func(t *testing.T) {
		a := newArchShim(t, map[string][]string{"v/a:1": {"arm64", "amd64"}}, map[string][]string{"v/a:1": {"arm64", "amd64"}})
		planAmd64(t, a, "  app:\n    image: v/a:1\n")
		if a.pulls() != 0 {
			t.Errorf("pulled an image whose local copy has an arm64 build: %v", a.calls())
		}
	})
}

// A failure that says nothing about builds writes nothing, and the image is not
// asked about again by the next service on it.
func TestAFailureThatIsNotAboutBuildsWritesNothing(t *testing.T) {
	t.Run("the registry cannot be reached", func(t *testing.T) {
		a := newArchShim(t, nil, nil)
		_, changes, _, _ := planAmd64(t, a, "  one:\n    image: v/a:down\n  two:\n    image: v/a:down\n")
		if len(appliedOnly(changes)) != 0 {
			t.Errorf("a pull that failed for the network was read as a missing build: %+v", changes)
		}
		if got := a.pulls(); got != 1 {
			t.Errorf("a failing pull was attempted %d times for two services on the image, want once: %v", got, a.calls())
		}
	})
	t.Run("amd64 cannot be had after arm64 was refused", func(t *testing.T) {
		a := newArchShim(t, nil, map[string][]string{"v/a:1": {"s390x"}})
		_, changes, _, _ := planAmd64(t, a, "  app:\n    image: v/a:1\n")
		if len(appliedOnly(changes)) != 0 {
			t.Errorf("an image with neither build was given a platform: %+v", changes)
		}
	})
}

// Pulling is a side effect of planning, so it is done only where the plan is acted
// on, and for the services this run starts.
func TestPlanningPullsOnlyWhereThePlanIsActedOn(t *testing.T) {
	plan := func(t *testing.T, a *archShim, setup func(o *Orchestrator), named []string) []Adaptation {
		t.Helper()
		dir := t.TempDir()
		writeFile(t, dir, "compose.yaml", "name: demo\nservices:\n  a:\n    image: v/a:1\n  b:\n    image: v/b:1\n")
		p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		o := New(p, a.rt, "opossum", io.Discard)
		if setup != nil {
			setup(o)
		}
		_, changes, _ := o.PlanOverlayFor(named)
		return changes
	}
	reg := map[string][]string{"v/a:1": {"amd64"}, "v/b:1": {"amd64"}}
	t.Run("--dry-run", func(t *testing.T) {
		a := newArchShim(t, nil, reg)
		a.rt.DryRun = true
		if ch := plan(t, a, nil, nil); len(ch) != 0 || a.pulls() != 0 || len(a.rt.Plan) != 0 {
			t.Errorf("a dry run pulled or guessed: changes %+v, calls %v, plan %v", ch, a.calls(), a.rt.Plan)
		}
	})
	t.Run("a plan that may not pull", func(t *testing.T) {
		a := newArchShim(t, map[string][]string{"v/a:1": {"amd64"}}, reg)
		ch := plan(t, a, func(o *Orchestrator) { o.SetPlanPulls(false) }, nil)
		if len(ch) != 0 || a.pulls() != 0 {
			t.Errorf("went on without pulling: %+v, %v", ch, a.calls())
		}
	})
	t.Run("only the named services", func(t *testing.T) {
		a := newArchShim(t, nil, reg)
		ch := plan(t, a, nil, []string{"a"})
		if a.count("image pull --progress none --platform linux/arm64 v/b:1") != 0 {
			t.Errorf("pulled for a service that was not named: %v", a.calls())
		}
		if len(ch) != 1 || ch[0].Service != "a" {
			t.Errorf("want a alone, got %+v", ch)
		}
	})
}

// An image the overlay reads from ghcr.io is asked about there; the lscr.io name is
// one the runtime refuses to pull at all. A service with no image asks nothing.
func TestTheNameTheOverlayGivesIsTheOneAskedAbout(t *testing.T) {
	a := newArchShim(t, nil, map[string][]string{"ghcr.io/linuxserver/foo:1": {"amd64"}})
	_, changes, _, _ := planAmd64(t, a, "  app:\n    image: lscr.io/linuxserver/foo:1\n")
	for _, c := range a.calls() {
		if strings.Contains(c, "lscr.io") {
			t.Errorf("the lscr.io name was asked about: %q", c)
		}
	}
	if got := len(appliedOnly(changes)); got != 2 { // the ghcr.io reading, and the platform
		t.Errorf("want the rename and the platform, got %+v", changes)
	}
	b := newArchShim(t, nil, nil)
	planAmd64(t, b, "  app:\n    command: [\"true\"]\n    build: .\n")
	if len(b.calls()) != 0 {
		t.Errorf("asked about an image that is not there: %v", b.calls())
	}
}

// Three services on one image are one question, and a service whose image is its
// own build is not asked at all.
func TestAnImageIsAskedAboutOnceWhateverTheNumberOfServices(t *testing.T) {
	a := newArchShim(t, nil, map[string][]string{"v/a:1": {"amd64"}, "v/o:2": {"arm64"}})
	_, changes, _, _ := planAmd64(t, a, "  one:\n    image: v/a:1\n  two:\n    image: v/a:1\n  three:\n    image: v/o:2\n  built:\n    build: .\n    image: v/built:3\n")
	if got := a.count("image pull --progress none --platform linux/arm64 v/a:1"); got != 1 {
		t.Errorf("v/a:1 asked for arm64 %d times, want once for two services", got)
	}
	if got := a.count("image pull --progress none --platform linux/amd64 v/a:1"); got != 1 {
		t.Errorf("v/a:1 asked for amd64 %d times, want once", got)
	}
	if got := a.count("image inspect v/built:3") + a.count("image pull --progress none --platform linux/arm64 v/built:3"); got != 0 {
		t.Errorf("a built image was asked about %d times", got)
	}
	services := map[string]bool{}
	for _, c := range appliedOnly(changes) {
		services[c.Service] = true
	}
	if len(services) != 2 || !services["one"] || !services["two"] {
		t.Errorf("want one and two given the platform, got %v", services)
	}
}

// The runtime has said "no build for arm64" in three ways, and each is read: the
// old `Error: platform linux/arm64`, an index that lists no arm64 build
// (`unsupported platform Platform(… _rawArch: "arm64")`, captured from container
// 1.5.0 on `amd64/busybox:1.36.1`), and a single-manifest image fetched in full and
// then refused (`does not support required platforms`, captured on `mailhog/mailhog`).
func TestEachWordingOfNoArm64BuildIsRead(t *testing.T) {
	for _, tag := range []string{"old", "index", "manifest"} {
		t.Run(tag, func(t *testing.T) {
			ref := "v/a:" + tag
			a := newArchShim(t, nil, map[string][]string{ref: {"amd64"}})
			_, changes, _, _ := planAmd64(t, a, "  app:\n    image: "+ref+"\n")
			if len(appliedOnly(changes)) != 1 {
				t.Errorf("a refusal worded the %s way was not read as a missing arm64 build: %+v", tag, changes)
			}
		})
	}
	// A refusal in other words says nothing about builds.
	a := newArchShim(t, nil, nil)
	_, changes, _, _ := planAmd64(t, a, "  app:\n    image: v/a:down\n")
	if len(appliedOnly(changes)) != 0 {
		t.Errorf("a failure about the network was read as a missing build: %+v", changes)
	}
}

// A local copy that lists neither build has nothing to run: it is not asked.
func TestALocalCopyWithNeitherBuildIsNotPulledFor(t *testing.T) {
	a := newArchShim(t, map[string][]string{"v/a:1": {"s390x"}}, map[string][]string{"v/a:1": {"arm64", "amd64"}})
	planAmd64(t, a, "  app:\n    image: v/a:1\n")
	if a.pulls() != 0 {
		t.Errorf("pulled for an image whose local copy lists neither build: %v", a.calls())
	}
}

// Once the image has been pulled for a platform it is here, and what the plan reads
// of it afterwards (a Postgres image's data directory, say) is read from it, not
// from the answer that said it was not.
func TestWhatPlanningReadsOfAnImageAfterThePullComesFromThePulledImage(t *testing.T) {
	a := newArchShim(t, nil, map[string][]string{"v/a:1": {"arm64", "amd64"}})
	dir := t.TempDir()
	writeFile(t, dir, "compose.yaml", "name: demo\nservices:\n  app:\n    image: v/a:1\n")
	p, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p, a.rt, "opossum", io.Discard)
	if _, ok := o.imageFacts("v/a:1"); ok {
		t.Fatal("the image should not be here before the plan")
	}
	o.PlanOverlay()
	if facts, ok := o.imageFacts("v/a:1"); !ok || len(facts.Archs) != 1 || facts.Archs[0] != "arm64" {
		t.Errorf("after the pull the image reads as %+v (here: %v), want its arm64 build", facts, ok)
	}
}

// Another service that builds this name makes it a local tag, not an address: the
// published image of the same name is not this service's, and is not pulled to find
// out what it has.
func TestAnImageAnotherServiceBuildsIsNotAskedAbout(t *testing.T) {
	a := newArchShim(t, nil, map[string][]string{"org/app:latest": {"amd64"}})
	_, changes, _, _ := planAmd64(t, a, "  builder:\n    build: .\n    image: org/app:latest\n  worker:\n    image: org/app:latest\n")
	if len(appliedOnly(changes)) != 0 || a.pulls() != 0 {
		t.Errorf("a name a sibling builds was asked about: %+v, %v", changes, a.calls())
	}
}
