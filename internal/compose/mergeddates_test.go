package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A date written without quotes (`2024-01-01`) for `init`, `platform`, `read_only`, `tty`, `user`, `working_dir` or `network_mode` is refused by docker compose as the project the files make
// is asked, and not as each file is read: a later `-f` file, a third file, the service that includes or extends it that writes a value over it, or resets it, makes the files fine, and a date
// that stays is refused (v5.5.1, `config -q`, every row measured; #1982).
func TestADateForAKeyAskedOfTheMergedServiceIsReadWhereAValueIsWrittenOverIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"init | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    init: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"init | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    init: 2024-01-01
`, "b.yaml": `services:
  web:
    init: true
`}, []string{"a.yaml", "b.yaml"}, false},
		{"init | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    init: 2024-01-01
`, "b.yaml": `services:
  web:
    init: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"init | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    init: true
`, "b.yaml": `services:
  web:
    init: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"init | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    init: true
`, "base.yaml": `services:
  x:
    image: xi
    init: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"init | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    init: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    init: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"init | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    init: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    init: true
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"init | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    init: true
`, "inc.yaml": `services:
  web:
    image: wi
    init: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"platform | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    platform: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"platform | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    platform: 2024-01-01
`, "b.yaml": `services:
  web:
    platform: linux/amd64
`}, []string{"a.yaml", "b.yaml"}, false},
		{"platform | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    platform: 2024-01-01
`, "b.yaml": `services:
  web:
    platform: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"platform | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    platform: linux/amd64
`, "b.yaml": `services:
  web:
    platform: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"platform | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    platform: linux/amd64
`, "base.yaml": `services:
  x:
    image: xi
    platform: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"platform | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    platform: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    platform: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"platform | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    platform: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    platform: linux/amd64
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"platform | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    platform: linux/amd64
`, "inc.yaml": `services:
  web:
    image: wi
    platform: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"read_only | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    read_only: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"read_only | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    read_only: 2024-01-01
`, "b.yaml": `services:
  web:
    read_only: true
`}, []string{"a.yaml", "b.yaml"}, false},
		{"read_only | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    read_only: 2024-01-01
`, "b.yaml": `services:
  web:
    read_only: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"read_only | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    read_only: true
`, "b.yaml": `services:
  web:
    read_only: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"read_only | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    read_only: true
`, "base.yaml": `services:
  x:
    image: xi
    read_only: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"read_only | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    read_only: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    read_only: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"read_only | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    read_only: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    read_only: true
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"read_only | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    read_only: true
`, "inc.yaml": `services:
  web:
    image: wi
    read_only: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"tty | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    tty: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"tty | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    tty: 2024-01-01
`, "b.yaml": `services:
  web:
    tty: true
`}, []string{"a.yaml", "b.yaml"}, false},
		{"tty | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    tty: 2024-01-01
`, "b.yaml": `services:
  web:
    tty: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"tty | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    tty: true
`, "b.yaml": `services:
  web:
    tty: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"tty | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    tty: true
`, "base.yaml": `services:
  x:
    image: xi
    tty: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"tty | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    tty: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    tty: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"tty | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    tty: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    tty: true
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"tty | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    tty: true
`, "inc.yaml": `services:
  web:
    image: wi
    tty: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"user | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    user: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"user | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    user: 2024-01-01
`, "b.yaml": `services:
  web:
    user: root
`}, []string{"a.yaml", "b.yaml"}, false},
		{"user | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    user: 2024-01-01
`, "b.yaml": `services:
  web:
    user: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"user | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    user: root
`, "b.yaml": `services:
  web:
    user: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"user | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    user: root
`, "base.yaml": `services:
  x:
    image: xi
    user: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"user | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    user: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    user: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"user | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    user: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    user: root
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"user | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    user: root
`, "inc.yaml": `services:
  web:
    image: wi
    user: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"working_dir | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    working_dir: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"working_dir | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    working_dir: 2024-01-01
`, "b.yaml": `services:
  web:
    working_dir: /w
`}, []string{"a.yaml", "b.yaml"}, false},
		{"working_dir | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    working_dir: 2024-01-01
`, "b.yaml": `services:
  web:
    working_dir: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"working_dir | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    working_dir: /w
`, "b.yaml": `services:
  web:
    working_dir: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"working_dir | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    working_dir: /w
`, "base.yaml": `services:
  x:
    image: xi
    working_dir: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"working_dir | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    working_dir: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    working_dir: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"working_dir | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    working_dir: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    working_dir: /w
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"working_dir | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    working_dir: /w
`, "inc.yaml": `services:
  web:
    image: wi
    working_dir: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"network_mode | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    network_mode: 2024-01-01
`}, []string{"a.yaml"}, true},
		{"network_mode | later valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    network_mode: 2024-01-01
`, "b.yaml": `services:
  web:
    network_mode: host
`}, []string{"a.yaml", "b.yaml"}, false},
		{"network_mode | later reset", map[string]string{"a.yaml": `services:
  web:
    image: wi
    network_mode: 2024-01-01
`, "b.yaml": `services:
  web:
    network_mode: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"network_mode | later date", map[string]string{"a.yaml": `services:
  web:
    image: wi
    network_mode: host
`, "b.yaml": `services:
  web:
    network_mode: 2024-01-01
`}, []string{"a.yaml", "b.yaml"}, true},
		{"network_mode | extender valid", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    network_mode: host
`, "base.yaml": `services:
  x:
    image: xi
    network_mode: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"network_mode | extender reset", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    network_mode: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    network_mode: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"network_mode | third file valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    network_mode: 2024-01-01
`, "b.yaml": `services:
  web:
    image: wi
`, "c.yaml": `services:
  web:
    network_mode: host
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"network_mode | include then own valid", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    network_mode: host
`, "inc.yaml": `services:
  web:
    image: wi
    network_mode: 2024-01-01
`}, []string{"a.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths := make([]string, len(tc.order))
			for i, f := range tc.order {
				paths[i] = filepath.Join(dir, f)
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}

// The date may come by an alias, and the service that holds it may not be the first of the file: the copy each file is checked by reads both (docker compose, v5.5.1, measured).
func TestADateByAnAliasOrInASecondServiceIsReadWhereAValueIsWrittenOverIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  string
		second string
	}{
		{"by an alias", "x-d: &d 2024-01-01\nservices:\n  web:\n    image: wi\n    user: *d\n", "services:\n  web:\n    user: root\n"},
		{"in the second service of the file", "services:\n  one:\n    image: oi\n  two:\n    image: ti\n    tty: 2024-01-01\n", "services:\n  two:\n    tty: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range map[string]string{"a.yaml": tc.first, "b.yaml": tc.second} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "a.yaml")}, nil); err == nil {
				t.Errorf("a date that stays is read")
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}, nil); err != nil {
				t.Errorf("a value written over the date should make the files fine: %v", err)
			}
		})
	}
}
