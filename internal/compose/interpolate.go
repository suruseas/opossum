package compose

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// loadEnv builds the scope used for interpolation: values from a `.env` file in
// dir (or the given --env-file paths), the process environment, and the built-in.
// A missing default .env file is not an error.
func loadEnv(dir string, envFiles []string) (envScope, error) {
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
		}
		if _, err := parseDotEnv(f, scope); err != nil {
			return envScope{}, err
		}
	}
	return scope, nil
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
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, i+1)
		}
		if strings.Contains(key, "\ufeff") {
			return nil, markInNameErr(path, i+1)
		}
		val = strings.TrimSpace(val)

		// A quoted value whose closing quote isn't on this line spans multiple
		// lines (e.g. a PEM key): gather following lines verbatim, preserving the
		// newlines, until the closing quote. An unterminated value is an error,
		// matching docker compose.
		if len(val) > 1 && (val[0] == '"' || val[0] == '\'') && strings.IndexByte(val[1:], val[0]) < 0 {
			q := val[0]
			literal := q == '\''
			start := i + 1
			var sb strings.Builder
			sb.WriteString(val[1:]) // content after the opening quote
			closed := false
			for i+1 < len(lines) {
				i++
				sb.WriteByte('\n')
				if j := strings.IndexByte(lines[i], q); j >= 0 {
					sb.WriteString(lines[i][:j])
					closed = true
					break
				}
				sb.WriteString(lines[i])
			}
			if !closed {
				return nil, fmt.Errorf("%s:%d: unterminated quoted value for %q", path, start, key)
			}
			v, err := expandEnvValue(sb.String(), literal, scope, path, start)
			if err != nil {
				return nil, err
			}
			out[key] = v
			scope.level[key] = v
			continue
		}
		v, err := expandEnvValue(unquote(val), singleQuoted(val), scope, path, i+1)
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

// unquote strips a single pair of matching surrounding quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// singleQuoted reports whether s is wrapped in a matching pair of single quotes,
// which is what suppresses expansion of its contents.
func singleQuoted(s string) bool {
	return len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\''
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
	// node is the document with the marks taken out, or nil when there is no tree
	// to hand over. Nil has two causes and they are not the same: no mark was
	// written at all (nothing needed repairing, and the bytes are already right),
	// or the text did not parse (below).
	node *yaml.Node
	// raw is always set: the expanded document. When the text did not parse, the
	// marks are still in it — on purpose.
	raw []byte
}

// into decodes the document into v.
func (d interpolated) into(v any) error {
	if d.node != nil {
		return d.node.Decode(v)
	}
	return yaml.Unmarshal(d.raw, v)
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
	out, err := expand(raw, lookup, emptied)
	if err != nil {
		var u unterminatedRef
		if errors.As(err, &u) {
			return interpolated{}, u.onLine(raw)
		}
		return interpolated{}, err
	}
	if !bytes.Contains(out, []byte(emptied)) {
		// Nothing expanded to nothing, so there is nothing to repair and the bytes
		// are already what the caller should read.
		return interpolated{raw: out}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(out, &doc); err != nil {
		// Hand the bytes back exactly as they are, and let the caller report the
		// syntax error in its own words — with the file name it knows, and the hint
		// about indentation and quoting that this has no business replacing. A file
		// with its own mistake does not parse before expansion either, and blaming a
		// variable for an indentation error sends the reader to the wrong place.
		//
		// Unchanged means the marks stay in. Taking them out is what would do harm:
		// a mark stands between what was on either side of it, so removing it JOINS
		// them — `c: *x${NOPE}y` becomes `c: *xy`, which is valid, points at a
		// different anchor, and loads without a word. Left in, the bytes fail to
		// parse for the caller exactly as they failed here, which is the truth.
		return interpolated{raw: out}, nil
	}
	unmark(&doc)
	return interpolated{node: &doc, raw: out}, nil
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
	return expand(raw, lookup, "")
}

// expand rewrites `$VAR`, `${VAR}`, defaults `${VAR:-d}` (d when unset or empty)
// and `${VAR-d}` (d only when unset), required `${VAR:?msg}` / `${VAR?msg}` (error
// when unset/empty or unset), and `$$` as a literal `$`. An undefined variable
// with no default expands to empty.
//
// emptyAs is written in place of a reference that produced nothing. A document
// passes the mark, so the parser can be asked afterwards where the value went; a
// `.env` value passes "" and simply loses the text, which is what it means.
func expand(raw []byte, lookup varLookup, emptyAs string) ([]byte, error) {
	var out bytes.Buffer
	s := string(raw)
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
			out.WriteByte('$')
			i += 2
		case next == '{':
			// Find the `}` that closes THIS reference, skipping any nested `${…}` so a
			// reference with a nested default (`${A:-${B}}`) is captured whole rather
			// than truncated at the first inner `}`.
			end := matchBrace(s[i+2:])
			if end < 0 {
				return nil, unterminatedRef{at: i}
			}
			expr := s[i+2 : i+2+end]
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
			if err := refuseMark("${"+expr+"}", val, emptyAs); err != nil {
				return nil, err
			}
			out.WriteString(orMark(val, emptyAs))
			i += 2 + end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			name := s[i+1 : j]
			val, _ := lookup(name)
			if err := refuseMark("$"+name, val, emptyAs); err != nil {
				return nil, err
			}
			out.WriteString(orMark(val, emptyAs))
			i = j
		default: // a lone $ (e.g. before a space) is literal
			out.WriteByte('$')
			i++
		}
	}
	return out.Bytes(), nil
}

// refuseMark stops a value that carries the mark from being written into a
// document. The file itself is checked before any of this starts; a value reaching
// it through the shell or an env file is the same problem arriving by another
// road, and letting it through would conflate what somebody set with what
// expansion produced.
func refuseMark(name, val, emptyAs string) error {
	if emptyAs == "" || !strings.Contains(val, emptyAs) {
		return nil
	}
	// The reference as it was written — `${V}`, or `${V:-fallback}` — and not what
	// it resolved to: a variable is where a password or a token lives, and the
	// reader needs to know which one to go and fix, not what is in it.
	return fmt.Errorf("the value of %s contains U+E000, a private-use character opossum uses to "+
		"track values that expand to nothing; remove it from that value", name)
}

// orMark is the expanded value, or the mark when there is nothing to write.
func orMark(val, emptyAs string) string {
	if val == "" {
		return emptyAs
	}
	return val
}

// matchBrace returns the index in s of the `}` that closes a `${` reference whose
// content starts at s[0], skipping nested `${…}` so the outer reference is captured
// whole. Returns -1 if no matching `}` is found.
func matchBrace(s string) int {
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
// argument of `:-`/`-`/`:?`/`?`) is itself interpolated, so a nested reference in
// the default (`${A:-${B:-x}}`) resolves too.
func expandBraced(expr string, lookup varLookup) (string, error) {
	expr = foldLineContinuations(expr)
	// Find the operator (:-, -, :?, ?) separating name from the argument. Scan only
	// up to the first nested `${…}` so an operator inside a nested default isn't
	// mistaken for this reference's operator.
	for idx := 0; idx < len(expr); idx++ {
		if expr[idx] == '$' && idx+1 < len(expr) && expr[idx+1] == '{' {
			break // the rest is a nested reference; this one has no operator before it
		}
		ch := expr[idx]
		if ch == '-' || ch == '?' {
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
