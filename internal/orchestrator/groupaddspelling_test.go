package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A refusal names the `group_add` entry as the file spells it. The loader
// reads a number to its decimal (`0x10` is the group 16, as docker compose
// reads it), so a reader told to fix `"16"` would not find it in a file that
// says `0x10` — and where two spellings reach the runtime as one group, the
// refusal says so and asks for the group to be listed once
// (groupaddfolding_test.go).
func TestAGroupAddRefusalNamesTheSpellingTheFileUses(t *testing.T) {
	for _, tc := range []struct {
		name, list, want string
	}{
		{"two spellings of one group", `[0x10, "16"]`, `names one group twice (group_add: "0x10", "16" — both the group 16)`},
		{"an octal beside a decimal", `[0o20, 16]`, `names one group twice (group_add: "0o20", "16" — both the group 16)`},
		{"a mode-looking number beside a decimal", `[0755, 16]`, `adds 2 groups (group_add: "0755", "16")`},
		{"two decimals", `[16, 17]`, `adds 2 groups (group_add: "16", "17")`},
		// The spelling that differs is the second entry, not the first.
		{"a decimal then the same in hex", `[16, 0x10]`, `names one group twice (group_add: "16", "0x10" — both the group 16)`},
		// The number is the reason here, so both are named.
		{"past the largest gid in hex", `[0x80000000]`, `adds the group "0x80000000" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"a zero written with a minus", `[-00]`, `adds the group "-00" (group_add)`},
		{"a negative number", `[-0x10]`, `adds the group "-0x10" (group_add)`},
		{"past the largest gid in digits", `[2147483648]`, `adds the group "2147483648" (group_add), which is past 2147483647`},
		{"a name", `[wheel]`, `adds the group "wheel" (group_add)`},
		{"not the digits of a gid", `[0xFFFFFFFFFFFFFFFF]`, `adds the group "0xFFFFFFFFFFFFFFFF" (group_add)`},
		{"an empty entry", `[""]`, `adds an empty group`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: "+tc.list+"\n")
			if err != nil {
				t.Fatal(err)
			}
			err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in:\n%v", tc.want, err)
			}
		})
	}
}

// Beside a `user:`, the refusal names the spelling too.
func TestTheGroupBesideAUserIsNamedAsWritten(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    user: \"1000\"\n    group_add: [0x10]\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), `adds the group "0x10" (group_add) beside user: "1000"`) {
		t.Fatalf("want the written spelling beside the user, got %v", err)
	}
}

// What the runtime is given is unchanged: the spelling is for the refusals,
// not for `--gid`, which takes the group the loader read.
func TestTheSpellingDoesNotReachTheRuntime(t *testing.T) {
	rt, log := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatal(err)
	}
	if i := indexOf(log(), "--gid 16"); i < 0 {
		t.Errorf("want `--gid 16` in:\n%s", strings.Join(log(), "\n"))
	}
}

// A service built in code rather than read from a file has no spellings
// recorded, and the refusal names what it does have.
func TestAServiceWithNoRecordedSpellingsIsNamedByWhatItHas(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"app": {Name: "app", Image: "alpine:3.20", GroupAdd: compose.GroupAdd{"16", "17"}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), `adds 2 groups (group_add: "16", "17")`) {
		t.Fatalf("want the groups named, got %v", err)
	}
}

// `run` and `run --audit` name the spelling too: the check is the same one,
// and the refusal is what the reader sees before anything is created.
func TestRunNamesTheSpellingTheFileUses(t *testing.T) {
	const body = "services:\n  app:\n    image: alpine:3.20\n    group_add: [0x10, \"16\"]\n"
	for _, cmd := range []string{"run", "run --audit"} {
		t.Run(cmd, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if cmd == "run" {
				err = o.RunOneOff("app", []string{"true"}, orchestrator.RunOneOffOptions{})
			} else {
				_, err = o.RunAudited("app", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			if err == nil || !strings.Contains(err.Error(), `names one group twice (group_add: "0x10", "16" — both the group 16)`) {
				t.Fatalf("want the spellings named, got %v", err)
			}
		})
	}
}

// Where the two lists somehow differ in length, the refusal names the
// reading for every entry rather than mixing the two — a line that read
// `"0x10", "17"` would say the file spells the second entry that way.
func TestMismatchedSpellingsAreNotMixedIntoOneLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written []string
	}{
		{"fewer spellings than groups", []string{"0x10"}},
		{"more spellings than groups", []string{"0x10", "0o21", "17"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"app": {
					Name: "app", Image: "alpine:3.20",
					GroupAdd:        compose.GroupAdd{"16", "17"},
					GroupAddWritten: tc.written,
				},
			})
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil || !strings.Contains(err.Error(), `adds 2 groups (group_add: "16", "17")`) {
				t.Fatalf("want the readings named, got %v", err)
			}
		})
	}
}

// One rule across the refusals: what the file spells is quoted, and what
// opossum read out of it is bare — in the note a refusal puts on a spelling
// that is two groups as well as in the refusal itself. Before this, three of the six refusals left
// the spelling bare, so the reading and the spelling stood in one sentence in
// the same shape (`the group 0x80000000 … reads as the group 2147483648`) and
// a reader had to know which was which.
func TestASpellingIsQuotedAndAReadingIsNot(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"two groups", "    group_add: [0x10, \"20\"]\n",
			`adds 2 groups (group_add: "0x10", "20")`},
		{"a negative number", "    group_add: [-5]\n",
			`adds the group "-5" (group_add); a gid is not negative`},
		// The negative branch is the one an entry reaches before anything has
		// checked that it is digits, so any spelling starting with `-` lands
		// here. Quoting shows where it begins and ends, and a character that
		// would otherwise be invisible is shown the way Go writes it — the
		// same as every other spelling opossum quotes.
		{"a negative spelling that is not a number", "    group_add: ['-a b']\n",
			`adds the group "-a b" (group_add); a gid is not negative`},
		{"a negative spelling holding a tab", "    group_add: [\"-a\\tb\"]\n",
			`adds the group "-a\tb" (group_add); a gid is not negative`},
		{"not the digits of a gid", "    group_add: [\"0x1g\"]\n",
			`adds the group "0x1g" (group_add), which is not the digits of a gid`},
		{"a name", "    group_add: [wheel]\n",
			`adds the group "wheel" (group_add); container 1.4.1's --gid takes a number`},
		{"past the largest gid, one spelling", "    group_add: [2147483648]\n",
			`adds the group "2147483648" (group_add), which is past 2147483647`},
		{"past the largest gid, two spellings", "    group_add: [0x80000000]\n",
			`adds the group "0x80000000" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"beside a user", "    user: \"1000\"\n    group_add: [0x10]\n",
			`adds the group "0x10" (group_add) beside user: "1000"`},
		// The rule holds in the note this puts on a spelling that is two
		// groups as well: the spelling stays quoted, and the reading beside
		// it — the group, or what is handed over — is bare.
		{"a spelling that is two groups", "    group_add: [020, \"020\"]\n",
			`adds 2 groups (group_add: "020" (the group 16), "020" (the group 20))`},
		{"a spelling that is two groups, neither of them one the runtime gives", "    group_add: [-0x10, \"-0x10\"]\n",
			`adds 2 groups (group_add: "-0x10" (handed over as -16), "-0x10" (handed over as -0x10))`},
		// Quotes in the file do not decide the quotes in the refusal: the
		// same group written both ways is one group, and one sentence.
		{"a decimal beside a user, written as a number", "    user: \"1000\"\n    group_add: [16]\n",
			`adds the group "16" (group_add) beside user: "1000"`},
		{"a decimal beside a user, written as a string", "    user: \"1000\"\n    group_add: [\"16\"]\n",
			`adds the group "16" (group_add) beside user: "1000"`},
		// What quotes in the file do decide is how the entry reads, and a
		// spelling that reads as no gid is refused for that instead — another
		// refusal, not another way of writing this one.
		{"a hex string beside a user is refused for its reading", "    user: \"1000\"\n    group_add: [\"0x10\"]\n",
			`adds the group "0x10" (group_add), which is not the digits of a gid`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n"+tc.body)
			if err != nil {
				t.Fatal(err)
			}
			err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in:\n%v", tc.want, err)
			}
		})
	}
}

// What the quotes hold is the entry as the refusal names it, which a second
// file changes: merging settles the spelling as the reading, so a project read
// from two files quotes `2147483648` where one file quotes `0x80000000`. The
// rest of the sentence already said this — the quotes follow it rather than
// making a claim of their own.
func TestASecondFileSettlesWhatIsQuoted(t *testing.T) {
	rt, _ := fakeShim(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	overlay := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("name: demo\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [0x80000000]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("services:\n  app:\n    environment:\n      X: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.LoadFiles([]string{base, overlay}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	if want := `adds the group "2147483648" (group_add), which is past 2147483647`; !strings.Contains(err.Error(), want) {
		t.Errorf("want %q in:\n%v", want, err)
	}
	if strings.Contains(err.Error(), "0x80000000") {
		t.Errorf("the spelling is gone by the time this is read:\n%v", err)
	}
}

// The empty entry is the one refusal with no spelling to name: it shows the
// entry as the file holds it and quotes nothing else.
func TestTheEmptyEntryNamesNoSpelling(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: [\"\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	// The whole sentence: an entry appended after it would leave the check
	// above green while naming the very thing this refusal has none of.
	want := `service "app" adds an empty group (group_add: [""]); the docker engine refuses it too (` + "`unable to find group`" + `) — write the group's number, or drop the entry`
	if err.Error() != want {
		t.Errorf("\n got %v\nwant %s", err, want)
	}
}
