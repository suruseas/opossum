package compose

import (
	"strconv"
	"strings"
	"testing"
)

// dockerComposeVolumeNameChars is every ASCII character from space to `~` docker
// compose v5.5.0 lets `a<c>b` be a volume's name with, measured one character
// at a time with `docker compose config` on a file that declares the name and
// mounts it by `type: volume`.
const dockerComposeVolumeNameChars = "-.0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"

// A volume name is loaded exactly when docker compose would load it, for every
// ASCII character from space to `~` between two letters; a refused one is named whole,
// in the place it was written. `$` is left out: it starts a variable, which
// this loader expands in the declaration's name too, where docker compose does
// not — a difference of its own.
func TestAVolumeNameIsLoadedExactlyWhenDockerComposeLoadsIt(t *testing.T) {
	for code := 32; code < 127; code++ {
		c := string(rune(code))
		if c == "$" {
			continue
		}
		name := "a" + c + "b"
		quoted := strconv.Quote(name)
		t.Run(quoted, func(t *testing.T) {
			ok := strings.Contains(dockerComposeVolumeNameChars, c)
			mounted := "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: " + quoted + ", target: /y}\nvolumes:\n  " + quoted + ": {}\n"
			declared := "services:\n  web:\n    image: alpine\nvolumes:\n  " + quoted + ": {}\n"
			for _, tc := range []struct{ form, body, refusal string }{
				{"mounted", mounted, "volumes entry 1 of 1: type: volume with source " + quoted + " — a volume name " + volumeNameRule},
				{"declared", declared, "volume name " + quoted + " " + volumeNameRule},
			} {
				_, err := Load(writeTemp(t, tc.body))
				switch {
				case ok && err != nil:
					t.Errorf("%s: docker compose loads it; got %v", tc.form, err)
				case !ok && err == nil:
					t.Errorf("%s: docker compose refuses it; it loaded", tc.form)
				case !ok && !strings.HasSuffix(err.Error(), tc.refusal):
					t.Errorf("%s:\n got %v\nwant it to end %q", tc.form, err, tc.refusal)
				}
			}
		})
	}
}

// A name starting with `.` is one docker compose lets a volume have, and one
// this loader carries in a spelling of its own; the characters after the `.`
// are held to the same rule, and a refused one is named as written — `.h:x`
// was reported as the undefined volume `.h`.
func TestAVolumeNameStartingWithADotFollowsTheSameRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{".hid", true},
		{".h:x", false},
		{".h!x", false},
	} {
		quoted := strconv.Quote(tc.name)
		t.Run(quoted, func(t *testing.T) {
			for _, form := range []struct{ name, body, refusal string }{
				{"mounted", "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: " + quoted + ", target: /y}\nvolumes:\n  " + quoted + ": {}\n",
					"volumes entry 1 of 1: type: volume with source " + quoted + " — a volume name " + volumeNameRule},
				{"declared", "services:\n  web:\n    image: alpine\nvolumes:\n  " + quoted + ": {}\n",
					"volume name " + quoted + " " + volumeNameRule},
			} {
				_, err := Load(writeTemp(t, form.body))
				switch {
				case tc.ok && err != nil:
					t.Errorf("%s: want it loaded, got %v", form.name, err)
				case !tc.ok && (err == nil || !strings.HasSuffix(err.Error(), form.refusal)):
					t.Errorf("%s:\n got %v\nwant it to end %q", form.name, err, form.refusal)
				}
			}
		})
	}
}

// The rest of the rule's edges, each refused as docker compose refuses it: no
// name at all, a letter outside ASCII, and a character after the last one the
// rule allows.
func TestAVolumeNameAtTheEdgesOfTheRuleIsRefused(t *testing.T) {
	for _, name := range []string{"", "aéb", "ab\n"} {
		quoted := strconv.Quote(name)
		t.Run(quoted, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\nvolumes:\n  "+quoted+": {}\n"))
			if want := "volume name " + quoted + " " + volumeNameRule; err == nil || !strings.HasSuffix(err.Error(), want) {
				t.Errorf("declared:\n got %v\nwant it to end %q", err, want)
			}
			if name == "" {
				return // an empty source is an anonymous volume, which docker compose loads
			}
			_, err = Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: "+quoted+", target: /y}\n"))
			if want := "type: volume with source " + quoted + " — a volume name " + volumeNameRule; err == nil || !strings.HasSuffix(err.Error(), want) {
				t.Errorf("mounted:\n got %v\nwant it to end %q", err, want)
			}
		})
	}
}
