package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// resolveEnvFiles reads the service's env_file(s) (relative to dir) and folds
// their KEY=VALUE entries into env, with entries in env (the service's explicit
// `environment`) taking precedence — matching docker-compose. Later env_file
// files override earlier ones. A missing env_file is an error unless the entry
// is marked `required: false`, in which case it is skipped (#85).
//
// scope is the project scope (the shell and the project's `.env`), which the
// files' own values expand against — docker compose expands these the same way it
// expands the compose file itself, so a value here can be `${SOME_VAR}/path`.
// Files accumulate as they are read, so a later env_file sees an earlier one's
// keys, the same way a later --env-file does.
//
// The service's own `environment:` sits between those two: a file's value can
// reference a key defined only there, and where both define one, `environment:`
// wins. That is docker compose's order, measured against v5.3.1.
func resolveEnvFiles(dir string, files EnvFiles, env []string, scope envScope) ([]string, error) {
	if len(files) == 0 {
		return env, nil
	}
	var fromFiles []string
	inner := scope.inner(explicitEnv(env))
	// A file listed more than once (in one file, across `-f` files, or
	// through `extends`) is read at every listing, its values standing where
	// they last appear and each listing's `required` and `format` counting;
	// docker compose (v5.5.1) reads it once, where first listed, with its
	// last listing's attributes — and spells a listing that came through
	// `extends` relative to the project, where this spells it absolute
	// (#1152).
	for _, f := range files {
		p := f.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); err != nil {
			if !f.Required {
				continue // optional and absent — skip
			}
			return nil, fmt.Errorf("env_file %q not found", f.Path)
		}
		var m map[string]string
		var err error
		switch f.Format {
		case "":
			m, err = parseDotEnv(p, inner)
		case "raw":
			m, err = parseRawEnv(p, inner)
		default:
			// Judged here, where the file is read, as docker compose (v5.5.1)
			// judges it: an entry whose file is not read is not refused for
			// its format. The word is compared as written — `RAW` is not it.
			return nil, fmt.Errorf("unsupported env_file format %q for %s — write `format: raw`, or leave the key out for the default", f.Format, f.Path)
		}
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic order within a file
		for _, k := range keys {
			fromFiles = append(fromFiles, k+"="+m[k])
		}
	}
	return mergeEnv(fromFiles, env), nil
}

// explicitEnv turns a service's `environment:` list into a lookup map. A bare
// `KEY` (no `=`) means "take it from the host", so it defines nothing here — the
// shell is already ranked above this level and will answer for it.
//
// It has to UNDO an earlier value for the same key rather than merely skip it.
// A list holding `K=value` and then a bare `K` sends nothing for K to the
// container, so leaving `value` in this map would let an env file expand `${K}`
// to a value the service does not actually have — the same key reading two ways
// in one service.
func explicitEnv(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		} else {
			delete(m, e)
		}
	}
	return m
}

// mergeEnv concatenates two KEY=VALUE (or bare KEY) lists, de-duplicating by key
// with later entries winning, preserving first-seen order. So env_file entries
// come first and the service's own `environment` overrides them.
func mergeEnv(base, override []string) []string {
	order := []string{}
	val := map[string]string{}
	add := func(entry string) {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if _, seen := val[key]; !seen {
			order = append(order, key)
		}
		val[key] = entry
	}
	for _, e := range base {
		add(e)
	}
	for _, e := range override {
		add(e)
	}
	out := make([]string, len(order))
	for i, k := range order {
		out[i] = val[k]
	}
	return out
}

// parseRawEnv reads an env file the way docker compose (v5.5.1) reads one
// under `format: raw`: a line is KEY=VALUE split at its first `=`, and the
// value is whatever follows, as written — quotes, `$` and `${VAR}`, a ` #`,
// trailing blanks, all of it. Nothing is expanded, so a `${VAR}` reaches the
// container as those characters. A line with no `=` is skipped (so is a
// `KEY: VALUE` line, which the default reading takes), a blank line and one
// whose first non-blank character is `#` are skipped, a byte-order mark at
// the very start is dropped, and CRLF reads as LF. A quote that is not
// closed on its line is not continued onto the next: each line is its own.
//
// The name has the blanks before it dropped (any Unicode blank) and nothing
// else: a space or a tab inside or after it (`A B=1`, `A =1`, and
// `export A=1`, whose `export` is not a prefix here) is refused, as is a
// line with no name before the `=`. All are refused by docker compose the
// same way; the default reading takes `A B=1` and `A =1`, and drops
// `export `.
//
// Values are written through to scope as they are read, so a later env_file
// (of either format) sees this file's keys the way it sees any earlier one's.
func parseRawEnv(path string, scope envScope) (map[string]string, error) {
	if scope.level == nil {
		return nil, fmt.Errorf("reading %s: the scope has no level map", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	out := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimLeftFunc(key, unicode.IsSpace)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: no variable name before the `=`", path, i+1)
		}
		if strings.ContainsAny(key, " \t") {
			// A space or a tab, and only those: docker compose takes a name
			// with a vertical tab, a form feed, a carriage return or a
			// no-break space in it, though it drops all of those in front
			// of the name. The name is safe to quote back; the value is not
			// (it is where secrets live), and is not.
			return nil, fmt.Errorf("%s:%d: variable %q contains whitespace — under `format: raw` a line is KEY=VALUE with nothing before the `=` but the name (no `export`)", path, i+1, key)
		}
		out[key] = val
		scope.level[key] = val
	}
	return out, nil
}
