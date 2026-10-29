package orchestrator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
)

// noArm64Build is whether what a platform-limited pull said is the runtime's way of
// saying the image has no build for that platform: the three wordings
// runErrorHint reads for a run that names no platform on 1.1.0, 1.2.2 and 1.4.1
// (each has its own). The fourth it reads, 1.6.1's for a run of an image that
// lists several platforms with none given, is not read here: these pulls always
// name the platform, and what 1.6.1 says to such a pull has not been measured.
func noArm64Build(said string) bool {
	return strings.Contains(said, "Error: platform linux/arm64") ||
		strings.Contains(said, "does not support required platforms") ||
		unsupportedPlatformArm64(said)
}

// archOfImage is what asking an image for one platform came to.
type archOfImage int

const (
	archUnknown archOfImage = iota // the pull failed for a reason that says nothing about builds
	archHasArm64
	archOnlyAmd64
)

// adaptAmd64OnlyImage asks `platform: linux/amd64` for a service whose image has
// no arm64 build and has an amd64 one.
//
// docker runs such an image without a word, under emulation; Apple `container`
// 1.5.0 refuses it unless the service says which platform to run (OPSM-412), and
// Rosetta runs the amd64 build. 1.6.1 runs an image that lists only an amd64 build
// without being told (measured), but it takes the platform from the image, and its
// source refuses one that lists several and no arm64 until a platform is given, so
// the entry is still written. It runs an image that lists only an amd64 build on
// both (measured); for one that lists several, the source picks the platform
// given. The service then runs the image its file names, as it does under docker,
// and nothing about what the project means changes.
//
// What the image has is found by asking for it, one platform at a time, because
// the local store cannot say: `image inspect` lists only the builds it holds, so an
// image pulled for amd64 alone lists amd64 alone, and a plain pull fetches every
// platform the image lists (a dozen builds, unpacked) to answer a question about
// one. So the image is pulled for linux/arm64: if that works the image has an arm64
// build and nothing is written; if the runtime says it has none, linux/amd64 is
// pulled the same way, and only if that works is the entry written. (An image
// published as a single manifest, with no index, is fetched in full by the arm64
// pull before it is refused, and fetched again by the amd64 one; one published
// with an index is refused at once, for the cost of the index.) Any other
// failure (the registry, the network, a login) says nothing about builds and
// writes nothing. A copy already here that lists an arm64 build is believed without
// a pull; one that lists amd64 alone is asked, for the reason above.
//
// Pulling is a side effect of planning, so it is done only where the plan is
// going to be acted on: not under --dry-run, not when an overlay already exists or
// -f was given (SetPlanPulls), and, when services were named, not for the others.
// Where it cannot be done an image is not guessed about. A service that already
// names a platform, and one that builds, are left alone. An image the overlay reads
// from ghcr.io (see adaptRegistryName) is asked about under that name.
func (o *Orchestrator) adaptAmd64OnlyImage(name string, svc *compose.Service) []serviceAdaptation {
	if o.rt == nil || svc.Build != nil || svc.Image == "" || svc.Platform != "" {
		return nil
	}
	// Another service that builds this very name makes it a local tag, not an address.
	for other, sib := range o.Project.Services {
		if other != name && sib != nil && sib.Build != nil && strings.EqualFold(sib.Image, svc.Image) {
			return nil
		}
	}
	ref := svc.Image
	if renamed, ok := ghcrName(ref); ok {
		ref = renamed
	}
	if facts, ok := o.imageFacts(ref); ok && (slices.Contains(facts.Archs, "arm64") || !slices.Contains(facts.Archs, "amd64")) {
		return nil // it says it has an arm64 build, or has no amd64 one to run
	}
	if o.rt.DryRun || o.planPullsOff || (o.planNamed != nil && !o.planNamed[name]) {
		return nil
	}
	if o.planArch == nil {
		o.planArch = map[string]archOfImage{}
	}
	got, seen := o.planArch[ref]
	if !seen {
		got = o.askForArm64(ref)
		o.planArch[ref] = got // each image is asked once for a plan, whatever the answer
		delete(o.imageFactsMemo, ref)
	}
	if got != archOnlyAmd64 {
		return nil
	}
	return []serviceAdaptation{{
		Adaptation: Adaptation{
			Service: name,
			Code:    string(codeImageNoArm64),
			Summary: fmt.Sprintf("service %q: image %s has an amd64 build and no arm64 one, so it runs as linux/amd64", name, svc.Image),
		},
		comment: commentBlock(
			fmt.Sprintf("%s service %q: run the amd64 build of its image.", overlayMarker, esc(name)),
			[]string{
				fmt.Sprintf("%s has no build for Apple silicon (arm64); it has an amd64 one.", esc(svc.Image)),
				fmt.Sprintf("docker runs it anyway, emulated; Apple container 1.5.0 does not unless the service says so (%s).", codeImageNoArm64),
				"`platform: linux/amd64` runs that build through Rosetta, so the service runs the",
				"image your file names, as it does under docker. It is slower than a native image,",
				"and it needs Rosetta: where Rosetta is not installed, or a macOS release stops",
				"providing it, this entry stops working and the image has to be replaced by one",
				"with an arm64 build.",
			},
			[]string{
				fmt.Sprintf("after `opossum up`, `opossum ps` should show %s running.", name),
			},
			[]string{
				fmt.Sprintf("run `opossum logs %s`. An image whose code is 32-bit x86 or needs a kernel", esc(name)),
				"feature Rosetta lacks can still fail to start; look for a native tag first.",
			},
		),
		block:   "platform",
		entries: []string{"linux/amd64"},
		scalar:  true,
	}}
}

// askForArm64 pulls ref for linux/arm64 and, when the runtime says it has no build
// for it, for linux/amd64.
func (o *Orchestrator) askForArm64(ref string) archOfImage {
	o.logf("Pulling %s to read which builds it has\n", ref)
	said, err := o.rt.PullPlatform(ref, "linux/arm64")
	if err == nil {
		return archHasArm64
	}
	if !noArm64Build(said) {
		return archUnknown
	}
	o.logf("Pulling %s for linux/amd64\n", ref)
	if _, err := o.rt.PullPlatform(ref, "linux/amd64"); err != nil {
		return archUnknown
	}
	return archOnlyAmd64
}
