package compose

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// loadEnv builds the variable lookup used for interpolation: values from a
// `.env` file in dir, overlaid by the process environment (the shell wins, as in
// docker-compose). A missing .env file is not an error.
func loadEnv(dir string, envFiles []string) (varLookup, error) {
	var fromFile map[string]string
	if len(envFiles) > 0 {
		// Explicit --env-file(s) replace the default .env; later files win, and a
		// named file that's missing is an error (unlike the optional default .env).
		fromFile = map[string]string{}
		for _, f := range envFiles {
			if _, err := os.Stat(f); err != nil {
				return nil, fmt.Errorf("env file %q: %w", f, err)
			}
			m, err := parseDotEnv(f)
			if err != nil {
				return nil, err
			}
			for k, v := range m {
				fromFile[k] = v
			}
		}
	} else {
		m, err := parseDotEnv(filepath.Join(dir, ".env"))
		if err != nil {
			return nil, err
		}
		fromFile = m
	}
	return func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		if v, ok := fromFile[name]; ok {
			return v, true
		}
		// Built-in fallback, resolved lazily so it costs nothing unless referenced.
		if name == hostGatewayVar {
			if addr := hostGatewayFunc(); addr != "" {
				return addr, true
			}
		}
		return "", false
	}, nil
}

// parseDotEnv reads a KEY=VALUE (or KEY: VALUE) file, matching docker compose's
// env_file handling. Blank lines and `#` comments are ignored, an `export ` prefix
// is dropped, and surrounding single/double quotes are stripped. A value whose
// opening quote isn't closed on the same line continues across lines — e.g. a
// multi-line PEM key — keeping the embedded newlines. A missing file yields an
// empty map (no error). Values are taken literally (no nested interpolation).
func parseDotEnv(path string) (map[string]string, error) {
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
			out[key] = sb.String()
			continue
		}
		out[key] = unquote(val)
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

// interpolate expands variable references in the raw compose bytes before YAML
// parsing. It supports `$VAR`, `${VAR}`, defaults `${VAR:-d}` (d when unset or
// empty) and `${VAR-d}` (d only when unset), required `${VAR:?msg}` / `${VAR?msg}`
// (error when unset/empty or unset), and `$$` as a literal `$`. An undefined
// variable with no default expands to empty.
func interpolate(raw []byte, lookup varLookup) ([]byte, error) {
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
				return nil, fmt.Errorf("unterminated variable reference: %q", s[i:])
			}
			expr := s[i+2 : i+2+end]
			val, err := expandBraced(expr, lookup)
			if err != nil {
				return nil, err
			}
			out.WriteString(val)
			i += 2 + end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			val, _ := lookup(s[i+1 : j])
			out.WriteString(val)
			i = j
		default: // a lone $ (e.g. before a space) is literal
			out.WriteByte('$')
			i++
		}
	}
	return out.Bytes(), nil
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
