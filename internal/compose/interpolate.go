package compose

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// varLookup resolves a variable name to its value and whether it was set at all.
// An empty-but-set variable returns ("", true).
type varLookup func(name string) (string, bool)

// loadEnv builds the variable lookup used for interpolation: values from a
// `.env` file in dir, overlaid by the process environment (the shell wins, as in
// docker-compose). A missing .env file is not an error.
func loadEnv(dir string) (varLookup, error) {
	fromFile, err := parseDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}
	return func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		v, ok := fromFile[name]
		return v, ok
	}, nil
}

// parseDotEnv reads a KEY=VALUE file. Blank lines and `#` comments are ignored,
// surrounding single/double quotes are stripped, and a missing file yields an
// empty map (no error). Values are taken literally (no nested interpolation).
func parseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		key, val, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, line, raw)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, line)
		}
		out[key] = unquote(strings.TrimSpace(val))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return out, nil
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
			end := strings.IndexByte(s[i+2:], '}')
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

// expandBraced resolves the inside of a `${...}` reference.
func expandBraced(expr string, lookup varLookup) (string, error) {
	// Find the operator (:-, -, :?, ?) separating name from the argument.
	for idx := 0; idx < len(expr); idx++ {
		ch := expr[idx]
		if ch == '-' || ch == '?' {
			name := expr[:idx]
			colon := false
			opStart := idx
			if idx > 0 && expr[idx-1] == ':' {
				colon = true
				name = expr[:idx-1]
				opStart = idx - 1
			}
			_ = opStart
			arg := expr[idx+1:]
			if err := validName(name); err != nil {
				return "", err
			}
			val, set := lookup(name)
			missing := !set || (colon && val == "")
			if ch == '-' {
				if missing {
					return arg, nil
				}
				return val, nil
			}
			// ch == '?': required
			if missing {
				msg := arg
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
