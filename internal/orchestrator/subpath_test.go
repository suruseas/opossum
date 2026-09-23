package orchestrator_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// A named-volume mount of a part of the volume is refused before anything is
// created, by `up`, `up --dry-run`, `run` and `run --audit` alike: container
// 1.4.1 cannot mount a part of a volume, and the whole volume in its place
// would hand the service other files than docker compose gives it (measured
// 2026-09-20: docker compose's `sub` shows only `f`, the whole volume shows
// `sub top`). The mounts docker compose reads the same way as the whole —
// an empty subpath, a bind's or a tmpfs's `volume.subpath` — go ahead.
func TestAPartOfAVolumeIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	const refusal = `service "app" mounts a part of a volume (data:/data (subpath sub)); container 1.4.1 cannot mount a part of a volume, only the whole of it`
	for _, tc := range []struct {
		name, mount string
		refused     bool
	}{
		{"a named volume's subpath", "{type: volume, source: data, target: /data, volume: {subpath: sub}}", true},
		{"an empty subpath", "{type: volume, source: data, target: /data, volume: {subpath: \"\"}}", false},
		{"a bind's volume.subpath", "{type: bind, source: ./shared, target: /data, volume: {subpath: sub}}", false},
		{"a tmpfs's volume.subpath", "{type: tmpfs, target: /data, volume: {subpath: sub}}", false},
		{"no subpath", "{type: volume, source: data, target: /data}", false},
		{"a subpath of .", "{type: volume, source: data, target: /data, volume: {subpath: .}}", false},
		{"a subpath that comes back up", "{type: volume, source: data, target: /data, volume: {subpath: sub/..}}", false},
		{"a subpath then the whole at its target", "{type: volume, source: data, target: /data, volume: {subpath: sub}}\n      - data:/data", false},
	} {
		for _, cmd := range []string{"up", "up --dry-run", "run", "run --audit"} {
			t.Run(cmd+", "+tc.name, func(t *testing.T) {
				rt, log := fakeShim(t)
				// Beside a service that sorts first and mounts nothing of the
				// kind, so the service named is the one at fault.
				p, err := loadProject(t, "services:\n  aaa:\n    image: alpine:3.20\n  app:\n    image: alpine:3.20\n    volumes:\n      - "+tc.mount+"\nvolumes:\n  data: {}\n")
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				o := orchestrator.New(p, rt, "opossum", &out)
				switch cmd {
				case "up":
					err = o.Up(true)
				case "up --dry-run":
					o.SetDryRun(true)
					err = o.Up(true)
				case "run":
					err = o.RunOneOff("app", []string{"true"}, orchestrator.RunOneOffOptions{})
				default:
					_, err = o.RunAudited("app", []string{"true"}, orchestrator.RunOneOffOptions{})
				}
				if !tc.refused {
					if err != nil {
						t.Fatalf("want it to go ahead, got %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), refusal) {
					t.Fatalf("want the refusal %q, got %v", refusal, err)
				}
				for _, made := range []string{"run ", "network create", "volume create"} {
					if i := indexOf(log(), made); i >= 0 {
						t.Errorf("refused, yet %q was done:\n%s", log()[i], strings.Join(log(), "\n"))
					}
				}
			})
		}
	}
}

// Only the services a command starts are looked at: `up aaa` goes ahead
// beside a service whose mount would be refused, and `run --no-deps` of a
// service that depends on it does not read it either.
// Two of them are both named, in the order written.
func TestAPartOfAVolumeNamesEveryMount(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    volumes:\n      - {type: volume, source: data, target: /a, volume: {subpath: x}}\n      - {type: volume, source: data, target: /b, volume: {subpath: y}}\nvolumes:\n  data: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), "(data:/a (subpath x), data:/b (subpath y))") {
		t.Fatalf("want both mounts named, got %v", err)
	}
}

func TestAPartOfAVolumeIsCheckedOnTheServicesACommandStarts(t *testing.T) {
	body := "services:\n  aaa:\n    image: alpine:3.20\n  app:\n    image: alpine:3.20\n    volumes:\n      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n  needsapp:\n    image: alpine:3.20\n    depends_on: [app]\nvolumes:\n  data: {}\n"
	t.Run("up of a sibling", func(t *testing.T) {
		rt, _ := fakeShim(t)
		p, err := loadProject(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true, "aaa"); err != nil {
			t.Fatalf("up aaa: %v", err)
		}
	})
	t.Run("run reads its dependency's", func(t *testing.T) {
		rt, _ := fakeShim(t)
		p, err := loadProject(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("needsapp", []string{"true"}, orchestrator.RunOneOffOptions{}); err == nil || !strings.Contains(err.Error(), `service "app" mounts a part of a volume`) {
			t.Fatalf("want the dependency refused, got %v", err)
		}
	})
	t.Run("run --no-deps does not", func(t *testing.T) {
		rt, _ := fakeShim(t)
		p, err := loadProject(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("needsapp", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
			t.Fatalf("run --no-deps: %v", err)
		}
	})
	t.Run("run --audit --no-deps does not", func(t *testing.T) {
		rt, _ := fakeShim(t)
		p, err := loadProject(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunAudited("needsapp", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
			t.Fatalf("run --audit --no-deps: %v", err)
		}
	})
}

// A mount of a part borrowed with volumes_from is the borrower's own to
// refuse: `run --no-deps` of the borrower, which starts nothing else, is
// refused too, where it used to mount the whole volume.
func TestAPartOfAVolumeBorrowedWithVolumesFromIsRefused(t *testing.T) {
	body := "services:\n  holder:\n    image: alpine:3.20\n    volumes:\n      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n  app:\n    image: alpine:3.20\n    volumes_from: [holder]\nvolumes:\n  data: {}\n"
	for _, cmd := range []string{"run --no-deps", "run --audit --no-deps"} {
		t.Run(cmd, func(t *testing.T) {
			rt, log := fakeShim(t)
			p, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if cmd == "run --no-deps" {
				err = o.RunOneOff("app", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
			} else {
				_, err = o.RunAudited("app", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
			}
			if err == nil || !strings.Contains(err.Error(), `service "app" mounts a part of a volume (data:/data (subpath sub))`) {
				t.Fatalf("want the borrower refused, got %v", err)
			}
			if i := indexOf(log(), "run "); i >= 0 {
				t.Errorf("refused, yet ran: %s", log()[i])
			}
		})
	}
}

// Refused where the service starts, and not called ignored first: the
// ignored fields `up` names — the one-line note, and each service's line under
// verbose — leave out every entry that asks for a part of a volume, which `up`
// refuses (the refusal names the first service it finds), and no other entry.
// The entries are numbered in the fixture, not read back from what the loader
// recorded.
func TestARefusedPartOfAVolumeIsNotCalledIgnoredFirst(t *testing.T) {
	const named = "{type: volume, source: data, target: %s, volume: {subpath: sub}}"
	const bind = "{type: bind, source: ./shared, target: %s, volume: {subpath: sub}}"
	for _, tc := range []struct {
		name string
		vols []string // app's own volumes
		from bool     // app borrows holder's (holder's entry 1 is a refused named subpath)
		want string   // app's verbose line; "" when none
	}{
		{"the refused one first", []string{fmt.Sprintf(named, "/a"), fmt.Sprintf(bind, "/b")}, false, "volumes entry 2.volume.subpath"},
		{"the refused one after a bind's", []string{fmt.Sprintf(bind, "/b"), fmt.Sprintf(named, "/a")}, false, "volumes entry 1.volume.subpath"},
		{"the refused one third", []string{fmt.Sprintf(bind, "/b"), "{type: volume, source: data, target: /e, volume: {subpath: \"\"}}", fmt.Sprintf(named, "/a")}, false, "volumes entry 1.volume.subpath, volumes entry 2.volume.subpath"},
		{"the refused one over a whole mount at its target", []string{"data:/a", fmt.Sprintf(named, "/a"), fmt.Sprintf(bind, "/b")}, false, "volumes entry 3.volume.subpath"},
		{"a whole mount over it is not refused, so named", []string{fmt.Sprintf(named, "/a"), "data:/a", fmt.Sprintf(bind, "/b"), fmt.Sprintf(named, "/c")}, false, "volumes entry 1.volume.subpath, volumes entry 3.volume.subpath"},
		{"two refused", []string{fmt.Sprintf(named, "/a"), fmt.Sprintf(bind, "/b"), fmt.Sprintf(named, "/c")}, false, "volumes entry 2.volume.subpath"},
		{"only refused ones", []string{fmt.Sprintf(named, "/a")}, false, ""},
		{"an anonymous volume's, refused too", []string{fmt.Sprintf(bind, "/b"), "{type: volume, target: /a, volume: {subpath: sub}}"}, false, "volumes entry 1.volume.subpath"},
		// The borrowed mount is the holder's entry 1; app's own entry 1 is a
		// bind's, which stays named.
		{"borrowed beside its own bind's", []string{fmt.Sprintf(bind, "/b")}, true, "volumes entry 1.volume.subpath"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  holder:\n    image: alpine:3.20\n    volumes:\n      - " + fmt.Sprintf(named, "/h") + "\n      - " + fmt.Sprintf(bind, "/hb") + "\n  app:\n    image: alpine:3.20\n"
			if tc.from {
				body += "    volumes_from: [holder]\n"
			}
			body += "    volumes:\n"
			for _, v := range tc.vols {
				body += "      - " + v + "\n"
			}
			body += "volumes:\n  data: {}\n"
			p, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			rt, _ := fakeShim(t)
			rt.Verbose = true
			var out bytes.Buffer
			err = orchestrator.New(p, rt, "opossum", &out).Up(true, "app")
			if err == nil || !strings.Contains(err.Error(), "mounts a part of a volume") {
				t.Fatalf("want the refusal, got %v", err)
			}
			line := ""
			for _, l := range strings.Split(out.String(), "\n") {
				if _, rest, ok := strings.Cut(l, `service "app": ignoring unsupported field(s): `); ok {
					line = rest
				}
			}
			if line != tc.want {
				t.Errorf("app's ignored fields = %q, want %q\n%s", line, tc.want, out.String())
			}
		})
	}
	// The one-line note, which names one of them: not the refused one.
	t.Run("the note", func(t *testing.T) {
		rt, _ := fakeShim(t)
		p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    volumes:\n      - "+fmt.Sprintf(named, "/a")+"\n      - "+fmt.Sprintf(bind, "/b")+"\nvolumes:\n  data: {}\n")
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err == nil {
			t.Fatal("want the refusal")
		}
		if want := "note: 1 compose field is ignored (app: volumes entry 2.volume.subpath)"; !strings.Contains(out.String(), want) {
			t.Errorf("want %q in:\n%s", want, out.String())
		}
	})
}
