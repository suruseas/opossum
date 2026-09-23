package orchestrator_test

// Evals for what a refusal counts when two `group_add` entries are written
// differently and reach the runtime as one group. `--gid` reads what it is
// handed in decimal (measured on container 1.4.1: `016` and `+16` are the
// group 16, `0020` is the group 20 — not octal), so entries fold by that
// reading and not by the spelling the file uses.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestEntriesThatReachTheRuntimeAsOneGroupAreCountedOnce(t *testing.T) {
	for _, tc := range []struct{ name, list, want string }{
		// Fold to one group: the file names one group more than once, and the
		// fix is to list it once — not to choose between two.
		{"a hex and a decimal string", `[0x10, "16"]`,
			`names one group twice (group_add: "0x10", "16" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		{"the same, the other way round", `["16", 0x10]`,
			`names one group twice (group_add: "16", "0x10" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		{"a decimal and one with a leading zero", `[16, "016"]`,
			`names one group twice (group_add: "16", "016" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		{"a decimal and one with a plus", `[16, "+16"]`,
			`names one group twice (group_add: "16", "+16" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		{"two strings the runtime reads alike", `["016", "+16"]`,
			`names one group twice (group_add: "016", "+16" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		// Neither is quoted: the spelling still differs, the reading does not.
		{"a hex and an octal", `[0x10, 0o20]`,
			`names one group twice (group_add: "0x10", "0o20" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		{"a YAML octal and a decimal", `[020, 16]`,
			`names one group twice (group_add: "020", "16" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		// One spelling handed over two ways that are one group all the same:
		// nothing to tell apart, so nothing is added.
		{"one spelling handed two ways, one group", `[+16, "+16"]`,
			`names one group twice (group_add: "+16", "+16" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},
		{"one group written three ways", `[0x10, "16", 0o20]`,
			`names one group 3 times (group_add: "0x10", "16", "0o20" — all the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`},

		// Fold to more than one: the count is what the runtime would be given,
		// and the entries that fold are named so the reader can see which
		// lines are the same group.
		{"three entries, two of them one group", `[0x10, "16", 20]`,
			`adds 2 groups (group_add: "0x10", "16", "20" — "0x10" and "16" are both the group 16); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"the folding pair not adjacent", `[0x10, 20, "16"]`,
			`adds 2 groups (group_add: "0x10", "20", "16" — "0x10" and "16" are both the group 16); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		// Three spellings of one group, beside another group: `both` holds
		// two, so this says all of them.
		{"three entries of one group beside another", `[0x10, "16", "016", 20]`,
			`adds 2 groups (group_add: "0x10", "16", "016", "20" — "0x10", "16" and "016" are all the group 16); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"two groups, one of them written three ways", `[0x10, "16", "016", 20, "020", 0x14]`,
			`adds 2 groups (group_add: "0x10", "16", "016", "20", "020", "0x14" — "0x10", "16" and "016" are all the group 16, "20", "020" and "0x14" are all the group 20); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		// The other end of that axis: `all` is per set, not per sentence. Three
		// sets of two stay `both`, and a sentence holding a set of four and a
		// set of two says each its own way.
		{"three pairs", `[0x10, "16", 20, "020", 24, "024"]`,
			`adds 3 groups (group_add: "0x10", "16", "20", "020", "24", "024" — "0x10" and "16" are both the group 16, "20" and "020" are both the group 20, "24" and "024" are both the group 24); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"a set of four beside a set of two", `[0x10, "16", "016", "+16", 20, "020"]`,
			`adds 2 groups (group_add: "0x10", "16", "016", "+16", "20", "020" — "0x10", "16", "016" and "+16" are all the group 16, "20" and "020" are both the group 20); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"two pairs", `[0x10, "16", 0x14, "20"]`,
			`adds 2 groups (group_add: "0x10", "16", "0x14", "20" — "0x10" and "16" are both the group 16, "0x14" and "20" are both the group 20); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},

		// "List the group once" promises the file then starts, so it is said
		// only where it would. A folded value the checks below refuse is
		// refused for that instead: writing it once would not start either,
		// and what to write is what the reader needs first.
		{"two spellings of one value the runtime refuses", `[-0x0, -00]`,
			"adds the group \"-0x0\" (group_add); a gid is not negative — the docker engine refuses it too (`uids and gids must be in range 0-2147483647`) — write the group's number"},
		{"two spellings of one group past the largest gid", `[0x80000000, "02147483648"]`,
			`adds the group "0x80000000" (group_add), which reads as the group 2147483648 and is past 2147483647, the largest gid the docker engine takes — write the group's number`},

		// What opossum can read is what it folds by: a number past what an
		// integer holds is left as the spelling, so two of those are two
		// groups here although `--gid` would read them alike. Both are
		// refused for being past the largest gid a step later.
		{"two spellings past what an integer holds", `["18446744073709551615", "018446744073709551615"]`,
			`adds 2 groups (group_add: "18446744073709551615", "018446744073709551615"); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},

		// One spelling handed over two ways is two groups, and the list would
		// otherwise quote it twice with nothing to tell the two apart: each
		// says which group it is. Only there — a spelling that is one group
		// however it is handed over says nothing extra.
		{"one spelling, two groups", `[020, "020"]`,
			`adds 2 groups (group_add: "020" (the group 16), "020" (the group 20)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"one spelling, two groups, beside a third", `[020, "020", 24]`,
			`adds 3 groups (group_add: "020" (the group 16), "020" (the group 20), "24"); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"one spelling, one of them the runtime cannot read", `[0x10, "0x10"]`,
			`adds 2 groups (group_add: "0x10" (the group 16), "0x10" (handed over as 0x10)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},

		// Past the range the docker engine takes is still a group container
		// 1.4.1 gives (up to 4294967294, measured), so it is named as one;
		// the string beside it is not, so it says what is handed over.
		{"one spelling, a group past the engine's range and a spelling it cannot read", `[0x80000000, "0x80000000"]`,
			`adds 2 groups (group_add: "0x80000000" (the group 2147483648), "0x80000000" (handed over as 0x80000000)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		// The advice a reader edits carries the qualifier too, so a set that
		// folds says which of the repeated spellings it holds.
		{"one spelling twice over, with a pair that folds", `[020, "020", 016, "016"]`,
			`adds 3 groups (group_add: "020" (the group 16), "020" (the group 20), "016" (the group 14), "016" (the group 16) — "020" (the group 16) and "016" (the group 16) are both the group 16); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"one spelling, one of them folding with a third", `[0x10, "0x10", 16]`,
			`adds 2 groups (group_add: "0x10" (the group 16), "0x10" (handed over as 0x10), "16" — "0x10" (the group 16) and "16" are both the group 16); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		// The two sides of the boundary, next to each other: 4294967294 is
		// the last gid container 1.4.1 gives, and 4294967295 is taken by the
		// flag and then fails to create the container (measured 2026-09-23),
		// so it is not a group to name either.
		{"one spelling, the last group the runtime gives", `[0xfffffffe, "0xfffffffe"]`,
			`adds 2 groups (group_add: "0xfffffffe" (the group 4294967294), "0xfffffffe" (handed over as 0xfffffffe)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"one spelling, one past the last group the runtime gives", `[0xffffffff, "0xffffffff"]`,
			`adds 2 groups (group_add: "0xffffffff" (handed over as 4294967295), "0xffffffff" (handed over as 0xffffffff)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		// Past what the runtime takes at all: a number that large is not a
		// group it would give, so it says what is handed over instead.
		{"one spelling, a number past every gid", `[0x100000000, "0x100000000"]`,
			`adds 2 groups (group_add: "0x100000000" (handed over as 4294967296), "0x100000000" (handed over as 0x100000000)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		// Neither of these is a group the runtime would give, so each says
		// what opossum hands over for it — which is what tells them apart.
		{"one spelling, neither of them a group the runtime gives", `[-0x10, "-0x10"]`,
			`adds 2 groups (group_add: "-0x10" (handed over as -16), "-0x10" (handed over as -0x10)); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},

		// Nothing folds: the count and the sentence are what they were.
		{"two groups", `[16, 20]`,
			`adds 2 groups (group_add: "16", "20"); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
		{"three groups", `[16, 20, 24]`,
			`adds 3 groups (group_add: "16", "20", "24"); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`},
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
			if want := `service "app" ` + tc.want; err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
		})
	}
}

// A `user:` beside them is what the reader has to settle first: listing the
// group once would not start either, so the refusal is the one about the user,
// naming the spelling the file leads with.
func TestAUserBesideFoldedEntriesIsRefusedForTheUser(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    user: \"1000\"\n    group_add: [0x10, \"16\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	want := `service "app" adds the group "0x10" (group_add) beside user: "1000"; container 1.4.1's --gid does nothing next to --user — drop group_add (the process then runs without the group, and a socket or device that needs it refuses it), or drop user: (the image's own user then runs with the group)`
	if err.Error() != want {
		t.Errorf("\n got %v\nwant %s", err, want)
	}
}

// Two entries the runtime could not read are folded by the same rule as the
// rest: what opossum would hand over. Two spellings it would hand over
// differently are two groups, whether or not the runtime could take either.
func TestTwoSpellingsTheRuntimeCannotReadAreNotOneGroup(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: [\"0x10\", wheel]\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	want := `service "app" adds 2 groups (group_add: "0x10", "wheel"); container 1.4.1's --gid takes one, and a second replaces the first — keep the one the process needs`
	if err.Error() != want {
		t.Errorf("\n got %v\nwant %s", err, want)
	}
}

// An entry the runtime could not take is not folded with anything: `0x10` as a
// string reaches `--gid` as those characters, which is not a number there, and
// the refusal for that shape is the one that says so.
func TestASpellingTheRuntimeCannotReadIsNotFolded(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: [\"0x10\", 16]\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	if want := `adds 2 groups (group_add: "0x10", "16")`; !strings.Contains(err.Error(), want) {
		t.Errorf("want %q in:\n%v", want, err)
	}
}
