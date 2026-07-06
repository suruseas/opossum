package compose

import "fmt"

// shellSplit splits a string into words using POSIX-ish shell quoting rules, so a
// compose `command: sh -c "echo hi"` becomes ["sh", "-c", "echo hi"] instead of a
// single opaque argument. It handles single quotes (fully literal), double quotes
// (literal except a backslash escaping `"` or `\`), and backslash escaping outside
// quotes. Unbalanced quotes or a trailing backslash are errors.
func shellSplit(s string) ([]string, error) {
	var words []string
	var cur []rune
	inWord := false
	runes := []rune(s)
	n := len(runes)

	for i := 0; i < n; {
		c := runes[i]
		switch {
		case c == '\'':
			inWord = true
			i++
			for i < n && runes[i] != '\'' {
				cur = append(cur, runes[i])
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
			i++ // consume closing quote

		case c == '"':
			inWord = true
			i++
			for i < n && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < n && (runes[i+1] == '"' || runes[i+1] == '\\') {
					cur = append(cur, runes[i+1])
					i += 2
					continue
				}
				cur = append(cur, runes[i])
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
			i++ // consume closing quote

		case c == '\\':
			if i+1 >= n {
				return nil, fmt.Errorf("trailing backslash in %q", s)
			}
			inWord = true
			cur = append(cur, runes[i+1])
			i += 2

		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, string(cur))
				cur = cur[:0]
				inWord = false
			}
			i++

		default:
			inWord = true
			cur = append(cur, c)
			i++
		}
	}
	if inWord {
		words = append(words, string(cur))
	}
	return words, nil
}
