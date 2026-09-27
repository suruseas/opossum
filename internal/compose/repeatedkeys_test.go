package compose

import (
	"strings"
	"testing"
)

// docker compose refuses a file that writes one key twice in a mapping wherever
// the mapping is (`mapping key "a" already defined at line 2`), and a block whose
// alias refers to the block that contains it (`cycle detected`). The blocks
// opossum reads into a typed shape refuse a repeat in the decoder's own words, and
// an `x-` extension is asked before the decode (extensiondup_test.go); what is
// left is the mappings the decode takes as they come, which accepted a repeat.
//
// Measured (docker compose v5.5.1, `config`): a repeat in `logging.options`,
// `logging` itself, `sysctls`, `ulimits`, `extra_hosts`, a network's `driver_opts`
// and a gpu device (7 of a sweep of 31 positions for a repeated key) and, by probes
// of their own, `deploy.resources.reservations` and a mount's `bind:` block was
// accepted here (in a single file) and refused there; the rest of the sweep was
// refused by both. The same
// keys once each, a key an alias or a merge brings in beside one written in the
// mapping, an anchored block used in two places and overlapping keys under
// `<<: [*a, *b]` are accepted by both.
func TestARepeatedKeyIsRefusedWhereverTheMappingIs(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name string
		body string
		// want is what the refusal has to say; empty means the file loads.
		want []string
	}{
		{"logging.options", "services:\n  web:\n    image: alpine\n    logging:\n      driver: json-file\n      options: {k: \"1\", k: \"2\"}\n", []string{"same key twice", `mapping key "k" already defined at line 6`}},
		{"logging itself", "services:\n  web:\n    image: alpine\n    logging:\n      driver: json-file\n      driver: syslog\n", []string{"same key twice", `mapping key "driver" already defined at line 5`}},
		{"deploy.resources.reservations", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations: {cpus: \"1\", cpus: \"2\"}\n", []string{"same key twice", `mapping key "cpus" already defined`}},
		{"a gpu device", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          devices:\n            - {driver: nvidia, driver: x, count: 1, capabilities: [gpu]}\n", []string{"same key twice", `mapping key "driver" already defined`}},
		{"sysctls", "services:\n  web:\n    image: alpine\n    sysctls: {a: 1, a: 2}\n", []string{"same key twice", `mapping key "a" already defined`}},
		{"ulimits", "services:\n  web:\n    image: alpine\n    ulimits: {nofile: 1, nofile: 2}\n", []string{"same key twice", `mapping key "nofile" already defined`}},
		{"extra_hosts", "services:\n  web:\n    image: alpine\n    extra_hosts: {a: 1.1.1.1, a: 2.2.2.2}\n", []string{"same key twice", `mapping key "a" already defined`}},
		{"a network's driver_opts", svc + "networks:\n  a:\n    driver_opts: {k: \"1\", k: \"2\"}\n", []string{"same key twice", `mapping key "k" already defined`}},
		// The block under `bind:` is read by name, and its keys the decoder drops:
		// a repeat was read past here (a documented difference in bindkeys_test.go)
		// and is refused now.
		{"a bind mount's bind block", "services:\n  web:\n    image: alpine\n    volumes:\n      - type: bind\n        source: ./a\n        target: /a\n        bind:\n          foo: 1\n          foo: 2\n", []string{"same key twice", `mapping key "foo" already defined at line 9`}},
		// Reached through an alias: the block is read where it is anchored.
		{"an anchored block used as logging.options", "x-o: &o {k: \"1\", k: \"2\"}\nservices:\n  web:\n    image: alpine\n    logging:\n      driver: json-file\n      options: *o\n", []string{"same key twice", `mapping key "k" already defined at line 1`}},

		// A block that contains itself.
		{"an alias that refers to its own block", "x-a: &a {b: *a}\n" + svc, []string{"cycle detected", "line 1"}},

		// Said once: a typed block answers in the decoder's words and is not asked
		// again by the check for the rest of the file.
		{"a typed block is said once", "services:\n  web:\n    image: alpine\n    image: busybox\n", []string{"same key twice", `mapping key "image" already defined`}},

		// The controls: docker compose accepts each of these too.
		{"the keys once each", "services:\n  web:\n    image: alpine\n    logging:\n      driver: json-file\n      options: {k: \"1\", j: \"2\"}\n", nil},
		{"the same key in two sibling mappings", "services:\n  web:\n    image: alpine\n    labels: {a: \"1\"}\n    sysctls: {a: 1}\n", nil},
		{"an anchored block used in two places", "x-l: &l {a: \"1\", b: \"2\"}\nservices:\n  web: {image: alpine, labels: *l}\n  api: {image: alpine, labels: *l}\n", nil},
		{"a key beside the merge that brings the same one in", "x-a: &a {a: \"1\"}\nservices:\n  web:\n    image: alpine\n    labels:\n      <<: *a\n      a: \"2\"\n", nil},
		{"overlapping keys under a merge list", "x-a: &a {image: alpine, user: \"1\"}\nx-b: &b {image: busybox, user: \"2\"}\nservices:\n  web:\n    <<: [*a, *b]\n", nil},
		{"a list that repeats an entry", "x-list: &list [a, a, a]\nservices:\n  web:\n    image: alpine\n    dns: *list\n", nil},
		// A reference written as a key is not expanded before keys are compared
		// (docker compose v5.5.1 accepts this file and prints the keys as written):
		// two of them are two keys however the variables are set. A control: the
		// expanded document (`raw`) keeps the keys as written too, so reading it
		// instead of the file as written (measured with that change) does not fail
		// this row — it would differ in the line numbers only.
		{"two references written as keys that expand alike", "services:\n  web:\n    image: alpine\n    labels: {\"${A}\": \"1\", \"${B}\": \"2\"}\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("A", "x")
			t.Setenv("B", "x")
			_, err := Load(writeTemp(t, tc.body))
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("docker compose takes this file, and it was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("docker compose refuses this file, and it loaded:\n%s", tc.body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not say %q:\n%v", w, err)
				}
			}
			if n := strings.Count(err.Error(), "already defined"); n > 1 {
				t.Errorf("the repeat is said %d times:\n%v", n, err)
			}
		})
	}
}

// A later file is read on its own: a repeat in its logging.options is its mistake,
// and the refusal names it. (The merge of the maps a later file is decoded into
// refused this before the check for the whole file existed: this is the control
// that the overlay path still does, not a row the check is answerable for.)
func TestARepeatInALaterFilesFreeFormMappingIsRefused(t *testing.T) {
	base := writeTemp(t, "services:\n  web:\n    image: alpine\n")
	overlay := writeTemp(t, "services:\n  web:\n    logging:\n      driver: json-file\n      options: {k: \"1\", k: \"2\"}\n")
	_, err := LoadFiles([]string{base, overlay}, nil)
	if err == nil {
		t.Fatal("the overlay repeats a key in logging.options and loaded")
	}
	if !strings.Contains(err.Error(), overlay) || !strings.Contains(err.Error(), `mapping key "k" already defined at line 5`) {
		t.Errorf("the refusal does not name the overlay and the key:\n%v", err)
	}
}
