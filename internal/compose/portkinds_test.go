package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fields of a service's `ports` entries and its `expose` entries are read as docker compose v5.5.1 reads them (#1536; every row
// measured with `docker compose config -q`, and the rows opossum refuses where it does not, with `up`). A `target` written as a
// float with a whole value (`80.0`, `1e2`, `1.0e2`) is the port it says; `host_ip: ""` and an `expose` entry that is a float are
// refused; a null, a bool, a list and a mapping in the fields of a long port are refused. known marks the rows docker compose reads
// at `config` and its engine refuses at `up` (`target: 0` and `65536`, a `published` that is no port, a `protocol` that is none of
// tcp, udp and sctp, measured with `up`): opossum says so at load, as it has for `target: 0` and `published: a`.
func TestThePortsAndExposeFieldsAreReadAsDockerComposeReadsThem(t *testing.T) {
	for _, tc := range []struct {
		name, yaml     string
		refused, known bool
	}{
		{"long published: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: ~}\n", true, false},
		{"long published: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: true}\n", true, false},
		{"long published: 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: 1}\n", false, false},
		{"long published: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: 1.5}\n", true, false},
		{"long published: !!float 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: !!float 1}\n", true, false},
		{"long published: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: [80]}\n", true, false},
		{"long published: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: {a: 1}}\n", true, false},
		{"long published: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"x\"}\n", true, true},
		{"long published: \"\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"\"}\n", false, false},
		{"long published: \"8080\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"8080\"}\n", false, false},
		{"long published: \"8080:80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"8080:80\"}\n", true, true},
		{"long published: \"80.0\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"80.0\"}\n", true, true},
		{"long published: \"8080:80/tcp\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"8080:80/tcp\"}\n", true, true},
		{"long published: \"80-90\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: \"80-90\"}\n", false, false},
		{"long published: 8080", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: 8080}\n", false, false},
		{"long published: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: 0}\n", false, false},
		{"long published: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: 65536}\n", true, true},
		{"long published: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, published: -1}\n", true, true},
		{"long host_ip: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: ~}\n", true, false},
		{"long host_ip: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: true}\n", true, false},
		{"long host_ip: 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: 1}\n", true, false},
		{"long host_ip: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: 1.5}\n", true, false},
		{"long host_ip: !!float 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: !!float 1}\n", true, false},
		{"long host_ip: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: [80]}\n", true, false},
		{"long host_ip: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: {a: 1}}\n", true, false},
		{"long host_ip: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"x\"}\n", true, false},
		{"long host_ip: \"\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"\"}\n", true, false},
		{"long host_ip: \"8080\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"8080\"}\n", true, false},
		{"long host_ip: \"8080:80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"8080:80\"}\n", true, false},
		{"long host_ip: \"80.0\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"80.0\"}\n", true, false},
		{"long host_ip: \"8080:80/tcp\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"8080:80/tcp\"}\n", true, false},
		{"long host_ip: \"80-90\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: \"80-90\"}\n", true, false},
		{"long host_ip: 8080", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: 8080}\n", true, false},
		{"long host_ip: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: 0}\n", true, false},
		{"long host_ip: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: 65536}\n", true, false},
		{"long host_ip: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: -1}\n", true, false},
		{"long protocol: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: ~}\n", true, false},
		{"long protocol: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: true}\n", true, false},
		{"long protocol: 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: 1}\n", true, false},
		{"long protocol: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: 1.5}\n", true, false},
		{"long protocol: !!float 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: !!float 1}\n", true, false},
		{"long protocol: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: [80]}\n", true, false},
		{"long protocol: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: {a: 1}}\n", true, false},
		{"long protocol: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"x\"}\n", true, true},
		{"long protocol: \"\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"\"}\n", false, false},
		{"long protocol: \"8080\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"8080\"}\n", true, true},
		{"long protocol: \"8080:80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"8080:80\"}\n", true, true},
		{"long protocol: \"80.0\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"80.0\"}\n", true, true},
		{"long protocol: \"8080:80/tcp\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"8080:80/tcp\"}\n", true, true},
		{"long protocol: \"80-90\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: \"80-90\"}\n", true, true},
		{"long protocol: 8080", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: 8080}\n", true, false},
		{"long protocol: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: 0}\n", true, false},
		{"long protocol: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: 65536}\n", true, false},
		{"long protocol: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, protocol: -1}\n", true, false},
		{"long name: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: ~}\n", true, false},
		{"long name: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: true}\n", true, false},
		{"long name: 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: 1}\n", true, false},
		{"long name: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: 1.5}\n", true, false},
		{"long name: !!float 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: !!float 1}\n", true, false},
		{"long name: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: [80]}\n", true, false},
		{"long name: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: {a: 1}}\n", true, false},
		{"long name: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"x\"}\n", false, false},
		{"long name: \"\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"\"}\n", false, false},
		{"long name: \"8080\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"8080\"}\n", false, false},
		{"long name: \"8080:80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"8080:80\"}\n", false, false},
		{"long name: \"80.0\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"80.0\"}\n", false, false},
		{"long name: \"8080:80/tcp\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"8080:80/tcp\"}\n", false, false},
		{"long name: \"80-90\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: \"80-90\"}\n", false, false},
		{"long name: 8080", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: 8080}\n", true, false},
		{"long name: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: 0}\n", true, false},
		{"long name: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: 65536}\n", true, false},
		{"long name: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, name: -1}\n", true, false},
		{"long app_protocol: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: ~}\n", true, false},
		{"long app_protocol: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: true}\n", true, false},
		{"long app_protocol: 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: 1}\n", true, false},
		{"long app_protocol: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: 1.5}\n", true, false},
		{"long app_protocol: !!float 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: !!float 1}\n", true, false},
		{"long app_protocol: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: [80]}\n", true, false},
		{"long app_protocol: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: {a: 1}}\n", true, false},
		{"long app_protocol: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"x\"}\n", false, false},
		{"long app_protocol: \"\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"\"}\n", false, false},
		{"long app_protocol: \"8080\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"8080\"}\n", false, false},
		{"long app_protocol: \"8080:80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"8080:80\"}\n", false, false},
		{"long app_protocol: \"80.0\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"80.0\"}\n", false, false},
		{"long app_protocol: \"8080:80/tcp\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"8080:80/tcp\"}\n", false, false},
		{"long app_protocol: \"80-90\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: \"80-90\"}\n", false, false},
		{"long app_protocol: 8080", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: 8080}\n", true, false},
		{"long app_protocol: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: 0}\n", true, false},
		{"long app_protocol: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: 65536}\n", true, false},
		{"long app_protocol: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, app_protocol: -1}\n", true, false},
		{"long mode: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: ~}\n", true, false},
		{"long mode: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: true}\n", true, false},
		{"long mode: 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: 1}\n", true, false},
		{"long mode: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: 1.5}\n", true, false},
		{"long mode: !!float 1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: !!float 1}\n", true, false},
		{"long mode: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: [80]}\n", true, false},
		{"long mode: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: {a: 1}}\n", true, false},
		{"long mode: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"x\"}\n", false, false},
		{"long mode: \"\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"\"}\n", false, false},
		{"long mode: \"8080\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"8080\"}\n", false, false},
		{"long mode: \"8080:80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"8080:80\"}\n", false, false},
		{"long mode: \"80.0\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"80.0\"}\n", false, false},
		{"long mode: \"8080:80/tcp\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"8080:80/tcp\"}\n", false, false},
		{"long mode: \"80-90\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: \"80-90\"}\n", false, false},
		{"long mode: 8080", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: 8080}\n", true, false},
		{"long mode: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: 0}\n", true, false},
		{"long mode: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: 65536}\n", true, false},
		{"long mode: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, mode: -1}\n", true, false},
		{"long target: 80.0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 80.0}\n", false, false},
		{"long target: 1e2", "services:\n  a:\n    image: x\n    ports:\n      - {target: 1e2}\n", false, false},
		{"long target: 1.0e2", "services:\n  a:\n    image: x\n    ports:\n      - {target: 1.0e2}\n", false, false},
		{"long target: !!float 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: !!float 0}\n", true, true},
		{"long target: !!float 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: !!float 65536}\n", true, true},
		{"long target: 0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 0}\n", true, true},
		{"long target: 65536", "services:\n  a:\n    image: x\n    ports:\n      - {target: 65536}\n", true, true},
		{"long target: \"80\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: \"80\"}\n", false, false},
		{"long target: \"x\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: \"x\"}\n", true, false},
		{"long target: ~", "services:\n  a:\n    image: x\n    ports:\n      - {target: ~}\n", true, false},
		{"long target: true", "services:\n  a:\n    image: x\n    ports:\n      - {target: true}\n", true, false},
		{"long target: [80]", "services:\n  a:\n    image: x\n    ports:\n      - {target: [80]}\n", true, false},
		{"long target: {a: 1}", "services:\n  a:\n    image: x\n    ports:\n      - {target: {a: 1}}\n", true, false},
		{"long target: -1", "services:\n  a:\n    image: x\n    ports:\n      - {target: -1}\n", true, false},
		{"long target: \"0x50\"", "services:\n  a:\n    image: x\n    ports:\n      - {target: \"0x50\"}\n", true, false},
		{"long target: 0x50", "services:\n  a:\n    image: x\n    ports:\n      - {target: 0x50}\n", false, false},
		{"long target: 8_0", "services:\n  a:\n    image: x\n    ports:\n      - {target: 8_0}\n", false, false},
		{"long target: 1.5", "services:\n  a:\n    image: x\n    ports:\n      - {target: 1.5}\n", true, false},
		{"expose !!float 80", "services:\n  a:\n    image: x\n    expose:\n      - !!float 80\n", true, false},
		{"expose 80.0", "services:\n  a:\n    image: x\n    expose:\n      - 80.0\n", true, false},
		{"expose 80", "services:\n  a:\n    image: x\n    expose:\n      - 80\n", false, false},
		{"expose \"80\"", "services:\n  a:\n    image: x\n    expose:\n      - \"80\"\n", false, false},
		{"expose \"80-90\"", "services:\n  a:\n    image: x\n    expose:\n      - \"80-90\"\n", false, false},
		{"expose \"x\"", "services:\n  a:\n    image: x\n    expose:\n      - \"x\"\n", false, false},
		{"expose ~", "services:\n  a:\n    image: x\n    expose:\n      - ~\n", true, false},
		{"expose true", "services:\n  a:\n    image: x\n    expose:\n      - true\n", true, false},
		{"expose [80]", "services:\n  a:\n    image: x\n    expose:\n      - [80]\n", true, false},
		{"expose {a: 1}", "services:\n  a:\n    image: x\n    expose:\n      - {a: 1}\n", true, false},
		{"expose 0", "services:\n  a:\n    image: x\n    expose:\n      - 0\n", false, false},
		{"expose 65536", "services:\n  a:\n    image: x\n    expose:\n      - 65536\n", false, false},
		{"expose -1", "services:\n  a:\n    image: x\n    expose:\n      - -1\n", false, false},
		{"expose \"80/tcp\"", "services:\n  a:\n    image: x\n    expose:\n      - \"80/tcp\"\n", false, false},
		{"expose \"80.0\"", "services:\n  a:\n    image: x\n    expose:\n      - \"80.0\"\n", false, false},
		{"expose 1.5", "services:\n  a:\n    image: x\n    expose:\n      - 1.5\n", true, false},
		{"a service with a profile: host_ip empty", "services:\n  a:\n    image: x\n    profiles: [p]\n    ports:\n      - {target: 80, host_ip: \"\"}\n", true, false},
		{"a service with a profile: expose float", "services:\n  a:\n    image: x\n    profiles: [p]\n    expose:\n      - 80.0\n", true, false},
		{"a service with a profile: target float", "services:\n  a:\n    image: x\n    profiles: [p]\n    ports:\n      - {target: 80.0}\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// What is asked of the merged project, and of the whole of a file as docker compose asks it (#1536; every row measured): a `host_ip`
// written empty is read by docker compose once the files are merged, so an entry a later file writes again, or a `!override` or
// `!reset` of `ports`, takes the refusal away; an `expose` float is refused in a service that is not taken too; a `target`
// float comes through a `<<` merge key and is the one written in the entry over the merged one; a key that is no field of the entry
// (`x-a`) does not set the target; a `target` written as a string (`"80.0"`) is no port.
func TestThePortsAndExposeFieldsAreAskedOfTheProjectAsDockerComposeAsksIt(t *testing.T) {
	for _, tc := range []struct {
		name           string
		files          map[string]string
		order          []string
		refused, known bool
	}{
		{"target \"80.0\"", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: \"80.0\"}\n"}, []string{"compose.yaml"}, true, false},
		{"target !!str 80.0", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: !!str 80.0}\n"}, []string{"compose.yaml"}, true, false},
		{"an extension key does not set the target (1.5)", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: 1.5, x-a: 80.0}\n"}, []string{"compose.yaml"}, true, false},
		{"an extension key does not set the target (0)", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: 0, x-a: 80.0}\n"}, []string{"compose.yaml"}, true, true},
		{"target through a merge key", map[string]string{"compose.yaml": "x-p: &m {target: 80.0}\nservices:\n  a:\n    image: x\n    ports:\n      - {<<: *m}\n"}, []string{"compose.yaml"}, false, false},
		{"target through a merge key, published too", map[string]string{"compose.yaml": "x-p: &m {target: 80.0}\nservices:\n  a:\n    image: x\n    ports:\n      - {<<: *m, published: 8080}\n"}, []string{"compose.yaml"}, false, false},
		{"target through a merge key of a list", map[string]string{"compose.yaml": "x-p: &m {target: 80.0}\nservices:\n  a:\n    image: x\n    ports:\n      - {<<: [*m]}\n"}, []string{"compose.yaml"}, false, false},
		{"the written target wins over the merged one", map[string]string{"compose.yaml": "x-p: &m {target: 1.5}\nservices:\n  a:\n    image: x\n    ports:\n      - {<<: *m, target: 80.0}\n"}, []string{"compose.yaml"}, false, false},
		{"host_ip empty in the second entry", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: 80}\n      - {target: 81, host_ip: ''}\n"}, []string{"compose.yaml"}, true, false},
		{"expose float in the second entry", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    expose:\n      - 80\n      - 81.0\n"}, []string{"compose.yaml"}, true, false},
		{"host_ip empty, a later file overrides ports", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: ''}\n", "b.yaml": "services:\n  a:\n    ports: !override\n      - {target: 80}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"host_ip empty, a later file resets ports", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: ''}\n", "b.yaml": "services:\n  a:\n    ports: !reset []\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"host_ip empty, a later file adds an entry", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: ''}\n", "b.yaml": "services:\n  a:\n    ports:\n      - {target: 81}\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"host_ip empty, extended, the extender overrides ports", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    ports:\n      - {target: 80, host_ip: ''}\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    ports: !override\n      - {target: 81}\n"}, []string{"compose.yaml"}, false, false},
		{"host_ip empty, extended, stays", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    ports:\n      - {target: 80, host_ip: ''}\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n"}, []string{"compose.yaml"}, true, false},
		{"expose float in a service of the extended file that is not taken", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    expose: [80.0]\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n"}, []string{"compose.yaml"}, true, false},
		{"expose float in the extended service", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    expose: [80.0]\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n"}, []string{"compose.yaml"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// A `target` written as a float is the number it says, and the entry reads as the one written with that number:
// the spelling that reaches the runtime is the same.
func TestAFloatTargetIsThePortItSaysInTheEntryThatReachesTheRuntime(t *testing.T) {
	for _, tc := range []struct{ entry, want string }{
		{"{target: 80.0}", "80:80"},
		{"{target: 1e2}", "100:100"},
		{"{target: 8.1e1}", "81:81"},
		{"{target: 80.0, published: 18080}", "18080:80"},
		{"{target: 1e2, published: 18082, protocol: udp}", "18082:100/udp"},
		{"{target: 80.0, host_ip: 127.0.0.1, published: 18083}", "127.0.0.1:18083:80"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yaml")
			if err := os.WriteFile(path, []byte("services:\n  a:\n    image: x\n    ports:\n      - "+tc.entry+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Services["a"].Ports; len(got) != 1 || got[0] != tc.want {
				t.Errorf("ports are %v, want [%s]", got, tc.want)
			}
		})
	}
}

// A load that goes on past a fault (the commands that take a project down) keeps the refusal of an empty `host_ip` that stays.
func TestTheRefusalOfAnEmptyHostIPIsKeptWhereTheLoadGoesOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte("services:\n  a:\n    image: x\n    ports:\n      - {target: 80, host_ip: ''}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFilesEnvDirSoft([]string{path}, nil, "")
	if err != nil {
		t.Fatalf("the load stopped: %v", err)
	}
	if err := p.CheckValueFaults(); err == nil || !strings.Contains(err.Error(), "services.a.ports[0].host_ip") {
		t.Fatalf("the fault kept is %v, want the refusal of the empty address", err)
	}
}
