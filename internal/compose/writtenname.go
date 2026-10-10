package compose

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WrittenProjectName is the name the compose files write at their top level (`name: xb`), read without loading them: for a take-down that is refused the file it would be
// read from, and has to say which project it means (#1953). The files are those named (paths, as written), or, when none is, the ones that are found in dir by the names
// docker compose looks for — the base file, its override, and opossum's overlay; a later file's name wins, as it does in the load. "" when none of them writes one that can
// be known: a name that is not a plain string, one with a `$` (a variable that is not resolved here), a file that cannot be read or parsed, and a file of several documents
// that do not agree (which project is meant cannot be told from it, #1483).
func WrittenProjectName(dir string, paths []string) string {
	if len(paths) == 0 {
		for _, name := range DefaultFileNames {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				paths = append(paths, p)
				break
			}
		}
		if len(paths) == 0 {
			return ""
		}
		if p := DiscoverOverride(dir); p != "" {
			paths = append(paths, p)
		}
		if p := DiscoverOpossumOverlay(dir); p != "" {
			paths = append(paths, p)
		}
	}
	name := ""
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, ok := nameOfDocuments(data)
		if !ok {
			return ""
		}
		if n != "" {
			name = n
		}
	}
	return name
}

// nameOfDocuments is the `name` the documents of a file write, and whether it can be known: documents that write different names, or one that is not a plain string
// without a `$`, are not known.
func nameOfDocuments(data []byte) (string, bool) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	name := ""
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				return name, true
			}
			return "", false
		}
		root := documentRoot(&doc)
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value != "name" || root.Content[i].Tag == "!!merge" {
				continue
			}
			v := unalias(root.Content[i+1])
			// An empty name is not known either: the load reads it as no name and the directory's takes its place, where docker compose keeps the one before it.
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" || v.Value == "" || strings.Contains(v.Value, "$") {
				return "", false
			}
			if v.Value != "" && name != "" && v.Value != name {
				return "", false
			}
			if v.Value != "" {
				name = v.Value
			}
		}
	}
}
