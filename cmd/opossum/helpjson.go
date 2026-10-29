package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// helpSchema is the version of the shape `--help --format json` prints. While it is
// 1, a key is not removed and does not change its type; keys may be added.
const helpSchema = 1

// helpJSONHint is the line the text help ends with, so that a program reading it
// learns the JSON form exists without reading the docs.
const helpJSONHint = "Machine-readable: opossum [command] --help --format json"

type helpDoc struct {
	Schema   int           `json:"schema"`
	Version  string        `json:"version"`
	Commands []helpCommand `json:"commands"`
}

type helpCommand struct {
	Path       string     `json:"path"`
	Aliases    []string   `json:"aliases"`
	Summary    string     `json:"summary"`
	Usage      string     `json:"usage"`
	Deprecated string     `json:"deprecated,omitempty"`
	Flags      []helpFlag `json:"flags"`
}

type helpFlag struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
	Inherited   bool   `json:"inherited"`
	Deprecated  string `json:"deprecated,omitempty"`
}

// helpFlagTypes names a flag's type in opossum's own words, so that the JSON does
// not promise the names of the library that parses the flags. A flag of a type not
// listed here is a new type, and has to be named here (and in the docs) before the
// JSON can be printed: helpDocFor says so instead of guessing.
var helpFlagTypes = map[string]string{
	"bool":        "bool",
	"string":      "string",
	"int":         "int",
	"stringArray": "string-list",
}

// helpJSONRequested reports whether args ask for the JSON help, and for which
// command. It reads them as the CLI would, on a tree of its own that has a
// `--format` flag on every command, so that a command which lets flags follow its
// arguments (`exec`, `run`) reads a `--help` there as an argument, as it does now.
// It answers no for anything else, and the CLI then runs as it always has: a
// `--format` without `--help` on a command that has none is refused by it exactly
// as before.
func helpJSONRequested(args []string) (*cobra.Command, bool) {
	root := newRootCmd()
	root.InitDefaultHelpFlag()
	if len(args) > 0 && args[0] == "help" {
		args = append(args[1:], "--help")
	}
	cmd, rest, err := root.Find(args)
	if err != nil {
		return nil, false
	}
	var withFormat func(c *cobra.Command)
	withFormat = func(c *cobra.Command) {
		if c.Flags().Lookup("format") == nil && c.PersistentFlags().Lookup("format") == nil {
			c.Flags().String("format", "", "")
		}
		c.InitDefaultHelpFlag()
		for _, sub := range c.Commands() {
			withFormat(sub)
		}
	}
	withFormat(root)
	if cmd.ParseFlags(rest) != nil {
		return nil, false
	}
	help, _ := cmd.Flags().GetBool("help")
	format := cmd.Flags().Lookup("format")
	if !help || format == nil || format.Value.String() != "json" {
		return nil, false
	}
	// The command asked about, in the tree the CLI itself runs, with nothing added.
	// A hidden command has no help to give, so it is left to the CLI as before.
	real, _, err := newRootCmd().Find(strings.Fields(cmd.CommandPath())[1:])
	if err != nil || real.Hidden {
		return nil, false
	}
	return real, true
}

// writeHelpJSON prints cmd and the commands under it.
func writeHelpJSON(w io.Writer, cmd *cobra.Command) error {
	doc, err := helpDocFor(cmd)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// helpDocFor is the JSON help of cmd and the visible commands under it, in path
// order, each with its flags in name order: its own, and those its parents pass
// down (inherited). Hidden commands and flags, and `--help`, are left out, as the
// text help leaves them out; a deprecated flag, which the text help hides too, is
// listed with its note, since it is still taken.
func helpDocFor(cmd *cobra.Command) (helpDoc, error) {
	cmd.Root().InitDefaultVersionFlag()
	doc := helpDoc{Schema: helpSchema, Version: cmd.Root().Version, Commands: []helpCommand{}}
	var walk func(c *cobra.Command) error
	walk = func(c *cobra.Command) error {
		if c.Hidden {
			return nil
		}
		entry := helpCommand{
			Path:       c.CommandPath(),
			Aliases:    append([]string{}, c.Aliases...),
			Summary:    c.Short,
			Usage:      c.CommandPath(),
			Deprecated: c.Deprecated,
			Flags:      []helpFlag{},
		}
		if rest := strings.Fields(c.Use); len(rest) > 1 {
			entry.Usage += " " + strings.Join(rest[1:], " ")
		}
		var bad error
		add := func(inherited bool) func(f *pflag.Flag) {
			return func(f *pflag.Flag) {
				// A deprecated flag is hidden from the text help, but it is still taken,
				// so it is listed, with its note.
				if (f.Hidden && f.Deprecated == "") || f.Name == "help" {
					return
				}
				typ, ok := helpFlagTypes[f.Value.Type()]
				if !ok && bad == nil {
					bad = fmt.Errorf("flag --%s of %q has type %q, which the JSON help has no name for", f.Name, c.CommandPath(), f.Value.Type())
				}
				entry.Flags = append(entry.Flags, helpFlag{
					Name: f.Name, Shorthand: f.Shorthand, Type: typ, Default: f.DefValue,
					Description: f.Usage, Inherited: inherited, Deprecated: f.Deprecated,
				})
			}
		}
		c.LocalFlags().VisitAll(add(false))
		c.InheritedFlags().VisitAll(add(true))
		if bad != nil {
			return bad
		}
		sort.Slice(entry.Flags, func(i, j int) bool { return entry.Flags[i].Name < entry.Flags[j].Name })
		doc.Commands = append(doc.Commands, entry)
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(cmd); err != nil {
		return helpDoc{}, err
	}
	sort.Slice(doc.Commands, func(i, j int) bool { return doc.Commands[i].Path < doc.Commands[j].Path })
	return doc, nil
}

// withHelpJSONHint ends root's text help, and every command's, with helpJSONHint.
func withHelpJSONHint(root *cobra.Command) {
	text := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		text(c, args)
		fmt.Fprintln(c.OutOrStdout(), "\n"+helpJSONHint)
	})
}
