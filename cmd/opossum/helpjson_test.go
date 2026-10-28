package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// helpJSON runs the CLI with args and decodes what it printed as the JSON help.
func helpJSON(t *testing.T, args ...string) (helpDoc, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := runCLI(args, &out, &errOut); code != 0 {
		t.Fatalf("%v: exit %d, stderr %q", args, code, errOut.String())
	}
	var doc helpDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v: not JSON: %v\n%s", args, err, out.String())
	}
	return doc, out.String()
}

func helpPaths(doc helpDoc) []string {
	var paths []string
	for _, c := range doc.Commands {
		paths = append(paths, c.Path)
	}
	return paths
}

// Every way of asking for help gives the same JSON once `--format json` is given with
// it: `--help`, `-h`, `opossum help <command>`, the flag before or after, joined with
// `=` or apart.
func TestEachWayOfAskingForHelpGivesTheSameJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		ways [][]string
	}{
		{"a command", [][]string{
			{"up", "--help", "--format", "json"},
			{"up", "-h", "--format", "json"},
			{"up", "--format=json", "--help"},
			{"help", "up", "--format", "json"},
		}},
		{"a command with a --format of its own", [][]string{
			{"ps", "--help", "--format", "json"},
			{"ps", "-h", "--format=json"},
			{"help", "ps", "--format", "json"},
		}},
		{"the root", [][]string{
			{"--help", "--format", "json"},
			{"-h", "--format=json"},
			{"help", "--format", "json"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, first := helpJSON(t, tc.ways[0]...)
			for _, way := range tc.ways[1:] {
				if _, got := helpJSON(t, way...); got != first {
					t.Errorf("%v gives other JSON than %v", way, tc.ways[0])
				}
			}
		})
	}
}

// A command's help lists that command and the ones under it; the root's lists every
// visible command, in path order.
func TestTheJSONHelpListsTheCommandAndTheOnesUnderIt(t *testing.T) {
	t.Run("a command with none under it", func(t *testing.T) {
		doc, _ := helpJSON(t, "up", "--help", "--format", "json")
		if got := helpPaths(doc); !reflect.DeepEqual(got, []string{"opossum up"}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("a command with some under it", func(t *testing.T) {
		doc, _ := helpJSON(t, "ws", "--help", "--format", "json")
		want := []string{"opossum ws", "opossum ws ls", "opossum ws prune", "opossum ws rm", "opossum ws rollback", "opossum ws snapshot"}
		if got := helpPaths(doc); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("one under another", func(t *testing.T) {
		doc, _ := helpJSON(t, "ws", "rm", "--help", "--format", "json")
		if got := helpPaths(doc); !reflect.DeepEqual(got, []string{"opossum ws rm"}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("the root, sorted, without the hidden command", func(t *testing.T) {
		doc, _ := helpJSON(t, "--help", "--format", "json")
		paths := helpPaths(doc)
		if !sort.StringsAreSorted(paths) || paths[0] != "opossum" {
			t.Errorf("want the root first and the paths sorted, got %v", paths)
		}
		for _, p := range paths {
			if strings.Contains(p, "__supervise") || strings.HasSuffix(p, " help") || strings.HasSuffix(p, " completion") {
				t.Errorf("listed %q", p)
			}
		}
	})
}

// The commands and flags in the JSON are the ones the CLI inventory records: the same
// walk over the same tree, so the JSON cannot list a flag the CLI does not have, or
// miss one it has. Each flag is counted where it is defined, as the inventory counts
// it; inherited copies are checked against those.
func TestTheJSONHelpListsWhatTheInventoryRecords(t *testing.T) {
	doc, _ := helpJSON(t, "--help", "--format", "json")
	byType := map[string]string{}
	for pflagType, ours := range helpFlagTypes {
		byType[ours] = pflagType
	}
	defined := map[string]helpFlag{}
	var got []string
	for _, c := range doc.Commands {
		got = append(got, c.Path)
		for _, a := range c.Aliases {
			got = append(got, c.Path+" alias "+a)
		}
		for _, f := range c.Flags {
			if f.Inherited {
				continue
			}
			short := ""
			if f.Shorthand != "" {
				short = " -" + f.Shorthand
			}
			got = append(got, c.Path+" --"+f.Name+short+" "+byType[f.Type]+" = "+f.Default)
			defined[f.Name] = f
		}
	}
	sort.Strings(got)
	if want := cliInventory(newRootCmd()); !reflect.DeepEqual(got, want) {
		t.Errorf("the JSON help and the inventory differ\nJSON:      %v\ninventory: %v", got, want)
	}
	for _, c := range doc.Commands {
		for _, f := range c.Flags {
			if !f.Inherited {
				continue
			}
			parent, ok := defined[f.Name]
			if !ok || parent.Type != f.Type || parent.Default != f.Default || parent.Shorthand != f.Shorthand {
				t.Errorf("%s: inherited --%s does not match where it is defined: %+v vs %+v", c.Path, f.Name, f, parent)
			}
		}
	}
}

// The keys of the JSON, at every level, are the ones schema 1 promises. Removing or
// renaming one breaks a program that reads it; adding one is allowed and shows here.
func TestTheJSONHelpKeysAreSchemaOnes(t *testing.T) {
	_, raw := helpJSON(t, "--help", "--format", "json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	if got, want := keys(doc), []string{"commands", "schema", "version"}; !reflect.DeepEqual(got, want) {
		t.Errorf("top-level keys %v, want %v", got, want)
	}
	if doc["schema"] != float64(1) {
		t.Errorf("schema = %v, want 1", doc["schema"])
	}
	cmdKeys := map[string]bool{}
	flagKeys := map[string]bool{}
	for _, c := range doc["commands"].([]any) {
		cm := c.(map[string]any)
		for _, k := range keys(cm) {
			cmdKeys[k] = true
		}
		for _, f := range cm["flags"].([]any) {
			for _, k := range keys(f.(map[string]any)) {
				flagKeys[k] = true
			}
		}
	}
	set := func(m map[string]bool) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	// `deprecated` is written only for what is deprecated, and nothing is today.
	if got, want := set(cmdKeys), []string{"aliases", "flags", "path", "summary", "usage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("command keys %v, want %v", got, want)
	}
	if got, want := set(flagKeys), []string{"default", "description", "inherited", "name", "shorthand", "type"}; !reflect.DeepEqual(got, want) {
		t.Errorf("flag keys %v, want %v", got, want)
	}
}

// What one command's entry holds: the flags in name order, its own and those passed
// down, the hidden one left out, types in opossum's names, and the usage from the
// command's own line.
func TestTheJSONHelpEntryOfACommand(t *testing.T) {
	doc, _ := helpJSON(t, "up", "--help", "--format", "json")
	up := doc.Commands[0]
	if up.Usage != "opossum up [service...]" {
		t.Errorf("usage = %q", up.Usage)
	}
	if up.Summary == "" {
		t.Error("no summary")
	}
	var names []string
	byName := map[string]helpFlag{}
	for _, f := range up.Flags {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("flags not in name order: %v", names)
	}
	if _, ok := byName["from-docker"]; ok {
		t.Error("the hidden --from-docker is listed")
	}
	if _, ok := byName["help"]; ok {
		t.Error("--help is listed")
	}
	if f := byName["build"]; f.Inherited || f.Type != "bool" || f.Default != "false" {
		t.Errorf("--build = %+v, want up's own bool defaulting to false", f)
	}
	if f := byName["file"]; !f.Inherited || f.Type != "string-list" || f.Shorthand != "f" {
		t.Errorf("--file = %+v, want the root's string-list -f, inherited", f)
	}
	if f := byName["dns-domain"]; !f.Inherited || f.Default != "opossum" {
		t.Errorf("--dns-domain = %+v, want inherited with its default", f)
	}
	for _, f := range up.Flags {
		if f.Description == "" {
			t.Errorf("--%s has no description", f.Name)
		}
	}
	if doc.Version != newRootCmd().Version || doc.Version == "" {
		t.Errorf("version = %q", doc.Version)
	}
}

// A deprecated command or flag says so, with cobra's note, and what is not deprecated
// carries no `deprecated` key.
func TestTheJSONHelpSaysWhatIsDeprecated(t *testing.T) {
	root := &cobra.Command{Use: "opossum", Version: "x"}
	old := &cobra.Command{Use: "old", Short: "s", Deprecated: "use new", Run: func(*cobra.Command, []string) {}}
	old.Flags().Bool("gone", false, "d")
	_ = old.Flags().MarkDeprecated("gone", "use --kept")
	old.Flags().Bool("kept", false, "d")
	root.AddCommand(old)
	doc, err := helpDocFor(old)
	if err != nil {
		t.Fatal(err)
	}
	c := doc.Commands[0]
	if c.Deprecated != "use new" {
		t.Errorf("command deprecated = %q", c.Deprecated)
	}
	for _, f := range c.Flags {
		switch f.Name {
		case "gone":
			if f.Deprecated != "use --kept" {
				t.Errorf("--gone deprecated = %q", f.Deprecated)
			}
		case "kept":
			if f.Deprecated != "" {
				t.Errorf("--kept deprecated = %q", f.Deprecated)
			}
		}
	}
	raw, _ := json.Marshal(doc)
	if strings.Count(string(raw), `"deprecated"`) != 2 {
		t.Errorf("want a deprecated key on the command and on --gone only: %s", raw)
	}
}

// A flag of a type the JSON has no name for stops the JSON help instead of printing
// the library's name for it, which would then be part of what schema 1 promises.
func TestAFlagTypeWithoutAnOpossumNameIsRefused(t *testing.T) {
	root := &cobra.Command{Use: "opossum", Version: "x"}
	c := &cobra.Command{Use: "wait", Short: "s", Run: func(*cobra.Command, []string) {}}
	c.Flags().Duration("timeout", 0, "d")
	root.AddCommand(c)
	if _, err := helpDocFor(c); err == nil || !strings.Contains(err.Error(), `"duration"`) {
		t.Errorf("want the duration flag refused, got %v", err)
	}
}

// Anything else than `--help` with `--format json` is left to the CLI as it was: a
// `--format` without `--help`, another value, an unknown command, and a `--help` after
// the arguments of a command that takes its flags first. The refusals are the ones the
// CLI gave before the JSON help existed.
func TestOnlyHelpWithFormatJSONIsTaken(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		err  string // the stderr the CLI gave before, for the ones it refuses
	}{
		{"--format without --help on a command that has none", []string{"up", "--format", "json"}, "opossum: unknown flag: --format\n"},
		{"--format=json without --help", []string{"up", "--format=json"}, "opossum: unknown flag: --format\n"},
		{"another value with --help", []string{"up", "--help", "--format", "yaml"}, "opossum: unknown flag: --format\n"},
		{"an unknown command", []string{"nope", "--help", "--format", "json"}, "opossum: unknown command \"nope\" for \"opossum\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := helpJSONRequested(tc.args); ok {
				t.Fatalf("%v was taken as a request for the JSON help", tc.args)
			}
			var out, errOut bytes.Buffer
			if code := runCLI(tc.args, &out, &errOut); code != 1 || errOut.String() != tc.err || out.Len() != 0 {
				t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, %q", code, out.String(), errOut.String(), tc.err)
			}
		})
	}
	for _, args := range [][]string{
		{"exec", "web", "sh", "--help", "--format", "json"},
		{"run", "web", "sh", "-h", "--format", "json"},
	} {
		if _, ok := helpJSONRequested(args); ok {
			t.Errorf("%v: a --help after the arguments is the container's, not opossum's", args)
		}
	}
	t.Run("a --format of the command's own, with another value", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := runCLI([]string{"ps", "--help", "--format", "table"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "List services") {
			t.Errorf("want ps's text help, got exit %d: %q", code, out.String())
		}
	})
}

// The text help ends by saying the JSON form exists, for every command.
func TestTheTextHelpSaysTheJSONFormExists(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"up", "--help"}, {"ps", "-h"}, {"help", "ws", "rm"}} {
		var out, errOut bytes.Buffer
		if code := runCLI(args, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if !strings.HasSuffix(out.String(), "\n"+helpJSONHint+"\n") {
			t.Errorf("%v: the text help does not end with %q:\n%s", args, helpJSONHint, out.String())
		}
	}
}

// The commands are in path order because the JSON help sorts them, not because cobra
// happens to hand them over sorted: with cobra's sorting off and the commands added
// in reverse, the order is the same.
func TestTheJSONHelpSortsTheCommandsItself(t *testing.T) {
	was := cobra.EnableCommandSorting
	cobra.EnableCommandSorting = false
	defer func() { cobra.EnableCommandSorting = was }()
	root := &cobra.Command{Use: "opossum", Version: "x"}
	for _, name := range []string{"zz", "mm", "aa"} {
		root.AddCommand(&cobra.Command{Use: name, Short: "s", Run: func(*cobra.Command, []string) {}})
	}
	doc, err := helpDocFor(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := helpPaths(doc), []string{"opossum", "opossum aa", "opossum mm", "opossum zz"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
