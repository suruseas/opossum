package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	for _, f := range files {
		p := filepath.Join(dir, f.Path)
		if _, err := os.Stat(p); err != nil {
			if !f.Required {
				continue // optional and absent — skip
			}
			return nil, fmt.Errorf("env_file %q not found", f.Path)
		}
		m, err := parseDotEnv(p, inner)
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
