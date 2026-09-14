package compose

import (
	"slices"
	"strings"
	"testing"
)

// A long-form tmpfs mount's `read_only`, `tmpfs.size` and `tmpfs.mode` become
// the short form's options, which container 1.4.1 mounts the way docker mounts
// the long form. Each want that is read is docker compose v5.5.0's `config`
// output for the same spelling. The refused rows are what docker compose
// refuses too, and — though docker compose reads some of them — YAML floats,
// other size spellings and whole numbers out of range.
func TestALongFormTmpfsMountCarriesItsOptions(t *testing.T) {
	load := func(t *testing.T, item string) (*Project, error) {
		t.Helper()
		return Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - "+item+"\n"))
	}
	for _, tc := range []struct{ item, want string }{
		{"{type: tmpfs, target: /t}", "/t"},
		{"{type: tmpfs, target: /t, read_only: true}", "/t:ro"},
		{"{type: tmpfs, target: /t, read_only: false}", "/t"},
		// size: a number of bytes, or a string with a unit (1024-based).
		{"{type: tmpfs, target: /t, tmpfs: {size: 1048576}}", "/t:size=1048576"},
		{`{type: tmpfs, target: /t, tmpfs: {size: "1048576"}}`, "/t:size=1048576"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1.5m}}", "/t:size=1572864"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1MB}}", "/t:size=1048576"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1MiB}}", "/t:size=1048576"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1 m}}", "/t:size=1048576"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1k}}", "/t:size=1024"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1K}}", "/t:size=1024"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 2g}}", "/t:size=2147483648"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1t}}", "/t:size=1099511627776"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1b}}", "/t:size=1"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 0x10}}", "/t:size=16"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1_000}}", "/t:size=1000"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1p}}", "/t:size=1125899906842624"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1KIB}}", "/t:size=1024"},
		// A fraction of a byte is dropped, not rounded.
		{"{type: tmpfs, target: /t, tmpfs: {size: 1.0005k}}", "/t:size=1024"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 0}}", "/t"},
		// mode: a number; 0755 and 0o755 are octal, 755 is decimal.
		{"{type: tmpfs, target: /t, tmpfs: {mode: 0755}}", "/t:mode=755"},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 0o755}}", "/t:mode=755"},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 493}}", "/t:mode=755"},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 755}}", "/t:mode=1363"},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 01777}}", "/t:mode=1777"},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 4294967295}}", "/t:mode=37777777777"},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 0}}", "/t"},
		// All three, in the order the options are written.
		{"{type: tmpfs, target: /t, read_only: true, tmpfs: {mode: 0700, size: 2m}}", "/t:ro,size=2097152,mode=700"},
	} {
		t.Run(tc.item, func(t *testing.T) {
			p, err := load(t, tc.item)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("want tmpfs %q, got %q", tc.want, got)
			}
			if ign := p.Services["web"].Unsupported; slices.ContainsFunc(ign, func(s string) bool { return strings.Contains(s, "tmpfs") }) {
				t.Errorf("a tmpfs mount's options are read, not ignored: %v", ign)
			}
		})
	}
	for _, tc := range []struct{ item, want string }{
		{"{type: tmpfs, target: /t, tmpfs: {size: 1Mi}}", `tmpfs.size "1Mi" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1ib}}", `tmpfs.size "1ib" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: 2 ib}}", `tmpfs.size "2 ib" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: 10IB}}", `tmpfs.size "10IB" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: x1m}}", `tmpfs.size "x1m" is not a size`},
		{`{type: tmpfs, target: /t, tmpfs: {size: "-1m"}}`, `tmpfs.size "-1m" is not a size`},
		{`{type: tmpfs, target: /t, tmpfs: {size: "-1"}}`, `tmpfs.size "-1" is not a size`},
		// No value, and an empty string (an unset `${VAR}`), are no size.
		{"{type: tmpfs, target: /t, tmpfs: {size: }}", `tmpfs.size "" is not a size`},
		{`{type: tmpfs, target: /t, tmpfs: {size: ""}}`, `tmpfs.size "" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: }}", `tmpfs.mode "" is not a mode`},
		{`{type: tmpfs, target: /t, tmpfs: {size: "1  m"}}`, `tmpfs.size "1  m" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: .5m}}", `tmpfs.size ".5m" is not a size`},
		{`{type: tmpfs, target: /t, tmpfs: {size: "+1m"}}`, `tmpfs.size "+1m" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1.m}}", `tmpfs.size "1.m" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: \"" + strings.Repeat("9", 401) + "\"}}", "is not a size"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 9223372036854775808}}", `tmpfs.size "9223372036854775808" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: 08}}", `tmpfs.size "08" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: -1}}", `tmpfs.size "-1" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: abc}}", `tmpfs.size "abc" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1.5}}", `tmpfs.size "1.5" is not a size`},
		{`{type: tmpfs, target: /t, tmpfs: {size: "1m "}}`, `tmpfs.size "1m " is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: true}}", `tmpfs.size "true" is not a size`},
		{"{type: tmpfs, target: /t, tmpfs: {size: [1]}}", "tmpfs.size"},
		{"{type: tmpfs, target: /t, tmpfs: {size: 1e3}}", `tmpfs.size "1e3" is not a size`},
		{`{type: tmpfs, target: /t, tmpfs: {mode: "0755"}}`, `tmpfs.mode "0755" is not a mode`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: -1}}", `tmpfs.mode "-1" is not a mode`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: true}}", `tmpfs.mode "true" is not a mode`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 1.5}}", `tmpfs.mode "1.5" is not a mode`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 4294967296}}", `tmpfs.mode "4294967296" is not a mode`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 9223372036854775808}}", `tmpfs.mode "9223372036854775808" is not a mode`},
		{"{type: tmpfs, target: /t, tmpfs: {mode: 08}}", `tmpfs.mode "08" is not a mode`},
		// A key docker compose does not take under `tmpfs:` is refused as there,
		// and so is a `tmpfs:` that is not a mapping.
		{"{type: tmpfs, target: /t, tmpfs: {foo: 1}}", "foo"},
		{"{type: tmpfs, target: /t, tmpfs: }", "tmpfs must be a mapping"},
		{"{type: tmpfs, target: /t, tmpfs: 1}", "not the shape that field takes"},
	} {
		t.Run("refused "+tc.item, func(t *testing.T) {
			_, err := load(t, tc.item)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error holding %q, got %v", tc.want, err)
			}
		})
	}
}

// On a bind or volume mount `tmpfs:` does nothing, so it is still named among
// the ignored fields; on a tmpfs mount it is not. The second mount carries
// the options, so an entry number that is always 1 would not tell them apart.
func TestTmpfsOptionsAreIgnoredOnlyOffATmpfsMount(t *testing.T) {
	p, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a, tmpfs: {size: 1m}}\n      - {type: bind, source: /tmp, target: /b, tmpfs: {size: 1m}}\n      - {type: volume, source: data, target: /c, tmpfs: {size: 1m}}\nvolumes:\n  data: {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ign := p.Services["web"].Unsupported
	for _, want := range []string{"volumes entry 2.tmpfs", "volumes entry 3.tmpfs"} {
		if !slices.Contains(ign, want) {
			t.Errorf("want %q among the ignored fields, got %v", want, ign)
		}
	}
	if slices.Contains(ign, "volumes entry 1.tmpfs") {
		t.Errorf("a tmpfs mount's options are read, got %v", ign)
	}
	if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{"/a:size=1048576"}) {
		t.Errorf("want tmpfs [/a:size=1048576], got %q", got)
	}
}

// The refusal names the mount by its place in the list.
func TestATmpfsOptionRefusalNamesTheMount(t *testing.T) {
	_, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {type: tmpfs, target: /b, tmpfs: {size: abc}}\n"))
	want := `volumes entry 2 of 2: tmpfs.size "abc" is not a size`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("want an error holding %q, got %v", want, err)
	}
}

// Values reached through YAML aliases are read as written in place: a size,
// a mode and the mount's type.
func TestTmpfsOptionsReadThroughAliases(t *testing.T) {
	p, err := Load(writeTemp(t, "name: demo\nx-sz: &sz 1m\nx-m: &m 0700\nx-t: &t tmpfs\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: *t, target: /t, tmpfs: {size: *sz, mode: *m}}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{"/t:size=1048576,mode=700"}) {
		t.Errorf("want tmpfs [/t:size=1048576,mode=700], got %q", got)
	}
	if ign := p.Services["web"].Unsupported; len(ign) != 0 {
		t.Errorf("want nothing ignored, got %v", ign)
	}
}

// A `tmpfs:` that is not a mapping is refused wherever it comes from — written
// in place, through an alias, through `<<:` — and on any mount, and the
// refusal names the mount by its place.
func TestATmpfsThatIsNotAMappingIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"in place, the second mount", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {type: tmpfs, target: /t, tmpfs: }\n"},
		{"through an alias", "x-n: &n\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {type: tmpfs, target: /t, tmpfs: *n}\n"},
		{"through a merge", "x-base: &b {type: tmpfs, target: /t, tmpfs: }\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {<<: *b}\n"},
		{"on a bind mount", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {type: bind, source: /tmp, target: /t, tmpfs: }\n"},
		{"on a volume mount", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {type: volume, source: data, target: /t, tmpfs: }\nvolumes:\n  data: {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "name: demo\n"+tc.body))
			want := "volumes entry 2 of 2: tmpfs must be a mapping"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want an error holding %q, got %v", want, err)
			}
		})
	}
}

// On a bind or volume mount the tmpfs options do nothing and are named among
// the ignored fields, but a size or mode docker compose refuses is refused
// there too.
func TestTmpfsOptionsOffATmpfsMountAreStillChecked(t *testing.T) {
	for _, typ := range []string{"type: bind, source: /tmp", "type: volume, source: data"} {
		for _, opt := range []string{"size: abc", "size: -1", "mode: \"0755\"", "mode: -1"} {
			t.Run(typ+"/"+opt, func(t *testing.T) {
				_, err := Load(writeTemp(t, "name: demo\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /a}\n      - {"+typ+", target: /b, tmpfs: {"+opt+"}}\nvolumes:\n  data: {}\n"))
				if err == nil || !strings.Contains(err.Error(), "volumes entry 2 of 2: tmpfs.") {
					t.Errorf("want the option refused on the second mount, got %v", err)
				}
			})
		}
	}
}

// A `tmpfs:` that is a mapping is read however it arrives: through an alias,
// and written in place over a `tmpfs:` a `<<:` brings in with no value.
func TestATmpfsMappingIsTakenThroughAnAliasAndOverAMerge(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"an alias to a mapping", "x-o: &o {size: 1m}\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {type: tmpfs, target: /t, tmpfs: *o}\n"},
		{"written over a merged null", "x-b: &b {type: tmpfs, target: /t, tmpfs: }\nservices:\n  web:\n    image: alpine\n    volumes:\n      - {<<: *b, tmpfs: {size: 1m}}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "name: demo\n"+tc.body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := []string(p.Services["web"].Tmpfs); !slices.Equal(got, []string{"/t:size=1048576"}) {
				t.Errorf("want tmpfs [/t:size=1048576], got %q", got)
			}
		})
	}
}
