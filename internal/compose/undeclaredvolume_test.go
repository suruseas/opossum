package compose

import (
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/testpair"
)

// A named volume a service mounts must be declared under top-level `volumes:`,
// as docker compose requires — a misspelling on either side otherwise made
// `up` create an empty volume under the misspelt name and mount it, while the
// declared one went unused. A host path (a bind mount) and a bare target (an
// anonymous volume) need no declaration.
//
// Declared and undeclared come as a pair in both orders (testpair) so the
// refusal cannot depend on the position of the entry; the short and the long
// form each play each role so the check cannot be about one spelling.
func TestAMountOfAnUndeclaredNamedVolumeIsRefused(t *testing.T) {
	long := func(src, target string) string { return "{type: volume, source: " + src + ", target: " + target + "}" }
	refused := func(t *testing.T, body string) string {
		t.Helper()
		_, err := Load(writeTemp(t, body))
		if err == nil {
			t.Fatalf("an undeclared named volume must be refused, but loaded:\n%s", body)
		}
		return err.Error()
	}
	for _, forms := range []struct {
		name       string
		good, typo string
	}{
		{"short and short", "good:/a", "typo:/b"},
		{"long and long", long("good", "/a"), long("typo", "/b")},
		{"short declared, long undeclared", "good:/a", long("typo", "/b")},
		{"long declared, short undeclared", long("good", "/a"), "typo:/b"},
	} {
		testpair.Run(t, forms.name, testpair.Pair[string]{A: forms.good, B: forms.typo}, func(t *testing.T, first, second string) {
			got := refused(t, "services:\n  db:\n    image: postgres:16\n    volumes:\n      - "+first+"\n      - "+second+"\nvolumes:\n  good: {}\n")
			if !strings.Contains(got, `refers to undefined volume "typo"`) || strings.Contains(got, `"good"`) {
				t.Errorf("the refusal names the undeclared volume and not the declared one, got: %s", got)
			}
		})
	}
	// Two services, either of which may be the one with the typo.
	testpair.Run(t, "every service is checked", testpair.Pair[string]{A: "one", B: "two"}, func(t *testing.T, fine, broken string) {
		got := refused(t, "services:\n  "+fine+":\n    image: alpine\n    volumes: [\"good:/a\"]\n  "+broken+":\n    image: alpine\n    volumes: [\"typo:/b\"]\nvolumes:\n  good: {}\n")
		if !strings.Contains(got, `service "`+broken+`" refers to undefined volume "typo"`) {
			t.Errorf("the refusal names the service with the undeclared mount, got: %s", got)
		}
	})
	// The commonest real case is not a typo but no `volumes:` section at all.
	// One entry: there is no second element whose order or attribution matters.
	t.Run("a file with no volumes section at all", func(t *testing.T) {
		got := refused(t, "services:\n  db:\n    image: postgres:16\n    volumes: [\"dbdata:/var/lib/postgresql/data\"]\n")
		if !strings.Contains(got, `refers to undefined volume "dbdata"`) {
			t.Errorf("want the refusal, got: %s", got)
		}
	})
	// Two entries at one target collapse to the later one before the check, as
	// docker compose collapses them: an undeclared name a later bind replaces
	// is never mounted and is not refused; the reverse order is. The two
	// orders carry different expectations, so they are written out.
	t.Run("an undeclared volume a later mount at the same target replaces is not refused", func(t *testing.T) {
		if _, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes: [\"typo:/x\", \"./x:/x\"]\n")); err != nil {
			t.Errorf("the bind replaced the undeclared volume, so nothing undeclared is mounted; got: %v", err)
		}
	})
	t.Run("an undeclared volume that replaces an earlier mount at the same target is refused", func(t *testing.T) {
		got := refused(t, "services:\n  web:\n    image: alpine\n    volumes: [\"./x:/x\", \"typo:/x\"]\n")
		if !strings.Contains(got, `refers to undefined volume "typo"`) {
			t.Errorf("want the refusal, got: %s", got)
		}
	})
	t.Run("the message says what to do", func(t *testing.T) {
		got := refused(t, "services:\n  db:\n    image: postgres:16\n    volumes: [\"dbdata:/var/lib/postgresql/data\"]\nvolumes:\n  dbdta: {}\n")
		want := `service "db" refers to undefined volume "dbdata" — declare it under top-level volumes:, or write a host path (` + "`./dbdata`" + `) for a bind mount`
		if got != want {
			t.Errorf("message:\n got %q\nwant %q", got, want)
		}
	})
	// A `~name` source is neither a volume nor a usable path: docker resolves it
	// to `$HOME/name`, which nobody means, so it is refused with the spellings
	// that do mean something. Two spellings (bare and with a path) as a pair so
	// the refusal cannot hang on the presence of a `/`.
	testpair.Run(t, "a ~name source is refused", testpair.Pair[string]{A: "~x:/y", B: "~bob/h:/z"}, func(t *testing.T, first, second string) {
		got := refused(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - "+first+"\n      - "+second+"\n")
		want := `service "web": the mount source "` + strings.TrimSuffix(strings.TrimSuffix(first, ":/y"), ":/z") + `" starts with ` + "`~` but not `~/` — `~` here means your own home only; write `~/" + strings.TrimPrefix(strings.TrimSuffix(strings.TrimSuffix(first, ":/y"), ":/z"), "~") + "` for a path under it, or an absolute path"
		if got != want {
			t.Errorf("refusal:\n got %q\nwant %q", got, want)
		}
	})
	// In the long form the type decides: a `type: volume` whose source starts
	// with `.` is a volume name (docker compose creates `<project>_.hidden`),
	// so it needs its declaration like any other — and the refusal names it as
	// written, not in the loader's spelling of it.
	t.Run("a long-form volume whose name starts with a dot needs its declaration", func(t *testing.T) {
		got := refused(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: .hidden, target: /y}\n")
		want := `service "web" refers to undefined volume ".hidden"`
		if !strings.Contains(got, want) || ShowsLoaderSpelling(got) {
			t.Errorf("refusal:\n got %q\nwant it to contain %q, and nothing but what was written", got, want)
		}
	})
	// `./x` is a name docker compose does not let a volume have: it refuses the
	// declaration (`volumes additional properties './x' not allowed`), and so
	// `../x` and `.x/y`, whose `/` is not the second character. Declared and
	// not, so the refusal cannot hang on the declaration being missing.
	for _, src := range []string{"./x", "../x", ".x/y"} {
		for _, decl := range []string{"", "volumes:\n  " + src + ": {}\n"} {
			t.Run("a long-form volume named with a slash is refused, declared or not: "+src+" "+strings.TrimSpace(decl), func(t *testing.T) {
				got := refused(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: "+src+", target: /y}\n"+decl)
				if want := "type: volume with source \"" + src + "\" — a volume name cannot contain `/`"; !strings.Contains(got, want) {
					t.Errorf("refusal:\n got %q\nwant it to contain %q", got, want)
				}
			})
		}
	}
	// A NUL byte in a mount or a volume's name is refused, as docker compose
	// refuses it: the loader's own spellings of a mount are made of them, so a
	// file could otherwise write one by hand and have `.hid:/y`, a path, run as
	// the volume `.hid`. One subtest per place a NUL can be written.
	for _, tc := range []struct{ name, body, want string }{
		{"in a short mount", "services:\n  web:\n    image: alpine\n    volumes:\n      - \"\\0opossumdotvolume\\0.hid:/y\"\nvolumes:\n  .hid: {}\n", "volumes entry 1 of 1 contains a NUL character"},
		{"in a long mount's source", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: \"\\0x\", target: /y}\n", "volumes entry 1 of 1 contains a NUL character"},
		{"in a long mount's target", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: bind, source: ./x, target: \"/y\\0\"}\n", "volumes entry 1 of 1 contains a NUL character"},
		{"in a volume's name", "services:\n  web:\n    image: alpine\nvolumes:\n  \"a\\0b\": {}\n", `compose.yaml: volume name "a\x00b" contains a NUL character`},
	} {
		t.Run("a NUL byte is refused "+tc.name, func(t *testing.T) {
			if got := refused(t, tc.body); !strings.Contains(got, tc.want) {
				t.Errorf("refusal:\n got %q\nwant it to contain %q", got, tc.want)
			}
		})
	}
	// A `/` or `~` name docker compose refuses too; the wording says so.
	for _, tc := range []struct{ src, want string }{
		{"/abs", "service \"web\": volumes entry 1 of 1: type: volume with source \"/abs\" — a volume name cannot start with `/` or `~` (docker compose refuses it as well); write type: bind for a host path, or a name for a volume"},
		{"~h", "service \"web\": volumes entry 1 of 1: type: volume with source \"~h\" — a volume name cannot start with `/` or `~` (docker compose refuses it as well); write type: bind for a host path, or a name for a volume"},
	} {
		t.Run("a long-form volume whose source is spelled like a path is refused: "+tc.src, func(t *testing.T) {
			got := refused(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: "+tc.src+", target: /y}\n")
			// Parse-time refusals carry the file and the service in front.
			if !strings.HasSuffix(got, tc.want) {
				t.Errorf("refusal:\n got %q\nwant suffix %q", got, tc.want)
			}
		})
	}
	// The forms that need no declaration, one file each so a wrongly refused
	// one is named: host paths in every spelling the runtime side reads as a
	// bind, anonymous volumes, tmpfs, and the declared forms with their options.
	//
	// The boundaries this judgement answers, one row each (docker compose's
	// rule: a source starting with `/`, `.` or `~` is a path, the rest is a
	// volume name): `/…` · `./…` · `../…` · `.` · `..` · `.hidden` ·
	// `.hidden/sub` · `..hidden` · `~` · `~/…` · (`~name` refused above) ·
	// empty source · bare target · long-form bind · long-form volume · tmpfs ·
	// a declared name with a mode / nocopy · external · (undeclared refused above).
	for _, tc := range []struct{ name, item string }{
		{"a relative host path", "./x:/y"},
		{"a parent-relative host path", "../x:/y"},
		{"the directory itself", ".:/y"},
		{"the parent directory itself", "..:/y"},
		{"an absolute host path", "/abs:/y"},
		{"a home-relative host path", "~/h:/y"},
		{"the home directory itself", "~:/y"},
		{"a hidden name, a path as docker reads it", ".hidden:/y"},
		{"a path under a hidden name", ".hidden/sub:/y"},
		{"a name starting with two dots", "..hidden:/y"},
		{"a long-form bind", "{type: bind, source: ./lb, target: /lb}"},
		{"an anonymous volume", "/anon"},
		{"an anonymous volume written with an empty source", ":/es"},
		{"a long-form anonymous volume", "{type: volume, target: /la}"},
		{"a tmpfs", "{type: tmpfs, target: /t}"},
		{"a declared volume with a mode", "good:/g:ro"},
		{"a declared volume with nocopy", "good:/n:nocopy"},
		{"a declared external volume", "ext:/e"},
		{"a declared volume in the long form", "{type: volume, source: good, target: /lg}"},
		{"a long-form bind whose source is a bare name", "{type: bind, source: data, target: /y}"},
	} {
		t.Run(tc.name+" needs no declaration", func(t *testing.T) {
			if _, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - "+tc.item+"\nvolumes:\n  good: {}\n  ext: {external: true}\n")); err != nil {
				t.Errorf("%s must load, got: %v", tc.name, err)
			}
		})
	}
}
