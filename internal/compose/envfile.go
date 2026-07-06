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
func resolveEnvFiles(dir string, files EnvFiles, env []string) ([]string, error) {
	if len(files) == 0 {
		return env, nil
	}
	var fromFiles []string
	for _, f := range files {
		p := filepath.Join(dir, f.Path)
		if _, err := os.Stat(p); err != nil {
			if !f.Required {
				continue // optional and absent — skip
			}
			return nil, fmt.Errorf("env_file %q not found", f.Path)
		}
		m, err := parseDotEnv(p)
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
