package compose

// Evals for the keys under a mount's `bind:`.
//
// opossum reads none of them, and says so: the block is named whole among
// the ignored fields (`volumes entry 1.bind`), which is what someone who
// wrote one needs to know — nothing under it is acted on. Saying that was
// also all that happened to it, so a key docker compose refuses went
// straight through: `bind: {subpath: sub}` loaded here and failed there.
//
// Both now. The block is still listed whole, and a key that does not belong
// in it is refused where docker compose refuses it
// (`services.app.volumes.0.bind additional properties 'subpath' not
// allowed`). Which keys belong is the compose specification's, not a list
// written here — `bind` has four and gained one of them along the way.

import (
	"strings"
	"testing"
)

func loadBind(t *testing.T, options string) (*Project, error) {
	t.Helper()
	return loadMount(t, "bind", "source: ./d", "bind:\n  "+strings.ReplaceAll(options, "\n", "\n  "))
}

// loadMount writes one long-form mount of the given type and whatever block
// the row is about, so that the type and the block can be varied apart from
// each other. `bind:` under a mount that is not a bind mount is a real
// compose file — docker compose reads the options there and checks them the
// same way — so the type has to be something a row can set.
func loadMount(t *testing.T, mountType, source, block string) (*Project, error) {
	t.Helper()
	body := "x-anchors:\n  bad: &bad {subpath: sub}\n  good: &good {propagation: rprivate}\n" +
		"volumes:\n  data: {}\n" +
		"services:\n  app:\n    image: alpine\n    volumes:\n" +
		"      - type: " + mountType + "\n        " + source + "\n        target: /t\n" +
		"        " + strings.ReplaceAll(block, "\n", "\n        ") + "\n"
	return Load(writeTemp(t, body))
}

// bindDerived are the ignored fields that come from a mount's `bind:`.
func bindDerived(p *Project) []string {
	var out []string
	for _, f := range p.Services["app"].Unsupported {
		if strings.Contains(f, ".bind") {
			out = append(out, f)
		}
	}
	return out
}

// The keys docker compose takes under `bind:`, measured one at a time with
// `docker compose config` on v5.5.1, and the same four the schema carries.
// `recursive` is one of them: on a bool it is refused for its type
// (`bind.recursive must be a string`), which is a different refusal from
// the one for a key that does not belong — reading only the exit status
// would put it on the wrong side of this list.
func TestABindOptionDockerComposeTakesIsAcceptedAndStillCalledIgnored(t *testing.T) {
	for _, tc := range []struct{ name, option string }{
		{"propagation", "propagation: rprivate"},
		{"create_host_path", "create_host_path: true"},
		{"selinux", "selinux: z"},
		{"recursive", "recursive: enabled"},
		{"all four at once", "propagation: rprivate\ncreate_host_path: true\nselinux: z\nrecursive: enabled"},
		// An `x-` key belongs anywhere in a compose file, including here.
		// On its own, because beside a key that belongs it would pass a
		// check that had stopped taking `x-` for an answer.
		{"an x- key on its own", "x-note: anything"},
		// Written somewhere else and pointed at. A check that read the node
		// as it found it would see an alias and no keys at all, and let
		// anything through.
		{"through an alias", "<<: *good"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := loadBind(t, tc.option)
			if err != nil {
				t.Fatalf("docker compose takes this and opossum refuses it: %v", err)
			}
			// Listed whole, not key by key: opossum acts on none of them,
			// and naming the block says so once. A run that quietly
			// accepted them would leave the writer thinking they took
			// effect.
			// Listed whole and only whole. Naming the block says it once;
			// naming its keys as well would say the same thing again, once
			// per option, and read as though each one had been considered.
			if got := bindDerived(p); len(got) != 1 || got[0] != "volumes entry 1.bind" {
				t.Errorf("the ignored fields from bind: are %v, want exactly "+
					"[\"volumes entry 1.bind\"] — the block is listed whole, not key by key", got)
			}
		})
	}
}

func TestABindOptionDockerComposeRefusesIsRefusedHere(t *testing.T) {
	for _, tc := range []struct{ name, option, want string }{
		// The key this was found by: a real compose key, but `volume:`'s,
		// not `bind:`'s. Writing it under `bind:` is the mistake that
		// looked like it worked.
		{"subpath, which belongs under volume:", "subpath: sub", "subpath"},
		{"a name nothing takes", "foo: 1", "foo"},
		// A typo of one that does belong. The refusal has to name the key
		// as written, or the writer compares it against the wrong word.
		{"a misspelling of one that belongs", "propogation: rprivate", "propogation"},
		// Which key is named when several are wrong: the first in sorted
		// order, so the message does not depend on how the file happened
		// to order them.
		{"several that do not belong", "zed: 1\nabc: 2\nmid: 3\nqux: 4", "abc"},
		// The same key, reached through an alias and through a merge.
		{"a bad key through an alias", "<<: *bad", "subpath"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadBind(t, tc.option)
			if err == nil {
				t.Fatalf("docker compose refuses %q under bind: and opossum took it", tc.option)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q, so the writer cannot see which key "+
					"to look at: %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "bind") {
				t.Errorf("the refusal does not say the key is under `bind:`, and the same "+
					"name may be right somewhere else in the entry: %v", err)
			}
		})
	}
}

// The block is checked, not read. An `x-` key belongs anywhere in a compose
// file, and the mount is still loaded as a bind mount with its source and
// target: checking the options must not change what the mount does.
func TestCheckingTheBindOptionsDoesNotChangeTheMount(t *testing.T) {
	p, err := loadBind(t, "x-note: anything\npropagation: rprivate")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["app"].Volumes; len(got) != 1 || !strings.HasSuffix(got[0], ":/t") {
		t.Errorf("the mount is %v; checking the options under bind: must leave the mount alone", got)
	}
}

// The same file has to be refused the same way every time. The keys of a
// mapping are walked in the order a Go map hands them over, which differs
// from run to run, so a refusal built without sorting names whichever key
// came first that time — and a reader comparing yesterday's message with
// today's finds a different word with nothing changed.
//
// Reading the file once cannot see this: one order is as plausible as
// another. Reading it many times can — with four keys to choose from, an
// unsorted walk settles on the alphabetically first one about a quarter of
// the time, so twenty readings that all agree are not a coincidence.
func TestTheRefusalNamesTheSameKeyEveryTime(t *testing.T) {
	const options = "zed: 1\nabc: 2\nmid: 3\nqux: 4"
	first := ""
	for i := 0; i < 20; i++ {
		_, err := loadBind(t, options)
		if err == nil {
			t.Fatalf("reading %d: none of these keys belongs under bind:, and the file loaded", i)
		}
		// The part of the message that names the key, not the whole of it:
		// each reading writes its own temporary file, so the path differs
		// every time and says nothing about ordering.
		named := err.Error()[strings.LastIndex(err.Error(), "bind:"):]
		if first == "" {
			first = named
			continue
		}
		if named != first {
			t.Fatalf("the same file is refused differently on different readings.\nfirst:  %s\nlater:  %s\n"+
				"The keys are being walked in a map's order, so which one the message names is "+
				"whichever came first that time.", first, named)
		}
	}
}

// `bind:` under a mount that is not a bind mount. docker compose reads the
// options there and checks them the same way, so opossum does too — the
// check is about the block, not about which type the mount is. Both
// answers, because a check that only ran on `type: bind` would pass every
// row above.
func TestBindOptionsAreCheckedWhateverTheMountType(t *testing.T) {
	for _, mount := range []struct{ kind, source string }{
		{"volume", "source: data"},
		// A tmpfs mount has no source; the row gives it a key that is
		// allowed there and says nothing, rather than a second target.
		{"tmpfs", "read_only: false"},
	} {
		t.Run(mount.kind+" refuses a key that does not belong", func(t *testing.T) {
			if _, err := loadMount(t, mount.kind, mount.source, "bind:\n  subpath: sub"); err == nil {
				t.Errorf("docker compose refuses subpath under bind: on a %s mount too, "+
					"and opossum took it", mount.kind)
			}
		})
		t.Run(mount.kind+" takes one that does", func(t *testing.T) {
			// Measured: `docker compose config` (v5.5.1) returns 0 for a
			// `bind: {propagation: rprivate}` under either type. The block
			// is pointless there, and saying so is the ignored list's job,
			// not a refusal's.
			if _, err := loadMount(t, mount.kind, mount.source, "bind:\n  propagation: rprivate"); err != nil {
				t.Errorf("docker compose takes propagation under bind: on a %s mount, and "+
					"opossum refused it: %v", mount.kind, err)
			}
		})
	}
}

// Keys the YAML decoder drops before anything can look at them: a key
// written twice, and a null key. docker compose refuses both
// (`mapping key "foo" already defined`, `non-string key`); opossum does
// not, here as before this change.
//
// What this row holds is that the block is still named among the ignored
// fields when that happens. The check runs on what the decoder returns, so
// a decode that fails returns nothing to check — and if the block dropped
// out of the ignored list with it, someone who wrote `bind:` twice would be
// told nothing at all about either copy.
func TestABlockWhoseKeysTheDecoderDropsIsStillNamedAmongTheIgnoredFields(t *testing.T) {
	for _, tc := range []struct{ name, options string }{
		{"the same key twice", "foo: 1\nfoo: 2"},
		{"a null key", "~: 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := loadBind(t, tc.options)
			if err != nil {
				t.Fatalf("this is read past here, as it was before: %v", err)
			}
			if got := bindDerived(p); len(got) != 1 || got[0] != "volumes entry 1.bind" {
				t.Errorf("the ignored fields from bind: are %v, want exactly "+
					"[\"volumes entry 1.bind\"]. The keys could not be read, but the block "+
					"is there and nothing under it is acted on — which is what the list says.", got)
			}
		})
	}
}

// Which entry the refusal names. A file with several mounts gets one
// message, and the number in it is how the writer finds the line: without
// it, `bind: "subpath" is not a key …` points at every mount at once.
func TestTheRefusalNamesTheEntryTheBlockIsIn(t *testing.T) {
	body := "volumes:\n  data: {}\nservices:\n  app:\n    image: alpine\n    volumes:\n" +
		"      - ./a:/a\n" +
		"      - type: bind\n        source: ./d\n        target: /t\n" +
		"        bind:\n          subpath: sub\n"
	_, err := Load(writeTemp(t, body))
	if err == nil {
		t.Fatal("subpath does not belong under bind: and the file loaded")
	}
	// The whole phrase, not just the word `bind`: the entry number and the
	// block are what locate the key, and either one alone does not.
	if want := `volumes entry 2.bind: "subpath"`; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %s, so the writer cannot tell which mount it is "+
			"about: %v", want, err)
	}
}

// The block written somewhere else and pointed at, rather than written
// here. `bind: *anchor` reaches this check as an alias node with no keys of
// its own: read as it arrives, it has nothing to refuse and nothing to
// list, so every key in it goes through.
//
// `<<: *anchor` does not cover this — a merge key is resolved as the
// mapping is decoded, so the block that arrives already has the keys in it.
// The two are different shapes, and only one of them needs the alias
// followed.
func TestABlockThatIsItselfAnAliasIsReadThrough(t *testing.T) {
	t.Run("a key that does not belong is refused", func(t *testing.T) {
		if _, err := loadMount(t, "bind", "source: ./d", "bind: *bad"); err == nil {
			t.Error("bind: *anchor points at {subpath: sub}, which docker compose refuses, " +
				"and opossum took it — the alias was read as itself rather than followed")
		}
	})
	t.Run("one that does is taken and listed", func(t *testing.T) {
		p, err := loadMount(t, "bind", "source: ./d", "bind: *good")
		if err != nil {
			t.Fatalf("bind: *anchor points at {propagation: rprivate}, which docker compose "+
				"takes: %v", err)
		}
		if got := bindDerived(p); len(got) != 1 || got[0] != "volumes entry 1.bind" {
			t.Errorf("the ignored fields from bind: are %v, want exactly "+
				"[\"volumes entry 1.bind\"]", got)
		}
	})
}
