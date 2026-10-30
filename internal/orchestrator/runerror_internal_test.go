package orchestrator

// Evals for #298: decoding cryptic `container run` failures the pre-flight can't
// catch into actionable hints. Signatures are the real stderr captured while
// dogfooding Haxxnet (#296): an amd64-only image, a host-port conflict the host
// probe can't see (Apple `container`'s DNS holds 53), and a missing file bind mount.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

func runErr(stderr string) error {
	return &rt.RunError{Err: fmt.Errorf("exit status 1"), Stderr: stderr}
}

// An image with no arm64 build is told so in whichever words the runtime uses,
// and an image with no amd64 build is not told to ask for amd64.
//
// The wordings are both measured, on the versions named beside them. The runtime
// changed how it says this between them, and the older text kept working while
// the newer one silently matched nothing: the guidance simply stopped appearing,
// which from the outside looks like guidance that was never written. Every
// wording here stays until the version that produces it is out of use.
//
// The last case is the one that makes the arm64 wording specific rather than
// loose. The 1.2.2 message names the platform that is *missing*, so asking an
// arm64-only image for amd64 says `platform linux/amd64` — and answering that
// with "add `platform: linux/amd64`" tells someone to do the thing that just
// failed.
func TestRunErrorHintPlatform(t *testing.T) {
	for _, tc := range []struct {
		name, platform, stderr string
		wantHint               bool
	}{
		// container 1.1.0, and 1.2.2 as well: measured on the corpus, an image
		// that is fetched before the mismatch is found still says this on 1.2.2
		// (Compose-Examples/examples/cs2-dedicated-server, atlas).
		{"the wording from both versions", "", "Error: image sha256:abc does not support required platforms", true},
		// container 1.2.2 said this for excalidraw/excalidraw-room:latest (image
		// index without arm64); 1.3.1 says it for an amd64-only local image run as
		// the default arm64 (platform-image-no-arm64-131.txt) — same wording,
		// different trigger.
		{"the wording both versions use for a missing arm64", "", "Error: platform linux/arm64", true},
		// container 1.2.2, measured by building an image for arm64 only and
		// running it with --platform linux/amd64. The message names what is
		// missing, so this is not an arm64 problem and amd64 is not the answer.
		{"an image with no amd64 build", "", "Error: platform linux/amd64", false},
		// container 1.4.1, an image that is not local yet
		// (platform-unsupported-fresh-pull-141.txt): the runtime pulls it and then
		// says this instead of `Error: platform linux/arm64`, with the same rule —
		// the arch it names is the one that is missing.
		{"a fresh pull of an image with no arm64 build", "",
			"[2/6] Unpacking image [2s]\n" + freshPullLine("arm64"), true},
		{"a fresh pull of an image with no amd64 build", "", freshPullLine("amd64"), false},
		// container 1.6.1 writes the arm64 default variant out
		// (platform-several-fresh-pull-161.txt, a pull limited to linux/arm64).
		{"a refusal with the arm64 variant written out", "",
			`Error: unsupported platform Platform(osVersion: nil, osFeatures: nil, variant: Optional("v8"), _rawOS: "linux", _rawArch: "arm64")`, true},
		// Asked for amd64 and told to ask for amd64 is not advice (the same rule as
		// the wordings above).
		{"a fresh pull that missed arm64, having already asked for amd64", "amd64", freshPullLine("arm64"), false},
		// The line has to be the runtime's own: it begins the line. A container that
		// repeats it, or a runtime line that carries arm64 somewhere else, is not
		// this failure.
		{"a container repeating the runtime's line", "", "worker: " + freshPullLine("arm64"), false},
		{"the runtime's line indented", "", " " + freshPullLine("arm64"), false},
		{"the platform on another line than the message", "",
			"Error: unsupported platform Platform(osVersion: nil)\n_rawArch: \"arm64\"\n", false},
		{"the arch on the message but not the missing one's key", "",
			"Error: unsupported platform Platform(osVersion: nil, arch: \"arm64\")\n", false},
		// container 1.6.1 takes the platform from the image, and refuses one that
		// lists several and no arm64 until a platform is given, naming the list
		// (platform-several-no-arm64-161.txt). With amd64 in it, Rosetta runs it.
		{"an image listing several platforms, amd64 among them", "",
			"[0/6] [0s]\n" + severalLine("amd64", "s390x"), true},
		{"the same list, amd64 last", "", severalLine("s390x", "amd64"), true},
		// No amd64 in the list: telling anyone to ask for amd64 sends them to a
		// build the image does not have.
		{"an image listing several platforms, amd64 not among them", "",
			severalLine("s390x", "ppc64le"), false},
		{"several platforms, having already asked for amd64", "linux/amd64",
			severalLine("amd64", "s390x"), false},
		// The runtime's own line: it begins the line, and the list is on it.
		{"a container repeating the several-platforms line", "", "worker: " + severalLine("amd64", "s390x"), false},
		{"the several-platforms line indented", "", " " + severalLine("amd64", "s390x"), false},
		{"the list on another line than the message", "",
			"Error: image x:1 has multiple platforms ([\n" + platformEntry("linux", "amd64", "nil") + "])\n", false},
		{"amd64 on an error line that is not the several-platforms one", "",
			"Error: image x:1 is broken ([" + platformEntry("linux", "amd64", "nil") + "])\n", false},
		// What decides is the refusal's own list: arm64 named on another line (here
		// a container's output from before) does not take the advice away.
		{"arm64 named on another line than the refusal", "",
			"worker: " + platformEntry("linux", "arm64", "nil") + "\n" + severalLine("amd64", "s390x"), true},
		// An image with an arm64 build has a build for this Mac. The runtime's source
		// refuses a list none of whose entries is the default exactly (an arm64 one
		// with a variant other than v8, which it reads as the default's), and that is
		// not a missing arm64 build.
		{"several platforms, an arm64 one among them", "",
			severalLineOf("x:1", platformEntry("linux", "arm64", `Optional("v9")`), platformEntry("linux", "amd64", "nil")), false},
		{"arm64 only in the image's name", "",
			severalLineOf("org/app-arm64:1", platformEntry("linux", "amd64", "nil"), platformEntry("linux", "s390x", "nil")), true},
		// The runtime matches amd64 down to the variant, so `platform: linux/amd64`
		// does not run an image whose only amd64 build is amd64/v2.
		{"several platforms, amd64 only with a variant", "",
			severalLineOf("x:1", platformEntry("linux", "amd64", `Optional("v2")`), platformEntry("linux", "s390x", "nil")), false},
		// The arch is read from the platform's own field, not from anywhere on the
		// line: an image whose name says amd64 lists what it lists.
		{"amd64 only in the image's name", "",
			severalLineOf("org/app:1.2-amd64", platformEntry("linux", "s390x", "nil"), platformEntry("linux", "ppc64le", "nil")), false},
		// `platform: linux/amd64` asks for a Linux build: an amd64 one for another OS
		// is not it.
		{"several platforms, amd64 only for another OS", "",
			severalLineOf("x:1", platformEntry("windows", "amd64", "nil"), platformEntry("linux", "s390x", "nil")), false},
		// container 1.6.1, a service that asks for arm64 run on a local image built
		// for amd64 alone (platform-not-supported-arm64-161.txt): the runtime says
		// what was asked for and lists what the image has.
		{"asked for arm64, the image lists amd64", "linux/arm64",
			"[0/6] [0s]\n" + notSupportedLine("linux/arm64", platformEntry("linux", "amd64", "nil")), true},
		{"asked for arm64 with --arch alone", "",
			notSupportedLine("*/arm64", platformEntry("linux", "amd64", "nil")), true},
		// Not a shape the runtime prints (it refuses arm64/v9 as an invalid
		// variant before this point): it pins that the arch is read as the second part.
		{"asked for an arm64 variant (made up)", "linux/arm64/v9",
			notSupportedLine("linux/arm64/v9", platformEntry("linux", "s390x", "nil"), platformEntry("linux", "amd64", "nil")), true},
		{"asked for arm64, arm64 only in the image's name", "linux/arm64",
			notSupportedLineOf("org/app-arm64:1", "linux/arm64", platformEntry("linux", "amd64", "nil")), true},
		// What is missing is what was asked for: another arch is not an arm64
		// problem, whatever the list or the image's name says.
		{"asked for amd64, the image lists arm64", "linux/amd64",
			notSupportedLine("linux/amd64", platformEntry("linux", "arm64", `Optional("v8")`)), false},
		{"asked for another arch, the image lists amd64", "linux/s390x",
			notSupportedLine("linux/s390x", platformEntry("linux", "amd64", "nil")), false},
		{"asked for another arch, arm64 in the image's name", "linux/s390x",
			notSupportedLineOf("org/app-arm64:1", "linux/s390x", platformEntry("linux", "amd64", "nil")), false},
		{"asked for another arch, /arm64 in the image's name", "linux/s390x",
			notSupportedLineOf("org/arm64/app:1", "linux/s390x", platformEntry("linux", "amd64", "nil")), false},
		{"asked for arm64 as the OS part", "",
			notSupportedLine("arm64/riscv64", platformEntry("linux", "amd64", "nil")), false},
		{"what was asked for, with no arch part", "",
			notSupportedLine("arm64", platformEntry("linux", "amd64", "nil")), false},
		// The list decides as for the several-platforms refusal.
		{"asked for arm64, no amd64 in the list", "linux/arm64",
			notSupportedLine("linux/arm64", platformEntry("linux", "s390x", "nil")), false},
		{"asked for arm64, amd64 only with a variant", "linux/arm64",
			notSupportedLine("linux/arm64", platformEntry("linux", "amd64", `Optional("v2")`)), false},
		{"asked for arm64, amd64 only for another OS", "linux/arm64",
			notSupportedLine("linux/arm64", platformEntry("windows", "amd64", "nil")), false},
		{"asked for arm64, the list has an arm64 v9 build", "linux/arm64",
			notSupportedLine("linux/arm64", platformEntry("linux", "arm64", `Optional("v9")`), platformEntry("linux", "amd64", "nil")), false},
		// What decides is the refusal's own list: arm64 named on another line does
		// not take the advice away.
		{"arm64 named on another line than the not-supported refusal", "linux/arm64",
			"worker: " + platformEntry("linux", "arm64", "nil") + "\n" + notSupportedLine("linux/arm64", platformEntry("linux", "amd64", "nil")), true},
		{"asked for arm64 v9 (made up), the list has arm64 v8", "linux/arm64/v9",
			notSupportedLine("linux/arm64/v9", platformEntry("linux", "arm64", `Optional("v8")`), platformEntry("linux", "amd64", "nil")), false},
		// The runtime's own line: it begins the line, and the list is on it.
		{"a container repeating the not-supported line", "linux/arm64",
			"worker: " + notSupportedLine("linux/arm64", platformEntry("linux", "amd64", "nil")), false},
		{"the not-supported line indented", "linux/arm64",
			" " + notSupportedLine("linux/arm64", platformEntry("linux", "amd64", "nil")), false},
		{"the not-supported list on another line", "linux/arm64",
			"Error: image x:1 does not support linux/arm64; available: [\n" + platformEntry("linux", "amd64", "nil") + "]\n", false},
		{"arm64 and amd64 on an error line with no list", "linux/arm64",
			"Error: image x:1 does not support linux/arm64 (" + platformEntry("linux", "amd64", "nil") + ")\n", false},
		{"an error line with a list and nothing asked for", "linux/arm64",
			"Error: image x:1 is linux/arm64; available: [" + platformEntry("linux", "amd64", "nil") + "]\n", false},
		{"the list before what was asked for", "linux/arm64",
			"Error: image x:1; available: [" + platformEntry("linux", "amd64", "nil") + "] does not support linux/arm64\n", false},
		{"arm64 and amd64 on an error line that does not name the image", "linux/arm64",
			"Error: pull does not support linux/arm64; available: [" + platformEntry("linux", "amd64", "nil") + "]\n", false},
		// The older wording does not say which platform was wanted, so the service
		// has to. Asked for amd64 and told to ask for amd64 is not advice.
		{"the older wording, having already asked for amd64", "linux/amd64",
			"Error: image sha256:abc does not support required platforms", false},
		// The same request, spelled the other way. The runtime treats both as
		// x86-64 and gives either one Rosetta; read differently here, a service
		// written this way is told to ask for what it already asked for.
		{"having asked for it as x86_64", "linux/x86_64",
			"Error: image sha256:abc does not support required platforms", false},
		{"having asked for it in capitals", "linux/AMD64",
			"Error: image sha256:abc does not support required platforms", false},
		// `platform linux/arm64` on its own is a substring of the flag a service
		// legitimately asks for. Nothing writes a command line into this stream
		// today — it carries the child's stderr and nothing else — so the anchor
		// guards against text arriving from somewhere that is not this failure,
		// rather than against a path anyone can point at now.
		{"a command line that names the platform", "linux/arm64",
			"run -d --platform linux/arm64 --name web web:latest: exit status 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := runErrorHint(&compose.Service{Platform: tc.platform}, "", false, runErr(tc.stderr))
			if got := strings.Contains(h, "platform: linux/amd64"); got != tc.wantHint {
				if tc.wantHint {
					t.Errorf("this is an image with no arm64 build and nothing said so; the runtime "+
						"wording changed and the match did not, got: %q", h)
				} else {
					t.Errorf("telling anyone to add `platform: linux/amd64` here is telling them to "+
						"repeat what just failed, or to fix something that did not break, got: %q", h)
				}
			}
			switch {
			case tc.wantHint && !strings.Contains(h, "arm64"):
				t.Errorf("the hint should say which architecture has no build, got: %q", h)
			case !tc.wantHint && h != "":
				// Not merely "no amd64 advice": any hint here is a wrong answer, and
				// checking only for the amd64 wording would let a different wrong one
				// through.
				t.Errorf("nothing here is diagnosed, so nothing should be claimed, got: %q", h)
			}
		})
	}
}

// A service that already names a platform, arm64, is told to set amd64 in its
// place: told to add it, a reader who writes a second `platform:` key has a file
// that no longer loads.
func TestRunErrorHintPlatformSaysSetNotAdd(t *testing.T) {
	h := runErrorHint(&compose.Service{Platform: "linux/arm64"}, "", false,
		runErr(notSupportedLine("linux/arm64", platformEntry("linux", "amd64", "nil"))))
	if !strings.Contains(h, "Set `platform: linux/amd64` on the service") || strings.Contains(h, "Add `platform") {
		t.Errorf("the advice should say to set the platform the service already names, got: %q", h)
	}
}

func TestRunErrorHintPortConflict(t *testing.T) {
	svc := &compose.Service{Ports: []string{"8080:80/tcp", "53:53/udp"}}
	h := runErrorHint(svc, "", false, runErr("Error: failed to bootstrap container (cause: bind(descriptor:ptr:bytes:): Address already in use) (errno: 48)"))
	// Names the service's published ports (svc-derived, not just the static text)…
	if !strings.Contains(h, "this service publishes") || !strings.Contains(h, "localhost:8080") || !strings.Contains(h, "localhost:53") {
		t.Errorf("port hint should echo the service's published ports, got: %q", h)
	}
	// …and appends the per-service culprit note because 53 is actually published.
	if !strings.Contains(h, "on macOS, 53 is the runtime's built-in DNS") || !strings.Contains(h, "Remap") {
		t.Errorf("publishing 53 should append the runtime-DNS culprit note, got: %q", h)
	}
}

// The per-service culprit note ("on macOS, …") only appears for the ports the
// service actually publishes — no spurious 53/AirPlay note for an unrelated port.
func TestRunErrorHintPortNoSpuriousCulprit(t *testing.T) {
	h := runErrorHint(&compose.Service{Ports: []string{"8080:80"}}, "", false,
		runErr("bind: Address already in use"))
	if strings.Contains(h, "AirPlay") || strings.Contains(h, "on macOS,") {
		t.Errorf("an 8080-only conflict must not append the 53/AirPlay culprit note, got: %q", h)
	}
	if !strings.Contains(h, "localhost:8080") {
		t.Errorf("should still name the published port, got: %q", h)
	}
}

func TestRunErrorHintFileBind(t *testing.T) {
	h := runErrorHint(&compose.Service{Volumes: []string{"./Caddyfile:/etc/caddy/Caddyfile"}}, "", false,
		runErr("Error: failed to start process (cause: mount failed with errno 20: failed to resolve '/etc/caddy/Caddyfile' in rootfs)"))
	if !strings.Contains(h, "/etc/caddy/Caddyfile") || !strings.Contains(h, "config FILE") || !strings.Contains(h, "directory") {
		t.Errorf("a file bind-mount failure should name the path and explain the dir-vs-file gotcha, got: %q", h)
	}
}

func TestRunErrorHintUnknownAndNonRunError(t *testing.T) {
	if h := runErrorHint(&compose.Service{}, "", false, runErr("some other failure\nexit 1")); h != "" {
		t.Errorf("an unrecognized failure must yield no hint, got: %q", h)
	}
	// A plain (non-RunError) error carries no captured stderr — no hint.
	if h := runErrorHint(&compose.Service{}, "", false, fmt.Errorf("exit status 1")); h != "" {
		t.Errorf("a non-RunError must yield no hint, got: %q", h)
	}
	// nil service is safe (port branch guards it).
	if h := runErrorHint(nil, "", false, runErr("Address already in use")); !strings.Contains(h, "host port") {
		t.Errorf("nil service should still give the generic port hint, got: %q", h)
	}
}

// decodeStartError appends the decoded hint for a recognized run failure, and
// falls back to the generic start-failed message otherwise.
func TestDecodeStartErrorAppendsHint(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"web": {Name: "web", Image: "x"}}}
	o := New(p, &rt.Runtime{}, "", &bytes.Buffer{})
	if s := o.decodeStartError("web", nil, runErr("Error: image does not support required platforms")).Error(); !strings.Contains(s, "platform: linux/amd64") {
		t.Errorf("a recognized run failure should get the decoded hint, got: %s", s)
	}
	// The service name opens the line and the hint closes it; both are strings,
	// and exchanged the hint would be quoted as the service's name (#559).
	if s := o.decodeStartError("web", nil, runErr("Error: image does not support required platforms")).Error(); !strings.HasPrefix(s, `starting service "web": `) {
		t.Errorf("the decoded error should open with the service's name, got: %s", s)
	}
	if s := o.decodeStartError("web", nil, runErr("random crash")).Error(); !strings.Contains(s, "opossum logs web") {
		t.Errorf("an unrecognized failure should fall back to startFailed, got: %s", s)
	}
}

// Every decoded hint carries a code, and it is the code that indexes its fix —
// this is the half the prose cannot do. Anything splitting "diagnosed" from
// "undiagnosed" has only the code to split on, so an accurate hint without one is
// counted as undiagnosed.
//
// The expected codes are written out rather than derived, so that pointing two
// signatures at one code — or at the wrong one — is a failure and not a rename.
func TestEachDecodedHintCarriesItsCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		svc    *compose.Service
		stderr string
		want   diagCode
	}{
		{"an amd64-only image", &compose.Service{},
			"Error: image sha256:abc does not support required platforms", codeImageNoArm64},
		// The same failure in the words 1.2.2 also uses. Listed separately because
		// a wording that decodes without carrying its code is a hint nobody can
		// look up, and matching on new text is exactly where that gets forgotten.
		{"an amd64-only image, in the newer wording", &compose.Service{},
			"Error: platform linux/arm64", codeImageNoArm64},
		// Reused, not minted: the pre-flight names this same conflict, and the
		// only difference here is that it could not see it in advance.
		{"a port the pre-flight could not see", &compose.Service{Ports: []string{"53:53/udp"}},
			"Error: bind(descriptor:ptr:bytes:): Address already in use", codeHostPortInUse},
		// Likewise: this is the placeholder directory OPSM-107 warns about,
		// arriving as a refusal instead of a warning.
		{"an amd64-only image that was not local", &compose.Service{},
			freshPullLine("arm64"), codeImageNoArm64},
		{"an image listing several platforms and no arm64", &compose.Service{},
			severalLine("amd64", "s390x"), codeImageNoArm64},
		{"an image with no arm64 build, asked for arm64", &compose.Service{Platform: "linux/arm64"},
			notSupportedLine("linux/arm64", platformEntry("linux", "amd64", "nil")), codeImageNoArm64},
		{"a bind mount that will not resolve", &compose.Service{},
			"Error: mount failed with errno 20: failed to resolve '/etc/caddy/Caddyfile' in rootfs", codeBindFilePlaceholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := runErrorHint(tc.svc, "", false, runErr(tc.stderr))
			if h == "" {
				t.Fatalf("this signature should still decode at all, got no hint")
			}
			if want := "[" + string(tc.want) + "]"; !strings.Contains(h, want) {
				t.Errorf("the hint should carry %s so it can be looked up, got: %q", want, h)
			}
		})
	}
}

// The generic start failure stays uncoded, and that is a decision rather than an
// omission: opossum has nothing specific to say there, so there is no fix for a
// code to index. Coding it would make every start failure look diagnosed and
// erase the distinction the decoding above exists to draw.
func TestAnUndecodedStartFailureCarriesNoCode(t *testing.T) {
	err := startFailed("web", runErr("Error: something nobody has decoded yet"))
	if strings.Contains(err.Error(), "OPSM-") {
		t.Errorf("an undiagnosed failure must not look diagnosed, got: %v", err)
	}
	if runErrorHint(&compose.Service{}, "", false, runErr("Error: something nobody has decoded yet")) != "" {
		t.Error("an unknown signature should decode to nothing at all")
	}
}

// freshPullLine is the runtime's message for an image with no build for arch,
// pulled for the first time, as container 1.4.1 words it (the capture keeps the
// whole line).
func freshPullLine(arch string) string {
	return `Error: unsupported platform Platform(osVersion: nil, osFeatures: nil, variant: nil, _rawOS: "linux", _rawArch: "` + arch + `")` + "\n"
}

// severalLine is container 1.6.1's refusal of a local image that lists the given
// Linux platforms, in the shape platform-several-no-arm64-161.txt holds.
func severalLine(archs ...string) string {
	var ps []string
	for _, a := range archs {
		ps = append(ps, platformEntry("linux", a, "nil"))
	}
	return severalLineOf("x:1", ps...)
}

func severalLineOf(image string, entries ...string) string {
	return "Error: image " + image + " has multiple platforms ([" + strings.Join(entries, ", ") + "]); pass --os/--arch or set a default\n"
}

func notSupportedLine(asked string, entries ...string) string {
	return notSupportedLineOf("x:1", asked, entries...)
}

func notSupportedLineOf(image, asked string, entries ...string) string {
	return "Error: image " + image + " does not support " + asked + "; available: [" + strings.Join(entries, ", ") + "]\n"
}

func platformEntry(os, arch, variant string) string {
	return `ContainerizationOCI.Platform(osVersion: nil, osFeatures: nil, variant: ` + variant + `, _rawOS: "` + os + `", _rawArch: "` + arch + `")`
}

// The kernel refusing to run what a service starts: `Exec format error`, from a
// program built for another architecture than the image's (docker runs it
// emulated) or from a script with no `#!` line (docker fails on it as well).
// The line is the runtime's own, as 1.5.0 printed it for a script with no `#!`
// line (testdata/error-wordings/exec-format-error-150.txt; the container's name is
// made up here); the amd64-binary-in-an-arm64-image form was reported by a probe
// of a real project with the same words and was not built here.
func TestRunErrorHintProgramWrongArch(t *testing.T) {
	const real = `Error: failed to start process a.proj.opossum in container a.proj.opossum (cause: "internalError: "failed to start process (cause: "internalError: "startProcess: failed to start process: internalError: "vmexec error: internalError: "failed to exec [sleep 120] Error Domain=NSPOSIXErrorDomain Code=8 "Exec format error""""")"")`
	for _, tc := range []struct {
		name   string
		svc    compose.Service
		stderr string
		want   bool
	}{
		{"the runtime's line", compose.Service{}, real, true},
		{"after the runtime's progress lines", compose.Service{}, "[6/6] Starting container [0s]\n" + real + "\n", true},
		{"after the foreground run's own first line", compose.Service{}, `Error: failed to exec [/t/x] Error Domain=NSPOSIXErrorDomain Code=8 "Exec format error"` + "\n" + real, true},
		{"a service that asks for arm64", compose.Service{Platform: "linux/arm64"}, real, true},
		{"a service that asks for arm64 in capitals", compose.Service{Platform: "LINUX/ARM64"}, real, true},
		{"a service that asks for arm64 alone", compose.Service{Platform: "arm64"}, real, true},
		{"a service that asks for arm64 with a variant", compose.Service{Platform: "linux/arm64/v8"}, real, true},
		{"a service that asks for aarch64", compose.Service{Platform: "linux/aarch64"}, real, true},
		{"a service that asks for arm64 with a space around it", compose.Service{Platform: " linux/arm64 "}, real, true},
		{"a service that builds", compose.Service{Build: &compose.Build{Context: "."}}, real, true},
		{"a service that builds and names its image", compose.Service{Image: "go-api:latest", Build: &compose.Build{Context: "."}}, real, true},
		// Asked for amd64 already: on Rosetta, so a different failure and no
		// advice measured for it. Asked for another platform: not what --rosetta is for.
		{"a service that already asks for amd64", compose.Service{Platform: "linux/amd64"}, real, false},
		{"a service that already asks for amd64 as x86_64", compose.Service{Platform: "linux/x86_64"}, real, false},
		{"a service that asks for amd64 with a variant", compose.Service{Platform: "linux/amd64/v2"}, real, false},
		{"a service that asks for riscv64", compose.Service{Platform: "linux/riscv64"}, real, false},
		{"a service that asks for 386", compose.Service{Platform: "linux/386"}, real, false},
		{"a service that asks for arm/v7", compose.Service{Platform: "linux/arm/v7"}, real, false},
		// Only the runtime's exec failure, in one line: the words alone, or one in
		// each of two lines of a container's output, are not it.
		{"the words without the exec failure", compose.Service{}, "sh: ./app: Exec format error", false},
		{"an exec failure for another reason", compose.Service{}, `failed to exec [sleep 120] Error Domain=NSPOSIXErrorDomain Code=2 "No such file or directory"`, false},
		{"the two words on two lines", compose.Service{}, "app: failed to exec [worker] retrying\nworker: Exec format error\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := tc.svc
			h := runErrorHint(&svc, "", false, runErr(tc.stderr))
			if got := strings.Contains(h, "["+string(codeProgramWrongArch)+"]"); got != tc.want {
				t.Fatalf("hint = %q, want the %s hint: %v", h, codeProgramWrongArch, tc.want)
			}
			if !tc.want {
				if h != "" {
					t.Errorf("nothing here is diagnosed, so nothing should be claimed, got: %q", h)
				}
				return
			}
			// Each thing the reader is told is its own check: a sentence dropped
			// leaves the rest reading as if nothing were missing.
			for _, part := range []string{"(`Exec format error`)", "another CPU architecture", "x86-64 inside an arm64 image", "which docker runs under emulation", "no `#!` line", "which docker cannot run either", "`container run --rosetta`", "no setting for that on a service yet"} {
				if !strings.Contains(h, part) {
					t.Errorf("the hint should say %q, got: %q", part, h)
				}
			}
			if tc.svc.Build != nil {
				if !strings.Contains(h, "Build the program for arm64 (the Dockerfile's target architecture") || strings.Contains(h, "native (arm64) tag") || strings.Contains(h, "does not help") {
					t.Errorf("a service that builds is told to fix the build, not to look for a tag or to ask for a platform, got: %q", h)
				}
			} else {
				for _, part := range []string{"`platform: linux/amd64` does not help", "Look for a native (arm64) tag of the image, or build the program for arm64."} {
					if !strings.Contains(h, part) {
						t.Errorf("the hint should say %q, got: %q", part, h)
					}
				}
			}
		})
	}
}
