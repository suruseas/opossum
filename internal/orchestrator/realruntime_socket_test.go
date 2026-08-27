package orchestrator_test

// The suite in this file runs against the real `container` runtime, not the
// fake — it exists to measure the two facts this package's socket guidance
// stands on, instead of reading them off older captures:
//
//   - a bind whose source is a symlink to a socket is refused by the runtime
//     (errno 95) — the situation the guidance triggers on;
//   - a bind whose source is the socket itself carries traffic end to end —
//     the way out the guidance offers.
//
// Both were measured on `container` 1.2.2 (2026-08-27, macOS 26): the second
// carried a ping/pong round trip between a host listener and a container.
// Before that measurement the way out had only ever been verified to mount,
// and a mount that exists with nothing behind it is exactly what the guidance
// warns about elsewhere — so "mounts" and "works" had to be told apart on the
// real thing.
//
// Gating: without OPOSSUM_REAL_RUNTIME=1 in the environment, this file skips —
// the daily gate belongs to the fake. With the flag set, a missing
// precondition (no `container` binary, runtime not running, image pull
// failing) is a FAILURE, not a skip: the flag says "measure", and a run that
// comes back green having measured nothing is the one outcome this file must
// not produce.

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/suitedir"
)

// clientImage is the container side of the measurement: alpine's busybox nc
// has no unix-socket support, so the client is a python one-liner instead.
const clientImage = "python:3.12-alpine"

// realRuntime skips without the opt-in flag, and with it fails loudly on
// every missing precondition. It returns the runtime binary's path.
func realRuntime(t *testing.T) string {
	t.Helper()
	// Any non-empty value opts in. Narrowing to exactly "1" would turn a
	// mistyped value into a silent skip — the one direction this file must
	// not fail toward.
	if os.Getenv("OPOSSUM_REAL_RUNTIME") == "" {
		t.Skip("no OPOSSUM_REAL_RUNTIME: this measurement needs the real runtime; the daily gate is the fake's")
	}
	bin, err := exec.LookPath("container")
	if err != nil {
		t.Fatal("OPOSSUM_REAL_RUNTIME is set, but there is no `container` binary to measure against — " +
			"unset the flag or install the runtime; skipping here would report a measurement that never ran")
	}
	// The negative is looked for first: "not running" contains "running", and
	// a status that says so politely with exit 0 must not slip past as up.
	out, err := exec.Command(bin, "system", "status").CombinedOutput()
	if err != nil || strings.Contains(string(out), "not running") || !strings.Contains(string(out), "running") {
		t.Fatalf("OPOSSUM_REAL_RUNTIME is set, but the runtime is not running (`container system start`): %v\n%s", err, out)
	}
	return bin
}

// socketDir makes the directory the socket lives in, through suitedir so a
// run that dies here leaves a name a later run may reap. $TMPDIR reaches the
// VM: measured on 1.2.2, a socket under it carried the same round trip as one
// under home. The path stays comfortably under the unix socket length
// ceiling; t.TempDir is not used because its nested per-test names carry no
// pid, and this file's whole point is runs against a machine that can wedge.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := suitedir.Make("opossum-real-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// listen serves one connection on path: read a line, answer "pong". The
// received bytes are handed back through got.
func listen(t *testing.T, path string) (got <-chan string) {
	t.Helper()
	// A unix socket path has a hard ceiling (104 bytes here); under the
	// default $TMPDIR this fits with room, but a lengthened TMPDIR fails at
	// bind with an error that does not say why — so say why first.
	if len(path) > 100 {
		t.Fatalf("the socket path is %d bytes, over what a unix socket accepts — $TMPDIR is too long for this measurement: %s", len(path), path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	ch := make(chan string, 1)
	var mu sync.Mutex
	var accepted net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if accepted != nil {
			accepted.Close() // unsticks a Read the peer never finished
		}
	})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return // the listener was closed by cleanup: the test has its verdict already
		}
		mu.Lock()
		accepted = conn
		mu.Unlock()
		defer conn.Close()
		buf := make([]byte, 16)
		n, _ := conn.Read(buf)
		ch <- string(buf[:n])
		_, _ = conn.Write([]byte("pong"))
	}()
	return ch
}

// run gives the runtime two minutes — a cold container start fetches kernel
// and init images — and hands back combined output with the exit error.
func run(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// The timeout kills the CLI, but CombinedOutput reads until everyone
	// holding the pipes is gone, and the CLI has children of its own. The
	// delay bounds that read, so the two-minute promise below is kept as a
	// Fatal with the output in hand rather than the suite's own timeout.
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the runtime did not answer within two minutes: %s\n%s", strings.Join(args, " "), out)
	}
	return string(out), err
}

const pingClient = "import socket; s=socket.socket(socket.AF_UNIX); s.connect('/s.sock'); " +
	"s.sendall(b'ping'); print('reply:', s.recv(16), flush=True)"

// The way out the socket guidance offers — mount what the link points at —
// has to actually work, not merely mount: a byte goes in from the container
// and a byte comes back from the host.
func TestARealSocketBindCarriesTraffic(t *testing.T) {
	bin := realRuntime(t)
	dir := socketDir(t)
	sock := filepath.Join(dir, "s.sock")
	got := listen(t, sock)

	out, err := run(t, bin, "run", "--rm", "-v", sock+":/s.sock", clientImage, "python3", "-c", pingClient)
	if err != nil {
		t.Fatalf("the direct socket bind should start and connect, got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "reply: b'pong'") {
		t.Errorf("the container never heard the host's answer, output:\n%s", out)
	}
	select {
	case b := <-got:
		if b != "ping" {
			t.Errorf("the host heard %q, want %q", b, "ping")
		}
	case <-time.After(5 * time.Second):
		t.Error("the host listener was never reached — the mount exists with nothing behind it")
	}
}

// The situation the guidance triggers on: the same bind through a symlink is
// refused by the runtime itself. If this ever starts working, the pre-flight
// that refuses it first is standing in front of a door that opened.
func TestARealSymlinkToASocketIsStillRefused(t *testing.T) {
	bin := realRuntime(t)
	dir := socketDir(t)
	sock := filepath.Join(dir, "s.sock")
	_ = listen(t, sock)
	link := filepath.Join(dir, "via", "s.sock")
	if err := os.Mkdir(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, bin, "run", "--rm", "-v", link+":/s.sock", clientImage, "python3", "-c", pingClient)
	if err == nil {
		t.Fatalf("the runtime accepted a symlink-to-socket bind it has always refused — "+
			"the guidance that refuses it first now blocks a road that is open; re-measure before "+
			"trusting either. Output:\n%s", out)
	}
	if !strings.Contains(out, "errno 95") {
		t.Errorf("refused, but not with the recorded shape (errno 95) — the diagnosis the "+
			"guidance matches on may have moved. Output:\n%s", out)
	}
}
