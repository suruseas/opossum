package compose

import (
	"strconv"
	"strings"
	"testing"
)

// A short mount is split at every `:` into SOURCE:TARGET[:MODE], by docker
// compose v5.5.0 and by container 1.4.1 alike. Four or more fields docker
// compose refuses (`too many colons`); a third field that holds a path is a
// mode to both, and the runtime fails to start the container on it. Those
// are refused when the file is loaded; the forms both read the way they look
// load. (`hh:xx` — a target without a leading `/` — is left alone: docker's
// engine refuses it, but container 1.4.1 mounts it at `/xx`.)
func TestAShortMountIsReadAsSourceTargetMode(t *testing.T) {
	decl := "volumes:\n  hh: {}\n"
	for _, tc := range []struct {
		entry   string
		refusal string // empty: it loads
	}{
		{"hh:/y", ""},
		{"hh:/y:ro", ""},
		{"hh:/y:ro,nocopy", ""},
		{"./d:/y:rw", ""},
		{"/abs:/y", ""},
		{"hh:xx", ""},
		{"hh:xx:/y", `volumes entry 1 of 1: in "hh:xx:/y" the third field "/y" is the mode (such as ` + "`ro`" + `), not a path — the target is "xx"; write SOURCE:TARGET with the target starting with ` + "`/`"},
		{"./hh:xx:/y", `volumes entry 1 of 1: in "./hh:xx:/y" the third field "/y" is the mode`},
		{"hh:xx:/y:ro", `volumes entry 1 of 1: "hh:xx:/y:ro" has too many colons — a short mount is SOURCE:TARGET or SOURCE:TARGET:MODE (docker compose refuses it as well); for a path with ` + "`:`" + ` in it, use the long form`},
		{"hh:xx:yy:/z", `volumes entry 1 of 1: "hh:xx:yy:/z" has too many colons`},
		{"/abs:x:y:/z", `volumes entry 1 of 1: "/abs:x:y:/z" has too many colons`},
		{"hh:/a:b:c:/d", `volumes entry 1 of 1: "hh:/a:b:c:/d" has too many colons`},
		{"hh:/y:ro/x", `volumes entry 1 of 1: in "hh:/y:ro/x" the third field "ro/x" is the mode (such as ` + "`ro`" + `), not a path — the target is "/y"; remove the third field, or write a mode there`},
		{"./d:/y:/z", `volumes entry 1 of 1: in "./d:/y:/z" the third field "/z" is the mode (such as ` + "`ro`" + `), not a path — the target is "/y"; remove the third field`},
		{"hh:xx:a/b", `volumes entry 1 of 1: in "hh:xx:a/b" the third field "a/b" is the mode (such as ` + "`ro`" + `), not a path — the target is "xx"; write SOURCE:TARGET with the target starting with`},
		{"hh::/y", `volumes entry 1 of 1: "hh::/y" has nothing between its colons — a short mount is SOURCE:TARGET or SOURCE:TARGET:MODE`},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - "+strconv.Quote(tc.entry)+"\n"+decl))
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("\n got %v\nwant it to contain %q", err, tc.refusal)
			}
		})
	}
}

// A long-form mount travels on as `source:target` and reaches the runtime as
// `-v` (or `--tmpfs`), which splits at every `:`: a `:` in a bind's host path
// or in any target moves the split. docker compose refuses a bind or a named
// volume like this (`invalid volume specification`) but starts a tmpfs or an
// anonymous volume at such a target, so only the first two say so. A volume's
// source cannot hold one at all (the name rule).
func TestALongFormMountWithAColonInAPathIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, entry, refusal string
	}{
		{"bind source", "{type: bind, source: ./da:ta, target: /y}", `volumes entry 1 of 1: the source "./da:ta" contains ` + "`:`" + `, which cannot be mounted (the runtime splits a mount at ` + "`:`" + `; docker compose refuses it as well) — rename the path without ` + "`:`"},
		{"bind target", "{type: bind, source: ./data, target: \"/y:z\"}", `volumes entry 1 of 1: the target "/y:z" contains ` + "`:`" + `, which cannot be mounted (the runtime splits a mount at ` + "`:`" + `; docker compose refuses it as well)`},
		{"named volume target", "{type: volume, source: hh, target: \"/y:z\"}", `volumes entry 1 of 1: the target "/y:z" contains ` + "`:`" + `, which cannot be mounted (the runtime splits a mount at ` + "`:`" + `; docker compose refuses it as well)`},
		{"anonymous volume target", "{type: volume, target: \"/y:z\"}", `volumes entry 1 of 1: the target "/y:z" contains ` + "`:`" + `, which cannot be mounted (the runtime splits a mount at ` + "`:`" + `) —`},
		{"tmpfs target", "{type: tmpfs, target: \"/y:z\"}", `volumes entry 1 of 1: the target "/y:z" contains ` + "`:`" + `, which cannot be mounted (the runtime splits a mount at ` + "`:`" + `) —`},
		{"a plain bind loads", "{type: bind, source: ./data, target: /y}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - "+tc.entry+"\nvolumes:\n  hh: {}\n"))
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("\n got %v\nwant it to contain %q", err, tc.refusal)
			}
		})
	}
}
