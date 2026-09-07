package compose

// A service field written with nothing after it (#735, the shape docker
// compose refuses). `volumes:` alone used to load as if the key were absent —
// the decoder never calls a field's UnmarshalYAML for a null — and so did a
// `${VOLS}` that expanded to nothing. docker compose (v5.5.0) refuses every
// key below except `command:` and `entrypoint:`, where a null means "no
// command"; the wording of what each field takes follows its message
// (`services.web.volumes must be a array`). Only fields opossum reads are
// checked here, plus `labels`, whose shape is checked though nothing reads
// it (#784): a bare `container_name:` (a field opossum ignores) is listed
// among the ignored fields as before, where docker refuses it (`must be a
// string`). An earlier test
// here pinned `networks:` alone as "no networks"; docker refuses that too,
// so it moved.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAServiceFieldWithNothingAfterItIsRefused(t *testing.T) {
	for _, tc := range []struct{ key, shape string }{
		{"volumes", "a list"}, {"ports", "a list"}, {"networks", "a list or a mapping"}, {"secrets", "a list"},
		{"depends_on", "a list or a mapping"}, {"env_file", "a string or a list"},
		{"environment", "a mapping or a list"},
		{"healthcheck", "a mapping"}, {"build", "a string or a mapping"},
		// The fields #760 checks for a wrong kind of value: a bare key there
		// is still this refusal, not "must be a list" or "must be a string".
		{"cap_add", "a list"}, {"cap_drop", "a list"}, {"image", "a string"}, {"user", "a string"},
		{"working_dir", "a string"}, {"platform", "a string"}, {"network_mode", "a string"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			body, line := "services:\n  web:\n    image: alpine\n    "+tc.key+":\n", "line 4"
			if tc.key == "image" {
				body, line = "services:\n  web:\n    image:\n", "line 3"
			}
			got := loadErr(t, body)
			want := line + ": " + tc.key + ": expected " + tc.shape + ", got nothing"
			if !strings.Contains(got, want) {
				t.Errorf("want %q, got:\n%s", want, got)
			}
			if !strings.Contains(got, `service "web"`) {
				t.Errorf("the refusal should name the service, got:\n%s", got)
			}
		})
	}
}

// A field opossum reads but has not measured docker's word for gets the
// neutral word; a field opossum does not read (container_name) is not
// refused at all — it is listed among the ignored fields, as before, where
// docker refuses it (`must be a string`). (`labels`, which opossum does not
// read either, is the exception: its shape is checked the way docker checks
// it, and a bare one is refused — see labelsshape_test.go.)
func TestAnUnmeasuredFieldSaysAValueAndAnUnreadFieldIsIgnoredNotRefused(t *testing.T) {
	got := loadErr(t, "services:\n  web:\n    image: alpine\n    init:\n")
	if !strings.Contains(got, "line 4: init: expected a value, got nothing") {
		t.Errorf("want the neutral word for an unmeasured field, got:\n%s", got)
	}
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    container_name:\n"))
	if err != nil {
		t.Fatalf("a bare container_name: is an ignored field, not a refusal: %v", err)
	}
	if got := strings.Join(p.Services["web"].Unsupported, ","); !strings.Contains(got, "container_name") {
		t.Errorf("container_name should be listed among the ignored fields, got %q", got)
	}
}

// The fields docker compose accepts with nothing after them: `command:` and
// `entrypoint:` mean "no command"; `deploy:` and `develop:` are mappings it
// lets be empty; the `x-` extension is never validated.
func TestABareCommandOrEntrypointIsNoCommandNotARefusal(t *testing.T) {
	for _, key := range []string{"command", "entrypoint", "deploy", "develop", "x-opossum-mcp-tools"} {
		t.Run(key, func(t *testing.T) {
			if _, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    "+key+":\n")); err != nil {
				t.Errorf("a bare %s: is accepted by docker compose, not a refusal: %v", key, err)
			}
		})
	}
}

// With two bare keys the first by name is the one named — not whichever the
// map hands out first.
func TestTheFirstBareKeyByNameIsNamed(t *testing.T) {
	for i := 0; i < 20; i++ {
		got := loadErr(t, "services:\n  web:\n    image: alpine\n    volumes:\n    ports:\n")
		if !strings.Contains(got, "line 5: ports: expected a list, got nothing") {
			t.Fatalf("run %d: want ports (first by name) named, got:\n%s", i, got)
		}
	}
}

// A reference that expanded to nothing is not a bare key here: interpolation
// leaves a mark where the reference stood, and the list decoder refuses that
// as a single value (as before). Pinned so the two refusals stay distinct.
func TestAReferenceThatExpandedToNothingIsStillRefusedAsASingleValue(t *testing.T) {
	got := loadErr(t, "services:\n  web:\n    image: alpine\n    volumes: ${OPOSSUM_TEST_UNSET_VOLUMES}\n")
	if !strings.Contains(got, `service "web": volumes must be a list, got a single value`) {
		t.Errorf("want the single-value refusal, got:\n%s", got)
	}
}

// Across -f files a later file's bare key keeps the earlier file's value
// (#732), so it never reaches the refusal; the first file is read as
// written, so a bare key there is refused naming that file and its line
// (each file is checked on its own before the merge).
func TestAcrossFilesOnlyAKeyNoFileGaveAValueToIsRefused(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n    ports: [\"8080:80\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    ports:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("a bare key in the override keeps the base value: %v", err)
	}
	if got := strings.Join(p.Services["web"].Ports, ","); got != "8080:80" {
		t.Errorf("ports = %q, want the base file's", got)
	}
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n    ports:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadFiles([]string{base, over}, nil)
	if err == nil || !strings.Contains(err.Error(), "ports: expected a list, got nothing") {
		t.Fatalf("the first file bare: want the refusal, got: %v", err)
	}
	if !strings.Contains(err.Error(), "base.yml") || !strings.Contains(err.Error(), "line 4") {
		t.Errorf("the refusal should name base.yml and its own line 4, got:\n%s", err)
	}
	if strings.Contains(err.Error(), "merged document") {
		t.Errorf("the mistake is in one file; nothing about a merged document belongs here:\n%s", err)
	}
}

// An alias to nothing is the same bare key: the node the second pass sees is
// the alias, and it must be followed before the tag is read.
func TestAnAliasToNothingIsABareKey(t *testing.T) {
	got := loadErr(t, "x-nada: &nada ~\nservices:\n  web:\n    image: alpine\n    volumes: *nada\n")
	if !strings.Contains(got, "volumes: expected a list, got nothing") {
		t.Errorf("want the bare-key refusal through the alias, got:\n%s", got)
	}
}

// `external:` with nothing after it on a declaration is refused too — a bool
// decode of a null quietly said false, so the volume was created as opossum's
// own instead of used as the pre-existing one.
func TestABareExternalOnADeclarationIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"volume", "services:\n  web:\n    image: alpine\n    volumes: [data:/data]\nvolumes:\n  data:\n    external:\n"},
		{"network", "services:\n  web:\n    image: alpine\n    networks: [back]\nnetworks:\n  back:\n    external:\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, "line 7: external: expected true/false or a mapping with name, got nothing") {
				t.Errorf("want the bare-external refusal at line 7, got:\n%s", got)
			}
		})
	}
}
