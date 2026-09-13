package orchestrator_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// Before its start loop, `up` creates the network, builds images, and fills
// new volumes from the image. A Ctrl-C landing in any of those came back as
// that step's own failure, with advice that does not apply (an unhealthy
// runtime, a Dockerfile the builder cannot handle, an image without `sh`).
// Each step here has its interrupted case and, beside it, its real failure —
// a verdict that sent every failure to "interrupted" would pass the first
// alone. The seeding step also has to take back what the kill left behind.
func TestUpInterruptedBeforeTheStartLoopSaysSoAndTakesBackTheSeed(t *testing.T) {
	const interruptedVerdict = "interrupted — rolling back"

	// up runs the project against the shim and, when `cancelWhen` is set,
	// cancels once that line has appeared in the shim log.
	up := func(t *testing.T, p *compose.Project, env []string, build bool, cancelWhen func([]string) bool) (error, string, []string) {
		t.Helper()
		rt, log := fakeShim(t)
		setShimEnv(rt, env...)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out bytes.Buffer
		o := orchestrator.New(p, rt, "opossum", &out)
		o.SetUpOptions(false, build, false, false, false)
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.Up(true) }()
		if cancelWhen != nil {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && !cancelWhen(log()) {
				time.Sleep(2 * time.Millisecond)
			}
			if !cancelWhen(log()) {
				t.Fatalf("the step this case interrupts never started, got %v", log())
			}
			cancel()
		}
		select {
		case err := <-done:
			return err, out.String(), log()
		case <-time.After(8 * time.Second):
			t.Fatal("up did not return")
			return nil, "", nil
		}
	}
	has := func(sub string) func([]string) bool { return func(l []string) bool { return indexOf(l, sub) >= 0 } }
	web := func() *compose.Project {
		return project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	}

	t.Run("network: Ctrl-C while the network is being created", func(t *testing.T) {
		err, _, _ := up(t, web(), []string{"NET_CREATE_HANG=1"}, false, has("network create demo-net"))
		if err == nil || err.Error() != interruptedVerdict {
			t.Errorf("want %q, got: %v", interruptedVerdict, err)
		}
		if err != nil && strings.Contains(err.Error(), "opossum doctor") {
			t.Errorf("the runtime is not unhealthy, got: %v", err)
		}
	})
	t.Run("network: a create that fails on its own keeps its wording", func(t *testing.T) {
		err, _, _ := up(t, web(), []string{"NET_CREATE_FAIL=1"}, false, nil)
		if err == nil || !strings.Contains(err.Error(), "couldn't create network") || !strings.Contains(err.Error(), "opossum doctor") {
			t.Errorf("a failed create is the runtime's failure, with its advice, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "interrupted") {
			t.Errorf("nothing was interrupted, got: %v", err)
		}
	})

	buildable := func() *compose.Project {
		return project("demo", map[string]*compose.Service{"web": {Build: &compose.Build{Context: "."}}})
	}
	t.Run("build: Ctrl-C while the image is being built", func(t *testing.T) {
		err, _, _ := up(t, buildable(), []string{"BUILD_HANG=1"}, true, has("build "))
		if err == nil || err.Error() != interruptedVerdict {
			t.Errorf("want %q, got: %v", interruptedVerdict, err)
		}
		if err != nil && strings.Contains(err.Error(), "opossum import") {
			t.Errorf("a Ctrl-C is not a Dockerfile the builder cannot handle, got: %v", err)
		}
	})
	t.Run("build: a build that fails on its own keeps its wording", func(t *testing.T) {
		err, _, _ := up(t, buildable(), []string{"BUILD_FAIL=1"}, true, nil)
		if err == nil || !strings.Contains(err.Error(), `building service "web"`) || !strings.Contains(err.Error(), "opossum import") {
			t.Errorf("a failed build keeps the import fallback, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "interrupted") {
			t.Errorf("nothing was interrupted, got: %v", err)
		}
	})

	// `opossum run` builds its own service's image on the same path, before
	// anything is started; the same Ctrl-C there has the same wrong advice.
	t.Run("build: Ctrl-C while `run` builds its service's image", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "BUILD_HANG=1")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := orchestrator.New(buildable(), rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true}) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && indexOf(log(), "build ") < 0 {
			time.Sleep(2 * time.Millisecond)
		}
		if indexOf(log(), "build ") < 0 {
			t.Fatalf("the build never started, got %v", log())
		}
		cancel()
		select {
		case err := <-done:
			if err == nil || err.Error() != "interrupted — the build of web was abandoned; the one-off did not start" {
				t.Errorf("want the interruption, worded for a one-off that never started, got: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("run did not return")
		}
	})
	t.Run("build: a `run` build that fails on its own keeps its wording", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "BUILD_FAIL=1")
		err := orchestrator.New(buildable(), rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
		if err == nil || !strings.Contains(err.Error(), "opossum import") || strings.Contains(err.Error(), "interrupted") {
			t.Errorf("a failed build keeps the import fallback, got: %v", err)
		}
	})

	seeded := func() *compose.Project {
		return project("demo", map[string]*compose.Service{"web": {Image: "node:20-alpine", Volumes: []string{"data:/var/data"}}})
	}
	seedName := runtime.SeedContainerName("demo_data")
	t.Run("seed: Ctrl-C while a new volume is being filled says interrupted, not the seeding advice", func(t *testing.T) {
		err, out, _ := up(t, seeded(), []string{"RUN_HANG=" + seedName}, false, has("--name "+seedName+" "))
		if err == nil || err.Error() != interruptedVerdict {
			t.Errorf("want %q, got: %v", interruptedVerdict, err)
		}
		if strings.Contains(out, "OPSM-108") || strings.Contains(out, "image without one") {
			t.Errorf("a Ctrl-C is not an image without sh; no seeding advice, got:\n%s", out)
		}
	})
	// The teardown is its own guard: the sentence could be right and the
	// throwaway still running with the half-filled volume attached.
	t.Run("seed: the interrupted fill is taken back — container stopped and removed, volume deleted", func(t *testing.T) {
		_, _, lines := up(t, seeded(), []string{"RUN_HANG=" + seedName}, false, has("--name "+seedName+" "))
		i := indexOf(lines, "--name "+seedName+" ")
		after := lines[i:]
		stop, del, vol := indexOf(after, "stop "+seedName), indexOf(after, "delete --force "+seedName), indexOf(after, "volume delete demo_data")
		if stop < 0 || del < 0 || vol < 0 {
			t.Fatalf("the interrupted seed must be taken back (stop, delete, volume delete), got %v", after)
		}
		// In that order: the runtime refuses to delete a volume a container still
		// holds, so the container goes first, stopped and then removed.
		if !(stop < del && del < vol) {
			t.Errorf("take-back order must be stop < delete < volume delete, got stop=%d delete=%d volume=%d in %v", stop, del, vol, after)
		}
	})

	// A seed that dies of a signal with nothing cancelled — killed from outside,
	// not by Ctrl-C — is an ordinary failed fill: the warning, no take-back.
	t.Run("seed: a fill killed by some other signal is a failed fill, not an interruption", func(t *testing.T) {
		err, out, lines := up(t, seeded(), []string{"RUN_DIE_SIGNAL=" + seedName}, false, nil)
		if err != nil {
			t.Fatalf("a failed fill is a warning, not a failed up: %v", err)
		}
		if !strings.Contains(out, "OPSM-108") {
			t.Errorf("a fill that died on its own gets the seeding warning, got:\n%s", out)
		}
		if indexOf(lines, "stop "+seedName) >= 0 || indexOf(lines, "volume delete demo_data") >= 0 {
			t.Errorf("nothing was interrupted, so nothing is taken back, got %v", lines)
		}
	})

	// The nocopy branch prepares the volume with the same throwaway (clearing
	// lost+found) and has the same interruption to take back.
	t.Run("seed: Ctrl-C while a nocopy volume is being prepared is taken back too", func(t *testing.T) {
		nocopy := func() *compose.Project {
			return project("demo", map[string]*compose.Service{"web": {Image: "node:20-alpine",
				Volumes: []string{"data:/var/data"}, NoCopy: []string{"/var/data"}}})
		}
		err, out, lines := up(t, nocopy(), []string{"RUN_HANG=" + seedName}, false, has("--name "+seedName+" "))
		if err == nil || err.Error() != interruptedVerdict {
			t.Errorf("want %q, got: %v", interruptedVerdict, err)
		}
		if strings.Contains(out, "OPSM-108") {
			t.Errorf("no seeding advice for a Ctrl-C, got:\n%s", out)
		}
		after := lines[indexOf(lines, "--name "+seedName+" "):]
		if indexOf(after, "stop "+seedName) < 0 || indexOf(after, "volume delete demo_data") < 0 {
			t.Errorf("the interrupted prepare must be taken back, got %v", after)
		}
	})

	// `opossum run` fills a new volume for its service on the same path; there
	// the interruption is worded for a one-off that never started, and the
	// take-back is the same.
	t.Run("seed: Ctrl-C while `run` fills its service's volume", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "RUN_HANG="+seedName)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := orchestrator.New(seeded(), rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true}) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && indexOf(log(), "--name "+seedName+" ") < 0 {
			time.Sleep(2 * time.Millisecond)
		}
		if indexOf(log(), "--name "+seedName+" ") < 0 {
			t.Fatalf("the fill never started, got %v", log())
		}
		cancel()
		select {
		case err := <-done:
			if err == nil || err.Error() != "interrupted — the fill of a new volume for web was taken back; the one-off did not start" {
				t.Errorf("want run's own wording, got: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("run did not return")
		}
		after := log()[indexOf(log(), "--name "+seedName+" "):]
		if indexOf(after, "stop "+seedName) < 0 || indexOf(after, "volume delete demo_data") < 0 {
			t.Errorf("run's interrupted fill is taken back too, got %v", after)
		}
	})

	// And `run` creates the project network before any of that.
	t.Run("network: Ctrl-C while `run` creates the network", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "NET_CREATE_HANG=1")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := orchestrator.New(web(), rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true}) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && indexOf(log(), "network create demo-net") < 0 {
			time.Sleep(2 * time.Millisecond)
		}
		if indexOf(log(), "network create demo-net") < 0 {
			t.Fatalf("the network create never started, got %v", log())
		}
		cancel()
		select {
		case err := <-done:
			if err == nil || err.Error() != "interrupted — the network for web was not created; the one-off did not start" {
				t.Errorf("want run's own wording, got: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("run did not return")
		}
	})
	t.Run("network: a `run` create that fails on its own keeps its wording", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "NET_CREATE_FAIL=1")
		err := orchestrator.New(web(), rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
		if err == nil || !strings.Contains(err.Error(), "couldn't create network") || strings.Contains(err.Error(), "interrupted") {
			t.Errorf("a failed create keeps the runtime's wording, got: %v", err)
		}
	})
	t.Run("seed: a fill that fails on its own keeps the seeding advice, and the volume", func(t *testing.T) {
		err, out, lines := up(t, seeded(), []string{"SEED_FAIL=1"}, false, nil)
		if err != nil {
			t.Fatalf("a failed fill is a warning, not a failed up: %v", err)
		}
		if !strings.Contains(out, "OPSM-108") {
			t.Errorf("the seeding advice belongs to a fill that failed on its own, got:\n%s", out)
		}
		if indexOf(lines, "stop "+seedName) >= 0 || indexOf(lines, "volume delete demo_data") >= 0 {
			t.Errorf("a fill that failed on its own is not taken back, got %v", lines)
		}
	})
}
