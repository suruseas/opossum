package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// What docker compose answers (1 refuses, 0 reads) for a number, boolean, list or map key of a service an extends takes from another file, for each shape of its value (in the
// order of takenShapes), in three places: nothing writes the key over (the value stays), the extender writes a valid value over it, and a second `-f` file does (measured,
// v5.5.1, `config -q`, 30 keys, #1948, #2002). The service that results is asked after the extends and before a later file: a value that stays is refused, one the extender
// writes over is read, and one a second file writes over is refused as it was when the extends was read.
var takenShapes = []string{"5", "1.5", "true", "abc", `""`, "[x]", "[1]", "{a: b}", "{a: 1}", "[{a: b}]", "~", "[~]", "{a: ~}", "[{a: ~}]", "2024-01-01"}

var takenOverWith = map[string]string{
	"cpu_count":        "5",
	"cpu_period":       "5",
	"cpu_quota":        "5",
	"cpu_rt_period":    "5",
	"cpu_rt_runtime":   "5",
	"cpu_shares":       "5",
	"oom_score_adj":    "5",
	"pids_limit":       "5",
	"scale":            "5",
	"init":             "true",
	"oom_kill_disable": "true",
	"privileged":       "true",
	"read_only":        "true",
	"stdin_open":       "true",
	"tty":              "true",
	"cpu_percent":      "50",
	"cpus":             "1.5",
	"cap_add":          "[x]",
	"cap_drop":         "[x]",
	"dns_opt":          "[x]",
	"expose":           "[80]",
	"links":            "[x]",
	"models":           "[x]",
	"profiles":         "[x]",
	"dns":              "[x]",
	"dns_search":       "[x]",
	"tmpfs":            "[x]",
	"annotations":      "{a: b}",
	"configs":          "[]",
	"devices":          "[]",
	"secrets":          "[]",
	"volumes":          "[]",
}

var takenNothingOver = map[string]string{
	"cpu_count":        "011111111111111",
	"cpu_period":       "001111111111111",
	"cpu_quota":        "001111111111111",
	"cpu_rt_period":    "001111111111111",
	"cpu_rt_runtime":   "001111111111111",
	"cpu_shares":       "001111111111111",
	"oom_score_adj":    "011111111111111",
	"pids_limit":       "001111111111111",
	"scale":            "011111111111111",
	"init":             "110111111111111",
	"oom_kill_disable": "110111111111111",
	"privileged":       "110111111111111",
	"read_only":        "110111111111111",
	"stdin_open":       "110111111111111",
	"tty":              "110111111111111",
	"cpu_percent":      "011111111111111",
	"cpus":             "001111111111111",
	"cap_add":          "111110111111111",
	"cap_drop":         "111110111111111",
	"dns_opt":          "111110111111111",
	"expose":           "111110011111111",
	"links":            "111111111111111",
	"models":           "111111111111111",
	"profiles":         "111110111111111",
	"dns":              "111000111111111",
	"dns_search":       "111000111111111",
	"tmpfs":            "111000111111111",
	"annotations":      "111110100111011",
	"configs":          "111111111111111",
	"devices":          "111110111111111",
	"secrets":          "111111111111111",
	"volumes":          "111110111111111",
}

var takenExtenderOver = map[string]string{
	"cpu_count":        "000111111101110",
	"cpu_period":       "000111111101110",
	"cpu_quota":        "000111111101110",
	"cpu_rt_period":    "000111111101110",
	"cpu_rt_runtime":   "000111111101110",
	"cpu_shares":       "000111111101110",
	"oom_score_adj":    "000111111101110",
	"pids_limit":       "000111111101110",
	"scale":            "000111111101110",
	"init":             "000111111101110",
	"oom_kill_disable": "000111111101110",
	"privileged":       "000111111101110",
	"read_only":        "000111111101110",
	"stdin_open":       "000111111101110",
	"tty":              "000111111101110",
	"cpu_percent":      "000111111101110",
	"cpus":             "000111111101110",
	"cap_add":          "000000111101110",
	"cap_drop":         "000000111101110",
	"dns_opt":          "000000100101010",
	"expose":           "000000011101110",
	"links":            "111111111111111",
	"models":           "111111111111111",
	"profiles":         "000000111101110",
	"dns":              "000000100101010",
	"dns_search":       "000000100101010",
	"tmpfs":            "000000100101010",
	"annotations":      "000000100101010",
	"configs":          "000001111101110",
	"devices":          "000000111101110",
	"secrets":          "000001111101110",
	"volumes":          "000000111101110",
}

var takenSecondFileOver = map[string]string{
	"cpu_count":        "011111111111110",
	"cpu_period":       "001111111111110",
	"cpu_quota":        "001111111111110",
	"cpu_rt_period":    "001111111111110",
	"cpu_rt_runtime":   "001111111111110",
	"cpu_shares":       "001111111111110",
	"oom_score_adj":    "011111111111110",
	"pids_limit":       "001111111111110",
	"scale":            "011111111111110",
	"init":             "110111111111110",
	"oom_kill_disable": "110111111111110",
	"privileged":       "110111111111110",
	"read_only":        "110111111111110",
	"stdin_open":       "110111111111110",
	"tty":              "110111111111110",
	"cpu_percent":      "011111111111110",
	"cpus":             "001111111111110",
	"cap_add":          "111110111111111",
	"cap_drop":         "111110111111111",
	"dns_opt":          "111110111111111",
	"expose":           "111110011111111",
	"links":            "111111111111111",
	"models":           "111111111111111",
	"profiles":         "111110111111111",
	"dns":              "111000111111110",
	"dns_search":       "111000111111110",
	"tmpfs":            "111000111111110",
	"annotations":      "111110100111011",
	"configs":          "111111111111111",
	"devices":          "111110111111111",
	"secrets":          "111111111111111",
	"volumes":          "111110111111111",
}

// The cells where opossum answers otherwise than docker compose, as they were before (named, not made different here): a date for `dns`, `dns_search` and `tmpfs`, and `true` for `cpus`, are decided by the files' own checks.
var takenKnownNone = map[string]bool{"dns/2024-01-01": true, "dns_search/2024-01-01": true}
var takenKnownExtender = map[string]bool{}
var takenKnownSecond = map[string]bool{"cpus/true": true, "tmpfs/2024-01-01": true}

func TestAValueTheExtenderWritesOverIsReadAndOneThatStaysIsRefused(t *testing.T) {
	for _, place := range []struct {
		name  string
		table map[string]string
		known map[string]bool
		files func(key, value string) map[string]string
		order []string
	}{
		{"nothing writes it over", takenNothingOver, takenKnownNone, func(k, v string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": " + v + "\n"}
		}, []string{"compose.yaml"}},
		{"the extender writes a value over it", takenExtenderOver, takenKnownExtender, func(k, v string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    " + k + ": " + takenOverWith[k] + "\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": " + v + "\n"}
		}, []string{"compose.yaml"}},
		{"a second file writes a value over it", takenSecondFileOver, takenKnownSecond, func(k, v string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "o.yaml": "services:\n  web:\n    " + k + ": " + takenOverWith[k] + "\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": " + v + "\n"}
		}, []string{"compose.yaml", "o.yaml"}},
	} {
		for key, answers := range place.table {
			for i, shape := range takenShapes {
				name := place.name + "/" + key + "/" + shape
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					for file, body := range place.files(key, shape) {
						if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					paths := make([]string, len(place.order))
					for j, f := range place.order {
						paths[j] = filepath.Join(dir, f)
					}
					want := answers[i] == '1'
					if place.known[key+"/"+shape] {
						want = !want
					}
					if _, err := LoadFiles(paths, nil); (err != nil) != want {
						t.Errorf("refused = %v, want %v (docker compose answers %c)", err != nil, want, answers[i])
					}
				})
			}
		}
	}
}
