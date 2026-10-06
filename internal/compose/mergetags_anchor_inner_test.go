package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A `!reset` or `!override` written inside an anchored mapping that is not tagged itself (`x-b: &b {ports: !reset []}`) is read where the
// anchor is used, as docker compose reads it: by a merge key to that one anchor (`<<: *b`, also through a merge of another anchor into it) or as the
// value of a key (`environment: *e`, `s: *b`), on the keys of the mapping that uses it, whether or not that mapping writes the key itself; and not
// by a merge key to a list of anchors (`<<: [*b]`), which docker compose leaves as plain (#1737). Every row is docker compose v5.5.1's `command`,
// `ports` (published:target), `labels` and `environment` of the service `s` (`config --format json`) over this base file, with the row's file merged
// after it with `-f`.
func TestATagInsideAnAnchoredMappingIsReadWhereTheAnchorIsUsed(t *testing.T) {
	const base = "services:\n  s:\n    image: x\n    command: [a, b]\n    ports: ['80:80']\n    labels: {b: '2'}\n    environment: {A: '1', B: '2'}\n"
	for _, tc := range []struct {
		name, over, want string
	}{
		{"<<: *b  ports !reset []", `x-b: &b
  ports: !reset []
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":null}"},
		{"<<: *b  labels !reset null", `x-b: &b
  labels: !reset null
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":null,\"ports\":[\"80:80\"]}"},
		{"<<: *b  labels !override {z: 9}", `x-b: &b
  labels: !override {z: '9'}
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"z\":\"9\"},\"ports\":[\"80:80\"]}"},
		{"<<: *b  command !reset null", `x-b: &b
  command: !reset null
services:
  s:
    <<: *b
`, "{\"command\":null,\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"<<: *b  command !override [z]", `x-b: &b
  command: !override [z]
services:
  s:
    <<: *b
`, "{\"command\":[\"z\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"<<: *b  ports !override [81:81]", `x-b: &b
  ports: !override ['81:81']
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"81:81\"]}"},
		{"<<: *b  environment !override {C: 3}", `x-b: &b
  environment: !override {C: '3'}
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"C\":\"3\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"<<: *b  environment {A: !reset null}", `x-b: &b
  environment: {A: !reset null}
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"<<: [*b]  ports !reset []", `x-b: &b
  ports: !reset []
services:
  s:
    <<: [*b]
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"<<: [*b]  labels !override", `x-b: &b
  labels: !override {z: '9'}
services:
  s:
    <<: [*b]
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\",\"z\":\"9\"},\"ports\":[\"80:80\"]}"},
		{"<<: [*b, *c]  ports reset, labels override", `x-b: &b
  ports: !reset []
x-c: &c
  labels: !override {z: '9'}
services:
  s:
    <<: [*b, *c]
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\",\"z\":\"9\"},\"ports\":[\"80:80\"]}"},
		{"whole service alias s: *b (ports reset)", `x-b: &b
  ports: !reset []
services:
  s: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":null}"},
		{"service own key beside <<: *b", `x-b: &b
  ports: !reset []
services:
  s:
    <<: *b
    image: y
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":null}"},
		{"own key overrides the merged key", `x-b: &b
  labels: !override {z: "9"}
services:
  s:
    <<: *b
    labels: {q: "1"}
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"q\":\"1\"},\"ports\":[\"80:80\"]}"},
		{"<<: *b  where b is itself <<: *c", `x-c: &c
  ports: !reset []
x-b: &b
  <<: *c
  image: y
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":null}"},
		{"own ports beside <<: *b ports !reset", `x-b: &b
  ports: !reset []
services:
  s:
    <<: *b
    ports: ["82:82"]
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"82:82\"]}"},
		{"own command beside <<: *b command !override", `x-b: &b
  command: !override [z]
services:
  s:
    <<: *b
    command: [q]
`, "{\"command\":[\"q\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"own command beside <<: *b command !reset", `x-b: &b
  command: !reset null
services:
  s:
    <<: *b
    command: [q]
`, "{\"command\":[\"q\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"value alias environment: *e (A reset)", `x-e: &e
  A: !reset null
services:
  s:
    environment: *e
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"value alias environment: *e (a plain anchor, no tag)", `x-e: &e
  C: "3"
services:
  s:
    environment: *e
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\",\"C\":\"3\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"value alias labels: *l (k override inside)", `x-l: &l
  z: !override "9"
services:
  s:
    labels: *l
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\",\"z\":\"9\"},\"ports\":[\"80:80\"]}"},
		{"two services use the one anchor", `x-b: &b
  ports: !reset []
services:
  s:
    <<: *b
  t:
    image: y
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":null}"},
		{"extends of a service that has <<: *b", `x-b: &b
  ports: !reset []
services:
  base:
    image: y
    <<: *b
  s:
    extends: {service: base}
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"anchor holds a list with a !reset item", `x-b: &b
  ports: [!reset "80:80", "83:83"]
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\",\"83:83\"]}"},
		{"anchor nested map key tagged and own sibling key", `x-b: &b
  environment:
    A: !reset null
    D: "4"
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"B\":\"2\",\"D\":\"4\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"<<: *b under a non-service mapping (x-ext)", `x-b: &b
  ports: !reset []
x-d:
  <<: *b
services:
  s:
    image: y
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"anchor holds a value alias to another anchor with a tag inside", `x-e: &e
  A: !reset null
x-b: &b
  environment: *e
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"B\":\"2\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"anchor holds an !override value with a tag inside it", `x-b: &b
  environment: !override
    A: !reset null
    C: "3"
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"null\",\"C\":\"3\"},\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"a merge key to a plain anchor with no tag inside", `x-b: &b
  labels: {z: "9"}
services:
  s:
    <<: *b
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"b\":\"2\",\"z\":\"9\"},\"ports\":[\"80:80\"]}"},
		{"two tags in one anchor, used as a value alias", `x-e: &e
  A: !reset null
  B: !reset null
services:
  s:
    environment: *e
`, "{\"command\":[\"a\",\"b\"],\"environment\":null,\"labels\":{\"b\":\"2\"},\"ports\":[\"80:80\"]}"},
		{"one anchor, used at two keys of the service", `x-t: &t
  A: !reset null
  b: !reset null
services:
  s:
    environment: *t
    labels: *t
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"B\":\"2\"},\"labels\":null,\"ports\":[\"80:80\"]}"},
		{"anchor defined in another service, used in s", `services:
  t:
    image: y
    labels: &l
      b: !reset null
      c: "3"
  s:
    labels: *l
`, "{\"command\":[\"a\",\"b\"],\"environment\":{\"A\":\"1\",\"B\":\"2\"},\"labels\":{\"c\":\"3\"},\"ports\":[\"80:80\"]}"},
		{"anchor defined in base used in o (not allowed)", `services:
  s:
    <<: *b
`, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, body := range []string{base, tc.over} {
				p := filepath.Join(dir, "f"+string(rune('0'+i))+".yaml")
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			project, err := LoadFiles(paths, nil)
			if tc.want == "error" {
				if err == nil {
					t.Fatal("load: docker compose refuses it, and it was read")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := project.Services["s"]
			if svc == nil {
				t.Fatal("no service s")
			}
			var cmd any
			if len(svc.Command) > 0 {
				cmd = []string(svc.Command)
			}
			var ports any
			if len(svc.Ports) > 0 {
				var ps []string
				for _, p := range svc.Ports {
					ps = append(ps, strings.SplitN(strings.SplitN(p, "/", 2)[0], ":", 3)[0]+":"+lastPart(p))
				}
				sort.Strings(ps)
				ports = ps
			}
			env, err := svc.ResolvedEnv()
			if err != nil {
				t.Fatalf("environment: %v", err)
			}
			var envAny any
			if len(env) > 0 {
				m := map[string]string{}
				for _, kv := range env {
					k, v, _ := strings.Cut(kv, "=")
					m[k] = v
				}
				envAny = m
			}
			var labels any
			if len(svc.Labels) > 0 {
				m := map[string]string{}
				for _, kv := range svc.Labels {
					k, v, _ := strings.Cut(kv, "=")
					m[k] = v
				}
				labels = m
			}
			b, _ := json.Marshal(map[string]any{"command": cmd, "environment": envAny, "labels": labels, "ports": ports})
			if string(b) != tc.want {
				t.Errorf("got  %s\nwant %s", b, tc.want)
			}
		})
	}
}

// lastPart is the container port of a `[host_ip:]host:container[/proto]` entry.
func lastPart(spec string) string {
	s := strings.SplitN(spec, "/", 2)[0]
	return s[strings.LastIndex(s, ":")+1:]
}

// Anchors that hold anchors, twice over, many times (`x-a1: &a1 {p: *a0, q: *a0}` and on to `x-a25`) hold one place per way through
// them. They are read where one is used and not before, so a file that writes the lattice and uses none of it loads at once, whatever the lattice
// holds — as it does without a tag in it — and one use that would put a tag at more places than anyone writes is refused, where it would
// have taken the time and memory of every way through.
func TestALatticeOfTaggedAnchorsIsReadOnlyWhereItIsUsedAndRefusedWhenItIsTooLarge(t *testing.T) {
	latticeOf := func(depth int, bottom string) string {
		var b strings.Builder
		b.WriteString("x-a0: &a0 " + bottom + "\n")
		for i := 1; i <= depth; i++ {
			b.WriteString("x-a" + strconv.Itoa(i) + ": &a" + strconv.Itoa(i) + " {p: *a" + strconv.Itoa(i-1) + ", q: *a" + strconv.Itoa(i-1) + "}\n")
		}
		return b.String()
	}
	lattice := func(depth int) string { return latticeOf(depth, "{k: !reset null}") }
	load := func(t *testing.T, body string) (time.Duration, error) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "f.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err := LoadFiles([]string{p}, nil)
		return time.Since(start), err
	}
	t.Run("a lattice nothing uses loads at once", func(t *testing.T) {
		took, err := load(t, lattice(30)+"services:\n  s:\n    image: x\n")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if took > 3*time.Second {
			t.Errorf("the load took %v: the lattice was read where nothing uses it", took)
		}
	})
	t.Run("a lattice that is used and too large is refused, and quickly", func(t *testing.T) {
		took, err := load(t, lattice(30)+"services:\n  s:\n    image: x\n    <<: *a30\n")
		if err == nil || !strings.Contains(err.Error(), "too many times") {
			t.Fatalf("want it refused for the places it holds, got %v", err)
		}
		if took > 3*time.Second {
			t.Errorf("the refusal took %v", took)
		}
	})
	t.Run("a lattice with no tag in it that is used is not refused for its size", func(t *testing.T) {
		took, err := load(t, latticeOf(30, "{k: plain}")+"services:\n  s:\n    image: x\n    <<: *a30\n  t:\n    image: y\n    command: !reset null\n")
		if err != nil && strings.Contains(err.Error(), "too many times") {
			t.Fatalf("a lattice with no tag in it refused for its size: %v", err)
		}
		if took > 3*time.Second {
			t.Errorf("the load took %v", took)
		}
	})
	t.Run("a lattice of some two thousand places that is used is not refused for its size", func(t *testing.T) {
		// Its keys (p, q) are no keys of a service, so the file is refused for that, and not for how many places it holds.
		if _, err := load(t, lattice(10)+"services:\n  s:\n    image: x\n    <<: *a10\n"); err != nil && strings.Contains(err.Error(), "too many times") {
			t.Fatalf("a lattice of depth 10 refused for its size: %v", err)
		}
	})
}

// The same tags are read on a network (docker compose v5.5.1 `config`: `<<: *nb` over `labels: !reset null` leaves no labels, over `labels: !override {z: "9"}`
// only `z`), and an anchor's tags cost nothing where they are not read: a lattice written under a service's own `x-` key, one with no tag
// next to one that has, a lattice used many times (the places looked at are counted over the whole file, not for each use), and an anchor that holds
// itself (which is refused, and does not run out of stack).
func TestATagInsideAnAnchorOnANetworkAndWhatItCostsWhereItIsNotRead(t *testing.T) {
	load := func(t *testing.T, bodies ...string) (*Project, time.Duration, error) {
		t.Helper()
		dir := t.TempDir()
		var paths []string
		for i, b := range bodies {
			p := filepath.Join(dir, "f"+strconv.Itoa(i)+".yaml")
			if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
		}
		start := time.Now()
		project, err := LoadFiles(paths, nil)
		return project, time.Since(start), err
	}
	const base = "services:\n  s:\n    image: x\n    networks: [n]\nnetworks:\n  n:\n    labels: {a: '1', b: '2'}\n"
	t.Run("a merge key over a network's labels, reset", func(t *testing.T) {
		p, _, err := load(t, base, "x-nb: &nb\n  labels: !reset null\nnetworks:\n  n:\n    <<: *nb\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Networks["n"].Labels; len(got) != 0 {
			t.Errorf("the network kept its labels %v, docker compose leaves none", got)
		}
	})
	t.Run("a merge key over a network's labels, override", func(t *testing.T) {
		p, _, err := load(t, base, "x-nb: &nb\n  labels: !override {z: '9'}\nnetworks:\n  n:\n    <<: *nb\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Networks["n"].Labels; len(got) != 1 || got[0] != "z=9" {
			t.Errorf("the network's labels are %v, docker compose has only z=9", got)
		}
	})
	lattice := func(depth int, bottom string) string {
		out := "x-a0: &a0 " + bottom + "\n"
		for i := 1; i <= depth; i++ {
			out += "x-a" + strconv.Itoa(i) + ": &a" + strconv.Itoa(i) + " {p: *a" + strconv.Itoa(i-1) + ", q: *a" + strconv.Itoa(i-1) + "}\n"
		}
		return out
	}
	t.Run("a lattice written under a service's own x- key and used nowhere", func(t *testing.T) {
		var defs strings.Builder
		defs.WriteString("      a0: &a0 {k: !reset null}\n")
		for i := 1; i <= 20; i++ {
			defs.WriteString("      a" + strconv.Itoa(i) + ": &a" + strconv.Itoa(i) + " {p: *a" + strconv.Itoa(i-1) + ", q: *a" + strconv.Itoa(i-1) + "}\n")
		}
		_, took, err := load(t, "services:\n  s:\n    image: x\n    x-defs:\n"+defs.String())
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if took > 3*time.Second {
			t.Errorf("the load took %v", took)
		}
	})
	t.Run("a lattice with no tag next to an anchor that has one", func(t *testing.T) {
		body := lattice(30, "{k: plain}") + "x-u: &u {ports: !reset [], big: *a30}\nservices:\n  s:\n    image: x\n    <<: *u\n"
		_, took, err := load(t, body)
		if err != nil && strings.Contains(err.Error(), "too many times") {
			t.Fatalf("refused for the size of a lattice with no tag in it: %v", err)
		}
		if took > 3*time.Second {
			t.Errorf("the load took %v", took)
		}
	})
	t.Run("a lattice that is within the limit, used many times, is refused in the whole", func(t *testing.T) {
		var uses strings.Builder
		for i := 0; i < 400; i++ {
			uses.WriteString("  s" + strconv.Itoa(i) + ":\n    image: x\n    <<: *a12\n")
		}
		body := "x-a0: &a0 {k: !reset null}\n"
		for i := 1; i <= 12; i++ {
			body += "x-a" + strconv.Itoa(i) + ": &a" + strconv.Itoa(i) + " {p: *a" + strconv.Itoa(i-1) + ", q: *a" + strconv.Itoa(i-1) + "}\n"
		}
		_, took, err := load(t, body+"services:\n"+uses.String())
		if err == nil || !strings.Contains(err.Error(), "too many times") {
			t.Fatalf("want it refused for the places looked at in all, got %v", err)
		}
		if took > 3*time.Second {
			t.Errorf("the refusal took %v", took)
		}
	})
	t.Run("an anchor that holds itself is refused", func(t *testing.T) {
		_, took, err := load(t, "x-a: &a {p: *a, k: !reset null}\nservices:\n  s:\n    image: x\n    <<: *a\n")
		if err == nil || !strings.Contains(err.Error(), "cycle detected") {
			t.Fatalf("want an anchor that holds itself refused as a cycle, got %v", err)
		}
		if took > time.Second {
			t.Errorf("the refusal took %v", took)
		}
	})
}
