package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Mounts at one target are read the way docker compose v5.5.0 reads them (each
// row measured with its `config` and `run`): among `volumes:` the later entry
// is kept, whatever the types. A service-level `tmpfs:` entry and another
// mount at its target are both passed on as written, where docker compose
// refuses the pair — a known difference.
func TestMountsAtOneTarget(t *testing.T) {
	for _, tc := range []struct {
		name, svc string
		tmpfs     []string
		volumes   int    // how many volumes are left
		volumeHas string // a volume left mounts this
	}{
		{"two long-form tmpfs, the later kept", "    volumes:\n      - {type: tmpfs, target: /t, tmpfs: {size: 1m}}\n      - {type: tmpfs, target: /t, read_only: true}\n", []string{"/t:ro"}, 0, ""},
		{"two long-form tmpfs the other way", "    volumes:\n      - {type: tmpfs, target: /t, read_only: true}\n      - {type: tmpfs, target: /t, tmpfs: {size: 1m}}\n", []string{"/t:size=1048576"}, 0, ""},
		{"a long-form tmpfs, then a bind", "    volumes:\n      - {type: tmpfs, target: /t}\n      - ./a:/t\n", nil, 1, ":/t"},
		{"a bind, then a long-form tmpfs", "    volumes:\n      - ./a:/t\n      - {type: tmpfs, target: /t}\n", []string{"/t"}, 0, ""},
		{"two binds, one with a trailing slash", "    volumes:\n      - ./a:/t\n      - ./b:/t/\n", nil, 1, "b:/t/"},
		// Passed on as written: docker compose refuses these (`target … already mounted`).
		{"two service-level tmpfs", "    tmpfs:\n      - /t:size=1m\n      - /t:ro\n", []string{"/t:size=1m", "/t:ro"}, 0, ""},
		{"a service-level tmpfs and a long-form one", "    tmpfs:\n      - /t:size=1m\n    volumes:\n      - {type: tmpfs, target: /t, read_only: true}\n", []string{"/t:size=1m", "/t:ro"}, 0, ""},
		{"a service-level tmpfs and a bind", "    tmpfs:\n      - /t\n    volumes:\n      - ./a:/t\n", []string{"/t"}, 1, "a:/t"},
		{"a service-level tmpfs and a volume", "    tmpfs:\n      - /t\n    volumes:\n      - data:/t\n", []string{"/t"}, 1, "data:/t"},
		{"a service-level tmpfs and a bind with a trailing slash", "    tmpfs:\n      - /t\n    volumes:\n      - ./a:/t/\n", []string{"/t"}, 1, "a:/t/"},
		{"a service-level tmpfs and an anonymous nocopy volume", "    tmpfs:\n      - /t\n    volumes:\n      - {type: volume, target: /t, volume: {nocopy: true}}\n", []string{"/t"}, 1, "/t"},
		// A nocopy volume with no source is at its target like any other.
		{"an anonymous nocopy volume, then a bind", "    volumes:\n      - {type: volume, target: /t, volume: {nocopy: true}}\n      - ./a:/t\n", nil, 1, "a:/t"},
		// The tmpfs leaves Volumes before the collapse after load looks, so only
		// the collapse here can keep it over the nocopy volume.
		{"an anonymous nocopy volume, then a long-form tmpfs", "    volumes:\n      - {type: volume, target: /t, volume: {nocopy: true}}\n      - {type: tmpfs, target: /t}\n", []string{"/t"}, 0, ""},
		// A long-form target with a trailing `/`: docker compose refuses this pair
		// (`target /t already mounted`), where opossum, which reads the long form
		// into the short form's spelling, keeps the later — a known difference.
		{"a long-form tmpfs at /t/, then a bind at /t", "    volumes:\n      - {type: tmpfs, target: /t/}\n      - ./a:/t\n", nil, 1, "a:/t"},
		// Compared as written among `tmpfs:`: docker compose mounts both.
		{"service-level tmpfs /t and /t/", "    tmpfs:\n      - /t\n      - /t/:ro\n", []string{"/t", "/t/:ro"}, 0, ""},
		// The root is a target like any other (docker compose keeps the later).
		{"two binds at /", "    volumes:\n      - ./a:/\n      - ./b:/\n", nil, 1, "b:/"},
		{"an anonymous volume at /, then a bind", "    volumes:\n      - /\n      - ./a:/\n", nil, 1, "a:/"},
		{"a long-form tmpfs at /, then a bind", "    volumes:\n      - {type: tmpfs, target: /}\n      - ./a:/\n", nil, 1, "a:/"},
		// A `tmpfs:` entry and a long-form tmpfs at one target are both passed
		// on: the `tmpfs:` list is collapsed before the long form joins it
		// (docker compose refuses the pair).
		{"a service-level tmpfs and a long-form one at the same spelling", "    tmpfs:\n      - /t\n    volumes:\n      - {type: tmpfs, target: /t}\n", []string{"/t", "/t"}, 0, ""},
		// An entry a later one replaces is not looked at afterwards: an undefined
		// volume there does not refuse the load (docker compose loads it too).
		{"an undefined volume, then a long-form tmpfs", "    volumes:\n      - nope:/t\n      - {type: tmpfs, target: /t}\n", []string{"/t"}, 0, ""},
		// An entry with no target written is not at any target: two of them are
		// both kept, as is one beside an entry at `/` (docker compose refuses
		// such an entry outright — `empty section between colons`).
		{"two entries with no target", "    volumes:\n      - \"./a:\"\n      - \"./b:\"\n", nil, 2, "b:"},
		{"an entry with no target, then a bind at /", "    volumes:\n      - \"./a:\"\n      - ./b:/\n", nil, 2, "b:/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n"+tc.svc+"volumes:\n  data: {}\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			web := p.Services["web"]
			if _, err := web.ResolvedEnv(); err != nil {
				t.Errorf("nothing is refused here, got %v", err)
			}
			if got := []string(web.Tmpfs); !slices.Equal(got, tc.tmpfs) {
				t.Errorf("want tmpfs %q, got %q", tc.tmpfs, got)
			}
			if len(web.Volumes) != tc.volumes || tc.volumeHas != "" && !strings.Contains(strings.Join(web.Volumes, " "), tc.volumeHas) {
				t.Errorf("want %d volumes holding %q, got %q", tc.volumes, tc.volumeHas, web.Volumes)
			}
		})
	}
}

// The same across -f files (measured with `docker compose -f … -f … config`):
// the collapse reads the merged service.
func TestMountsAtOneTargetAcrossFiles(t *testing.T) {
	for _, tc := range []struct {
		name, base, over string
		tmpfs            []string
		volumes          int
		volumeHas        string
	}{
		{"a tmpfs in the base, a bind in the override", "    tmpfs:\n      - /t\n", "    volumes:\n      - ./a:/t\n", []string{"/t"}, 1, "a:/t"},
		{"a bind in the base, a tmpfs in the override", "    volumes:\n      - ./a:/t\n", "    tmpfs:\n      - /t\n", []string{"/t"}, 1, "a:/t"},
		{"a tmpfs in each", "    tmpfs:\n      - /t\n", "    tmpfs:\n      - /t:ro\n", []string{"/t", "/t:ro"}, 0, ""},
		{"a long-form tmpfs in the base, a bind in the override", "    volumes:\n      - {type: tmpfs, target: /t, tmpfs: {size: 1m}}\n", "    volumes:\n      - ./a:/t\n", nil, 1, "a:/t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "compose.yaml")
			over := filepath.Join(dir, "override.yaml")
			if err := os.WriteFile(base, []byte("name: demo\nservices:\n  web:\n    image: alpine\n"+tc.base), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(over, []byte("services:\n  web:\n"+tc.over), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{base, over}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			web := p.Services["web"]
			if got := []string(web.Tmpfs); !slices.Equal(got, tc.tmpfs) {
				t.Errorf("want tmpfs %q, got %q", tc.tmpfs, got)
			}
			if len(web.Volumes) != tc.volumes || tc.volumeHas != "" && !strings.HasSuffix(strings.Join(web.Volumes, " "), tc.volumeHas) {
				t.Errorf("want %d volumes ending %q, got %q", tc.volumes, tc.volumeHas, web.Volumes)
			}
		})
	}
}

// Service-level `tmpfs:` entries alike up to the first `=` are collapsed, the
// later kept at the first one's position, as docker compose v5.5.0 collapses
// them (each row measured with its `config`); entries that differ there are
// both passed on, where docker compose refuses two at one target.
func TestTmpfsEntriesAlikeUpToTheFirstEqualsAreCollapsed(t *testing.T) {
	for _, tc := range []struct {
		name, svc string
		tmpfs     []string
	}{
		{"the same entry twice", "    tmpfs: [/t, /t]\n", []string{"/t"}},
		{"two sizes", "    tmpfs: [/t:size=1m, /t:size=2m]\n", []string{"/t:size=2m"}},
		{"two modes", "    tmpfs: [/t:mode=700, /t:mode=755]\n", []string{"/t:mode=755"}},
		{"a size and a mode, then a size", "    tmpfs: [\"/t:size=1m,mode=700\", /t:size=2m]\n", []string{"/t:size=2m"}},
		{"ro twice", "    tmpfs: [/t:ro, /t:ro]\n", []string{"/t:ro"}},
		{"kept at the first position", "    tmpfs: [/t:size=1m, /u, /t:size=2m]\n", []string{"/t:size=2m", "/u"}},
		{"a mode, then a size", "    tmpfs: [/t:mode=700, /t:size=2m]\n", []string{"/t:mode=700", "/t:size=2m"}},
		{"ro and a size, then a size", "    tmpfs: [\"/t:ro,size=1m\", /t:size=1m]\n", []string{"/t:ro,size=1m", "/t:size=1m"}},
		{"a tmpfs at /t/ and a bind at /t/", "    tmpfs: [/t/]\n    volumes: [./a:/t/]\n", []string{"/t/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n"+tc.svc))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, tc.tmpfs) {
				t.Errorf("want tmpfs %q, got %q", tc.tmpfs, got)
			}
		})
	}
}

// The same across -f files and extends (measured with docker compose v5.5.0).
func TestTmpfsEntriesAreCollapsedAcrossFilesAndExtends(t *testing.T) {
	for _, tc := range []struct{ name, base, over, want string }{
		{"a size in each file", "    tmpfs: [/t:size=1m]\n", "    tmpfs: [/t:size=2m]\n", "/t:size=2m"},
		{"the same entry in each file", "    tmpfs: [/t]\n", "    tmpfs: [/t]\n", "/t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "compose.yaml")
			over := filepath.Join(dir, "override.yaml")
			if err := os.WriteFile(base, []byte("name: demo\nservices:\n  web:\n    image: alpine\n"+tc.base), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(over, []byte("services:\n  web:\n"+tc.over), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{base, over}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("want tmpfs [%s], got %q", tc.want, got)
			}
		})
	}
	t.Run("a size in the base service and in the extending one", func(t *testing.T) {
		p, err := Load(writeTemp(t, "name: demo\nservices:\n  base:\n    image: alpine\n    tmpfs: [/t:size=1m]\n  web:\n    extends: base\n    tmpfs: [/t:size=2m]\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{"/t:size=2m"}) {
			t.Errorf("want tmpfs [/t:size=2m], got %q", got)
		}
	})
}

// The entry kept stays at the first one's position (docker compose v5.5.0
// prints `./a:/t, ./c:/u, ./b:/t` as `b:/t, c:/u`), which decides how nested
// mounts overlay.
func TestTheMountKeptStaysAtTheFirstPosition(t *testing.T) {
	p, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - ./a:/t\n      - ./c:/u\n      - {type: tmpfs, target: /t}\n      - ./b:/t\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	want := []string{"/b:/t", "/c:/u"}
	if len(web.Tmpfs) != 0 || len(web.Volumes) != len(want) {
		t.Fatalf("want volumes ending %q and no tmpfs, got volumes %q tmpfs %q", want, web.Volumes, web.Tmpfs)
	}
	for i, w := range want {
		if !strings.HasSuffix(web.Volumes[i], w) {
			t.Errorf("want volume %d ending %q, got %q", i, w, web.Volumes)
		}
	}
}

// A nocopy volume followed by another at its target leaves no nocopy there:
// docker compose v5.5.0 keeps the later entry, without it. The target is read
// with every trailing `/` dropped (`/t//` is `/t`), as docker compose reads it.
func TestALaterVolumeDropsTheEarlierNocopy(t *testing.T) {
	for _, first := range []string{"data:/t:nocopy", "data:/t//:nocopy"} {
		t.Run(first, func(t *testing.T) {
			p, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - "+first+"\n      - data2:/t\nvolumes:\n  data: {}\n  data2: {}\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			web := p.Services["web"]
			if len(web.NoCopy) != 0 || len(web.Volumes) != 1 || !strings.HasPrefix(web.Volumes[0], "data2:") {
				t.Errorf("want data2 alone and no nocopy, got volumes %q nocopy %q", web.Volumes, web.NoCopy)
			}
		})
	}
}
