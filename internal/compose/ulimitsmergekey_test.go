package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge key in the long form of a ulimit (`nofile: {<<: *l}`) brings in what it names, as docker compose v5.5.1 reads it (`config`, every row measured, rc and
// the soft and hard it comes to): the mapping's own keys win, an earlier source of a list wins over a later one, and a merge in a merge is read. What the merge
// brings in is read as what is written there: a key `ulimits` does not take, or a number that is not one, is refused through it (#1903).
func TestAUlimitInTheLongFormTakesWhatAMergeKeyBringsIn(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n    ulimits:\n"
	for _, tc := range []struct {
		name, body string
		soft, hard int64
		refused    bool
		says       string // what the refusal says, where it says it
	}{
		{"a merge key brings in both", "x-l: &l {soft: 1, hard: 2}\n" + svc + "      nofile: {<<: *l}\n", 1, 2, false, ""},
		{"the mapping's own key wins", "x-l: &l {soft: 1, hard: 2}\n" + svc + "      nofile: {soft: 3, <<: *l}\n", 3, 2, false, ""},
		{"the mapping's own key wins when it is written after the merge key", "x-l: &l {soft: 1, hard: 2}\n" + svc + "      nofile: {<<: *l, soft: 3}\n", 3, 2, false, ""},
		{"a list of sources, each bringing one", "x-l: &l {soft: 1}\nx-m: &m {hard: 2}\n" + svc + "      nofile: {<<: [*l, *m]}\n", 1, 2, false, ""},
		{"an earlier source of a list wins", "x-l: &l {soft: 1, hard: 9}\nx-m: &m {soft: 5, hard: 2}\n" + svc + "      nofile: {<<: [*m, *l]}\n", 5, 2, false, ""},
		{"its own key and the earlier source win in turn", "x-l: &l {soft: 1, hard: 9}\nx-m: &m {soft: 5, hard: 2}\n" + svc + "      nofile: {hard: 7, <<: [*m, *l]}\n", 5, 7, false, ""},
		{"a merge in a merge is read", "x-a: &a {soft: 8}\nx-b: &b {hard: 9, <<: *a}\n" + svc + "      nofile: {<<: *b}\n", 8, 9, false, ""},
		{"a mapping written there is merged as well", svc + "      nofile: {<<: {soft: 4, hard: 6}}\n", 4, 6, false, ""},
		{"a merge key that brings in only soft leaves hard missing", "x-l: &l {soft: 1}\n" + svc + "      nofile: {<<: *l}\n", 0, 0, true, ""},
		{"a key ulimits does not take, brought in by a merge", svc + "      nofile: {soft: 1, hard: 2, <<: {x: 1}}\n", 0, 0, true, ""},
		{"a number that is not one, brought in by a merge", "x-l: &l {soft: many, hard: 2}\n" + svc + "      nofile: {<<: *l}\n", 0, 0, true, ""},
		{"a merge of something that is not a mapping", "x-s: &s 5\n" + svc + "      nofile: {<<: *s}\n", 0, 0, true, "`<<:` merge requires a mapping"},
		{"a key that is not soft or hard, written beside a merge key", "x-l: &l {soft: 1, hard: 2}\n" + svc + "      nofile: {limit: 3, <<: *l}\n", 0, 0, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{path}, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if tc.refused {
				if !strings.Contains(err.Error(), "ulimits") {
					t.Errorf("refused, but not by the ulimits: %v", err)
				}
				if tc.says != "" && !strings.Contains(err.Error(), tc.says) {
					t.Errorf("the refusal should say %q, got: %v", tc.says, err)
				}
				return
			}
			got := p.Services["s"].Ulimits["nofile"]
			if got.Soft != tc.soft || got.Hard != tc.hard {
				t.Errorf("nofile is soft %d hard %d, want soft %d hard %d", got.Soft, got.Hard, tc.soft, tc.hard)
			}
		})
	}
}
