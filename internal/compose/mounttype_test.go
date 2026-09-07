package compose

// A mount written in the long form without its `type` (#784, from the
// service-key sweep). docker compose (v5.5.0) needs the type there: an
// entry without one matches neither the short form nor the long and is
// refused (`services.web.volumes.0 must be a string`), and an empty one is
// refused by the list of what it may be (`value must be one of 'bind',
// 'volume', 'tmpfs', …`). opossum read `{target: /x}` and `{source: ./a,
// target: /x}` as if the type were left to the runtime to guess from the
// source. A bare `type:` was already refused as a key with nothing after it.

import (
	"strings"
	"testing"
)

func TestALongFormMountWithoutItsTypeIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n    volumes:\n"
	for _, tc := range []struct{ name, body, want string }{
		{"target only", svc + "      - target: /x\n", "volumes entry 1 of 1 has no type — write `type: bind`, `type: volume` or `type: tmpfs`"},
		{"source and target", svc + "      - {source: ./a, target: /x}\n", "volumes entry 1 of 1 has no type"},
		{"an empty type", svc + "      - {type: \"\", target: /x}\n", "volumes entry 1 of 1 has no type"},
		{"the second of two", svc + "      - ./a:/a\n      - {target: /x}\n", "volumes entry 2 of 2 has no type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// A bind mount without `source` (#816): docker compose refuses it
	// (`invalid mount config for type "bind": field Source must not be
	// empty`); read past, it became an anonymous volume, which is not the
	// kind of mount that was written. `source: ""` is a different case —
	// docker compose reads it as the project directory — and is left as
	// it was (an anonymous volume here; a known difference).
	for _, tc := range []struct{ name, body, want string }{
		{"a bind mount with no source", svc + "      - {type: bind, target: /x}\n", "volumes entry 1 of 1 is a bind mount with no source — write the host path, as in `source: ./data`, or `type: volume` for a volume"},
		{"the second of two, a bind mount with no source", svc + "      - ./a:/a\n      - {type: bind, target: /x}\n", "volumes entry 2 of 2 is a bind mount with no source"},
		{"the first of two, a bind mount with no source", svc + "      - {type: bind, target: /x}\n      - ./a:/a\n", "volumes entry 1 of 2 is a bind mount with no source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Each of the three types is read as before.
	for _, tc := range []struct{ name, item, want string }{
		{"bind", "{type: bind, source: ./a, target: /x}", "./a:/x"},
		{"volume", "{type: volume, source: data, target: /x}", "data:/x"},
		{"an anonymous volume", "{type: volume, target: /x}", "/x"},
		{"a bind mount with an empty source, as before", "{type: bind, source: \"\", target: /x}", "/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, svc+"      - "+tc.item+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Volumes; len(got) != 1 || got[0] != tc.want {
				t.Errorf("volumes = %v, want [%s]", got, tc.want)
			}
		})
	}
	t.Run("tmpfs", func(t *testing.T) {
		p, err := Load(writeTemp(t, svc+"      - {type: tmpfs, target: /x}\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["web"].Tmpfs; len(got) != 1 || got[0] != "/x" {
			t.Errorf("tmpfs = %v, want [/x]", got)
		}
	})
}
