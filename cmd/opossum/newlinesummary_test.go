package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The applied and suggestion summaries quote compose values, and a newline in
// one used to hand the rest of the value a line of its own — starting at
// column zero, where opossum's sentences start, so a project could put words
// in opossum's mouth (#513; the note class was closed in #509, these are the
// other two). The screen is read whole, the way TestAPathCannotWriteItsOwnNote
// reads it: no line may begin with the planted text.
func TestAnAdaptationSummaryCannotSpeakForOpossum(t *testing.T) {
	forged := "FORGED opossum deleted your database"
	fakeShim(t)
	dir := t.TempDir()
	body := "name: p\nservices:\n  db:\n    image: postgres:16\n    volumes:\n" +
		"      - \"./pg\\n" + forged + ":/var/lib/postgresql/data\"\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// The newline travels as the YAML `\n` escape; written as a real line
	// break it would be folded to a space before opossum ever saw it, and this
	// whole test would pass against an input that lost its teeth (the unit
	// tests learned this the measured way — the guard is repeated here so a
	// rewrite of this fixture alone cannot go quietly vacuous).
	if !strings.Contains(body, `\n`) || strings.Contains(body, "./pg\n") {
		t.Fatalf("the fixture must carry its newline as a YAML escape, got: %q", body)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	// The line this test is about has to have printed — the moved-data-dir
	// summary that quotes the host path.
	if !strings.Contains(out, "[OPSM-105]") {
		t.Fatalf("the applied summary never printed, so nothing was checked:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "FORGED") {
			t.Errorf("a compose value speaks from column zero:\n%s", out)
		}
	}
	// Flattened, the whole value still reads on opossum's line.
	if !strings.Contains(out, "./pg "+forged) {
		t.Errorf("the value should survive flattened on its line, got:\n%s", out)
	}

	// And the overlay this project writes (a second run, without --dry-run)
	// must survive its own self-check — the newline used to carry YAML out of
	// the applied comment and cost every fix in the file.
	if _, err := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor"); err != nil {
		t.Fatalf("up: %v", err)
	}
	written, err := os.ReadFile("compose.opossum.yaml")
	if err != nil {
		t.Fatalf("the overlay was not written — one value's newline cost every fix again: %v", err)
	}
	if !strings.Contains(string(written), "./pg "+forged) {
		t.Errorf("the overlay should carry the flattened value, got:\n%s", written)
	}
}
