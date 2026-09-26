package compose

import (
	"fmt"
	"strings"
	"testing"
)

// A long-form mount's `target` that is a number, a boolean or a date is refused, as docker
// compose refuses it (v5.5.1, `config`, measured 2026-09-26: `service volume
// services.s.volumes.[0] is missing a mount target`, for 5, true, 1.5, 0, -1 and 2026-09-20).
// Read into a string it was the path `5`, and `up` passed `-v <volume>:5` to the
// runtime, which takes it.
func TestALongFormMountTargetThatIsNotText(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		refused      bool
	}{
		{"an integer", "5", true},
		{"a float", "1.5", true},
		{"zero", "0", true},
		{"a negative integer", "-1", true},
		{"a boolean", "true", true},
		{"a boolean written false", "false", true},
		{"a hexadecimal number", "0x10", true},
		{"a date", "2026-09-20", true},
		// The controls: text is text, whatever it looks like, and a path is a path.
		{"a number written as text", `"5"`, false},
		{"a path", "/data", false},
		{"a relative path", "data", false},
		{"a boolean written as text", `"true"`, false},
	} {
		for _, form := range []struct{ name, entry string }{
			{"a volume", "{type: volume, source: v, target: %s}"},
			{"an anonymous volume", "{type: volume, target: %s}"},
			{"a bind", "{type: bind, source: ./x, target: %s}"},
			{"a tmpfs", "{type: tmpfs, target: %s}"},
		} {
			t.Run(tc.name+" in "+form.name, func(t *testing.T) {
				body := "services:\n  s:\n    image: alpine\n    volumes:\n      - " + fmt.Sprintf(form.entry, tc.target) + "\nvolumes: {v: {}}\n"
				_, err := Load(writeTemp(t, body))
				if tc.refused {
					if err == nil || !strings.Contains(err.Error(), "is not a path") || !strings.Contains(err.Error(), "volumes entry 1 of 1") {
						t.Fatalf("want the target refused as not a path, naming the entry, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("want the target taken, got %v", err)
				}
			})
		}
	}
}

// The entry named is the entry that has it, and an alias to a number is a number.
func TestTheMountTargetRefusalNamesTheEntryAndFollowsAnAlias(t *testing.T) {
	_, err := Load(writeTemp(t, "services:\n  s:\n    image: alpine\n    volumes:\n      - ./a:/a\n      - {type: volume, source: v, target: /b}\n      - {type: volume, target: 7}\nvolumes: {v: {}}\n"))
	if err == nil || !strings.Contains(err.Error(), "volumes entry 3 of 3") {
		t.Errorf("want the third entry named, got %v", err)
	}
	_, err = Load(writeTemp(t, "x-n: &n 9\nservices:\n  s:\n    image: alpine\n    volumes:\n      - {type: volume, target: *n}\n"))
	if err == nil || !strings.Contains(err.Error(), "is not a path") {
		t.Errorf("want a target written through an alias read as the number it is, got %v", err)
	}
}

// A target that a `<<` merge brings in is the target the entry has: the merge is
// expanded when the entry is decoded, and the number it holds is read as the path
// `5`, exactly as one written in the entry is. A key written in the entry outranks
// the merge's, so a text target beside a merged number is the text.
func TestAMountTargetBroughtInByAMergeKey(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"a number by a merge key", "x-m: &m {target: 5}\nservices:\n  s:\n    image: alpine\n    volumes:\n      - <<: *m\n        type: volume\n        source: v\nvolumes: {v: {}}\n", true},
		{"a number by a list of merges", "x-a: &a {type: volume}\nx-m: &m {target: 5}\nservices:\n  s:\n    image: alpine\n    volumes:\n      - <<: [*a, *m]\n        source: v\nvolumes: {v: {}}\n", true},
		{"a text target beside a merged number", "x-m: &m {target: 5}\nservices:\n  s:\n    image: alpine\n    volumes:\n      - <<: *m\n        type: volume\n        source: v\n        target: /data\nvolumes: {v: {}}\n", false},
		{"a merged text target", "x-m: &m {target: \"5\"}\nservices:\n  s:\n    image: alpine\n    volumes:\n      - <<: *m\n        type: volume\n        source: v\nvolumes: {v: {}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if tc.refused && (err == nil || !strings.Contains(err.Error(), "is not a path")) {
				t.Fatalf("want the merged number refused, got %v", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("want it taken, got %v", err)
			}
		})
	}
}
