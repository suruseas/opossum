package compose

// A boolean written as the quoted word — `read_only: "true"`, `internal:
// "True"` — which to YAML is a string. docker compose (v5.5.0) reads it as
// the boolean (`"true"`, `"True"`, `"TRUE"`, `"false"`; and `"yes"`,
// `"no"`, `"on"`, `"off"`, `"y"`, `"n"` in any case, with a warning that
// YAML 1.2 does not have them), and refuses `"1"`, `"t"`, `""` and other
// words (`invalid boolean`). opossum's bool fields took the YAML 1.1 words
// in their usual spellings (`"yes"`, `"Off"`, `"y"`) and refused the rest,
// `"true"` itself included (`cannot unmarshal !!str into bool`) — one of
// the recorded differences from the service-key sweep. `external` had its
// own reading, which took `"1"` and `"t"` where docker compose refuses
// them; it reads the same words now.

import (
	"strings"
	"testing"
)

func TestABooleanWrittenAsTheQuotedWordIsReadAsTheBoolean(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		check      func(p *Project) bool
	}{
		{"read_only", svc + "    read_only: \"true\"\n", func(p *Project) bool { return p.Services["web"].ReadOnly }},
		{"init, in title case", svc + "    init: 'True'\n", func(p *Project) bool { return p.Services["web"].Init }},
		{"ssh, in upper case", svc + "    ssh: \"TRUE\"\n", func(p *Project) bool { return p.Services["web"].SSH }},
		{"false, quoted", svc + "    read_only: \"false\"\n", func(p *Project) bool { return !p.Services["web"].ReadOnly }},
		{"yes, the YAML 1.1 word, in mixed case", svc + "    read_only: \"yES\"\n", func(p *Project) bool { return p.Services["web"].ReadOnly }},
		{"off, the YAML 1.1 word, in mixed case", svc + "    init: \"oFF\"\n", func(p *Project) bool { return !p.Services["web"].Init }},
		{"a network's external, quoted", svc + "    networks: [back]\nnetworks:\n  back: {external: \"True\"}\n", func(p *Project) bool { return p.Networks["back"].External }},
		{"a mount's read_only", svc + "    volumes:\n      - {type: bind, source: ./a, target: /x, read_only: \"true\"}\n", func(p *Project) bool { return p.Services["web"].Volumes[0] == "./a:/x:ro" }},
		{"a mount's nocopy", svc + "    volumes:\n      - {type: volume, source: d, target: /x, volume: {nocopy: \"true\"}}\n", func(p *Project) bool {
			return len(p.Services["web"].NoCopy) == 1 && p.Services["web"].NoCopy[0] == "/x"
		}},
		{"an env file's required", svc + "    env_file:\n      - {path: ./nope.env, required: \"false\"}\n", func(p *Project) bool { return !p.Services["web"].EnvFile[0].Required }},
		{"a healthcheck's disable", svc + "    healthcheck: {test: [\"CMD\", \"true\"], disable: \"true\"}\n", func(p *Project) bool { return p.Services["web"].Healthcheck.Disabled }},
		{"a network's internal", svc + "    networks: [back]\nnetworks:\n  back: {internal: \"true\"}\n", func(p *Project) bool { return p.Networks["back"].Internal }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if !tc.check(p) {
				t.Errorf("the quoted word was not read as the boolean:\n%s", tc.body)
			}
		})
	}
	// Any other quoted word is still a string, and refused as one — the
	// words docker compose refuses too. A word reached through an alias is
	// left as it is written, a string, since the anchor may be read
	// elsewhere as one.
	for _, tc := range []struct{ name, body, want string }{
		{"a quoted 1", svc + "    read_only: \"1\"\n", "into bool"},
		{"a quoted t", svc + "    init: \"t\"\n", "into bool"},
		{"an empty string", svc + "    ssh: \"\"\n", "into bool"},
		{"another word", svc + "    read_only: \"maybe\"\n", "into bool"},
		{"a network's internal, as another word", svc + "    networks: [back]\nnetworks:\n  back: {internal: \"maybe\"}\n", "into bool"},
		{"a network's external, as a quoted 1", svc + "    networks: [back]\nnetworks:\n  back: {external: \"1\"}\n", "external: expected true/false or a mapping with name, got \"1\""},
		{"a mount's nocopy through an alias", "x-v: &v {nocopy: \"true\"}\n" + svc + "    volumes:\n      - {type: volume, source: d, target: /x, volume: *v}\n", "into bool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// The reading is per field, not per word: under environment a quoted
	// "True" is the value "True", and `command: "true"` is the program
	// named true, as they were.
	p, err := Load(writeTemp(t, svc+"    command: \"true\"\n    environment: {read_only: \"True\", init: \"yes\"}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Environment, ","); got != "init=yes,read_only=True" {
		t.Errorf("environment = %q, want the quoted words kept as written", got)
	}
	if got := strings.Join(p.Services["web"].Command, " "); got != "true" {
		t.Errorf("command = %q, want the program named true", got)
	}
}
