package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

const (
	errorFormatFlag  = "error-format"
	errorFormatEnv   = "OPOSSUM_ERROR_FORMAT"
	errorFormatText  = "text"
	errorFormatJSON  = "json"
	diagnosticSchema = 1
)

// warnSink receives the coded warnings when failures are reported as JSON.
var warnSink func(code, message string)

func newOrchestrator(p *compose.Project, rt *runtime.Runtime, domain string, out io.Writer) *orchestrator.Orchestrator {
	o := orchestrator.New(p, rt, domain, out)
	o.Warn = warnSink
	return o
}

// diagnosticLine is one line of `--error-format json`. Its keys and their types
// are part of what a script may rely on; the words of Message are not.
type diagnosticLine struct {
	Schema  int    `json:"schema"`
	Level   string `json:"level"`
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Exit    *int   `json:"exit,omitempty"`
}

func writeDiagnostic(w io.Writer, line diagnosticLine) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // an image reference's `&` stays readable
	_ = enc.Encode(line)
}

func writeWarningJSON(w io.Writer, code, message string) {
	writeDiagnostic(w, diagnosticLine{Schema: diagnosticSchema, Level: "warning", Source: "opossum", Code: code, Message: message})
}

// reportFailure says how a command failed, as a line of text or of JSON.
func reportFailure(w io.Writer, asJSON bool, err error, exit int) {
	if !asJSON {
		fmt.Fprintln(w, "opossum: "+quoted(err.Error()))
		return
	}
	source := "opossum"
	var ce *orchestrator.ContainerExitError
	if errors.As(err, &ce) {
		source = "container"
	}
	code := orchestrator.DiagCode(err)
	message := err.Error()
	if code != "" {
		// Only the opening: a code further in belongs to a wrapped message.
		message = strings.TrimPrefix(message, "["+code+"] ")
	}
	writeDiagnostic(w, diagnosticLine{Schema: diagnosticSchema, Level: "error", Source: source, Code: code, Message: message, Exit: &exit})
}

// reportsJSON decides how failures are reported. It reads the arguments itself
// because a failure to parse them has to be reported too. A value of the flag
// that is valid settles it without the variable being looked at; an empty
// variable is an unset one. A value that is neither word is refused as text.
func reportsJSON(args []string, getenv func(string) string) (bool, error) {
	if len(args) > 0 && args[0] == "__supervise" {
		return false, nil // its stderr is a log file for people
	}
	if value, ok := errorFormatInArgs(args); ok {
		if value != errorFormatText && value != errorFormatJSON {
			return false, fmt.Errorf("--%s must be %s or %s, not %q", errorFormatFlag, errorFormatText, errorFormatJSON, value)
		}
		return value == errorFormatJSON, nil
	}
	value := getenv(errorFormatEnv)
	if value != "" && value != errorFormatText && value != errorFormatJSON {
		return false, fmt.Errorf("%s must be %s or %s, not %q", errorFormatEnv, errorFormatText, errorFormatJSON, value)
	}
	return value == errorFormatJSON, nil
}

// passesThrough names the commands whose arguments after the first positional
// are the container's, not opossum's.
var passesThrough = map[string]bool{"run": true, "exec": true}

// errorFormatInArgs finds the flag without parsing, the way cobra would read the
// line: it knows which flags take a value and which words are subcommands. The
// last occurrence counts. It stops at `--`, and at the service of a command that
// hands the rest to the container.
func errorFormatInArgs(args []string) (value string, found bool) {
	cmd := newRootCmd()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return value, found
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			if sub := subcommandNamed(cmd, arg); sub != nil {
				cmd = sub
				continue
			}
			if passesThrough[cmd.Name()] {
				return value, found
			}
			continue
		}
		if name, ok := strings.CutPrefix(arg, "--"); ok {
			name, inline, hasInline := strings.Cut(name, "=")
			if name == errorFormatFlag {
				found = true
				value = inline
				if !hasInline {
					value = ""
					if i+1 < len(args) {
						i++
						value = args[i]
					}
				}
				continue
			}
			if !hasInline && takesValue(lookupFlag(cmd, name)) {
				i++
			}
			continue
		}
		if len(arg) == 2 && takesValue(lookupShorthand(cmd, arg[1:])) {
			i++
		}
	}
	return value, found
}

func subcommandNamed(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name || sub.HasAlias(name) {
			return sub
		}
	}
	return nil
}

func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	for _, set := range []*pflag.FlagSet{cmd.LocalFlags(), cmd.InheritedFlags()} {
		if f := set.Lookup(name); f != nil {
			return f
		}
	}
	return nil
}

func lookupShorthand(cmd *cobra.Command, name string) *pflag.Flag {
	for _, set := range []*pflag.FlagSet{cmd.LocalFlags(), cmd.InheritedFlags()} {
		if f := set.ShorthandLookup(name); f != nil {
			return f
		}
	}
	return nil
}

func takesValue(f *pflag.Flag) bool { return f != nil && f.NoOptDefVal == "" }
