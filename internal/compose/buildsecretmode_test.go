package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The `mode` of an entry of a `build`'s `secrets` is asked as the `mode` of an entry of a service's `secrets` is: `-0` is a float to docker compose and is refused, in the entry,
// through a merge key and an alias, and in a `build` a merge key brings in; a quoted `-0`, an octal, a whole number and `0` are read (measured, v5.5.1, `config -q`, every
// row; #1865). `mode` is what the anchor `x-m` holds for the rows that merge it.
func TestAModeOfNegativeZeroInABuildSecretIsRefusedAsInAServiceSecret(t *testing.T) {
	for _, tc := range []struct {
		name, build, mode string
		refused           bool
	}{
		{"build.secrets entry / -0", "build: {context: ., secrets: [{source: s, mode: -0}]}", "-0", true},
		{"build.secrets entry / -0.0", "build: {context: ., secrets: [{source: s, mode: -0.0}]}", "-0.0", true},
		{"build.secrets entry / \"-0\"", "build: {context: ., secrets: [{source: s, mode: \"-0\"}]}", "\"-0\"", false},
		{"build.secrets entry / 0o400", "build: {context: ., secrets: [{source: s, mode: 0o400}]}", "0o400", false},
		{"build.secrets entry / 0400", "build: {context: ., secrets: [{source: s, mode: 0400}]}", "0400", false},
		{"build.secrets entry / \"0400\"", "build: {context: ., secrets: [{source: s, mode: \"0400\"}]}", "\"0400\"", false},
		{"build.secrets entry / 400", "build: {context: ., secrets: [{source: s, mode: 400}]}", "400", false},
		{"build.secrets entry / 1.5", "build: {context: ., secrets: [{source: s, mode: 1.5}]}", "1.5", true},
		{"build.secrets entry / true", "build: {context: ., secrets: [{source: s, mode: true}]}", "true", true},
		{"build.secrets entry / ~", "build: {context: ., secrets: [{source: s, mode: ~}]}", "~", true},
		{"build.secrets entry / -1", "build: {context: ., secrets: [{source: s, mode: -1}]}", "-1", false},
		{"build.secrets entry / +0", "build: {context: ., secrets: [{source: s, mode: +0}]}", "+0", false},
		{"build.secrets entry / 0", "build: {context: ., secrets: [{source: s, mode: 0}]}", "0", false},
		{"build.secrets entry / \"0\"", "build: {context: ., secrets: [{source: s, mode: \"0\"}]}", "\"0\"", false},
		{"build.secrets entry / \"abc\"", "build: {context: ., secrets: [{source: s, mode: \"abc\"}]}", "\"abc\"", true},
		{"build.secrets via merge key / -0", "build: {context: ., secrets: [{source: s, <<: {mode: -0}}]}", "-0", true},
		{"build.secrets via merge key / -0.0", "build: {context: ., secrets: [{source: s, <<: {mode: -0.0}}]}", "-0.0", true},
		{"build.secrets via merge key / \"-0\"", "build: {context: ., secrets: [{source: s, <<: {mode: \"-0\"}}]}", "\"-0\"", false},
		{"build.secrets via merge key / 0o400", "build: {context: ., secrets: [{source: s, <<: {mode: 0o400}}]}", "0o400", false},
		{"build.secrets via merge key / 0400", "build: {context: ., secrets: [{source: s, <<: {mode: 0400}}]}", "0400", false},
		{"build.secrets via merge key / \"0400\"", "build: {context: ., secrets: [{source: s, <<: {mode: \"0400\"}}]}", "\"0400\"", false},
		{"build.secrets via merge key / 400", "build: {context: ., secrets: [{source: s, <<: {mode: 400}}]}", "400", false},
		{"build.secrets via merge key / 1.5", "build: {context: ., secrets: [{source: s, <<: {mode: 1.5}}]}", "1.5", true},
		{"build.secrets via merge key / true", "build: {context: ., secrets: [{source: s, <<: {mode: true}}]}", "true", true},
		{"build.secrets via merge key / ~", "build: {context: ., secrets: [{source: s, <<: {mode: ~}}]}", "~", true},
		{"build.secrets via merge key / -1", "build: {context: ., secrets: [{source: s, <<: {mode: -1}}]}", "-1", false},
		{"build.secrets via merge key / +0", "build: {context: ., secrets: [{source: s, <<: {mode: +0}}]}", "+0", false},
		{"build.secrets via merge key / 0", "build: {context: ., secrets: [{source: s, <<: {mode: 0}}]}", "0", false},
		{"build.secrets via merge key / \"0\"", "build: {context: ., secrets: [{source: s, <<: {mode: \"0\"}}]}", "\"0\"", false},
		{"build.secrets via merge key / \"abc\"", "build: {context: ., secrets: [{source: s, <<: {mode: \"abc\"}}]}", "\"abc\"", true},
		{"build.secrets via alias / -0", "build: {context: ., secrets: [{source: s, <<: *m}]}", "-0", true},
		{"build.secrets via alias / -0.0", "build: {context: ., secrets: [{source: s, <<: *m}]}", "-0.0", true},
		{"build.secrets via alias / \"-0\"", "build: {context: ., secrets: [{source: s, <<: *m}]}", "\"-0\"", false},
		{"build.secrets via alias / 0o400", "build: {context: ., secrets: [{source: s, <<: *m}]}", "0o400", false},
		{"build.secrets via alias / 0400", "build: {context: ., secrets: [{source: s, <<: *m}]}", "0400", false},
		{"build.secrets via alias / \"0400\"", "build: {context: ., secrets: [{source: s, <<: *m}]}", "\"0400\"", false},
		{"build.secrets via alias / 400", "build: {context: ., secrets: [{source: s, <<: *m}]}", "400", false},
		{"build.secrets via alias / 1.5", "build: {context: ., secrets: [{source: s, <<: *m}]}", "1.5", true},
		{"build.secrets via alias / true", "build: {context: ., secrets: [{source: s, <<: *m}]}", "true", true},
		{"build.secrets via alias / ~", "build: {context: ., secrets: [{source: s, <<: *m}]}", "~", true},
		{"build.secrets via alias / -1", "build: {context: ., secrets: [{source: s, <<: *m}]}", "-1", false},
		{"build.secrets via alias / +0", "build: {context: ., secrets: [{source: s, <<: *m}]}", "+0", false},
		{"build.secrets via alias / 0", "build: {context: ., secrets: [{source: s, <<: *m}]}", "0", false},
		{"build.secrets via alias / \"0\"", "build: {context: ., secrets: [{source: s, <<: *m}]}", "\"0\"", false},
		{"build.secrets via alias / \"abc\"", "build: {context: ., secrets: [{source: s, <<: *m}]}", "\"abc\"", true},
		{"build merged in / -0", "build: {<<: {context: ., secrets: [{source: s, mode: -0}]}}", "-0", true},
		{"build merged in / -0.0", "build: {<<: {context: ., secrets: [{source: s, mode: -0.0}]}}", "-0.0", true},
		{"build merged in / \"-0\"", "build: {<<: {context: ., secrets: [{source: s, mode: \"-0\"}]}}", "\"-0\"", false},
		{"build merged in / 0o400", "build: {<<: {context: ., secrets: [{source: s, mode: 0o400}]}}", "0o400", false},
		{"build merged in / 0400", "build: {<<: {context: ., secrets: [{source: s, mode: 0400}]}}", "0400", false},
		{"build merged in / \"0400\"", "build: {<<: {context: ., secrets: [{source: s, mode: \"0400\"}]}}", "\"0400\"", false},
		{"build merged in / 400", "build: {<<: {context: ., secrets: [{source: s, mode: 400}]}}", "400", false},
		{"build merged in / 1.5", "build: {<<: {context: ., secrets: [{source: s, mode: 1.5}]}}", "1.5", true},
		{"build merged in / true", "build: {<<: {context: ., secrets: [{source: s, mode: true}]}}", "true", true},
		{"build merged in / ~", "build: {<<: {context: ., secrets: [{source: s, mode: ~}]}}", "~", true},
		{"build merged in / -1", "build: {<<: {context: ., secrets: [{source: s, mode: -1}]}}", "-1", false},
		{"build merged in / +0", "build: {<<: {context: ., secrets: [{source: s, mode: +0}]}}", "+0", false},
		{"build merged in / 0", "build: {<<: {context: ., secrets: [{source: s, mode: 0}]}}", "0", false},
		{"build merged in / \"0\"", "build: {<<: {context: ., secrets: [{source: s, mode: \"0\"}]}}", "\"0\"", false},
		{"build merged in / \"abc\"", "build: {<<: {context: ., secrets: [{source: s, mode: \"abc\"}]}}", "\"abc\"", true},
		{"build is an alias to a build with a mode of -0", "build: *b", "-0", true},
		{"the secrets of the build are an alias to a list with a mode of -0", "build: {context: ., secrets: *l}", "-0", true},
		{"the service merges a mapping that brings a build with a mode of -0", "<<: *s", "-0", true},
		{"an anchor of a build with a mode of -0 that the service does not use", "build: {context: .}", "-0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			body := "x-m: &m {mode: " + tc.mode + "}\nx-b: &b {context: ., secrets: [{source: s, mode: -0}]}\nx-l: &l [{source: s, mode: -0}]\nx-s: &s {build: {context: ., secrets: [{source: s, mode: -0}]}}\nservices:\n  w:\n    image: wi\n    " + tc.build + "\nsecrets:\n  s: {file: ./f.txt}\n"
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
