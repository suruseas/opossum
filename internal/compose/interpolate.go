package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// varLookup resolves a variable name to its value and whether it was set at all.
// An empty-but-set variable returns ("", true).
type varLookup func(name string) (string, bool)

// hostGatewayVar is a built-in interpolation variable that resolves to the
// address a container can use to reach services running on the host. The default
// network is NAT-only (no host.docker.internal, no --add-host), but a container
// can reach the host at the host's own LAN address, so that is what this exposes.
// Reference it from a compose file, e.g.
//
//	environment:
//	  OLLAMA_HOST: http://${OPOSSUM_HOST_GATEWAY}:11434
//
// A shell env var or `.env` entry of the same name overrides it, and if the host
// address can't be determined (e.g. offline) it stays unset so a `:-` default
// applies.
const hostGatewayVar = "OPOSSUM_HOST_GATEWAY"

// hostGatewayFunc resolves the host gateway address; overridable in tests.
var hostGatewayFunc = defaultHostGateway

var (
	hostGWOnce sync.Once
	hostGWAddr string
)

// defaultHostGateway returns the host's primary LAN address — the source IP the
// OS would pick for outbound traffic, which is also the address a container on
// the default network reaches the host at. It opens no connection (UDP Dial just
// selects a route) and is cached for the process. Returns "" if it can't be
// determined, e.g. the host has no network.
func defaultHostGateway() string {
	hostGWOnce.Do(func() {
		conn, err := net.Dial("udp", "1.1.1.1:80")
		if err != nil {
			return
		}
		defer conn.Close()
		if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
			hostGWAddr = a.IP.String()
		}
	})
	return hostGWAddr
}

// envScope is the layered scope an env-file value expands against.
//
// The layers exist because "which definition wins" has two different answers
// depending on whether the other definition is at the same level. Measured
// against docker compose v5.3.1:
//
//   - A strictly outer level always wins. The shell beats any env file, and the
//     project's `.env` beats a key defined on the line above in a service's
//     `env_file:`.
//   - Within one level, the files behave as a single map filled top to bottom:
//     the LAST assignment wins, and a file's own line beats a file read before
//     it. `one.env` setting `A=first` then `two.env` setting `A=second` and
//     reading `B=${A}` gives `B=second`, not `B=first`.
//
// The second rule is why level is a map rather than another lookup: a file's own
// entries and the entries of files already read at that level are the same
// thing, so they cannot be ranked against each other at all.
type envScope struct {
	// outer is everything defined at a strictly outer level. It wins.
	outer varLookup
	// level accumulates this level's entries as each file is read. It is written
	// through while a file is being parsed, so a value sees the keys above it —
	// including one a previous file at this level defined and this file has since
	// overwritten.
	level map[string]string
	// builtin is opossum's own OPOSSUM_HOST_GATEWAY, ranked last so that both the
	// shell and any env file can override it — the contract this package and
	// docs/compatibility.md both state.
	builtin varLookup
	// faults, when not nil, is where a variable name a `.env` line refuses is
	// recorded instead of failing the read: the line is left out and the read goes
	// on (see parseDotEnv). It is set for the project's own `.env` and not for a
	// service's `env_file:`, which is read only by the commands that start or print
	// a service.
	faults *[]error

	// values, when not nil, is where the first refusal of a value docker compose
	// reads as another kind — a string that is not a number, one outside a key's
	// bounds, a count below zero — is recorded instead of failing the read
	// (LoadFilesEnvDirSoft). It is for the commands that take a project down: an
	// earlier opossum passed such a value on, and a project may be running on it.
	values *[]error
}

// firstFault is the first name a `.env` line refused, or nil.
func (s envScope) firstFault() error {
	if s.faults == nil || len(*s.faults) == 0 {
		return nil
	}
	return (*s.faults)[0]
}

// lookup is the scope as it stands: outer, then this level, then the built-in.
// It serves both the compose file and a value being expanded mid-file — level is
// a live map, so a value sees the keys above it and not the ones below simply
// because they are not in the map yet. There is no second, "finished" variant:
// two identical implementations would be two places to edit.
func (s envScope) lookup() varLookup {
	return chainLookup(s.outer, mapLookup(s.level), s.builtin)
}

// inner returns the scope for files read at a level underneath this one — a
// service's `env_file:`. This scope's outer and level become strictly outer, and
// the new level starts empty.
//
// between is the service's own `environment:` block, which docker compose ranks
// under the project's `.env` and over the `env_file:` chain. It is a real level
// and not a detail: an `env_file:` value may reference a key that exists only in
// `environment:`, and where both define a key, `environment:` is what the file's
// values see.
//
// The built-in is NOT folded into the outer chain, even though lookup() would
// put it there. Folding it made it outrank the inner level, so an `env_file:`
// that set OPOSSUM_HOST_GATEWAY could not use its own value on the next line —
// one file holding two values for one variable, which is the exact defect this
// package fixed one level up. It stays ranked last at every level.
func (s envScope) inner(between map[string]string) envScope {
	return envScope{
		outer:   chainLookup(s.outer, mapLookup(s.level), mapLookup(between)),
		level:   map[string]string{},
		builtin: s.builtin,
	}
}

// projectNameVar is the variable docker compose reads the project's name from,
// and profilesVar the one it reads the active profiles from.
const (
	projectNameVar = "COMPOSE_PROJECT_NAME"
	profilesVar    = "COMPOSE_PROFILES"
)

// EnvProjectName is Project.EnvName for a caller that has no project to ask: the
// name COMPOSE_PROJECT_NAME gives, read the way a load from dir would read it
// (the shell over the `.env` in dir, or over envFiles when given), without
// reading any compose file.
func EnvProjectName(dir string, envFiles []string) (string, error) {
	return EnvValue(dir, envFiles, projectNameVar)
}

// EnvValue is what a variable that configures opossum itself — not one a
// compose file refers to — is set to, read the way a load from dir reads its
// variables: the shell over the `.env` in dir, or over envFiles when given. ""
// when it is not set, and when it is set and empty.
func EnvValue(dir string, envFiles []string, name string) (string, error) {
	// Soft: a name a `.env` line refuses is left out and not a failure here, since
	// this only asks for one variable — the commands that take a project down ask
	// it too, and are not to be stopped by a line they do not read.
	scope, err := loadEnvLayers(dir, "", envFiles, true)
	if err != nil {
		return "", err
	}
	v, _ := scope.lookup()(name)
	return v, nil
}

// EnvNameFault is the first variable name a project's `.env` (or an `--env-file`)
// refuses — a space or punctuation in one — or nil. EnvValue reads past such a
// line, and a caller that has no compose file to load, and so no Project to ask,
// asks here so that the `.env` is what it reports first (as docker compose does).
func EnvNameFault(dir string, envFiles []string) error {
	scope, err := loadEnvLayers(dir, "", envFiles, true)
	if err != nil {
		return err
	}
	return scope.firstFault()
}

// loadEnv builds the scope used for interpolation: values from a `.env` file in
// dir (or the given --env-file paths), the process environment, and the built-in.
// A missing default .env file is not an error.
func loadEnv(dir string, envFiles []string) (envScope, error) {
	return loadEnvLayers(dir, "", envFiles, false)
}

// loadEnvLayers is loadEnv with a second `.env` under the first: the one in
// under, read for what the `.env` in dir (and the shell) do not set. It is how
// docker compose reads a project whose compose file COMPOSE_FILE chose in
// another directory than the one it runs in — the working directory's `.env`
// first, then the compose file's directory's (measured on v5.5.1):
//
//   - a variable the first sets — to nothing, too — is the first's;
//   - a value in the second sees the shell, the first file, and the lines above
//     it in its own file, in that order, so the first file's value of a variable
//     wins over the second's own; a value in the first sees nothing of the second;
//   - with an `--env-file` there is one env file and no second place.
//
// under is "" (or dir itself) where there is no second place.
//
// soft is whether a variable name a `.env` refuses is recorded (envScope.faults)
// and read past, rather than failing the read. A project's commands that start or
// print a service refuse it afterwards (Project.CheckDeclaredNames); the ones that
// take a project down go on, because an earlier opossum passed such a name on and
// a project may be running on it.
func loadEnvLayers(dir, under string, envFiles []string, soft bool) (envScope, error) {
	scope := envScope{
		outer: func(name string) (string, bool) { return os.LookupEnv(name) },
		level: map[string]string{},
		// Resolved lazily so it costs nothing unless referenced.
		builtin: func(name string) (string, bool) {
			if name == hostGatewayVar {
				if addr := hostGatewayFunc(); addr != "" {
					return addr, true
				}
			}
			return "", false
		},
	}

	if soft {
		scope.faults = &[]error{}
	}
	files := envFiles
	if len(files) == 0 {
		files = []string{filepath.Join(dir, ".env")}
	}
	// Explicit --env-file(s) replace the default .env; later files win, and a
	// named file that's missing is an error (unlike the optional default .env).
	named := len(envFiles) > 0
	for _, f := range files {
		if named {
			if _, err := os.Stat(f); err != nil {
				return envScope{}, fmt.Errorf("env file %q: %w", f, err)
			}
		} else if isDir(f) {
			// A `.env` that is a directory is no `.env`: docker compose reads on
			// as if it were not there (measured). One that was named is refused.
			continue
		}
		if _, err := parseDotEnv(f, scope); err != nil {
			return envScope{}, err
		}
	}
	if file := filepath.Join(under, ".env"); !named && under != "" && under != dir && !isDir(file) {
		second := scope.inner(nil)
		second.faults = scope.faults
		if _, err := parseDotEnv(file, second); err != nil {
			return envScope{}, err
		}
		for k, v := range second.level {
			if _, set := scope.level[k]; !set {
				scope.level[k] = v
			}
		}
	}
	return scope, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// mapLookup adapts a map to a varLookup. The map is captured by reference, so a
// lookup built from one that is still being filled sees each entry as it lands.
func mapLookup(m map[string]string) varLookup {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

// chainLookup tries each scope in turn, first match winning.
func chainLookup(scopes ...varLookup) varLookup {
	return func(name string) (string, bool) {
		for _, s := range scopes {
			if v, ok := s(name); ok {
				return v, true
			}
		}
		return "", false
	}
}

// parseDotEnv reads a KEY=VALUE (or KEY: VALUE) file, matching docker compose's
// env_file handling. Blank lines and `#` comments are ignored, an `export ` prefix
// is dropped, and surrounding single/double quotes are stripped. A value whose
// opening quote isn't closed on the same line continues across lines — e.g. a
// multi-line PEM key — keeping the embedded newlines. A missing file yields an
// empty map (no error).
//
// Values are expanded against scope plus the entries already read from this file,
// so `B=${A}/b` after `A=/a` gives `/a/b`. The rules were measured against docker
// compose v5.3.1 rather than assumed, and two of them are not what a reader would
// guess:
//
//   - Only what is defined ABOVE the line is visible. A reference to a key defined
//     further down expands to empty, not to that key's value.
//   - A single-quoted value is NOT expanded, the way a shell treats single quotes.
//     Double-quoted and unquoted values are.
//
// An undefined reference with no default expands to empty (interpolate's rule),
// which is also what compose does here.
func parseDotEnv(path string, scope envScope) (map[string]string, error) {
	if scope.level == nil {
		// Writing through to a nil map panics several branches later, which is a
		// poor way to learn that a scope was built by hand instead of by loadEnv
		// or inner.
		return nil, fmt.Errorf("reading %s: the scope has no level map", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	// A byte-order mark belongs to the file, not to the first line. Editors on
	// Windows write one without being asked, and leaving it in makes the first
	// key `\ufeffA` instead of `A` — so `${A}` finds nothing and expands to
	// empty, with no error anywhere to say why. Only at the very start: a mark
	// further in is a character somebody put there, and removing it would be
	// inventing a rule about the contents.
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	out := map[string]string{}
	for i := 0; i < len(lines); i++ {
		raw := strings.TrimSpace(lines[i])
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		key, val, ok := splitEnvLine(raw)
		// A name this refuses (a space or punctuation) is recorded and its line read
		// past when the scope asks for that (the project's `.env`), and is the read's
		// failure otherwise. The line is still walked to its end, so that a quoted
		// value that spans lines does not leave its second line to be read as one of
		// its own.
		var refused error
		if !ok && strings.Contains(raw, "\ufeff") {
			// A mark on a line with no separator lands in a name too — the whole
			// line is the name, and there is nothing to assign. Saying "no `=`"
			// would be true and useless: what the reader has to remove is a
			// character they cannot see.
			return nil, markInNameErr(path, i+1)
		}
		if !ok {
			// Where, and what is missing — but not the line itself. An env file is
			// where passwords and tokens live, and a token pasted onto a line of its
			// own is exactly the shape that lands here; quoting it back would put it
			// in the terminal, the CI log, and whatever issue the output gets pasted
			// into. The reader can open their own file at the line named.
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, but the line has no `=`", path, i+1)
		}
		key = strings.TrimSpace(key)
		// Only the two rules an earlier opossum did not hold are read past (a space
		// and the punctuation): a project may have been started on a name they
		// refuse. An empty name and a mark in a name were refused before, so nothing
		// was ever started on one.
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, i+1)
		}
		if strings.Contains(key, "\ufeff") {
			return nil, markInNameErr(path, i+1)
		}
		switch {
		case strings.Contains(key, " "):
			// A space inside the name is refused, as docker compose refuses it
			// (v5.5.1, measured: `failed to read …: line 1: key cannot contain a
			// space`). Only the space itself: a tab or a no-break space in a name is
			// taken by docker compose, and so is a space after the name — that is the
			// blank before the `=`, dropped above. The name is not quoted back: this is
			// the line an env file's secrets are on.
			refused = fmt.Errorf("%s:%d: key cannot contain a space", path, i+1)
		default:
			// And the ASCII punctuation and control characters docker compose refuses
			// in a name (`unexpected character "/" in variable name`, v5.5.1, measured
			// for every printable ASCII character and every control character): a name
			// is letters, digits and `_ . - [ ]` and what a reader can see in one. The
			// characters it takes past ASCII are not asked about here (it takes letters
			// and a no-break space and refuses symbols and some spaces; measured on a
			// dozen, not on the whole of Unicode). The name is not quoted back, for the
			// reason above.
			if at := strings.IndexFunc(key, badNameChar); at >= 0 {
				refused = fmt.Errorf("%s:%d: unexpected character %q in variable name", path, i+1, string([]rune(key[at:])[0]))
			}
		}
		if refused != nil {
			if err := failName(scope, refused); err != nil {
				return nil, err
			}
		}
		val = strings.TrimSpace(val)
		if refused != nil {
			// Read past: the name is recorded and nothing of the line is kept — and a
			// quoted value that opens here is walked to its closing quote, unexpanded.
			if len(val) > 1 && (val[0] == '"' || val[0] == '\'') && closingQuote(val, val[0], 1) < 0 {
				start, closed := i+1, false
				for i+1 < len(lines) {
					i++
					if closingQuote(lines[i], val[0], 0) >= 0 {
						closed = true
						break
					}
				}
				if !closed {
					// Not read past: a quote that never closes takes the rest of the
					// file, and every line in it, `COMPOSE_PROJECT_NAME` among them,
					// with it — as it does for a name that is fine.
					return nil, fmt.Errorf("%s:%d: unterminated quoted value for %q", path, start, key)
				}
			}
			continue
		}

		// A quoted value whose closing quote isn't on this line spans multiple
		// lines (e.g. a PEM key): gather following lines verbatim, preserving the
		// newlines, until the closing quote. An unterminated value is an error,
		// matching docker compose.
		if len(val) > 1 && (val[0] == '"' || val[0] == '\'') && closingQuote(val, val[0], 1) < 0 {
			q := val[0]
			literal := q == '\''
			start := i + 1
			var sb strings.Builder
			sb.WriteString(val[1:]) // content after the opening quote
			closed := false
			for i+1 < len(lines) {
				i++
				sb.WriteByte('\n')
				if j := closingQuote(lines[i], q, 0); j >= 0 {
					sb.WriteString(lines[i][:j])
					closed = true
					break
				}
				sb.WriteString(lines[i])
			}
			if !closed {
				return nil, fmt.Errorf("%s:%d: unterminated quoted value for %q", path, start, key)
			}
			v, err := expandEnvValue(unescapeEnv(sb.String(), literal), literal, scope, path, start)
			if err != nil {
				return nil, err
			}
			out[key] = v
			scope.level[key] = v
			continue
		}
		val, literal := cutEnvValue(val)
		v, err := expandEnvValue(val, literal, scope, path, i+1)
		if err != nil {
			return nil, err
		}
		out[key] = v
		// Written through as we go: the next line sees this key, and so does the
		// next file at this level.
		scope.level[key] = v
	}
	return out, nil
}

// failName is what a refused variable name comes to: the read's failure, or —
// where the scope records them (the project's `.env`) — a note kept for later and
// no failure at all.
func failName(scope envScope, err error) error {
	if scope.faults == nil {
		return err
	}
	*scope.faults = append(*scope.faults, err)
	return nil
}

// markInNameErr is what a byte-order mark outside the file's first position gets.
//
// One mark, at the very start, is the file saying how it is encoded and is
// dropped when the file is read. Anywhere else it ends up in a name, and a name
// nobody can type is the quiet kind of failure: the reference to it finds nothing
// and expands to empty with nothing to say why. docker compose v5.3.1 refuses
// this too (measured 2026-08-21). It quotes the whole line back in its message;
// this names the place and the character instead, because the line is in an env
// file.
func markInNameErr(path string, line int) error {
	return fmt.Errorf("%s:%d: a byte-order mark (U+FEFF) in a variable name; "+
		"one at the very start of the file is the encoding and is ignored, "+
		"but here it is part of the name", path, line)
}

// splitEnvLine splits an env_file line into key and value on the first `=` or `:`
// (whichever appears first). `=` is the canonical separator; `:` is accepted for
// docker compose compatibility.
func splitEnvLine(s string) (key, val string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' || s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// cutEnvValue reads a one-line env-file value the way docker compose
// (v5.5.0) reads one, and says whether it was single-quoted (which is what
// suppresses expansion of its contents). A quoted value is what stands
// between its quotes — the closing quote is the first one not preceded by
// a backslash, in either style (`"a\"b"`, `'a\'b'`) — and whatever follows
// it, a comment or anything else, is dropped, and the escapes inside are
// read (unescapeEnv). An unquoted value ends at the
// first ` #` (a space, then a hash: `a#b` is whole, a tab before the hash
// does not count, and a value that is only `# …` after the `=` is kept),
// with the blanks before it trimmed. Before this, `H=with # hash` was the
// value `with # hash`, and `"q" # note` the text `"q" # note`, quotes and
// all.
func cutEnvValue(val string) (string, bool) {
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		if j := closingQuote(val, val[0], 1); j >= 0 {
			return unescapeEnv(val[1:j], val[0] == '\''), val[0] == '\''
		}
	}
	if i := strings.Index(val, " #"); i >= 0 {
		val = strings.TrimRight(val[:i], " \t")
	}
	return val, false
}

// closingQuote is the index in s, at or after from, of the quote q that
// closes a quoted env-file value — the first one not preceded by a
// backslash — or -1 when the value does not close on this text.
func closingQuote(s string, q byte, from int) int {
	for j := from; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case q:
			return j
		}
	}
	return -1
}

// unescapeEnv reads the escapes inside a quoted env-file value the way
// docker compose (v5.5.0) reads them. Between double quotes `\"` and `\\`
// stand for the character after the backslash, `\$` for a dollar that is
// not a reference, and `\n`, `\t`, `\r` for the control character (docker
// compose reads `\a` and `\b` too; this does not); any other pair (`\x`,
// `\'`) is kept as written. Between single quotes only `\'` is read, and everything else
// (`\\`, `\n`) is kept as written. An unquoted value is not read here at
// all: every backslash in it is text.
func unescapeEnv(s string, single bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		c := s[i+1]
		switch {
		case single && c == '\'':
			b.WriteByte(c)
		case !single && (c == '"' || c == '\\'):
			b.WriteByte(c)
		case !single && c == '$':
			// A literal dollar, which expansion would otherwise read as a
			// reference: written as the `$$` that expansion reads as one `$`.
			b.WriteString("$$")
		case !single && c == 'n':
			b.WriteByte('\n')
		case !single && c == 't':
			b.WriteByte('\t')
		case !single && c == 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte('\\')
			b.WriteByte(c)
		}
		i++
	}
	return b.String()
}

// expandEnvValue expands one env-file value against the scope as it stands right
// now — see envScope for which definition wins.
func expandEnvValue(val string, literal bool, scope envScope, path string, line int) (string, error) {
	if literal || !strings.ContainsRune(val, '$') {
		return val, nil
	}
	b, err := interpolate([]byte(val), scope.lookup())
	if err != nil {
		return "", fmt.Errorf("%s:%d: %w", path, line, err)
	}
	return string(b), nil
}

// emptied is what an expansion writes where a reference produced nothing.
//
// Expanding raw text before the parser loses a value that was only a reference:
// `KEY: ${NOSUCHVAR}` becomes `KEY:`, which YAML reads as null, and under
// `environment:` null is not "empty" but "inherit this one from the host" — so a
// service is handed a value the compose file never mentions.
//
// A character that survives parsing says where the value went. The parser then
// answers every question that reading positions in the text could not: an item of
// a flow sequence still exists (`[a, ${X}]` is two items, not one), a value
// written under its key is found, and the inside of a `|` block is text so the
// character rides along in it. A comment is kept by the parser rather than
// dropped, so the mark has to be taken out of one — but a mark in a comment is
// never mistaken for the value beside it, which is what reading positions could
// not manage.
//
// U+E000 is in the private use area: it has no meaning of its own, so a compose
// file holding one is either doing something this cannot support or is corrupt.
// Either way it is refused rather than quietly conflated with an emptied value.
const emptied = "\ue000"

// held is what an expansion writes where a reference produced a value that
// cannot ride in the text: one carrying a line break. Expansion happens before
// the parser, so writing the value itself would push its second line into the
// document as structure — `KEY: ${PEM}` with a three-line PEM stops parsing at
// all, and docker (which expands after parsing) loads the same file fine.
//
// The marker is one line — held, the value's index, held again — so the
// document parses, and the repair pass that already takes the emptied mark out
// of the tree puts the real value back into the scalar that carries the
// marker. Downstream reads the tree, so it sees the value whole, line breaks
// and all, exactly as a post-parse expansion would have handed it over.
//
// U+E001 is private-use like U+E000, refused on the same grounds wherever it
// arrives already written (the file, a variable's value): a marker that could
// be forged is an index into someone else's table.
const held = "\ue001"

// heldMarker matches what expansion wrote: the index between two marks.
var heldMarker = regexp.MustCompile(held + `(\d+)` + held)

// interpolated is an expanded compose document, in whichever form survived.
//
// The tree is the better one and is used when there is one: it carries the
// positions the parser found in the text as written, so a failure further on
// names the line the reader would count to. Bytes are always there as the
// fallback, and are what the caller parses when no tree could be handed over.
//
// One method reads it, rather than the caller choosing: every consumer that
// picked the wrong one would silently get the old behaviour back, and a suite
// with no assertion about line numbers would stay green through it.
type interpolated struct {
	// nameEmptied is that the top-level `name:` was written with a reference and came to nothing (`name: ${P:-}` with
	// P unset or empty): docker compose refuses that for the project name, where `name: ""` falls back to the directory.
	nameEmptied bool
	// node is the document with the marks taken out, or nil when there is no tree
	// to hand over. Nil has two causes and they are not the same: no mark was
	// written at all (nothing needed repairing, and the bytes are already right),
	// or the text did not parse (below).
	node *yaml.Node
	// raw is always set: the expanded document. When the text did not parse, the
	// marks are still in it — on purpose.
	raw []byte
	// written is the file as the reader wrote it, before anything was expanded:
	// what a check about the text itself has to read. docker compose expands values
	// and not the keys of a mapping, and counts its lines in this text, so a
	// question about a key written twice is asked of this and not of raw — an
	// expanded `${A}` would otherwise read as whatever A holds. Nil for a document
	// no file text stands behind (the merge of several).
	written []byte
}

// into decodes the document into v.
func (d interpolated) into(v any) error {
	node := d.node
	if node == nil {
		var parsed yaml.Node
		if err := yaml.Unmarshal(d.raw, &parsed); err != nil {
			return yaml.Unmarshal(d.raw, v) // the caller reports the syntax error in its own words
		}
		node = &parsed
	}
	spreadSequenceMerges(node)
	// A tree is decoded without the merge key that brings a block in by itself: the load that starts a project refuses the file (validateOne, or where a file is
	// read first the check of what was written, which says it of the alias), and a command that takes a project down reads it so (#1475), the refusal kept. The tree
	// is the one the caller holds, so what is taken out here is out for what reads it after.
	selfMerging(node, true)
	return node.Decode(v)
}

// intoMarked is into with the `mode` of a `secrets` or `configs` entry that is written `-0` read as the word negZeroMode: docker compose
// reads `-0` as a float and refuses a mode that stays so, once the files are merged, where a later file or an extending service that writes the
// mode over it makes the file fine; the tree the files are merged as would read it as the integer 0 and lose the way it was written (#1778).
func (d interpolated) intoMarked(v any) error {
	node := d.node
	if node == nil {
		var parsed yaml.Node
		if err := yaml.Unmarshal(d.raw, &parsed); err != nil {
			return yaml.Unmarshal(d.raw, v)
		}
		node = &parsed
	}
	markNegativeZeroModes(node)
	spreadSequenceMerges(node)
	// A tree is decoded without the merge key that brings a block in by itself: the load that starts a project refuses the file (validateOne, or where a file is
	// read first the check of what was written, which says it of the alias), and a command that takes a project down reads it so (#1475), the refusal kept. The tree
	// is the one the caller holds, so what is taken out here is out for what reads it after.
	selfMerging(node, true)
	return node.Decode(v)
}

// spreadSequenceMerges puts the list an alias stands for where a merge key holds the alias (`<<: *l` over `x-l: &l [*a, *b]`): docker compose
// reads that as the merge of the mappings in the list, as it reads the list written there, where yaml.v3 refuses the alias to a list
// (`map merge requires map or sequence of maps as the value`), in a service, a block of one, a list of items, and the other mappings of the
// file alike; not at the top of the document, where docker compose refuses it. The list's own items are left as they are — aliases to mappings, which it merges.
func spreadSequenceMerges(n *yaml.Node) {
	spreadSequenceMergesThrough(n, map[*yaml.Node]bool{}, map[*yaml.Node]bool{}, map[*yaml.Node]bool{})
}

// spreadSequenceMergesThrough is spreadSequenceMerges that goes into what an alias stands for as well, once: an anchor a `!reset` took out of the tree with the
// key it was written under (`x-r: !reset {a: &in {<<: *e}}`) is reached only from the alias that uses it (`environment: *in`), and docker compose reads it there
// (#1874).
// An alias's target is walked once however many aliases name it: a block that holds itself (`x-a: &a {k: *a}`) would never come to an end, and a lattice of
// aliases (two branches over forty levels, measured) would be walked once for every path through it. This runs before the decode, which is what refuses either.
// A node is walked once as well, whatever reached it: a block that an alias reaches again after the list it is in was walked (`x-l: &l [&m {K: v, <<: *l}]`, then
// `x-y: *m`) is walked with the list no longer on the path, where the merge key is spread and makes a cycle with no alias in it, which seen, kept for the
// targets of aliases, does not stop (#1898).
// And the list is not put where its own block is: onPath holds the nodes this walk is inside, and a merge key whose alias is to one of them is left as it is,
// which the decode refuses (`map merge requires map or sequence of maps`) where it would have gone round for ever in every file the decode reads (an
// override file, an included one, a service, a declaration) and not only in the walks here.
func spreadSequenceMergesThrough(n *yaml.Node, seen, visited, onPath map[*yaml.Node]bool) {
	if n == nil || visited[n] {
		return
	}
	visited[n] = true
	onPath[n] = true
	defer delete(onPath, n)
	// A merge key at the top of the document is refused whatever it holds (docker compose: `map merge requires …` at the root), and not spread.
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 && n.Content[0].Kind == yaml.MappingNode {
		for _, c := range n.Content[0].Content {
			spreadSequenceMergesThrough(c, seen, visited, onPath)
		}
		return
	}
	if n.Kind == yaml.AliasNode && n.Alias != nil && !seen[n.Alias] {
		seen[n.Alias] = true
		spreadSequenceMergesThrough(n.Alias, seen, visited, onPath)
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "<<" {
				if v := n.Content[i+1]; v.Kind == yaml.AliasNode && v.Alias != nil && v.Alias.Kind == yaml.SequenceNode && !onPath[v.Alias] {
					n.Content[i+1] = v.Alias
				}
			}
		}
	}
	for _, c := range n.Content {
		spreadSequenceMergesThrough(c, seen, visited, onPath)
	}
}

// interpolateDocument expands a compose FILE and puts back the emptiness that
// expanding raw text would otherwise turn into null.
//
// The document is parsed and every scalar carrying the mark has it removed. A
// scalar that was nothing but the mark becomes an empty string, which is what
// Docker Compose reads such a value as (measured against v5.3.1). A `KEY:`
// written by hand still means what it says: nothing expanded there, so there is
// no mark on it.
//
// The tree is handed on rather than written back out. Writing it out used to be
// how the marks were removed, and it cost the reader their line numbers: blank
// lines went, a literal block collapsed, a flow sequence opened out, and every
// later failure named a line several off from the one in their file — in both
// directions, growing with the document.
//
// interpolate, which expands `.env` values and `${VAR:-default}` arguments, marks
// nothing: those are not YAML, and a value like `LIB=${PREFIX}/lib:` ends in a
// colon without being a mapping entry.
func interpolateDocument(raw []byte, lookup varLookup) (interpolated, error) {
	if bytes.Contains(raw, []byte(emptied)) {
		return interpolated{}, fmt.Errorf("the compose file contains U+E000, a private-use character this uses to " +
			"track values that expand to nothing; remove it (it is not something a compose file needs)")
	}
	if bytes.Contains(raw, []byte(held)) {
		return interpolated{}, fmt.Errorf("the compose file contains U+E001, a private-use character this uses to " +
			"carry expanded values through parsing; remove it (it is not something a compose file needs)")
	}
	var vals []heldValue
	out, err := expand(raw, lookup, emptied, &vals)
	if err != nil {
		var u unterminatedRef
		if errors.As(err, &u) {
			return interpolated{}, u.onLine(raw)
		}
		return interpolated{}, err
	}
	if !bytes.Contains(out, []byte(emptied)) && len(vals) == 0 {
		// Nothing expanded to nothing and nothing is held aside, so there is
		// nothing to repair and the bytes are already what the caller should read.
		return interpolated{raw: out, written: raw}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(out, &doc); err != nil {
		// Hand the bytes back exactly as they are, and let the caller report the
		// syntax error in its own words — with the file name it knows, and the hint
		// about indentation and quoting that this has no business replacing. Only
		// a file with a mistake of its own gets here: a value rides through parsing
		// as a marker and an empty reference as a mark, so neither turns a document
		// that parsed into one that does not (a reference inside an anchor or
		// alias name never parsed to begin with, there as in docker compose).
		//
		// Unchanged means the marks stay in. Taking them out is what would do harm:
		// a mark stands between what was on either side of it, so removing it JOINS
		// them (`x${NOPE}y` reads back as `xy`), and the caller would report a file
		// nobody wrote. Left in, the bytes fail to parse for the caller exactly as
		// they failed here, which is the truth.
		return interpolated{raw: out, written: raw}, nil
	}
	emptiedName := nameEmptied(&doc, vals)
	unmark(&doc)
	restore(&doc, vals, false)
	return interpolated{node: &doc, raw: out, written: raw, nameEmptied: emptiedName}, nil
}

// restore puts held values back into the scalars whose markers stand for them,
// after unmark — the two marks answer different questions and neither may see
// the other's. A scalar that gains a line break stays a scalar: the tree is
// decoded, not written back out, and a decoded string carries its newlines
// whole, which is exactly what a post-parse expansion would have produced.
//
// A scalar that is a mapping's key gets back what was written in its place, not
// what the reference resolved to: docker compose expands the values of a
// mapping and never its keys, so `E${SFX}: 1` under `environment:` names a
// variable `E${SFX}` there.
func restore(n *yaml.Node, vals []heldValue, inKey bool) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, held) {
		n.Value = heldMarker.ReplaceAllStringFunc(n.Value, func(m string) string {
			i, err := strconv.Atoi(strings.Trim(m, held))
			if err != nil || i < 0 || i >= len(vals) {
				return m // not one of ours; the document was refused if it held the mark, so this cannot happen
			}
			if inKey {
				return vals[i].written
			}
			return vals[i].value
		})
		// The parser already resolved the marker as a string and wrote that
		// tag, so this pin changes nothing for a plain scalar; it is kept so
		// that a value restored into a scalar the author tagged (`!!int
		// ${TAG}`) is still the text it is.
		n.Tag = "!!str"
	}
	for i, c := range n.Content {
		restore(c, vals, n.Kind == yaml.MappingNode && i%2 == 0)
	}
}

// unmark takes the mark out of every scalar that carries one, in keys as well as
// values, and out of comments — where a reference may also have been written, and
// where a mark left behind would ride out into the file opossum hands on.
//
// A scalar that was nothing else is left as an empty string rather than the null
// it would otherwise be read as. Its tag becomes `!!str`: `!!int` with nothing
// under it is not a number, and the document would come back unreadable. A tag the
// author wrote themselves goes the same way, which costs nothing here — compose
// has no use for one.
func unmark(n *yaml.Node) {
	// Values and keys, and nothing else. The space in front of a reference is part
	// of what the author wrote — `"one ${NOPE}"` is `"one "` — and taking it would
	// make the value depend on whether the variable happened to be set, which is
	// the whole defect this exists to remove.
	//
	// Comments are not touched, because nothing downstream can see one: the tree is
	// decoded, not written back out, and decoding ignores comments. A mark left in
	// a comment reaches no one.
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, emptied) {
		n.Value = strings.ReplaceAll(n.Value, emptied, "")
		// The tag has to be set or a value that was only the mark comes back as
		// null, and null under `environment:` means "inherit from the host".
		n.Tag = "!!str"
	}
	for _, c := range n.Content {
		unmark(c)
	}
}

// interpolate expands references in text that is not a YAML document: an env-file
// value, or a default argument.
func interpolate(raw []byte, lookup varLookup) ([]byte, error) {
	return expand(raw, lookup, "", nil)
}

// expand rewrites `$VAR`, `${VAR}`, defaults `${VAR:-d}` (d when unset or empty)
// and `${VAR-d}` (d only when unset), required `${VAR:?msg}` / `${VAR?msg}` (error
// when unset/empty or unset), and `$$` as a literal `$`. An undefined variable
// with no default expands to empty.
//
// emptyAs is written in place of a reference that produced nothing. A document
// passes the mark, so the parser can be asked afterwards where the value went; a
// `.env` value passes "" and simply loses the text, which is what it means.
//
// hold, when non-nil, collects every expanded value, and a one-line marker
// is written in its place for restore to undo after parsing, so that what a
// variable holds is read as text and nothing else — the way docker compose
// reads an expansion. Written into the text, a value read as YAML: `42` was
// a number where a string belonged, `[1]` a list, `1.50` the number 1.5,
// and a line break was structure. A `.env` value passes nil: it is not
// parsed as YAML, so its text rides as itself.
func expand(raw []byte, lookup varLookup, emptyAs string, hold *[]heldValue) ([]byte, error) {
	var out bytes.Buffer
	s := string(raw)
	var quoted []quotedScalar // the double-quoted scalars of the file, found when a reference needs them
	quotedKnown := false
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			out.WriteByte(c)
			i++
			continue
		}
		// c == '$'
		if i+1 >= len(s) {
			out.WriteByte('$')
			break
		}
		switch next := s[i+1]; {
		case next == '$': // escape: $$ -> $
			if hold != nil {
				// Set aside like a reference, so that a key it was written in gets `$$`
				// back and not the `$` a value gets.
				writeHeld(&out, "$", "$$", hold)
			} else {
				out.WriteByte('$')
			}
			i += 2
		case next == '{':
			// Find the `}` that closes THIS reference, skipping any nested `${…}` so a
			// reference with a nested default (`${A:-${B}}`) is captured whole rather
			// than truncated at the first inner `}`.
			end := matchBrace(s, i+2, hold != nil)
			if end < 0 {
				return nil, unterminatedRef{at: i}
			}
			expr := s[i+2 : i+2+end]
			written := s[i : i+2+end+1]
			// In a double-quoted scalar the word is written in YAML's escapes (`"${C:-{\"a\":1}}"`):
			// docker compose reads the YAML first and interpolates the string that came of it, so a
			// `\"` or a `\\` in the word is the character it stands for. Here the word is set aside
			// before the parser sees it, so it is read the same way now (#1706) — where the YAML parser
			// says the reference is in a double-quoted scalar. Only the text of the file: a value a
			// variable holds is what it is. A word with neither a backslash nor a quote is the same read
			// either way, and the file is not parsed for it.
			if hold != nil && strings.ContainsAny(expr, "\\\"") {
				if !quotedKnown {
					quoted, quotedKnown = quotedScalars(s), true
				}
				if q, ok := quotedAround(quoted, i); ok {
					// A key is not interpolated, so a line break in it is no break in a reference.
					un, err := unescapeDoubleQuoted(expr, q.key)
					if err != nil {
						return nil, err
					}
					expr, written = un, "${"+un+"}"
				}
			}
			val, err := expandBraced(expr, lookup)
			if err != nil {
				// A failure from inside a default was found in a different string,
				// so whatever position it carries counts there and not here.
				var nested unterminatedRef
				if errors.As(err, &nested) {
					return nil, unterminatedRef{at: i}
				}
				return nil, err
			}
			if err := refuseMark("${"+expr+"}", val, emptyAs, hold); err != nil {
				return nil, err
			}
			writeVal(&out, val, written, emptyAs, hold)
			i += 2 + end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			name := s[i+1 : j]
			val, _ := lookup(name)
			if err := refuseMark("$"+name, val, emptyAs, hold); err != nil {
				return nil, err
			}
			writeVal(&out, val, s[i:j], emptyAs, hold)
			i = j
		default: // a lone $ (e.g. before a space) is literal
			out.WriteByte('$')
			i++
		}
	}
	return out.Bytes(), nil
}

// quotedScalar is a double-quoted scalar of the file as YAML reads it: where it opens and where it
// closes in the text (the indexes of its two quotes), and whether it is a mapping key.
type quotedScalar struct {
	open, close int
	key         bool
}

// parseQuoted reads text as YAML and returns the double-quoted scalars in it, in the order they are
// written; ok is false where text is not YAML.
func parseQuoted(text string) ([]quotedScalar, bool) {
	// The line breaks YAML counts: LF, CR, CRLF as one, and NEL, LS and PS.
	lineStart := []int{0}
	for i := 0; i < len(text); i++ {
		switch {
		case text[i] == '\n':
			lineStart = append(lineStart, i+1)
		case text[i] == '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			lineStart = append(lineStart, i+1)
		case text[i] == 0xC2 && i+1 < len(text) && text[i+1] == 0x85,
			text[i] == 0xE2 && i+2 < len(text) && text[i+1] == 0x80 && (text[i+2] == 0xA8 || text[i+2] == 0xA9):
			i += map[byte]int{0xC2: 1, 0xE2: 2}[text[i]]
			lineStart = append(lineStart, i+1)
		}
	}
	// A position the parser gives is a line and a column, counted in characters: the byte it is at.
	// The scalars come in the order they are written, so a position on the line of the last one is
	// reached from it and the line is not counted again from its start.
	curLine, curColumn, curOff := 0, 0, 0
	at := func(line, column int) int {
		if line < 1 || line > len(lineStart) {
			return len(text)
		}
		if line != curLine || column < curColumn {
			curLine, curColumn, curOff = line, 1, lineStart[line-1]
		}
		for ; curColumn < column && curOff < len(text); curColumn++ {
			_, w := utf8.DecodeRuneInString(text[curOff:])
			curOff += w
		}
		return curOff
	}
	var found []quotedScalar
	var walk func(n *yaml.Node, key bool)
	walk = func(n *yaml.Node, key bool) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, false)
			}
		case yaml.MappingNode:
			for i, c := range n.Content {
				walk(c, i%2 == 0)
			}
		case yaml.ScalarNode:
			if n.Style&yaml.DoubleQuotedStyle == 0 {
				return
			}
			open := openingQuote(text, at(n.Line, n.Column))
			if open < 0 {
				return
			}
			for i := open + 1; i < len(text); i++ {
				if text[i] == '\\' {
					i++
				} else if text[i] == '"' {
					found = append(found, quotedScalar{open, i, key})
					return
				}
			}
		}
	}
	var doc yaml.Node
	if err := yaml.NewDecoder(strings.NewReader(text)).Decode(&doc); err != nil {
		return nil, err == io.EOF // an empty file has no scalars; anything else is not YAML
	}
	walk(&doc, false)
	return found, true
}

// quotedScalars reads text, the file before anything is expanded, as YAML, and returns the
// double-quoted scalars in it in the order they are written. Asking the parser is what tells a
// quoted scalar from a quote in a plain value (`a,"b"`), a line of a block scalar (`|`, `>`), a line
// that continues a multi-line value, or a quote of a single-quoted one — every line-by-line guess
// at it was wrong somewhere. nil where the text is not YAML as it stands (a reference whose word
// holds a flow collection in a plain value does not parse before it is expanded): nothing is known
// to be quoted then, and every word is left as written.
func quotedScalars(text string) []quotedScalar {
	if found, ok := parseQuoted(text); ok {
		return found
	}
	// The references themselves can be what does not parse (a word with a bad escape or a quote in
	// it, a `{` in a plain value in a flow collection): they are masked, same length, and the rest of
	// the text is read.
	if masked := maskReferences(text); masked != text {
		if found, ok := parseQuoted(masked); ok {
			return found
		}
	}
	return nil
}

// maskReferences is text with each `${…}` reference written over with `_`, byte for byte, line breaks
// kept, so that a position in the one is the same position in the other.
func maskReferences(text string) string {
	b := []byte(text)
	for i := 0; i+1 < len(text); {
		switch {
		case text[i] == '$' && text[i+1] == '$':
			i += 2
		case text[i] == '$' && text[i+1] == '{':
			end := matchBrace(text, i+2, true)
			if end < 0 {
				return text
			}
			for j := i; j <= i+2+end; j++ {
				if b[j] != '\n' && b[j] != '\r' {
					b[j] = '_'
				}
			}
			i += 2 + end + 1
		default:
			i++
		}
	}
	return string(b)
}

// openingQuote finds the quote that opens the scalar the parser put at text[from]: the node starts at
// its anchor or its tag when it has one, and what is between them and the quote — more of them, white
// space, line breaks, a comment (which may hold quotes of its own) — is passed over.
func openingQuote(text string, from int) int {
	for i := from; i < len(text); i++ {
		switch text[i] {
		case '"':
			return i
		case '#':
			if i == 0 || strings.ContainsRune(" \t\r\n", rune(text[i-1])) {
				for i < len(text) && text[i] != '\n' && text[i] != '\r' {
					i++
				}
			}
		case '&', '!':
			for i < len(text) && !strings.ContainsRune(" \t\r\n", rune(text[i])) {
				i++
			}
		}
	}
	return -1
}

// quotedAround finds the double-quoted scalar the reference that starts at text[at] is written in:
// the one whose quotes are on each side of the `$`.
func quotedAround(scalars []quotedScalar, at int) (quotedScalar, bool) {
	i := sort.Search(len(scalars), func(i int) bool { return scalars[i].open >= at }) - 1
	if i >= 0 && scalars[i].open < at && at < scalars[i].close {
		return scalars[i], true
	}
	return quotedScalar{}, false
}

// unescapeDoubleQuoted reads s, text written inside a YAML double-quoted scalar, as YAML does:
// `\"` is a quote, `\\` a backslash, `\t` a tab, `\x41`, `\u00e9` and `\U0001F600` the character
// of that code, and the rest of the table of the YAML specification (`\0 \a \b \v \f \r \e`, `\ `,
// `\N \_ \L \P`). What YAML refuses is refused the way docker compose refuses it (v5.5.1): an
// escape it does not know (`\q`, `\$`, and `\/`, which go-yaml does not take) and a hexadecimal
// escape that is too short or not hexadecimal, and a bare `"`, which ends the scalar. So is `\n` — a line break in a reference (not in a key, which is not interpolated), which docker
// compose finds no reference in (`invalid interpolation format`). A backslash before a line break is
// a continuation, left for the one that folds it.
func unescapeDoubleQuoted(s string, key bool) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			// The scalar ends here, in the middle of the reference: docker compose finds the rest
			// of the file is not YAML (`found unexpected end of stream`).
			return "", fmt.Errorf("a `\"` in the word of a reference written in double quotes ends the quoted value: write it as `\\\"`")
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("a backslash ends the word of a reference written in double quotes: found unknown escape character")
		}
		switch e := s[i]; e {
		case '0':
			b.WriteByte(0)
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 't', '\t':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case 'e':
			b.WriteByte(0x1b)
		case ' ':
			b.WriteByte(' ')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case 'N':
			b.WriteString("\u0085")
		case '_':
			b.WriteString("\u00a0")
		case 'L':
			b.WriteString("\u2028")
		case 'P':
			b.WriteString("\u2029")
		case '\n', '\r':
			b.WriteByte('\\') // a continuation: folded by foldLineContinuations
			b.WriteByte(e)
		case 'n':
			if key {
				b.WriteByte('\n')
				continue
			}
			return "", fmt.Errorf("a `\\n` in the word of a reference written in double quotes puts a line break in the reference: invalid interpolation format — write the word without it")
		case 'x', 'u', 'U':
			width := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			if i+width >= len(s) {
				return "", fmt.Errorf("a `\\%c` escape in the word of a reference written in double quotes is too short: did not find expected hexadecimal number", e)
			}
			code, err := strconv.ParseUint(s[i+1:i+1+width], 16, 32)
			if err != nil {
				return "", fmt.Errorf("a `\\%c` escape in the word of a reference written in double quotes is not hexadecimal: did not find expected hexadecimal number", e)
			}
			if (code >= 0xD800 && code <= 0xDFFF) || code > 0x10FFFF {
				return "", fmt.Errorf("a `\\%c` escape in the word of a reference written in double quotes is not a Unicode character: found invalid Unicode character escape code", e)
			}
			b.WriteRune(rune(code))
			i += width
		default:
			return "", fmt.Errorf("a `\\%c` in the word of a reference written in double quotes is not an escape YAML knows: found unknown escape character", e)
		}
	}
	return b.String(), nil
}

// refuseMark stops a value that carries the mark from being written into a
// document. The file itself is checked before any of this starts; a value reaching
// it through the shell or an env file is the same problem arriving by another
// road, and letting it through would conflate what somebody set with what
// expansion produced.
func refuseMark(name, val, emptyAs string, hold *[]heldValue) error {
	// The reference as it was written — `${V}`, or `${V:-fallback}` — and not what
	// it resolved to: a variable is where a password or a token lives, and the
	// reader needs to know which one to go and fix, not what is in it.
	if emptyAs != "" && strings.Contains(val, emptyAs) {
		return fmt.Errorf("the value of %s contains U+E000, a private-use character opossum uses to "+
			"track values that expand to nothing; remove it from that value", name)
	}
	if hold != nil && strings.Contains(val, held) {
		return fmt.Errorf("the value of %s contains U+E001, a private-use character opossum uses to "+
			"carry expanded values through parsing; remove it from that value", name)
	}
	return nil
}

// writeVal writes the expanded value: the mark when there is nothing to write,
// a one-line marker (the value held aside for restore) where the document is
// being expanded, and the value itself where text that is not YAML is.
func writeVal(out *bytes.Buffer, val, written, emptyAs string, hold *[]heldValue) {
	switch {
	case val == "" && emptyAs != "":
		out.WriteString(emptyAs)
		writeHeld(out, val, written, hold)
	case hold != nil:
		writeHeld(out, val, written, hold)
	default:
		out.WriteString(val)
	}
}

// writeHeld sets a value aside, with the text the reader wrote for it, and
// writes the marker that stands for both.
func writeHeld(out *bytes.Buffer, val, written string, hold *[]heldValue) {
	if hold == nil {
		return
	}
	*hold = append(*hold, heldValue{value: val, written: written})
	fmt.Fprintf(out, "%s%d%s", held, len(*hold)-1, held)
}

// heldValue is one expansion set aside while the document is parsed. A value
// takes what the reference resolved to; a key takes what was written, because
// docker compose expands the values of a mapping and never its keys.
type heldValue struct {
	value   string
	written string
}

// matchBrace returns the index, relative to from, of the `}` that closes the `${` reference whose
// content starts at text[from]. It finds it the way docker compose does (measured, v5.5.1, and
// the same as compose-go's getFirstBraceClosingIndex): every `{` met is an opening — the one of
// a nested `${…}` and one the word has of its own (`${E:-{x}}`) — and the character right after
// it is not looked at, so `{{x}}` costs one `}` and not two (`${E:-{{x}}}` closes at the second
// `}`). Counting only `${`, as this did, closed `${E:-{x}}` at the first `}` and left the rest of
// the word behind as text (#1631). When that count never gets back to zero (a `{` the word
// does not close, or `{}`, whose `}` the skip eats), docker compose takes the last `}` of the
// value.
//
// docker compose counts inside one YAML value; this reads the file's text, before it is parsed,
// so the value is found first (scalarEnd) and the count is made inside it: a `}` after the
// closing quote, in a comment or of a flow mapping is not the word's. Where the value cannot be
// told (a comment, a quote that is never closed), the
// reference is closed as it was before the word's own `{` counted — and so is a reference whose
// word has no `{` of its own, which is read exactly as it always was. Returns -1 if there is none.
func matchBrace(text string, from int, inDocument bool) int {
	old := matchBraceAcrossLines(text[from:])
	if old < 0 {
		return -1
	}
	// Without a `{` of the word's own the count above is the one this has always made, and
	// reading the value's extent could only narrow it (a reference whose `}` is lines away).
	if !hasBareBrace(text[from : from+old]) {
		return old
	}
	// Text that is not a YAML document (a `.env` value, a default being read again) is one value.
	end := len(text)
	if inDocument {
		if end = scalarEnd(text, from); end < 0 {
			return old
		}
	}
	value := text[from:end]
	depth := 1
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '{':
			depth++
			i++ // the character after a `{` is not looked at
		case '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	if old >= len(value) {
		return old // the reference goes on past the value as read here (a multi-line one)
	}
	if last := strings.LastIndexByte(value, '}'); last >= 0 {
		return last
	}
	return old
}

// scalarEnd returns the index in text where the YAML scalar that holds text[at] ends, or -1
// when it cannot tell. A quoted scalar ends at its closing quote, which may be lines away
// (`"` with `\` escapes, `'` with `”`); a plain scalar of a block ends at a ` #` comment or at
// the end of its line. A quote opens a scalar only where a node can begin (after `:`, `-`, `?`,
// `,`, `[`, `{` or the start of the line, or after a tag or an anchor), as YAML reads one. The
// reading is one line's, from its start to at: where the line is itself a comment, it says -1.
// (A plain value inside a flow collection is not looked for: docker compose refuses one that has
// a `{` in it, and a word with no `{` of its own never gets here.)
func scalarEnd(text string, at int) int {
	state, known := scalarState(text, at)
	if !known {
		return -1
	}
	switch state {
	case scalarDouble:
		for k := at; k < len(text); k++ {
			if text[k] == '\\' {
				k++
			} else if text[k] == '"' {
				return k
			}
		}
		return -1
	case scalarSingle:
		for k := at; k < len(text); k++ {
			if text[k] == '\'' {
				if k+1 < len(text) && text[k+1] == '\'' {
					k++
					continue
				}
				return k
			}
		}
		return -1
	}
	for k := at; k < len(text); k++ {
		if text[k] == '\n' || text[k] == '\r' || (text[k] == '#' && (text[k-1] == ' ' || text[k-1] == '\t')) {
			return k
		}
	}
	return len(text)
}

// What kind of YAML scalar a reference is written in.
const (
	scalarPlain = iota
	scalarSingle
	scalarDouble
)

// scalarState says which kind of scalar holds the reference whose `${` is at text[at-2:at], read
// from the start of its line; known is false where that cannot be told (the line is itself a
// comment). A scalar that was opened on an earlier line is not known to be one: the state is the
// plain one there.
func scalarState(text string, at int) (state int, known bool) {
	start := strings.LastIndexByte(text[:at], '\n') + 1
	if cr := strings.LastIndexByte(text[start:at], '\r'); cr >= 0 {
		start += cr + 1
	}
	prev := byte(0)
	for k := start; k < at-2; k++ { // up to the `${` of the reference itself
		c := text[k]
		switch state {
		case scalarDouble:
			switch c {
			case '\\':
				k++
			case '"':
				state, prev = scalarPlain, c
			}
		case scalarSingle:
			if c == '\'' {
				if k+1 < at-2 && text[k+1] == '\'' {
					k++
				} else {
					state, prev = scalarPlain, c
				}
			}
		default:
			switch {
			case c == '"' && opensNode(prev):
				state = scalarDouble
			case c == '\'' && opensNode(prev):
				state = scalarSingle
			case c == '#' && (prev == 0 || text[k-1] == ' ' || text[k-1] == '\t'):
				return scalarPlain, false // inside a comment
			case (c == '!' || c == '&') && opensNode(prev):
				// A tag or an anchor before the node: the quote after it opens the scalar.
				for k < at-2 && text[k] != ' ' && text[k] != '\t' {
					k++
				}
			case c != ' ' && c != '\t':
				prev = c
			}
		}
	}
	return state, true
}

// opensNode says whether a quote right after prev (the last byte of the line that is not a
// space, 0 for none) begins a scalar.
func opensNode(prev byte) bool {
	switch prev {
	case 0, ':', '-', '?', ',', '[', '{':
		return true
	}
	return false
}

// hasBareBrace says whether s has a `{` that is not the one of a `${`.
func hasBareBrace(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' && (i == 0 || s[i-1] != '$') {
			return true
		}
	}
	return false
}

// matchBraceAcrossLines is how a reference that does not end on its line was closed before the
// word's own `{` counted: the `}` that balances the `${` openings.
func matchBraceAcrossLines(s string) int {
	depth := 1
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++ // consume the '{' so it isn't recounted
		case s[i] == '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// foldLineContinuations removes YAML double-quoted line continuations from inside a
// `${…}` expression. Interpolation runs on the raw file text (before YAML parsing),
// so a reference a compose author wrote across several lines — `"${VAR:\<newline>
// -default}"` — still carries the `\`+newline+indent that YAML would fold away.
// Collapsing them here lets such a reference parse as one line (only `\` immediately
// before a newline is folded, so a literal backslash elsewhere is untouched).
func foldLineContinuations(expr string) string {
	if !strings.Contains(expr, "\\") {
		return expr
	}
	var b strings.Builder
	for i := 0; i < len(expr); i++ {
		if expr[i] == '\\' && i+1 < len(expr) && (expr[i+1] == '\n' || expr[i+1] == '\r') {
			i++ // skip the backslash; land on CR or LF
			if expr[i] == '\r' && i+1 < len(expr) && expr[i+1] == '\n' {
				i++ // skip the LF of a CRLF
			}
			for i+1 < len(expr) && (expr[i+1] == ' ' || expr[i+1] == '\t') {
				i++ // skip the continuation line's leading whitespace
			}
			continue
		}
		b.WriteByte(expr[i])
	}
	return b.String()
}

// expandBraced resolves the inside of a `${...}` reference. A default value (the
// argument of `:-`/`-`/`:?`/`?`/`:+`/`+`) is itself interpolated, so a nested reference in
// the default (`${A:-${B:-x}}`) resolves too.
func expandBraced(expr string, lookup varLookup) (string, error) {
	expr = foldLineContinuations(expr)
	// Find the operator (:-, -, :?, ?, :+, +) separating name from the argument. Scan only
	// up to the first nested `${…}` so an operator inside a nested default isn't
	// mistaken for this reference's operator.
	for idx := 0; idx < len(expr); idx++ {
		if expr[idx] == '$' && idx+1 < len(expr) && expr[idx+1] == '{' {
			break // the rest is a nested reference; this one has no operator before it
		}
		ch := expr[idx]
		if ch == '-' || ch == '?' || ch == '+' {
			name := expr[:idx]
			colon := false
			if idx > 0 && expr[idx-1] == ':' {
				colon = true
				name = expr[:idx-1]
			}
			arg := expr[idx+1:]
			if err := validName(name); err != nil {
				return "", err
			}
			val, set := lookup(name)
			missing := !set || (colon && val == "")
			if ch == '-' {
				if missing {
					return interpolateStr(arg, lookup) // resolve nested refs in the default
				}
				return val, nil
			}
			if ch == '+' {
				// The other way round: the word is what the variable being set (and, with the
				// colon, not empty) gives, and nothing is what it being missing gives. The word
				// is only read when it is taken, so a `${F:?…}` in it is only asked then — as
				// docker compose does (measured, v5.5.1).
				if missing {
					return "", nil
				}
				return interpolateStr(arg, lookup)
			}
			// ch == '?': required
			if missing {
				msg, err := interpolateStr(arg, lookup)
				if err != nil {
					return "", err
				}
				if msg == "" {
					msg = "required variable is not set"
				}
				return "", fmt.Errorf("variable %q: %s", name, msg)
			}
			return val, nil
		}
	}
	// Plain ${NAME}.
	if err := validName(expr); err != nil {
		return "", err
	}
	val, _ := lookup(expr)
	return val, nil
}

// interpolateStr is the string form of interpolate, for resolving a default value.
func interpolateStr(s string, lookup varLookup) (string, error) {
	b, err := interpolate([]byte(s), lookup)
	return string(b), err
}

func validName(name string) error {
	if name == "" || !isNameStart(name[0]) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	for i := 1; i < len(name); i++ {
		if !isNameChar(name[i]) {
			return fmt.Errorf("invalid variable name %q", name)
		}
	}
	return nil
}

func isNameStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isNameChar(b byte) bool {
	return isNameStart(b) || (b >= '0' && b <= '9')
}

// unterminatedRef is a `${` with no `}`, carrying where it started rather than
// what followed it.
//
// What followed it used to be quoted back, and it is the rest of whatever was
// being expanded: on the document road that is the tail of the compose file, and
// on the value road it is the value — where a password lives. The two roads want
// different words for the same fault, so the position travels and each frames it.
type unterminatedRef struct{ at int }

func (unterminatedRef) Error() string { return "unterminated variable reference" }

// onLine names the line the reference started on, counting in the text expand was
// given. Only the document road has anything to count: a value is handed over
// with the file and line it came from already attached.
//
// The position has to have been taken in this same text. It is dropped rather
// than carried when expansion goes down into a value, so an offset from one
// string is never read against another — but the guard is here as well, because
// being wrong about that would name a line at random, or read past the end.
func (u unterminatedRef) onLine(in []byte) error {
	if u.at < 0 || u.at > len(in) {
		return u
	}
	return fmt.Errorf("%w on line %d", u, countLines(in[:u.at]))
}

// countLines counts the line the offset is on, breaking lines where YAML breaks
// them rather than where Go's newline does.
//
// A file written on an old Mac separates its lines with CR alone, and the parser
// reads those as lines: saying "line 1" for something on the fifth of them, while
// the parser's own complaints about the same file count to five, is worse than
// saying nothing. NEL, LINE SEPARATOR and PARAGRAPH SEPARATOR are breaks to YAML
// too — rare, but the file that has one is exactly the file nobody can debug.
func countLines(in []byte) int {
	n := 1
	for i := 0; i < len(in); i++ {
		switch in[i] {
		case '\n':
			n++
		case '\r':
			n++
			if i+1 < len(in) && in[i+1] == '\n' { // CRLF is one break, not two
				i++
			}
		case 0xC2: // NEL is C2 85
			if i+1 < len(in) && in[i+1] == 0x85 {
				n++
				i++
			}
		case 0xE2: // LS is E2 80 A8, PS is E2 80 A9
			if i+2 < len(in) && in[i+1] == 0x80 && (in[i+2] == 0xA8 || in[i+2] == 0xA9) {
				n++
				i += 2
			}
		}
	}
	return n
}

// badNameChar is an ASCII character docker compose refuses in the name of a
// variable in an env file: the punctuation outside `_ . - [ ]`, and the control
// characters other than the tab, the vertical tab, the form feed and the
// carriage return, which it takes (a lone `\r` inside a name is kept: only a
// `\r\n` at the end of a line is a line ending). A space and a colon are not
// asked about: the one is refused before this, in docker compose's own words,
// and the other ends a name before it gets here.
func badNameChar(r rune) bool {
	switch {
	case r >= 0x80:
		return false
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	switch r {
	case '_', '.', '-', '[', ']', '\t', '\v', '\f', '\r':
		return false
	}
	return true
}

// nameEmptied is whether the document's top-level `name` is a scalar written with references that came to nothing, found
// by the marks that stand for them before unmark and restore take them out (an alias to a scalar of the kind counts,
// as docker compose reads it through the alias).
func nameEmptied(doc *yaml.Node, vals []heldValue) bool {
	root := doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "name" {
			continue
		}
		v := root.Content[i+1]
		for v.Kind == yaml.AliasNode && v.Alias != nil {
			v = v.Alias
		}
		if v.Kind != yaml.ScalarNode || !(strings.Contains(v.Value, emptied) || strings.Contains(v.Value, held)) {
			return false // written out, with no reference in it
		}
		// What the references came to, the held values put back as restore does: a default that is empty rides as a
		// held value, a reference to a variable that is unset as the mark.
		resolved := heldMarker.ReplaceAllStringFunc(strings.ReplaceAll(v.Value, emptied, ""), func(m string) string {
			if i, err := strconv.Atoi(strings.Trim(m, held)); err == nil && i >= 0 && i < len(vals) {
				return vals[i].value
			}
			return m
		})
		return resolved == ""
	}
	return false
}
