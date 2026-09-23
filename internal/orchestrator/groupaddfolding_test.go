package orchestrator_test

// Evals for what a refusal counts when two `group_add` entries are written
// differently and reach the runtime as one group. `--gid` reads what it is
// handed in decimal (measured on container 1.4.1: `016` and `+16` are the
// group 16, `0020` is the group 20 — not octal), so entries fold by that
// reading and not by the spelling the file uses.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
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

		// The reading folds them however large the number is: `--gid` reads
		// these two alike, so they are one group, and the refusal is about
		// that group being past the largest gid rather than about there
		// being two. They used to be counted as two — the reading was an
		// integer's — and the file was told to keep one of two groups it had
		// written once. (TestFoldingDoesNotTurnOnHowManyDigits holds the
		// boundary that put there.)
		{"two spellings past what an integer holds", `["18446744073709551615", "018446744073709551615"]`,
			`adds the group "18446744073709551615" (group_add), which is past 2147483647, the largest gid the docker engine takes — write the group's number`},

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

// The shape that only reaches this check once a second file is read: two
// spellings of one group settle into the one spelling in the merge, so what
// arrives here is the same characters twice over. It used to be refused as
// the file was read — which stopped `down` as well — and is now left to this
// check, which asks for the group to be listed once and lets the project come
// down. Held here because the load no longer refuses it: nothing else would
// notice if this check stopped answering for it.
func TestOneGroupInTwoSpellingsIsRefusedHereAfterAMerge(t *testing.T) {
	rt, _ := fakeShim(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	overlay := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("name: demo\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [0x10, \"16\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The second file says nothing about `group_add`: what changes is that
	// there is a merge at all.
	if err := os.WriteFile(overlay, []byte("services:\n  app:\n    environment:\n      X: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.LoadFiles([]string{base, overlay}, nil)
	if err != nil {
		t.Fatalf("want the files read, got %v", err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	want := `names one group twice (group_add: "16", "16" — both the group 16); one --gid is handed over, and opossum does not choose which of them to hand it — list the group once`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("want %q in:\n%v", want, err)
	}
}

// Whether two entries are one group does not turn on how many digits the
// number has. The reading folds them — a `+` and leading zeros are no part of
// the number — and reading it by parsing an integer put a boundary in the
// middle of that: the largest integer and the one after it are the same pair
// of spellings, and only the first was folded. A file that wrote one group
// once was then told it had named two and to keep one, and keeping one left
// it refused all the same for a group past the largest gid.
//
// docker compose has no answer to hold this against: it refuses such a file
// where the YAML is read (`expected type 'string', got unsigned integer`,
// measured on v5.5.1), so the pair never reaches its own repeat check.
func TestFoldingDoesNotTurnOnHowManyDigits(t *testing.T) {
	for _, tc := range []struct{ name, list, want string }{
		// The largest integer, and the same pair one larger: the second used
		// to be counted as two groups.
		{"the largest integer", `[9223372036854775807, "+9223372036854775807"]`,
			`adds the group "9223372036854775807" (group_add), which is past 2147483647`},
		{"one past the largest integer", `[9223372036854775808, "+9223372036854775808"]`,
			`adds the group "9223372036854775808" (group_add), which is past 2147483647`},
		{"the largest a uint64 holds", `[18446744073709551615, "+18446744073709551615"]`,
			`adds the group "18446744073709551615" (group_add), which is past 2147483647`},
		// Leading zeros are no part of the number either, at any size.
		{"leading zeros past the largest integer", `["018446744073709551615", "18446744073709551615"]`,
			`adds the group "018446744073709551615" (group_add), which reads as the group 18446744073709551615 and is past 2147483647`},
		// Well inside an integer, where the two readings always agreed.
		{"a group an integer holds easily", `[2147483648, "+2147483648"]`,
			`adds the group "2147483648" (group_add), which is past 2147483647`},
		// Two groups are still two, however large: the readings differ.
		{"two groups past the largest integer", `[18446744073709551615, 18446744073709551614]`,
			`adds 2 groups (group_add: "18446744073709551615", "18446744073709551614")`},
		// A spelling `--gid` cannot read as a number has no reading to fold
		// by, so it stands for itself. Dropping a leading zero from one
		// would make `"0x10"` and `"x10"` the same entry, and they are two
		// spellings the runtime refuses separately.
		{"two spellings neither of which is a number", `["0x10", "x10"]`,
			`adds 2 groups (group_add: "0x10", "x10")`},
		// The group 0, whose spellings are all zeros and a sign: dropping
		// them leaves nothing, and the reading has to be the number 0 rather
		// than nothing at all. With nothing, these entries fold under a key
		// `--gid` would not take, and the file is told neither that it names
		// one group twice nor which group that is — it starts instead, with
		// no group at all.
		{"the group 0 written two ways", `[0, "00"]`,
			`names one group twice (group_add: "0", "00" — both the group 0); one --gid is handed over`},
		{"the group 0 with a sign", `["0", "+0"]`,
			`names one group twice (group_add: "0", "+0" — both the group 0); one --gid is handed over`},
		{"the group 0 written three ways", `[0, "+0", "000"]`,
			`names one group 3 times (group_add: "0", "+0", "000" — all the group 0); one --gid is handed over`},
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

// The reading folds the same way when a second file is read beside the first.
// The merge writes the tree back out, so an unquoted number arrives in its
// decimal — which is the reading — while a quoted one is the characters the
// file wrote; both reach this check, and both fold by what `--gid` reads.
func TestFoldingDoesNotTurnOnHowManyDigitsAfterAMerge(t *testing.T) {
	rt, _ := fakeShim(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	overlay := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("name: demo\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [18446744073709551615, \"+18446744073709551615\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The second file says nothing about `group_add`: what changes is that
	// there is a merge at all.
	if err := os.WriteFile(overlay, []byte("services:\n  app:\n    environment:\n      X: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.LoadFiles([]string{base, overlay}, nil)
	if err != nil {
		t.Fatalf("want the files read, got %v", err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	want := `adds the group "18446744073709551615" (group_add), which is past 2147483647`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("want %q in:\n%v", want, err)
	}
}
