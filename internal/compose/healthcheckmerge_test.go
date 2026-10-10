package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The keys of the `healthcheck` of a service nothing takes that a merge key brings in count as those written there, the ones written in the mapping standing over them: a word
// for `retries` or `disable` is refused wherever it comes from, and one that a key written there stands over is not (measured, v5.5.1, `config -q`, every row; #1991).
func TestTheKeysAMergeKeyBringsIntoAHealthcheckOfAServiceNothingTakesCount(t *testing.T) {
	const header = "x-r: &r {retries: abc}\nx-d: &d {disable: abc}\nx-g: &g {interval: 5}\nx-ml: &ml [{retries: abc}]\n"
	for _, tc := range []struct {
		health  string
		refused bool
	}{
		{"{<<: {retries: abc}, retries: ~}", false},
		{"{<<: {retries: ~}, a: ~}", false},
		{"{<<: {interval: abc}, retries: ~}", false},
		{"{<<: {test: ~}, retries: 3}", false},
		{"{<<: {retries: 3}, a: ~}", false},
		{"{<<: {retries: abc}, interval: abc}", true},
		{"{<<: {disable: abc}, interval: abc}", true},
		{"{<<: {disable: abc}, retries: 3}", true},
		{"{<<: *r, a: ~}", true},
		{"{<<: *d, a: ~}", true},
		{"{<<: [*r], a: ~}", true},
		{"{<<: [*g, *r]}", true},
		{"{<<: [*r, *g]}", true},
		{"{<<: {<<: {retries: abc}}}", true},
		{"{<<: {retries: abc}, retries: 3}", false},
		{"{<<: {retries: 3}, retries: abc}", true},
		{"{<<: {retries: true}, a: ~}", false},
		{"{<<: {retries: [1]}}", false},
		{"{<<: {retries: [1]}, retries: 2}", false},
		{"{<<: {disable: 5}}", false},
		{"{<<: {test: [CMD, x]}, interval: 5}", false},
		{"{<<: *r}", true},
		{"{retries: 3, <<: *r}", false},
		{"{<<: *r, retries: 3}", false},
		{"{<<: [{retries: abc}, {retries: 3}]}", true},
		{"{<<: [{retries: 3}, {retries: abc}]}", false},
		{"{<<: *ml}", true},
		{"{<<: *ml, retries: 3}", false},
	} {
		t.Run(tc.health, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    extends: {file: base.yaml, service: x}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(header+"services:\n  x:\n    image: xi\n  y:\n    image: yi\n    healthcheck: "+tc.health+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
