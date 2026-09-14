package compose

import (
	"strconv"
	"strings"
	"testing"
)

// A volume name that reaches the runtime as written — a declaration's own
// `name:`, or the key of an external volume that has none — has to be one a
// volume can be created with. container 1.4.1 and the docker engine agree on
// `^[A-Za-z0-9][A-Za-z0-9_.-]*$` (measured with `container volume create` and
// `docker volume create`); docker compose's `config` checks neither place.
// A key that is not external and has no `name:` becomes `<project>_<key>`, so
// its first character is the project's, and it keeps the key rule instead.
func TestAVolumeNameThatReachesTheRuntimeIsOneItCanCreate(t *testing.T) {
	mount := "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: %s, target: /y}\n"
	for _, tc := range []struct {
		name, key, decl, refusal string
	}{
		{"a name the runtime creates", "data", "{name: ok-name}", ""},
		{"a name with an underscore after the first character", "data", "{name: n_u}", ""},
		{"a name starting with a digit", "data", "{name: 0digit}", ""},
		{"a name with a dot after the first character", "data", `{name: "app.data"}`, ""},
		{"a name with capitals", "data", "{name: PgData}", ""},
		{"a name with every allowed character", "data", `{name: "a0.B-c_d"}`, ""},
		{"an external key with a capital and a dot", "Ext.v1", "{external: true}", ""},
		{"a name starting with a dash", "data", `{name: "-dash"}`, `volume "data": name "-dash" ` + runtimeVolumeNameRule},
		{"a name starting with a dot", "data", `{name: ".dot"}`, `volume "data": name ".dot" ` + runtimeVolumeNameRule},
		{"a name starting with an underscore", "data", `{name: _u}`, `volume "data": name "_u" ` + runtimeVolumeNameRule},
		{"a name with a plus", "data", `{name: "a+b"}`, `volume "data": name "a+b" ` + runtimeVolumeNameRule},
		{"a name with a slash", "data", `{name: "a/b"}`, `volume "data": name "a/b" ` + runtimeVolumeNameRule},
		{"an external key the runtime creates", "ext", "{external: true}", ""},
		{"an external key starting with a dot", ".hid", "{external: true}", `external volume ".hid" is used by that name, which ` + runtimeVolumeNameRule + "; set `name:` to the volume's real name"},
		{"an external key starting with a dash", "-m", "{external: true}", `external volume "-m" is used by that name`},
		{"an external key starting with an underscore", "_u", "{external: true}", `external volume "_u" is used by that name`},
		{"an external key with a name of its own", ".hid", "{external: true, name: real}", ""},
		{"an external volume whose own name cannot be created", "ext", `{external: true, name: "-x"}`, `volume "ext": name "-x" ` + runtimeVolumeNameRule},
		{"a key starting with a dot that is namespaced", ".hid", "{}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := strconv.Quote(tc.key)
			body := strings.Replace(mount, "%s", key, 1) + "volumes:\n  " + key + ": " + tc.decl + "\n"
			_, err := Load(writeTemp(t, body))
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("\n got %v\nwant it to contain %q", err, tc.refusal)
			}
		})
	}
}

// Only the volumes a service mounts: docker compose drops a declaration
// nothing uses (measured on v5.5.0: `config` and `up` both succeed with an
// unused `name: "-dash"` or an unused external `.hid`), and nothing opossum
// runs names it either.
func TestAnUnusedVolumeDeclarationIsNotHeldToTheRuntimeRule(t *testing.T) {
	for _, decl := range []string{`unused: {name: "-dash"}`, `unused: {name: "a+b"}`, `".unused": {external: true}`} {
		t.Run(decl, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    volumes:\n      - {type: volume, source: data, target: /y}\nvolumes:\n  data: {}\n  " + decl + "\n"
			if _, err := Load(writeTemp(t, body)); err != nil {
				t.Errorf("an unused declaration should not stop the file loading, got %v", err)
			}
		})
	}
}
