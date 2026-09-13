package orchestrator

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
	"github.com/suruseas/opossum/internal/testpair"
)

// OPSM-103 names the container holding the volume and tells the person to
// stop it. When that holder is the volume's own seeding container — another
// `up` or `run` still filling it from the image — stopping it leaves the
// volume half-filled, and the next start takes it as already there. The
// advice then is to wait. Both readings sit here side by side: the holder that
// is this volume's seed, and every holder that merely looks like one.
func TestVolumeAttachAdviceTellsApartTheVolumesOwnSeed(t *testing.T) {
	const vol = "vzint_data"
	decode := func(t *testing.T, holder string) string {
		t.Helper()
		rt := lsShim(t, oneRunning(holder, vol))
		p := project103("vzint", map[string]*compose.Service{
			"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}},
		})
		o := New(p, rt, "", &bytes.Buffer{})
		err, ok := o.decodeVolumeAttachError("app", "app.vzint.opossum", &runtime.RunError{Err: errors.New("exit status 1"), Stderr: realVZError})
		if !ok || err == nil {
			t.Fatalf("the VZError should decode to OPSM-103, got ok=%v err=%v", ok, err)
		}
		return err.Error()
	}

	t.Run("the volume's own seed: wait for the fill, do not stop it", func(t *testing.T) {
		s := decode(t, runtime.SeedContainerName(vol))
		if !strings.Contains(s, "[OPSM-103]") || !strings.Contains(s, "still being filled") || !strings.Contains(s, "Wait for that fill") {
			t.Errorf("want the wait-for-the-fill advice, got: %s", s)
		}
		if strings.Contains(s, "container stop") {
			t.Errorf("stopping the seed leaves the volume half-filled; the advice must not say to, got: %s", s)
		}
		if !strings.Contains(s, `"`+runtime.SeedContainerName(vol)+`"`) {
			t.Errorf("the filler should be named, got: %s", s)
		}
	})

	// The pre-flight warning is the other exit of OPSM-103, printed before the
	// start is even tried; it forks on the same predicate, for `up` and for
	// `run`.
	for _, exit := range []string{"up", "run"} {
		t.Run("pre-flight ("+exit+"): the volume's own seed gets the wait advice", func(t *testing.T) {
			rt := lsShim(t, oneRunning(runtime.SeedContainerName(vol), vol))
			p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}}})
			var buf bytes.Buffer
			o := New(p, rt, "", &buf)
			if exit == "up" {
				o.warnBusyNamedVolumes([]string{"app"})
			} else {
				o.warnBusyVolumesFor([]string{"app"}, map[string]bool{"app-run.vzint.opossum": true})
			}
			s := buf.String()
			if !strings.Contains(s, "[OPSM-103]") || !strings.Contains(s, "still being filled") || !strings.Contains(s, "Wait for it") {
				t.Errorf("the pre-flight warning should say to wait for the fill, got: %s", s)
			}
			if strings.Contains(s, "container stop") {
				t.Errorf("the pre-flight warning must not say to stop the seed, got: %s", s)
			}
			if !strings.Contains(s, `"`+runtime.SeedContainerName(vol)+`"`) {
				t.Errorf("the pre-flight warning should name the filler, so the person knows what to watch, got: %s", s)
			}
		})
		// The seed beside another holder, in both sort orders: the other holder
		// is what to stop, and a first-only check would pass for one order.
		for _, other := range []string{"ghost", "zombie"} {
			t.Run("pre-flight ("+exit+"): the seed and another holder together keep the stop advice ("+other+")", func(t *testing.T) {
				rt := lsShim(t, `[{"status":{"state":"running"},"configuration":{"id":"`+runtime.SeedContainerName(vol)+`","mounts":[{"type":{"volume":{"name":"`+vol+`"}}}]}},`+
					`{"status":{"state":"running"},"configuration":{"id":"`+other+`","mounts":[{"type":{"volume":{"name":"`+vol+`"}}}]}}]`)
				p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}}})
				var buf bytes.Buffer
				o := New(p, rt, "", &buf)
				if exit == "up" {
					o.warnBusyNamedVolumes([]string{"app"})
				} else {
					o.warnBusyVolumesFor([]string{"app"}, map[string]bool{"app-run.vzint.opossum": true})
				}
				if s := buf.String(); !strings.Contains(s, "container stop") || strings.Contains(s, "being filled") {
					t.Errorf("with another holder present the stop advice stands, got: %s", s)
				}
			})
		}
		t.Run("pre-flight ("+exit+"): another holder keeps the stop advice", func(t *testing.T) {
			rt := lsShim(t, oneRunning("otherapp", vol))
			p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}}})
			var buf bytes.Buffer
			o := New(p, rt, "", &buf)
			if exit == "up" {
				o.warnBusyNamedVolumes([]string{"app"})
			} else {
				o.warnBusyVolumesFor([]string{"app"}, map[string]bool{"app-run.vzint.opossum": true})
			}
			if s := buf.String(); !strings.Contains(s, "container stop otherapp") || strings.Contains(s, "being filled") {
				t.Errorf("a plain holder keeps the stop advice, got: %s", s)
			}
		})
	}

	// One service, two volumes: the sentence is built per volume, and a check
	// that compared every holder with the first volume's seed, or that let a
	// fill beside another holder turn into the wait advice, would only show
	// here. (i) both being filled — both named, in order; (ii) one being
	// filled, one held by a plain container — the stop advice for that holder,
	// and the fill still named as one not to stop; (iii) one being filled, the
	// other free — the wait advice, naming only the one being filled.
	twoVols := func(t *testing.T, ls string) string {
		t.Helper()
		rt := lsShim(t, ls)
		p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib", "cache:/var/cache"}}})
		err, ok := New(p, rt, "", &bytes.Buffer{}).decodeVolumeAttachError("app", "app.vzint.opossum", &runtime.RunError{Err: errors.New("exit status 1"), Stderr: realVZError})
		if !ok || err == nil {
			t.Fatalf("the VZError should decode to OPSM-103, got ok=%v err=%v", ok, err)
		}
		return err.Error()
	}
	running := func(name, v string) string {
		return `{"status":{"state":"running"},"configuration":{"id":"` + name + `","mounts":[{"type":{"volume":{"name":"` + v + `"}}}]}}`
	}
	const cache = "vzint_cache"
	t.Run("two volumes, both being filled: both named, in order", func(t *testing.T) {
		s := twoVols(t, "["+running(runtime.SeedContainerName(vol), vol)+","+running(runtime.SeedContainerName(cache), cache)+"]")
		if !strings.Contains(s, "are still being filled") || !strings.Contains(s, "Wait for those fills") || strings.Contains(s, "container stop") {
			t.Errorf("want the wait advice in the plural, got: %s", s)
		}
		i, j := strings.Index(s, `"`+cache+`" (being filled by "`+runtime.SeedContainerName(cache)+`")`), strings.Index(s, `"`+vol+`" (being filled by "`+runtime.SeedContainerName(vol)+`")`)
		if i < 0 || j < 0 || i > j {
			t.Errorf("both fills named, cache before data, got: %s", s)
		}
	})
	t.Run("two volumes, one being filled and one held elsewhere: stop that holder, and still do not stop the fill", func(t *testing.T) {
		s := twoVols(t, "["+running(runtime.SeedContainerName(vol), vol)+","+running("ghost", cache)+"]")
		if !strings.Contains(s, `"`+cache+`" (held by running container "ghost")`) || !strings.Contains(s, "container stop") {
			t.Errorf("the plain holder gets the stop advice, got: %s", s)
		}
		if !strings.Contains(s, `"`+vol+`" (being filled by "`+runtime.SeedContainerName(vol)+`")`) || !strings.Contains(s, "wait for that one, do not stop it") {
			t.Errorf("the fill must still be named as one not to stop, got: %s", s)
		}
	})
	// Three volumes, two being filled and one held elsewhere: the note names
	// both fills, not just the first, and in the plural.
	t.Run("three volumes, two being filled and one held elsewhere: both fills named", func(t *testing.T) {
		rt := lsShim(t, "["+running(runtime.SeedContainerName(vol), vol)+","+running(runtime.SeedContainerName("vzint_logs"), "vzint_logs")+","+running("ghost", cache)+"]")
		p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib", "cache:/var/cache", "logs:/var/log"}}})
		err, ok := New(p, rt, "", &bytes.Buffer{}).decodeVolumeAttachError("app", "app.vzint.opossum", &runtime.RunError{Err: errors.New("exit status 1"), Stderr: realVZError})
		if !ok || err == nil {
			t.Fatal("expected the decoded OPSM-103")
		}
		s := err.Error()
		for _, want := range []string{`"` + vol + `" (being filled by "` + runtime.SeedContainerName(vol) + `")`, `"vzint_logs" (being filled by "` + runtime.SeedContainerName("vzint_logs") + `")`, "are still being filled", "wait for those, do not stop them", "container stop"} {
			if !strings.Contains(s, want) {
				t.Errorf("want %q in the sentence, got: %s", want, s)
			}
		}
	})

	t.Run("two volumes, one being filled and the other free: the wait advice names only that one", func(t *testing.T) {
		s := twoVols(t, "["+running(runtime.SeedContainerName(vol), vol)+"]")
		if !strings.Contains(s, "Wait for that fill") || strings.Contains(s, "container stop") || strings.Contains(s, cache) {
			t.Errorf("want the wait advice naming only the filled volume, got: %s", s)
		}
	})

	// The seed alongside some other holder: that other one is what blocks the
	// attach once the fill ends, so the advice stays the stop advice. Both
	// orders of the pair, because holders are sorted before they are read: a
	// check that looked only at the first name would pass for one order and
	// not the other ("ghost" sorts before "seed-…", "zombie" after).
	for _, other := range []string{"ghost", "zombie"} {
		t.Run("the seed and another holder together: the stop advice ("+other+")", func(t *testing.T) {
			rt := lsShim(t, `[{"status":{"state":"running"},"configuration":{"id":"`+runtime.SeedContainerName(vol)+`","mounts":[{"type":{"volume":{"name":"`+vol+`"}}}]}},`+
				`{"status":{"state":"running"},"configuration":{"id":"`+other+`","mounts":[{"type":{"volume":{"name":"`+vol+`"}}}]}}]`)
			p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}}})
			err, ok := New(p, rt, "", &bytes.Buffer{}).decodeVolumeAttachError("app", "app.vzint.opossum", &runtime.RunError{Err: errors.New("exit status 1"), Stderr: realVZError})
			if !ok || err == nil || !strings.Contains(err.Error(), "container stop") || strings.Contains(err.Error(), "being filled") {
				t.Errorf("with another holder present the stop advice stands, got: %v", err)
			}
		})
	}

	// Holders that are not this volume's seed keep the stop advice: a plain
	// container, one whose name merely starts with `seed-`, and the seed of
	// some other volume (the exact name is the match, not the prefix).
	for _, holder := range []string{"ghost", "seed-something", "myseed-" + vol + ".opossum", runtime.SeedContainerName("vzint_other")} {
		t.Run("another holder keeps the stop advice: "+holder, func(t *testing.T) {
			s := decode(t, holder)
			if !strings.Contains(s, "container stop") || !strings.Contains(s, `"`+holder+`"`) {
				t.Errorf("a holder that is not this volume's seed is stopped, and named, got: %s", s)
			}
			if strings.Contains(s, "being filled") {
				t.Errorf("nothing is being filled here, got: %s", s)
			}
		})
	}
}

// The same "seed beside another holder" case, written through testpair: the
// helper supplies the other holder's two names — one sorting before the seed's,
// one after — and runs both orders, so the case cannot be written with the
// one name that lets a first-holder-only check pass. New cases about a
// sequence are written this way; the hand-written pair above is left as it is.
func TestVolumeAttachAdviceThroughTestpair(t *testing.T) {
	const vol = "vzint_data"
	seed := runtime.SeedContainerName(vol)
	testpair.Run(t, "the seed and another holder keep the stop advice", testpair.Around(seed), func(t *testing.T, other, _ string) {
		rt := lsShim(t, `[{"status":{"state":"running"},"configuration":{"id":"`+seed+`","mounts":[{"type":{"volume":{"name":"`+vol+`"}}}]}},`+
			`{"status":{"state":"running"},"configuration":{"id":"`+other+`","mounts":[{"type":{"volume":{"name":"`+vol+`"}}}]}}]`)
		p := project103("vzint", map[string]*compose.Service{"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}}})
		var buf bytes.Buffer
		o := New(p, rt, "", &buf)
		err, ok := o.decodeVolumeAttachError("app", "app.vzint.opossum", &runtime.RunError{Err: errors.New("exit status 1"), Stderr: realVZError})
		if !ok || err == nil || !strings.Contains(err.Error(), "container stop") || strings.Contains(err.Error(), "Wait for that fill") {
			t.Errorf("decode: with %q beside the seed the stop advice stands, got: %v", other, err)
		}
		o.warnBusyNamedVolumes([]string{"app"})
		if s := buf.String(); !strings.Contains(s, "container stop") || strings.Contains(s, "being filled") {
			t.Errorf("pre-flight: with %q beside the seed the stop advice stands, got: %s", other, s)
		}
	})
}
