package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// `volumes_from`, as docker compose (v5.5.1, measured 2026-09-18) mounts it:
// each row is one rule, with the mounts the service ends up with and the
// dependency it gains.
func TestVolumesFromMountsTheNamedServicesVolumes(t *testing.T) {
	const head = "volumes:\n  named: {}\nservices:\n"
	for _, tc := range []struct {
		name, body string
		volumes    []string // user's mounts after the load, in order
		deps       []Dependency
		err        string
	}{
		{"the holder's bind and named volumes come along",
			"  holder:\n    image: a\n    volumes: [./h:/shared, 'named:/data']\n  user:\n    image: a\n    volumes_from: [holder]\n",
			[]string{"./h:/shared", "named:/data"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"the :ro suffix changes nothing",
			"  holder:\n    image: a\n    volumes: [./h:/shared]\n  user:\n    image: a\n    volumes_from: ['holder:ro']\n",
			[]string{"./h:/shared"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"the holder's own read-only mount stays read-only",
			"  holder:\n    image: a\n    volumes: [./h:/shared:ro]\n  user:\n    image: a\n    volumes_from: [holder]\n",
			[]string{"./h:/shared:ro"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"what the holder borrowed is borrowed on (a chain of three)",
			"  c:\n    image: a\n    volumes: [./c:/cdata]\n  b:\n    image: a\n    volumes: [./b:/bdata]\n    volumes_from: [c]\n  user:\n    image: a\n    volumes_from: [b]\n",
			[]string{"./c:/cdata", "./b:/bdata"}, []Dependency{{"b", ConditionStarted, false}}, ""},
		{"the chain read from the far end first",
			"  user:\n    image: a\n    volumes_from: [b]\n  b:\n    image: a\n    volumes_from: [c]\n  c:\n    image: a\n    volumes: [./c:/cdata]\n",
			[]string{"./c:/cdata"}, []Dependency{{"b", ConditionStarted, false}}, ""},
		{"the service's own mount of a target wins over the holder's bind",
			"  holder:\n    image: a\n    volumes: [./h:/shared, ./h2:/other]\n  user:\n    image: a\n    volumes: [./u:/shared]\n    volumes_from: [holder]\n",
			[]string{"./h2:/other", "./u:/shared"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"the service's own mount of a target wins over the holder's named volume",
			"  holder:\n    image: a\n    volumes: ['named:/shared']\n  user:\n    image: a\n    volumes: [./u:/shared]\n    volumes_from: [holder]\n",
			[]string{"./u:/shared"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"a dependency already written is kept as written",
			"  holder:\n    image: a\n    volumes: [./h:/shared]\n    healthcheck: {test: [CMD, \"true\"]}\n  user:\n    image: a\n    depends_on:\n      holder: {condition: service_healthy}\n    volumes_from: [holder]\n",
			[]string{"./h:/shared"}, []Dependency{{"holder", ConditionHealthy, false}}, ""},
		{"two holders, in the order written",
			"  h1:\n    image: a\n    volumes: [./a:/a]\n  h2:\n    image: a\n    volumes: [./b:/b]\n  user:\n    image: a\n    volumes_from: [h2, h1]\n",
			[]string{"./b:/b", "./a:/a"}, []Dependency{{"h2", ConditionStarted, false}, {"h1", ConditionStarted, false}}, ""},
		{"a service that is not there",
			"  user:\n    image: a\n    volumes_from: [nope]\n",
			nil, nil, `service "user" depends on undefined service "nope"`},
		{"service:holder is not a spelling",
			"  holder:\n    image: a\n  user:\n    image: a\n    volumes_from: ['service:holder']\n",
			nil, nil, `depends on undefined service "service"`},
		{"a cycle",
			"  a:\n    image: a\n    volumes_from: [b]\n  b:\n    image: a\n    volumes_from: [a]\n",
			nil, nil, "dependency cycle detected: a -> b -> a"},
		{"a holder's anonymous volume is not carried",
			"  holder:\n    image: a\n    volumes: [/data]\n  user:\n    image: a\n    volumes_from: [holder]\n",
			nil, nil, "anonymous volume at /data"},
		{"a container outside the file",
			"  user:\n    image: a\n    volumes_from: ['container:other']\n",
			nil, nil, "names a container outside this compose file"},
		{"not a list",
			"  user:\n    image: a\n    volumes_from: holder\n",
			nil, nil, "volumes_from must be a list"},
		{"an entry that is not a name",
			"  user:\n    image: a\n    volumes_from: [1]\n",
			nil, nil, "volumes_from entry 1 of 1 must be a string, got a number"},
		{"the key with nothing after it",
			"  user:\n    image: a\n    volumes_from:\n",
			nil, nil, "volumes_from: expected a list, got nothing"},
		{"the same service twice",
			"  holder:\n    image: a\n  user:\n    image: a\n    volumes_from: [holder, holder]\n",
			nil, nil, "volumes_from items at 0 and 1 are equal"},
		{"a cycle entered from outside is named from where it comes back to",
			"  a:\n    image: a\n    volumes_from: [b]\n  b:\n    image: a\n    volumes_from: [c]\n  c:\n    image: a\n    volumes_from: [b]\n",
			nil, nil, "dependency cycle detected: b -> c -> b"},
		{"a service naming itself",
			"  user:\n    image: a\n    volumes_from: [user]\n",
			nil, nil, "dependency cycle detected: user -> user"},
		{"the suffix and a written dependency together: the dependency is kept",
			"  holder:\n    image: a\n    volumes: [./h:/shared]\n    healthcheck: {test: [CMD, \"true\"]}\n  user:\n    image: a\n    depends_on:\n      holder: {condition: service_healthy}\n    volumes_from: ['holder:ro']\n",
			[]string{"./h:/shared"}, []Dependency{{"holder", ConditionHealthy, false}}, ""},
		{"a holder's anonymous volume after a bind mount is not carried either",
			"  holder:\n    image: a\n    volumes: [./h:/shared, /data]\n  user:\n    image: a\n    volumes_from: [holder]\n",
			nil, nil, "anonymous volume at /data"},
		{"the holder's long-form read-only bind and nocopy volume come along as such",
			"  holder:\n    image: a\n    volumes:\n      - {type: bind, source: ./h, target: /shared, read_only: true}\n      - {type: volume, source: named, target: /nc, volume: {nocopy: true}}\n  user:\n    image: a\n    volumes_from: [holder]\n",
			[]string{"./h:/shared:ro", "named:/nc"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"the service's own tmpfs at a borrowed path takes it",
			"  holder:\n    image: a\n    volumes: [./h:/shared, ./h2:/other]\n  user:\n    image: a\n    tmpfs: [/shared]\n    volumes_from: [holder]\n",
			[]string{"./h2:/other"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"a tmpfs target is compared as written: /y/ does not take /y",
			"  holder:\n    image: a\n    volumes: [./h:/x, ./h:/y]\n  user:\n    image: a\n    tmpfs: ['/x', '/y/']\n    volumes_from: [holder]\n",
			[]string{"./h:/y"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"a tmpfs target with options takes the path",
			"  holder:\n    image: a\n    volumes: [./h:/x]\n  user:\n    image: a\n    tmpfs: ['/x:size=1m']\n    volumes_from: [holder]\n",
			[]string{}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"a holder's anonymous volume at a path the service mounts itself is not borrowed, so not refused",
			"  holder:\n    image: a\n    volumes: [/data, ./h:/shared]\n  user:\n    image: a\n    volumes: [./u:/data]\n    volumes_from: [holder]\n",
			[]string{"./h:/shared", "./u:/data"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"nor at a path the service's tmpfs takes",
			"  holder:\n    image: a\n    volumes: [/data]\n  user:\n    image: a\n    tmpfs: [/data]\n    volumes_from: [holder]\n",
			[]string{}, []Dependency{{"holder", ConditionStarted, false}}, ""},
		{"the same service twice, further apart",
			"  h1:\n    image: a\n  h2:\n    image: a\n  user:\n    image: a\n    volumes_from: [h1, h2, h1]\n",
			nil, nil, "volumes_from items at 0 and 2 are equal"},
		{"a null entry",
			"  user:\n    image: a\n    volumes_from: [null]\n",
			nil, nil, "volumes_from entry 1 of 1 must be a string"},
		{"an empty name is a service that is not there",
			"  user:\n    image: a\n    volumes_from: ['']\n",
			nil, nil, `depends on undefined service ""`},
		{"the holder's tmpfs entries are not carried",
			"  holder:\n    image: a\n    tmpfs: [/t1]\n    volumes: [./h:/shared, {type: tmpfs, target: /t2}]\n  user:\n    image: a\n    volumes_from: [holder]\n",
			[]string{"./h:/shared"}, []Dependency{{"holder", ConditionStarted, false}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, head+tc.body))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["user"]
			if got := []string(svc.Volumes); !reflect.DeepEqual(got, tc.volumes) {
				t.Errorf("mounts: got %q, want %q", got, tc.volumes)
			}
			if got := []Dependency(svc.DependsOn); !reflect.DeepEqual(got, tc.deps) {
				t.Errorf("depends_on: got %v, want %v", got, tc.deps)
			}
		})
	}
}

// `config` prints `volumes_from` as written and the dependency it adds, and
// not the borrowed mounts — the service's own `volumes:` only, as docker
// compose prints it.
func TestConfigPrintsVolumesFromAsWrittenAndNotTheBorrowedMounts(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  holder:\n    image: a\n    volumes: [./h:/shared]\n  user:\n    image: a\n    volumes: [./u:/own]\n    volumes_from: ['holder:ro']\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	user := out[strings.Index(out, "  user:"):]
	for _, want := range []string{"volumes_from:\n            - holder:ro", "condition: service_started", "/own"} {
		if !strings.Contains(user, want) {
			t.Errorf("want %q in the user's config, got\n%s", want, user)
		}
	}
	if strings.Contains(user, "/shared") {
		t.Errorf("the borrowed mount is printed under the user's volumes:\n%s", user)
	}
	// And the mounts are there for the commands all the same.
	if got := []string(p.Services["user"].Volumes); !reflect.DeepEqual(got, []string{"./h:/shared", "./u:/own"}) {
		t.Errorf("mounts: got %q", got)
	}
}

// A holder's `nocopy` comes along with the mount it is on — and only while
// that mount is the one this service ends up with: not where this service's
// own mount takes the path, nor where a later holder's does.
func TestVolumesFromCarriesNocopyWithTheWinningMount(t *testing.T) {
	const head = "volumes:\n  nv: {}\n  nw: {}\nservices:\n"
	for _, tc := range []struct {
		name, body string
		nocopy     []string
	}{
		{"the holder's nocopy comes along",
			"  holder:\n    image: a\n    volumes:\n      - {type: volume, source: nv, target: /x, volume: {nocopy: true}}\n  user:\n    image: a\n    volumes_from: [holder]\n",
			[]string{"/x"}},
		{"not where the service's own mount takes the path",
			"  holder:\n    image: a\n    volumes:\n      - {type: volume, source: nv, target: /x, volume: {nocopy: true}}\n  user:\n    image: a\n    volumes: ['nw:/x']\n    volumes_from: [holder]\n",
			nil},
		{"not where a later holder's mount takes the path",
			"  h1:\n    image: a\n    volumes:\n      - {type: volume, source: nv, target: /x, volume: {nocopy: true}}\n  h2:\n    image: a\n    volumes: ['nw:/x']\n  user:\n    image: a\n    volumes_from: [h1, h2]\n",
			nil},
		{"a nocopy target written with a trailing slash",
			"  holder:\n    image: a\n    volumes:\n      - {type: volume, source: nv, target: /x/, volume: {nocopy: true}}\n  user:\n    image: a\n    volumes_from: [holder]\n",
			[]string{"/x"}},
		{"and where a later holder's mount has it, it does",
			"  h1:\n    image: a\n    volumes: ['nw:/x']\n  h2:\n    image: a\n    volumes:\n      - {type: volume, source: nv, target: /x, volume: {nocopy: true}}\n  user:\n    image: a\n    volumes_from: [h1, h2]\n",
			[]string{"/x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, head+tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Services["user"].NoCopy; !reflect.DeepEqual(got, tc.nocopy) {
				t.Errorf("nocopy: got %v, want %v", got, tc.nocopy)
			}
		})
	}
}

// Across `-f` files a holder listed in both is one listing, as docker compose
// reads it (a single file listing it twice is refused).
func TestVolumesFromListedInTwoFilesIsOneListing(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.yaml": "services:\n  holder:\n    image: a\n    volumes: [./h:/shared]\n  user:\n    image: a\n    volumes_from: [holder]\n",
		"b.yaml": "services:\n  user:\n    volumes_from: [holder]\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadFiles([]string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(p.Services["user"].VolumesFrom); !reflect.DeepEqual(got, []string{"holder"}) {
		t.Errorf("volumes_from: got %q", got)
	}
}

// Known differences from docker compose (v5.5.1, measured 2026-09-18), kept
// with docker compose's answer beside opossum's: a row is red when opossum
// stops giving its answer, and `docker` records what docker compose gave.
// Each is the loader's doing — `volumes_from` is folded as the file is
// read, on every service — and is what a change there would move.
func TestVolumesFromKnownDifferences(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		err        string // opossum refuses the load with this
		docker     string
	}{
		{"a cycle a volumes_from entry closes with a depends_on is not read at the load",
			"  a:\n    image: a\n    volumes_from: [b]\n  b:\n    image: a\n    depends_on: [a]\n",
			"", "config rc 1: dependency cycle detected: a -> b -> a"},
		{"a cycle behind a profile that is off is read",
			"  main:\n    image: a\n  a:\n    image: a\n    profiles: [extra]\n    volumes_from: [b]\n  b:\n    image: a\n    profiles: [extra]\n    volumes_from: [a]\n",
			"dependency cycle detected: a -> b -> a", "config, ps, down rc 0 (the corner is not read)"},
		{"an anonymous volume behind a profile that is off is read",
			"  main:\n    image: a\n  holder:\n    image: a\n    profiles: [extra]\n    volumes: [/data]\n  user:\n    image: a\n    profiles: [extra]\n    volumes_from: [holder]\n",
			"anonymous volume at /data", "config rc 0"},
		{"a missing service behind a profile that is off is read",
			"  main:\n    image: a\n  user:\n    image: a\n    profiles: [extra]\n    volumes_from: [nope]\n",
			`depends on undefined service "nope"`, "config rc 0"},
		{"a container: entry behind a profile that is off is read",
			"  main:\n    image: a\n  user:\n    image: a\n    profiles: [extra]\n    volumes_from: ['container:other']\n",
			"names a container outside this compose file", "config rc 0"},
		{"an anonymous volume at a path a later holder mounts is refused",
			"  h1:\n    image: a\n    volumes: [/data]\n  h2:\n    image: a\n    volumes: [./h:/data]\n  user:\n    image: a\n    volumes_from: [h1, h2]\n",
			"anonymous volume at /data", "up rc 0: the user mounts h2's bind at /data and no anonymous volume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n"+tc.body))
			if tc.err == "" {
				if err != nil {
					t.Fatalf("want the load to go ahead (docker compose: %s), got %v", tc.docker, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("want a refusal saying %q (docker compose: %s), got %v", tc.err, tc.docker, err)
			}
		})
	}
}
