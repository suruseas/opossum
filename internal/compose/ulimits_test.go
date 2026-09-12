package compose

import (
	"strings"
	"testing"
)

// `shm_size` and `ulimits` (#885), read as docker compose (v5.5.0) reads
// them: shm_size in any size spelling, normalised to a byte count; ulimits
// as a number (soft and hard alike) or `{soft, hard}`; the oracle is in
// sweeps/ulimits-oracle.
func TestShmSizeAndUlimitsAreRead(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\n    shm_size: 1gb\n    ulimits:\n      nofile: 65536\n      nproc: {soft: 100, hard: 200}\n  b:\n    image: a\n    shm_size: \"64M\"\n  c:\n    image: a\n    shm_size: 67108864\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(p.Services["web"].ShmSize); got != "1073741824" {
		t.Errorf("shm_size 1gb = %q, want the byte count 1073741824", got)
	}
	if got := string(p.Services["b"].ShmSize); got != "67108864" {
		t.Errorf("shm_size \"64M\" = %q, want 67108864", got)
	}
	if got := string(p.Services["c"].ShmSize); got != "67108864" {
		t.Errorf("shm_size 67108864 = %q, want kept as bytes", got)
	}
	u := p.Services["web"].Ulimits
	if u["nofile"] != (Ulimit{Soft: 65536, Hard: 65536}) || u["nproc"] != (Ulimit{Soft: 100, Hard: 200}) {
		t.Errorf("ulimits = %+v, want nofile 65536/65536 and nproc 100/200", u)
	}
	if got := strings.Join(u.Args(), " "); got != "nofile=65536 nproc=100:200" {
		t.Errorf("ulimit args = %q, want name=soft for a number and name=soft:hard for the mapping, in name order", got)
	}
	// The flags come out in name order whatever order the file wrote them.
	many := Ulimits{"stack": {Soft: 1, Hard: 1}, "nproc": {Soft: 2, Hard: 2}, "nofile": {Soft: 3, Hard: 3}, "memlock": {Soft: 4, Hard: 4}, "core": {Soft: 5, Hard: 5}, "as": {Soft: 6, Hard: 6}}
	if got := strings.Join(many.Args(), " "); got != "as=6 core=5 memlock=4 nofile=3 nproc=2 stack=1" {
		t.Errorf("ulimit args = %q, want name order", got)
	}
	for _, k := range []string{"shm_size", "ulimits"} {
		if indexOfStr(p.Services["web"].Unsupported, k) >= 0 {
			t.Errorf("%s is read and must not be listed as ignored, got %v", k, p.Services["web"].Unsupported)
		}
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{"shm_size: \"1073741824\"", "nofile: 65536", "soft: 100", "hard: 200"} {
		if !strings.Contains(out, want) {
			t.Errorf("config output should show %q, got:\n%s", want, out)
		}
	}
}

// Aliases and a `<<:` merge key are read as docker compose reads them: the
// merged mapping's limits come in, a limit written here wins, an aliased
// value is the value it points at.
func TestUlimitsReadAliasesAndMergeKeys(t *testing.T) {
	p, err := Load(writeTemp(t, "x-lim: &lim {nofile: 4096, nproc: 50}\nx-n: &n 8192\nx-u: &u {nofile: 1}\nservices:\n  web:\n    image: a\n    ulimits:\n      <<: *lim\n      nproc: 10\n      stack: *n\n  b:\n    image: a\n    ulimits: *u\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Ulimits.Args(), " "); got != "nofile=4096 nproc=10 stack=8192" {
		t.Errorf("ulimits with a merge key = %q, want the merged nofile, the local nproc winning, and the aliased stack", got)
	}
	if got := strings.Join(p.Services["b"].Ulimits.Args(), " "); got != "nofile=1" {
		t.Errorf("ulimits through an alias = %q, want nofile=1", got)
	}
}

// `<<:` follows YAML's precedence, the one docker compose's reader applies:
// a key written in the mapping wins wherever it stands, an earlier source
// in a list wins over a later one, a merged source's own `<<:` is followed,
// and a source that is not a mapping is refused.
func TestUlimitsMergeKeysFollowYamlPrecedence(t *testing.T) {
	head := "x-a: &a {nofile: 1, nproc: 1}\nx-b: &b {nofile: 2, stack: 2}\nx-base: &base {nofile: 100, core: 100}\nx-mid: &mid {nofile: 200, <<: *base}\nx-n: &n 5\nx-l: &l [*a, *b]\nx-badmid: &badmid {nofile: 3, <<: *n}\n"
	for _, tc := range []struct{ name, ulimits, want string }{
		{"a local key before the merge key still wins", "{nproc: 10, <<: *a}", "nofile=1 nproc=10"},
		{"a local key after the merge key wins", "{<<: *a, nproc: 10}", "nofile=1 nproc=10"},
		{"the earlier source in a list wins", "{<<: [*a, *b]}", "nofile=1 nproc=1 stack=2"},
		{"a source's own merge key is followed, its own keys winning", "{<<: *mid}", "core=100 nofile=200"},
		{"a list reached through an alias is walked like a written one", "{<<: *l}", "nofile=1 nproc=1 stack=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, head+"services:\n  web:\n    image: a\n    ulimits: "+tc.ulimits+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(p.Services["web"].Ulimits.Args(), " "); got != tc.want {
				t.Errorf("ulimits %s = %q, want %q", tc.ulimits, got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, ulimits string }{
		{"a scalar source", "{<<: *n}"},
		{"a list holding a scalar", "{<<: [*a, 7]}"},
		{"a scalar source one level down, inside a source's own merge key", "{<<: *badmid}"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := Load(writeTemp(t, head+"services:\n  web:\n    image: a\n    ulimits: "+tc.ulimits+"\n"))
			if err == nil || !strings.Contains(err.Error(), "merge requires a mapping or a list of mappings") {
				t.Errorf("want the merge source refused, got: %v", err)
			}
		})
	}
}

func TestShmSizeAndUlimitsRefusals(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"shm_size that is not a size", "services:\n  web:\n    image: a\n    shm_size: big\n", "shm_size \"big\" is not a size"},
		{"shm_size that rounds to nothing", "services:\n  web:\n    image: a\n    shm_size: \"0.5\"\n", "shm_size \"0.5\" is not a size"},
		{"shm_size of zero", "services:\n  web:\n    image: a\n    shm_size: 0\n", "shm_size \"0\" is not a size"},
		{"a mapping hard that is not a number", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: {soft: 1, hard: abc}\n", "ulimits nofile.hard must be a non-negative whole number"},
		{"a mapping soft that is negative", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: {soft: -1, hard: 2}\n", "ulimits nofile.soft must be a non-negative whole number"},
		{"ulimits that is not a mapping", "services:\n  web:\n    image: a\n    ulimits: notamap\n", "ulimits must be a mapping, got a single value"},
		{"ulimits as a list", "services:\n  web:\n    image: a\n    ulimits: [nofile]\n", "ulimits must be a mapping, got a list"},
		{"a bare ulimits", "services:\n  web:\n    image: a\n    ulimits:\n", "ulimits: expected a mapping, got nothing"},
		{"a bare shm_size", "services:\n  web:\n    image: a\n    shm_size:\n", "shm_size: expected a size, got nothing"},
		{"a limit that is not a number", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: abc\n", "ulimits nofile must be a non-negative whole number"},
		{"a negative limit", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: -1\n", "ulimits nofile must be a non-negative whole number"},
		{"a mapping missing hard", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: {soft: 1}\n", "must set both soft and hard"},
		{"a mapping with an unknown key", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: {soft: 1, hard: 2, max: 3}\n", "has a key it does not take: max"},
		{"a limit that is a list", "services:\n  web:\n    image: a\n    ulimits:\n      nofile: [1]\n", "must be a whole number or"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
}
