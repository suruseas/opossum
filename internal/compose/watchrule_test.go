package compose

// A watch rule's shape and a quoted retries count (#735's last small
// neighbours). docker compose (v5.5.0), exit codes and full output read:
// `retries: "3"` is taken (a quoted count), `retries: three` is not;
// `ignore: node_modules` is taken (one glob), `ignore: [42]` and a bare
// `ignore:` are refused; `path: ""` is `value can't be blank`, a rule with no
// `path` is `missing properties 'path', 'action'`, and a key a rule does not
// have is `additional properties … not allowed` (with both `path` and
// `action` missing it says `missing properties 'path', 'action'`). opossum
// used to refuse the quoted count and the single glob, and take the empty
// path, the unknown key and the missing path. A path of spaces (`" "`)
// docker takes, and so does this.

import (
	"strings"
	"testing"
)

func TestAQuotedRetriesCountIsTakenAndAWordIsNot(t *testing.T) {
	hc := "services:\n  web:\n    image: alpine\n    healthcheck:\n      test: [CMD, \"true\"]\n"
	for _, tc := range []struct {
		name, retries string
		want          int
	}{
		{"quoted", "\"3\"", 3}, {"bare", "5", 5}, {"a bare decimal, truncated as docker does", "2.5", 2}, {"an exponent", "1e1", 10}, {"zero is the default here (docker keeps 0)", "0", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, hc+"      retries: "+tc.retries+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Healthcheck.Retries; got != tc.want {
				t.Errorf("retries = %d, want %d", got, tc.want)
			}
		})
	}
	// Not a count, as docker compose reads them: a word, a negative, a
	// blank (also what an unset `${R}` leaves), a padded or decimal quoted
	// number (quoted text must be a whole number), a boolean, infinity.
	for _, bad := range []string{"three", "-1", "\"\"", "\" \"", "\"3 \"", "\"3.9\"", "true", ".inf"} {
		got := loadErr(t, hc+"      retries: "+bad+"\n")
		if !strings.Contains(got, "healthcheck retries: not a count — use a whole number, as in 3") {
			t.Errorf("want the count refusal for %s, got:\n%s", bad, got)
		}
	}
	// Bare or null, the shape table's refusal stands (docker refuses too).
	if got := loadErr(t, hc+"      retries: ~\n"); !strings.Contains(got, "healthcheck.retries must be a number or string, got nothing") {
		t.Errorf("a null retries keeps the bare-key refusal, got:\n%s", got)
	}
	t.Setenv("OPOSSUM_TEST_RETRIES_UNSET", "")
	if got := loadErr(t, hc+"      retries: \"${OPOSSUM_TEST_RETRIES_UNSET}\"\n"); !strings.Contains(got, "not a count") {
		t.Errorf("a reference that expanded to nothing is a blank, not the default, got:\n%s", got)
	}
}

func TestAWatchRuleIsReadTheWayDockerReadsIt(t *testing.T) {
	watch := "services:\n  web:\n    build: .\n    develop:\n      watch:\n"
	p, err := Load(writeTemp(t, watch+"        - path: ./src\n          action: sync\n          target: /app\n          ignore: node_modules\n"))
	if err != nil {
		t.Fatalf("one glob: %v", err)
	}
	if got := strings.Join(p.Services["web"].Develop.Watch[0].Ignore, ","); got != "node_modules" {
		t.Errorf("ignore = %q, want the one glob", got)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"ignore items must be strings", watch + "        - path: ./src\n          action: sync\n          target: /app\n          ignore: [42]\n", "develop.watch entry 1.ignore entry 1 of 1 must be a string, got a number"},
		{"bare ignore", watch + "        - path: ./src\n          action: sync\n          target: /app\n          ignore:\n", "develop.watch entry 1.ignore must be a string or list, got nothing"},
		{"an empty path", watch + "        - path: \"\"\n          action: sync\n          target: /app\n", "develop.watch entry 1 has an empty path — write the directory to watch, as in `path: ./src`"},
		{"a rule with no path", watch + "        - action: sync\n          target: /app\n", "develop.watch entry 1 has no path"},
		{"the second rule with no path", watch + "        - path: ./a\n          action: sync\n          target: /a\n        - action: sync\n          target: /b\n", "develop.watch entry 2 has no path"},
		{"a bare path keeps the shape table's message", watch + "        - path:\n          action: sync\n          target: /app\n", "develop.watch entry 1.path must be a string, got nothing"},
		{"a rule that is not a mapping", watch + "        - ./src\n", "develop.watch: a rule must be a mapping (path, action, target), got a single value"},
		{"ignore through an alias", "x-ig: &ig [42]\n" + watch + "        - path: ./src\n          action: sync\n          target: /app\n          ignore: *ig\n", "develop.watch entry 1.ignore entry 1 of 1 must be a string, got a number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}

// A rule key docker compose takes that opossum does not read (`exec`) is
// listed with the ignored fields; `action` left out stays `sync` — a
// leniency docker compose does not share, pinned so it is a known one.
func TestAnUnreadWatchRuleKeyIsListedAndAMissingActionDefaults(t *testing.T) {
	// The rule comes in through an alias: the listing must see through it.
	p, err := Load(writeTemp(t, "x-r: &r\n  path: ./src\n  target: /app\n  exec: {command: echo}\nservices:\n  web:\n    build: .\n    develop:\n      watch:\n        - *r\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if got := strings.Join(web.Unsupported, ","); !strings.Contains(got, "develop.watch entry 1.exec") {
		t.Errorf("the unread key should be listed among the ignored fields, got %q", got)
	}
	if web.Develop.Watch[0].Action != "" {
		t.Errorf("action left out is left empty here (the watcher reads it as sync), got %q", web.Develop.Watch[0].Action)
	}
}

// A path of spaces is a path to docker compose; only the empty one is not.
func TestAWatchPathOfSpacesIsTaken(t *testing.T) {
	if _, err := Load(writeTemp(t, "services:\n  web:\n    build: .\n    develop:\n      watch:\n        - path: \" \"\n          action: sync\n          target: /app\n")); err != nil {
		t.Errorf("a path of spaces is taken as written: %v", err)
	}
}
