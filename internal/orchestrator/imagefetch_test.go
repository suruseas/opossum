package orchestrator_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// When `up` fails because the registry refused the read of the image's
// manifest or of one of its layers, the way out is the image name and whether
// it can be reached — not the logs of a container that was never made. opossum
// shows the runtime's own words above this line, so what the last line has to
// do is point at the right place.
//
// The shapes are container 1.4.1's, measured by asking it directly with
// nothing logged in and kept in testdata/error-wordings: a registry that needs
// a login, an image that is not there, a tag that is not there, and a layer
// that is not there (the last against a registry on this machine over
// `--scheme http`, which opossum passes nowhere — the wording is what was
// measured, not the way opossum reaches it). Some of them answer 401 — Docker Hub says 401 for a
// repository that does not exist — so the guidance names both the name and the
// reachability, the way `pull` names them, rather than sending someone with a
// typo to log in.
func TestUpPointsAtTheImageWhenTheRegistryWouldNotHandItOver(t *testing.T) {
	for _, tc := range []struct {
		name, image, reason, url string
	}{
		{
			name:   "a registry that needs a login",
			image:  "ghcr.io/suruseas/opossum-no-such-private-image:latest",
			url:    "https://ghcr.io/v2/suruseas/opossum-no-such-private-image/manifests/latest",
			reason: "401 Unauthorized. Reason: access denied or wrong credentials, no credentials found for host ghcr.io",
		},
		{
			name:   "an image that is not there",
			image:  "docker.io/nosuchorg-inu1089/nosuchimage:latest",
			url:    "https://registry-1.docker.io/v2/nosuchorg-inu1089/nosuchimage/manifests/latest",
			reason: "401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io",
		},
		{
			name:   "a tag that is not there",
			image:  "docker.io/library/alpine:nosuchtag-inu1089",
			url:    "https://registry-1.docker.io/v2/library/alpine/manifests/nosuchtag-inu1089",
			reason: "404 Not Found. Reason: Unknown",
		},
		{
			// The image's manifest was read and then one of its layers was
			// not there — measured against a registry with the blob removed.
			name:   "a layer that is not there",
			image:  "localhost:15089/inu/blobtest:v1",
			url:    "http://localhost:15089/v2/inu/blobtest/blobs/sha256:efaa657ec46f9bdb08d66c25346f4389897ab9cd21bc2dbb559863b65cb29a6e",
			reason: `404 Not Found. Reason: {"errors":[{"code":"BLOB_UNKNOWN","detail":"sha256:efaa657ec46f9bdb08d66c25346f4389897ab9cd21bc2dbb559863b65cb29a6e","message":"blob unknown to registry"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			setShimEnv(rt, "RUN_IMAGE_FETCH_FAIL="+tc.image, "RUN_IMAGE_FETCH_REASON="+tc.reason, "RUN_IMAGE_FETCH_URL="+tc.url)
			proj, err := loadProject(t, "services:\n  web:\n    image: "+tc.image+"\n")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(proj, rt, "opossum", &out)
			err = o.Up(true)
			if err == nil {
				t.Fatal("want the up refused")
			}
			// The runtime's own words are kept — they are what says which of
			// the three shapes this is, and the reader sees them: the runtime
			// writes its stderr to the terminal and to this copy at once.
			var re *runtime.RunError
			if !errors.As(err, &re) || !strings.Contains(re.Stderr, tc.reason) {
				t.Errorf("want the runtime's answer kept, got %v", err)
			}
			// The request it names as well: the reader needs to see which
			// registry was asked for what, and it is the half this code reads.
			if !strings.Contains(re.Stderr, tc.url) {
				t.Errorf("want the request named, got %q", re.Stderr)
			}
			// Including the lines it writes before the failure, so that what
			// is read here is what the runtime really writes — the blob stage
			// says how many it is fetching, which the manifest stage does not.
			if !strings.Contains(re.Stderr, "Fetching image") {
				t.Errorf("want the runtime's progress kept, got %q", re.Stderr)
			}
			if blobs := strings.Contains(tc.url, "/blobs/"); blobs != strings.Contains(re.Stderr, "(4 blobs)") {
				t.Errorf("want the blob stage's own progress line = %v, got %q", blobs, re.Stderr)
			}
			// And the way out names the image and its reachability, as `pull`
			// does over the same failure.
			if want := "check the image name " + `"` + tc.image + `"` + " and that it's reachable (registry auth / network)"; !strings.Contains(err.Error(), want) {
				t.Errorf("want %q, got %v", want, err)
			}
			// Not the logs of a container that was never made.
			if strings.Contains(err.Error(), "opossum logs") {
				t.Errorf("want no pointer at the logs of a container that does not exist, got %v", err)
			}
			// And nothing was made: the run was asked for and failed, and no
			// container of the name is there afterwards — which is also what
			// the guidance rests on.
			if rt.Inspect("web.demo.opossum").Exists {
				t.Errorf("want no container left by the failed run")
			}
			asked := false
			for _, l := range log() {
				if strings.HasPrefix(l, "run ") && strings.Contains(l, "web.demo.opossum") {
					asked = true
				}
			}
			if !asked {
				t.Errorf("want the run asked for, got\n%s", strings.Join(log(), "\n"))
			}
		})
	}
}

// The fifth measured shape is not told apart, and this row says so. A blob
// whose download ends early says `Error: stream ended at an unexpected time`
// and names no request: nothing in it says the image was what failed. It keeps
// the answer every other start failure gets — the logs, which for this shape
// is a dead end too, because no container was made. Telling it apart would
// mean reading the progress lines, which also appear above a container that
// started after its image came down fine; that shape has not been measured, so
// this is left where it is rather than guessed at.
func TestAFetchThatEndsWithNoRequestNamedKeepsTheAnswerItHad(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, "RUN_IMAGE_FETCH_FAIL=localhost:15089/inu/blobtest:v1",
		"RUN_IMAGE_FETCH_TRUNCATED=1")
	proj, err := loadProject(t, "services:\n  web:\n    image: localhost:15089/inu/blobtest:v1\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want the up refused")
	}
	// The generic start failure, not the image guidance. Marked by the half of
	// it that does not move: where to read depends on whether a container is
	// left (#1103), and for this shape none is.
	if !strings.Contains(err.Error(), "verify the image, command, and mounts in the compose file") {
		t.Errorf("want the answer it had, got %v", err)
	}
	if strings.Contains(err.Error(), "check the image name") {
		t.Errorf("this shape is not told apart, so it must not be told as an image failure, got %v", err)
	}
}

// What is read is the runtime's own line, in its own shape. These three are
// not that line, and none of them is a measured shape — they are here to say
// that this answer is given for the shape that was measured and not for
// anything that merely carries the same words. A container that talks to a
// registry prints lines like the first two, and a `up` in the foreground hands
// this code the container's output as well as the runtime's.
func TestOnlyTheRuntimesOwnLineIsReadAsAnImageFailure(t *testing.T) {
	for _, tc := range []struct{ name, stderr string }{
		{"the words without the request", "worker: purged cache entry /v2/library/alpine/manifests/latest\n"},
		{"the word manifests, not a request", "worker: rebuilding manifests\nError: exit status 1\n"},
		{"the runtime's shape, printed by the container", "worker: Error: HTTP request to https://r/v2/x/manifests/latest failed with response: 404 Not Found\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			setShimEnv(rt, "RUN_FAIL=web.demo.opossum", "RUN_FAIL_STDERR="+tc.stderr)
			proj, err := loadProject(t, "services:\n  web:\n    image: alpine:3.20\n")
			if err != nil {
				t.Fatal(err)
			}
			// In the foreground, which is the `up` that hands this code the
			// container's own output alongside the runtime's — a detached run
			// captures only what the start said.
			err = orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(false)
			if err == nil {
				t.Fatal("want the up refused")
			}
			if !strings.Contains(err.Error(), "verify the image, command, and mounts in the compose file") {
				t.Errorf("want the answer every other start failure gets, got %v", err)
			}
			if strings.Contains(err.Error(), "check the image name") {
				t.Errorf("this line is not the runtime's own, so it must not be told as an image failure, got %v", err)
			}
		})
	}
}

// A failure that is not a run's at all — nothing was captured from the
// runtime — keeps the answer it had too.
func TestAFailureWithNoCapturedOutputKeepsTheAnswerItHad(t *testing.T) {
	proj, err := loadProject(t, "services:\n  web:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := fakeShim(t)
	o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
	if s := o.DecodeStartErrorForTest("web", errors.New("random crash")); !strings.Contains(s, "opossum logs web") {
		t.Errorf("want the answer it had, got %s", s)
	}
}

// A service that fails once its container is running is a different question,
// and the image is not the answer to it. This row is here so that the guidance
// above is not given to every failure. What it gets instead is the generic start
// failure, whose own wording depends on whether a container is left to read
// (#1103) — which is not what this row is about, so it is marked by the half
// that does not move.
func TestUpStillGivesTheGenericAnswerWhenTheContainerWasMade(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum")
	proj, err := loadProject(t, "services:\n  web:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want the up refused")
	}
	if !strings.Contains(err.Error(), "verify the image, command, and mounts in the compose file") {
		t.Errorf("want the generic start failure, got %v", err)
	}
	if strings.Contains(err.Error(), "check the image name") {
		t.Errorf("a container was made, so this is not an image failure, got %v", err)
	}
}

// The two things that have to hold before a failure is told as an image
// failure, each asked for on its own. In `up` one of them usually answers
// first — a container that printed these lines is still there, and a run that
// failed before the image came down made none — so this asks the decision
// directly, with the other side held still.
func TestWhatIsToldAsAnImageFailure(t *testing.T) {
	const runtimeLine = "[1/6] Fetching image [0s]\nError: HTTP request to https://r/v2/x/manifests/latest failed with response: 404 Not Found. Reason: Unknown\n"
	for _, tc := range []struct {
		name, stderr string
		madeIt       bool // the container is there
		unanswered   bool // the runtime would not say whether it is
		built        bool // the service is built here rather than pulled
		alsoNamed    bool // ... and names an image: as well
		want         string
	}{
		{name: "the runtime's line, no container", stderr: runtimeLine,
			want: "check the image name"},
		// The captured line from the fourth measured shape, copied whole: a
		// blob read, over http, to a registry on this machine. The scheme, the
		// host and the path are all different from the row above, and all
		// three are in the captures.
		{name: "a blob read over http, no container",
			stderr: "[1/6] Fetching image (4 blobs) [0s]\n" +
				`Error: HTTP request to http://localhost:15089/v2/inu/blobtest/blobs/sha256:efaa657ec46f9bdb08d66c25346f4389897ab9cd21bc2dbb559863b65cb29a6e failed with response: 404 Not Found. Reason: {"errors":[{"code":"BLOB_UNKNOWN","detail":"sha256:efaa657ec46f9bdb08d66c25346f4389897ab9cd21bc2dbb559863b65cb29a6e","message":"blob unknown to registry"}]}` + "\n",
			want: "check the image name"},
		// The runtime would not say whether the container is there. "Could not
		// ask" is not "gone" (#957): the guard this answer rests on is not
		// standing, so the failure keeps the guidance it had.
		{name: "the runtime's line, and no answer about the container", stderr: runtimeLine, unanswered: true,
			want: "opossum logs web"},
		// A service the compose file builds: its image is made here, so a
		// registry refusing is not about a name anyone typed.
		{name: "the runtime's line, for a service that is built here", stderr: runtimeLine, built: true,
			want: "opossum logs web"},
		{name: "the runtime's line, but the container is there", stderr: runtimeLine, madeIt: true,
			want: "opossum logs web"},
		{name: "the container's own line, no container of that name", madeIt: false,
			stderr: "worker: Error: HTTP request to https://r/v2/x/manifests/latest failed with response: 404\n",
			want:   "opossum logs web"},
		{name: "the words spread over two lines", madeIt: false,
			stderr: "Error: HTTP request to https://r/v2/x/blobs/sha256:a failed\n[1/6] failed with response: 404\n",
			want:   "opossum logs web"},
		// The request has to be the image's own: a read of its manifest or of
		// one of its blobs. Another request of the runtime's is not one of the
		// measured shapes, so it keeps the answer it had. (`/v2/…/tags/list`
		// is not a measured shape — it is here as the other side of the
		// judgement, not as a claim about what the runtime says.)
		{name: "the runtime's line, but not the image's request", madeIt: false,
			stderr: "[1/6] Fetching image [0s]\n" +
				"Error: HTTP request to https://r/v2/x/tags/list failed with response: 500 Internal Server Error. Reason: Unknown\n",
			want: "opossum logs web"},
		// And the request is read from the line that failed, not from the
		// whole of what was captured: another line mentioning a manifest — a
		// container's own log, say — is not this failure's request.
		{name: "the image's request on some other line", madeIt: false,
			stderr: "[1/6] Fetching image [0s]\n" +
				"worker: warmed /v2/library/alpine/manifests/latest\n" +
				"Error: HTTP request to https://r/v2/x/tags/list failed with response: 500 Internal Server Error. Reason: Unknown\n",
			want: "opossum logs web"},
		// Built here and named in the file as well: the run asks for the built
		// tag, so naming the file's `image:` would point at something this run
		// never asked any registry for. docker compose tags such a service by
		// its `image:` too, which is why the two names differ at all.
		{name: "the runtime's line, for a service built here and named too", stderr: runtimeLine, built: true, alsoNamed: true,
			want: "opossum logs web"},
		// A failure the coded hints name keeps its code: they say what to do
		// about this exact thing, where the image guidance says where to look.
		{name: "a coded hint beside the runtime's line", madeIt: false,
			stderr: runtimeLine + "Error: image does not support required platforms\n",
			want:   "[OPSM-412]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			switch {
			case tc.unanswered:
				setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum")
			case !tc.madeIt:
				setShimEnv(rt, "INSPECT_ABSENT=web.demo.opossum")
			}
			body := "services:\n  web:\n    image: alpine:3.20\n"
			if tc.built {
				body = "services:\n  web:\n    build:\n      context: .\n"
				if tc.alsoNamed {
					body = "services:\n  web:\n    image: registry.example/app:3\n    build:\n      context: .\n"
				}
			}
			proj, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			got := o.DecodeStartErrorForTest("web",
				orchestrator.RunErrorForTest(errors.New("exit status 1"), tc.stderr))
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got %s", tc.want, got)
			}
			if tc.want != "check the image name" && strings.Contains(got, "check the image name") {
				t.Errorf("want no image guidance here, got %s", got)
			}
			// The image guidance opens a line of its own, indented, as every
			// other way out does: it is read as the next thing to do, not as
			// part of the failure. (The other answer's words sit inside its
			// line, so only this one can be asked for by its opening.)
			if strings.HasPrefix(tc.want, "check the image name") && !strings.Contains(got, "\n  "+tc.want) {
				t.Errorf("want %q on its own indented line, got %s", tc.want, got)
			}
		})
	}
}
