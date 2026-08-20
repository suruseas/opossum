package compose

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	out := map[string]string{}
	for i := 0; i < len(lines); i++ {
		raw := strings.TrimSpace(lines[i])
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		key, val, ok := splitEnvLine(raw)
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, i+1, raw)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, i+1)
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

// interpolateDocument expands a compose FILE and repairs the one thing expanding
// raw text before parsing can break: a value that was a reference and is now
// nothing, which YAML then reads as null. Under `environment:` null is not
// "empty", it is "inherit this one from the host", so the container is handed a
// value the compose file never mentions.
//
// The repair is made on the parsed document rather than on the text. Deciding
// "is this line a mapping entry whose value vanished" from the bytes means
// knowing where YAML is and is not: block scalars, multi-line quoted scalars,
// flow collections, sequence items. Three rounds of review each found one more
// shape the text-level rule got wrong. The parser already knows all of them, so
// the rule here is the one it can answer exactly — a null scalar, on a line an
// expansion wrote into, becomes an empty string.
//
// interpolate, which expands `.env` values and `${VAR:-default}` arguments, gets
// no repair: those are not YAML. A value like `LIB=${PREFIX}/lib:` ends in a
// colon and is not a mapping entry.
//
// Two things follow from handing the document back through the marshaller, which
// only happens when there was something to repair. Formatting is the
// marshaller's rather than the author's, so a line number in a later parse error
// can differ from the line in the file (a `>` scalar, for instance, comes back
// on one line). And a stream of several `---` documents comes back as its first
// document alone — which is what the caller reads in either case, so nothing is
// lost that was being used.
func interpolateDocument(raw []byte, lookup varLookup) ([]byte, error) {
	out, offsets, err := expand(raw, lookup)
	if err != nil {
		return nil, err
	}
	if len(offsets) == 0 {
		return out, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(out, &doc); err != nil {
		// Not parseable: hand back the bytes so the caller reports the syntax error
		// in its own words, with the file name it knows and this one does not.
		return out, nil
	}
	if !emptyOutNulls(&doc, wroteAt(locate(out, offsets))) {
		return out, nil // nothing to repair; keep the bytes exactly as expanded
	}
	unflow(&doc)
	fixed, err := yaml.Marshal(&doc)
	if err != nil {
		return out, nil
	}
	return fixed, nil
}

// refMark is a place in the expanded output where a reference was written — the
// value it produced starts here, and may be empty. Line and column are counted
// the way the parser counts them, so a mark and a node position can be compared.
type refMark struct{ line, col int }

// locate walks the expanded output once and returns it as lines of runes, along
// with the position of each byte offset an expansion wrote at.
//
// It is the only place that knows how the parser counts, and it is a single walk
// rather than bookkeeping kept during expansion, because the two would be two
// implementations of the same rule. What it has to agree with, measured against
// gopkg.in/yaml.v3:
//
//   - a column is a rune, not a byte: `{A: 日本語, K: }` reports K's null at
//     column 13, and a byte count says 19
//   - LF, CRLF, CR, NEL (U+0085), LS (U+2028) and PS (U+2029) each start a line
//   - a byte-order mark at the start of the stream occupies no column
//
// Offsets must be ascending, which they are: expansion writes left to right.
func locate(out []byte, offsets []int) ([][]rune, []refMark) {
	s := string(out)
	var lines [][]rune
	var marks []refMark
	var cur []rune
	line, col, next := 1, 1, 0
	for i := 0; i < len(s); {
		for next < len(offsets) && offsets[next] <= i {
			marks = append(marks, refMark{line, col})
			next++
		}
		if n := lineBreak(s[i:]); n > 0 {
			lines = append(lines, cur)
			cur, line, col = nil, line+1, 1
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if i == 0 && r == '\ufeff' {
			i += size // the parser skips it, so it takes up no column here either
			continue
		}
		cur = append(cur, r)
		col++
		i += size
	}
	for next < len(offsets) { // a reference that expanded at the very end
		marks = append(marks, refMark{line, col})
		next++
	}
	return append(lines, cur), marks
}

// lineBreak returns the length in bytes of the line break at the start of s, or
// zero. CRLF is one break rather than two.
func lineBreak(s string) int {
	switch {
	case strings.HasPrefix(s, "\r\n"):
		return 2
	case s[0] == '\n' || s[0] == '\r':
		return 1
	case strings.HasPrefix(s, "\u0085"):
		return 2
	case strings.HasPrefix(s, "\u2028"), strings.HasPrefix(s, "\u2029"):
		return 3
	}
	return 0
}

// wroteAt reports, for a position in the expanded output, whether an expansion
// wrote there: whether some mark on that line is separated from the position by
// whitespace alone.
//
// A line is too coarse a unit on its own. `KEY:  # ${VAR}` expands in the comment,
// which leaves the author's own `KEY:` — "inherit this one from the host" — on a
// line an expansion touched. Reading that as a vanished value would rewrite a null
// the file states outright.
//
// The two positions are compared in either direction because the parser does not
// always put a null exactly where its text would have been: in `{k: ${VAR}, j: 1}`
// it reports the comma, which is past the mark rather than before it. What both
// directions mean is the same thing — nothing but space stands between the
// value's place and the reference that emptied it.
//
// The line is also the limit, and deliberately so. A value written under its key
// rather than beside it is one mapping entry whose null the parser reports up on
// the key's line, so it is not repaired; the test named for that shape says so.
// Letting a match cross the line is not the fix: the next line's own
// indent is whitespace too, so `K:` followed by `${PREFIX}FOO: 1` — a key Compose
// lets you interpolate — reads as adjacent, and the author's null is rewritten.
// That is the one thing this must never do.
func wroteAt(lines [][]rune, marks []refMark) func(line, col int) bool {
	byLine := map[int][]int{}
	for _, m := range marks {
		byLine[m.line] = append(byLine[m.line], m.col)
	}
	return func(line, col int) bool {
		if line < 1 || line > len(lines) {
			return false
		}
		src := lines[line-1]
		for _, c := range byLine[line] {
			lo, hi := col, c
			if lo > hi {
				lo, hi = hi, lo
			}
			if lo < 1 || hi-1 > len(src) {
				continue
			}
			if blank(src[lo-1 : hi-1]) {
				return true
			}
		}
		return false
	}
}

// unflow asks for block style on every collection, because writing the document
// back out in flow style does not survive a null.
//
// `{K: , J: 1}` is a mapping whose K is null — under `environment:` that means
// "inherit K from the host" — and the marshaller renders that null as an empty
// string. The value the author wrote would come back meaning something else, in
// the one direction this whole repair exists to prevent. In block style the same
// null comes back as `K:`.
//
// Collections only, because that is all this needs: a scalar's style is the
// quoting the author chose, and there is no reason to restyle it. Doing so would
// in fact be harmless — the marshaller re-quotes whatever the tag requires, so
// ` x `, `123`, `null` and a `|` block all read back the same either way — but a
// narrower change is a narrower thing to be wrong about.
func unflow(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style &^= yaml.FlowStyle
	}
	for _, c := range n.Content {
		unflow(c)
	}
}

// valueColumn is the column just past a node's anchor.
//
// A null carrying an anchor is reported at the `&`, not at the value: in
// `A: &anc ${VAR}` the report is five columns left of the value, with the anchor
// name in between, and reading that as "something else is in the way" left the
// value as null — the host leak this whole repair exists to stop. The node names
// its own anchor, so stepping over it needs nothing the parser has not already
// said.
//
// What comes back is the column after the name and not the value's own column:
// only whitespace separates them, and blankBetween already crosses whitespace, so
// there is nothing to be gained by guessing how much of it there is.
func valueColumn(n *yaml.Node) int {
	if n.Anchor == "" {
		return n.Column
	}
	// Counted in runes to match every other column here, though the parser only
	// accepts letters, digits, `-` and `_` in an anchor name, so the two agree.
	return n.Column + utf8.RuneCountInString(n.Anchor) + 1 // + the `&`
}

func blank(rs []rune) bool {
	for _, r := range rs {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// emptyOutNulls turns every null scalar standing where an expansion wrote into an
// empty string, and reports whether it changed anything.
//
// A null that the author typed is left alone: it still means what it says. The
// parser has already decided what is a value and what is the inside of a string,
// so the only question left is whether this particular value is the one that
// vanished — which wrote asks.
func emptyOutNulls(n *yaml.Node, wrote func(line, col int) bool) bool {
	changed := false
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" && wrote(n.Line, valueColumn(n)) {
		n.Tag = "!!str"
		n.Value = ""
		changed = true
	}
	for _, c := range n.Content {
		if emptyOutNulls(c, wrote) {
			changed = true
		}
	}
	return changed
}

// interpolate expands references in text that is not a YAML document: an env-file
// value, or a default argument.
func interpolate(raw []byte, lookup varLookup) ([]byte, error) {
	out, _, err := expand(raw, lookup)
	return out, err
}

// expand rewrites `$VAR`, `${VAR}`, defaults `${VAR:-d}` (d when unset or empty)
// and `${VAR-d}` (d only when unset), required `${VAR:?msg}` / `${VAR?msg}` (error
// when unset/empty or unset), and `$$` as a literal `$`. An undefined variable
// with no default expands to empty.
//
// It also reports the byte offset in the OUTPUT of every value it wrote, which is
// what lets interpolateDocument tell a null the author typed from one an
// expansion left behind. Offsets rather than line and column: turning them into
// positions needs the parser's own counting rules, and those belong in one place
// (locate) rather than in a cursor carried through this loop.
func expand(raw []byte, lookup varLookup) ([]byte, []int, error) {
	var out bytes.Buffer
	s := string(raw)
	var offsets []int
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
				return nil, nil, fmt.Errorf("unterminated variable reference: %q", s[i:])
			}
			expr := s[i+2 : i+2+end]
			val, err := expandBraced(expr, lookup)
			if err != nil {
				return nil, nil, err
			}
			offsets = append(offsets, out.Len())
			out.WriteString(val)
			i += 2 + end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			val, _ := lookup(s[i+1 : j])
			offsets = append(offsets, out.Len())
			out.WriteString(val)
			i = j
		default: // a lone $ (e.g. before a space) is literal
			out.WriteByte('$')
			i++
		}
	}
	return out.Bytes(), offsets, nil
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
