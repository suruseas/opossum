package compose

// A long-form item that leaves out the key it cannot do without — a
// mount's or a port's `target`, a secret's `source`, an env file's `path`
// — is refused naming the entry by number and saying what to write, the
// way the other entry refusals do; it used to say "port entry is missing
// a target" with no number. Written with nothing after it, the same key
// is refused as bare (docker compose: `services.web.volumes.[0] is
// missing a mount target`, `…ports.0.target must be a integer or string`,
// `…secrets.0.source must be a string`). Two things docker compose's
// `config` lets through and this refuses, on purpose: a secret reference
// without a `source` (`{target: s}`), and an empty string where the key
// is required (`target: ""`) — both are the same as leaving the key out.

import (
	"strings"
	"testing"
)

func TestALongFormItemWithoutItsKeyIsRefusedByNumber(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a mount without a target", svc + "    volumes: [{type: bind, source: .}]\n", "volumes entry 1 of 1 has no target — write the path in the container, as in `target: /app`"},
		{"the second mount without a target", svc + "    volumes: [./a:/a, {type: bind, source: .}]\n", "volumes entry 2 of 2 has no target"},
		{"a port without a target", svc + "    ports: [{published: 8080}]\n", "ports entry 1 of 1 has no target — write the container port, as in `target: 80`"},
		{"the second port without a target", svc + "    ports: [\"80:80\", {published: 8080}]\n", "ports entry 2 of 2 has no target"},
		{"a secret without a source", svc + "    secrets: [{target: s}]\nsecrets:\n  s: {file: ./s}\n", "secrets entry 1 of 1 has no source — write the secret's name, as in `source: db-password`"},
		{"an env file without a path", svc + "    env_file: [{required: false}]\n", "env_file entry 1 of 1 has no path — write the file, as in `path: ./app.env`"},
		// Written with nothing after it: bare, not left out.
		{"a mount's target bare", svc + "    volumes: [{type: bind, source: ., target: }]\n", "volumes entry 1 of 1: target has nothing after it"},
		{"a port's target bare", svc + "    ports: [{target: , published: 8080}]\n", "ports entry 1 of 1: target has nothing after it"},
		{"a secret's source bare", svc + "    secrets: [{source: }]\nsecrets:\n  s: {file: ./s}\n", "secrets entry 1 of 1: source has nothing after it"},
		{"an env file's path bare", svc + "    env_file: [{path: }]\n", "env_file entry 1 of 1: path has nothing after it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}
