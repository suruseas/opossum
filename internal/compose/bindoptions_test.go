package compose

import (
	"strings"
	"testing"
)

// The options of a mount's `bind:` block are checked for their type and their words as docker
// compose checks them (#1177): `propagation` is a string, `selinux` is `z` or `Z`, `recursive` is
// `enabled`, `disabled`, `writable` or `readonly`, `create_host_path` is a boolean or a word of
// one. Nothing in the block is read here (it is listed among the ignored fields whole), so a
// value docker compose refuses went through; the names of the keys were already asked of its
// schema (#1248). Every value below was written under each option and given to `docker compose
// config` (v5.5.1, 2026-10-05): accepted is rc 0, refused is rc 1 (`must be a string`, `must be
// a boolean`, `invalid boolean`, `must be one of …`).
func TestABindOptionIsCheckedAsDockerComposeChecksIt(t *testing.T) {
	for option, vals := range map[string]struct{ accepted, refused []string }{
		"propagation": {
			accepted: []string{`rshared`, `z`, `Z`, `zz`, `enabled`, `readonly`, `writable`, `disabled`, `yes`, `no`, `on`, `off`, `y`, `n`, `t`, `f`, `tRuE`, `Y`, `"y"`, `"t"`, `"1"`, `"0"`, `""`, `abc`, `!!str 5`, `!!binary aGk=`, `!custom x`, `"5"`, `private`, `rprivate`},
			refused:  []string{`true`, `false`, `True`, `TRUE`, `1`, `0`, `5`, `1.5`, `null`, `~`, `[a]`, `{a: 1}`, `[]`, `{}`, `2020-01-01`},
		},
		"selinux": {
			accepted: []string{`z`, `Z`},
			refused:  []string{`rshared`, `zz`, `enabled`, `readonly`, `writable`, `disabled`, `true`, `false`, `yes`, `no`, `on`, `off`, `y`, `n`, `t`, `f`, `True`, `TRUE`, `tRuE`, `Y`, `"y"`, `"t"`, `"1"`, `"0"`, `1`, `0`, `5`, `1.5`, `null`, `~`, `""`, `abc`, `[a]`, `{a: 1}`, `[]`, `{}`, `2020-01-01`, `!!str 5`, `!!binary aGk=`, `!custom x`, `"5"`, `private`, `rprivate`},
		},
		"recursive": {
			accepted: []string{`enabled`, `readonly`, `writable`, `disabled`},
			refused:  []string{`rshared`, `z`, `Z`, `zz`, `true`, `false`, `yes`, `no`, `on`, `off`, `y`, `n`, `t`, `f`, `True`, `TRUE`, `tRuE`, `Y`, `"y"`, `"t"`, `"1"`, `"0"`, `1`, `0`, `5`, `1.5`, `null`, `~`, `""`, `abc`, `[a]`, `{a: 1}`, `[]`, `{}`, `2020-01-01`, `!!str 5`, `!!binary aGk=`, `!custom x`, `"5"`, `private`, `rprivate`},
		},
		"create_host_path": {
			accepted: []string{`true`, `false`, `yes`, `no`, `on`, `off`, `y`, `n`, `True`, `TRUE`, `tRuE`, `Y`, `"y"`},
			refused:  []string{`rshared`, `z`, `Z`, `zz`, `enabled`, `readonly`, `writable`, `disabled`, `t`, `f`, `"t"`, `"1"`, `"0"`, `1`, `0`, `5`, `1.5`, `null`, `~`, `""`, `abc`, `[a]`, `{a: 1}`, `[]`, `{}`, `2020-01-01`, `!!str 5`, `!!binary aGk=`, `!custom x`, `"5"`, `private`, `rprivate`},
		},
	} {
		for want, list := range map[bool][]string{true: vals.accepted, false: vals.refused} {
			for _, v := range list {
				t.Run(option+"="+v, func(t *testing.T) {
					body := "services:\n  app:\n    image: alpine\n    volumes:\n      - type: bind\n        source: .\n        target: /d\n        bind: {" + option + ": " + v + "}\n"
					_, err := Load(writeTemp(t, body))
					if (err == nil) != want {
						t.Errorf("bind: {%s: %s} loads = %v, docker compose accepts it = %v", option, v, err == nil, want)
					}
					if err != nil && !strings.Contains(err.Error(), "volumes entry 1.bind."+option) {
						t.Errorf("the refusal does not name the option: %v", err)
					}
				})
			}
		}
	}
}

// The block itself has to be a mapping: `bind: foo`, `bind: [a]` and `bind:` with nothing are
// refused as docker compose refuses them (`bind must be a mapping`), where they were listed among
// the ignored fields. An alias to a mapping, `{}` and an `x-` key are taken.
func TestABindBlockThatIsNotAMappingIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, bind string
		refused    bool
	}{
		{"a word", "bind: foo", true},
		{"a list", "bind: [a]", true},
		{"nothing", "bind:", true},
		{"null", "bind: null", true},
		{"a number", "bind: 5", true},
		{"an empty mapping", "bind: {}", false},
		{"an extension key", "bind: {x-note: 1}", false},
		{"an alias to a mapping", "bind: *b", false},
		{"an alias to a word", "bind: *w", true},
		{"an option through an alias", "bind: {propagation: *w}", false},
		{"a number through an alias", "bind: {propagation: *n}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "x-b: &b {propagation: rshared}\nx-w: &w foo\nx-n: &n 5\nservices:\n  app:\n    image: alpine\n    volumes:\n      - type: bind\n        source: .\n        target: /d\n        " + tc.bind + "\n"
			_, err := Load(writeTemp(t, body))
			if (err != nil) != tc.refused {
				t.Errorf("%q: load error = %v, want refused = %v", tc.bind, err, tc.refused)
			}
			if err != nil && tc.name != "a number through an alias" && !strings.Contains(err.Error(), "volumes entry 1.bind") {
				t.Errorf("the refusal does not say where: %v", err)
			}
		})
	}
}

// The options are read in every entry of a service's `volumes:`, the second one as well, and in a
// `type: volume` or `type: tmpfs` mount (docker compose checks `bind:` whatever the type is), and the
// message says where and on which line.
func TestABindOptionIsCheckedInEveryEntryAndUnderEveryType(t *testing.T) {
	for _, typ := range []string{"bind", "volume", "tmpfs"} {
		t.Run(typ, func(t *testing.T) {
			target := "      - type: " + typ + "\n        target: /d\n        bind: {selinux: q}\n"
			if typ == "bind" {
				target = "      - type: bind\n        source: .\n        target: /d\n        bind: {selinux: q}\n"
			}
			_, err := Load(writeTemp(t, "services:\n  app:\n    image: alpine\n    volumes:\n      - ./a:/a\n"+target))
			want := "volumes entry 2.bind.selinux must be one of z, Z (line 8)"
			if typ == "bind" {
				want = "volumes entry 2.bind.selinux must be one of z, Z (line 9)"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("type %s: err = %v, want it to contain %q", typ, err, want)
			}
		})
	}
}

// What the refusal says about the value it refused: what it read it as, so that the person who
// wrote `create_host_path: 1` or `propagation: yes please` finds what is wrong (a number is not
// a boolean, a list is not a string), and the line.
func TestABindOptionRefusalSaysWhatItReadTheValueAs(t *testing.T) {
	for _, tc := range []struct{ option, value, want string }{
		{"create_host_path", "1", "volumes entry 1.bind.create_host_path must be true or false, got a number (line 8)"},
		{"create_host_path", "null", "volumes entry 1.bind.create_host_path must be true or false, got nothing (line 8)"},
		{"create_host_path", "[a]", "volumes entry 1.bind.create_host_path must be true or false, got a list (line 8)"},
		{"create_host_path", "{a: 1}", "volumes entry 1.bind.create_host_path must be true or false, got a mapping (line 8)"},
		{"create_host_path", "maybe", "volumes entry 1.bind.create_host_path must be true or false, got a word that is not one (line 8)"},
		{"propagation", "5", "volumes entry 1.bind.propagation must be a string, got a number (line 8)"},
		{"propagation", "true", "volumes entry 1.bind.propagation must be a string, got true/false (line 8)"},
		{"propagation", "2020-01-01", "volumes entry 1.bind.propagation must be a string, got a date (line 8)"},
		{"propagation", "~", "volumes entry 1.bind.propagation must be a string, got nothing (line 8)"},
		{"recursive", "true", "volumes entry 1.bind.recursive must be a string, got true/false (line 8)"},
		{"recursive", "all", `volumes entry 1.bind.recursive must be one of enabled, disabled, writable, readonly (line 8)`},
		{"selinux", "x", `volumes entry 1.bind.selinux must be one of z, Z (line 8)`},
	} {
		t.Run(tc.option+"="+tc.value, func(t *testing.T) {
			body := "services:\n  app:\n    image: alpine\n    volumes:\n      - type: bind\n        source: .\n        target: /d\n        bind: {" + tc.option + ": " + tc.value + "}\n"
			_, err := Load(writeTemp(t, body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// More than one option in a block: each is read, the ones after a correct one too (a boolean that
// is right does not end the look), and the first that is wrong is the one named. docker compose
// refuses all of these (v5.5.1).
func TestEveryOptionOfABindBlockIsRead(t *testing.T) {
	for _, tc := range []struct{ bind, want string }{
		{"{selinux: z, propagation: 5}", "bind.propagation must be a string"},
		{"{create_host_path: true, selinux: q}", "bind.selinux must be one of z, Z"},
		{"{propagation: rshared, create_host_path: 5}", "bind.create_host_path must be true or false"},
		{"{propagation: rshared, selinux: z, recursive: enabled, create_host_path: yes, x-note: 1}", ""},
		{"{propagation: rshared, selinux: z, recursive: enabled, create_host_path: yes, selinux2: 1}", "is not a key docker compose takes"},
	} {
		t.Run(tc.bind, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  app:\n    image: alpine\n    volumes:\n      - type: bind\n        source: .\n        target: /d\n        bind: "+tc.bind+"\n"))
			if tc.want == "" {
				if err != nil {
					t.Errorf("loads with %v, want it taken", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A value written as `!!binary` is the text its base64 stands for, as docker compose reads it
// (v5.5.1: `create_host_path: !!binary dHJ1ZQ==` is `true`, `selinux: !!binary eg==` is `z`), and
// a key is the option whatever its tag (`!custom selinux: q` is refused as `selinux: q` is).
func TestABindValueTagIsReadAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct {
		bind    string
		refused bool
	}{
		{"{create_host_path: !!binary dHJ1ZQ==}", false},
		{"{create_host_path: !!binary eWVz}", false},
		{"{selinux: !!binary eg==}", false},
		{"{recursive: !!binary cmVhZG9ubHk=}", false},
		{"{selinux: !!binary eA==}", true},
		{"{create_host_path: !!binary MQ==}", true},
		{"{!custom selinux: q}", true},
		{"{!custom selinux: z}", false},
		{"{selinux: !custom z}", false},
		{"{selinux: !custom q}", true},
	} {
		t.Run(tc.bind, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  app:\n    image: alpine\n    volumes:\n      - type: bind\n        source: .\n        target: /d\n        bind: "+tc.bind+"\n"))
			if (err != nil) != tc.refused {
				t.Errorf("bind: %s loads with %v, docker compose refuses it = %v", tc.bind, err, tc.refused)
			}
		})
	}
}

// The refusal's line is the value's, in the block form as well, where the block opens a line
// above its options; the block's own line is where `bind: foo` and `bind:` are; and the words
// for a value are as before (case and a space count: `Enabled` and `"z "` are not the words).
func TestABindRefusalNamesTheLineOfWhatIsWrong(t *testing.T) {
	const head = "services:\n  app:\n    image: alpine\n    volumes:\n      - type: bind\n        source: .\n        target: /d\n"
	for _, tc := range []struct{ name, bind, want string }{
		{"an option in the block form", "        bind:\n          propagation: rshared\n          selinux: q\n", "bind.selinux must be one of z, Z (line 10)"},
		{"a type in the block form", "        bind:\n          create_host_path: true\n          propagation: 5\n", "bind.propagation must be a string, got a number (line 10)"},
		{"a boolean word in the block form", "        bind:\n          propagation: rshared\n          create_host_path: maybe\n", "bind.create_host_path must be true or false, got a word that is not one (line 10)"},
		{"a boolean type in the block form", "        bind:\n          propagation: rshared\n          create_host_path: 5\n", "bind.create_host_path must be true or false, got a number (line 10)"},
		{"a word", "        bind: foo\n", "volumes entry 1.bind must be a mapping, got a single value (line 8)"},
		{"nothing", "        bind:\n        x-k: 1\n", "volumes entry 1.bind must be a mapping, got nothing (line 8)"},
		{"a list", "        bind: [a]\n", "volumes entry 1.bind must be a mapping, got a list (line 8)"},
		{"a float", "        bind: {propagation: 1.5}\n", "bind.propagation must be a string, got a number (line 8)"},
		{"a word of another case", "        bind: {recursive: Enabled}\n", "bind.recursive must be one of enabled, disabled, writable, readonly (line 8)"},
		{"a word with a space", "        bind: {selinux: \"z \"}\n", "bind.selinux must be one of z, Z (line 8)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, head+tc.bind))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
