package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A compose file that holds more than one YAML document (`---` between them) is
// read by docker compose as a stack of them, each merged into the ones before as
// several `-f` files are (measured, v5.5.1: `services` of every document appear,
// the later value wins, `command` is replaced, `ports` append, `name:` is the last
// one's). opossum read the first document and dropped the rest without a word — a
// service the second one defined was never started. A top-level file is read as
// that many files now.
//
// An empty document is refused by docker compose wherever it is (`top-level object
// must be a mapping`: a trailing `---`, two in a row, a leading pair) and is here.
// One document with a leading `---`, or `...` at the end, is one document.
func TestTheDocumentsOfAFileAreMergedInOrder(t *testing.T) {
	const web = "services:\n  web:\n    image: alpine:3\n    labels: {a: \"1\"}\n    command: [a, b]\n    ports: [\"8080:80\"]\n"
	body := "name: one\n" + web + "---\nname: two\nservices:\n  web:\n    image: alpine:4\n    labels: {b: \"2\"}\n    command: [c]\n    ports: [\"8081:80\"]\n  db:\n    image: alpine:3\n---\nservices:\n  cache:\n    image: alpine:5\n"
	p, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("docker compose merges the three documents, and it was refused: %v", err)
	}
	if got := len(p.Services); got != 3 {
		t.Errorf("services from all three documents: %d, want 3 (web, db, cache)", got)
	}
	web1 := p.Services["web"]
	if web1 == nil {
		t.Fatal("web is gone")
	}
	if web1.Image != "alpine:4" {
		t.Errorf("the later document's image wins: %q, want alpine:4", web1.Image)
	}
	if web1.Labels == nil || len(web1.Labels) != 2 {
		t.Errorf("the labels of both documents: %v, want a and b", web1.Labels)
	}
	if got := strings.Join(web1.Command, " "); got != "c" {
		t.Errorf("command is replaced by the later document's: %q, want c", got)
	}
	if len(web1.Ports) != 2 {
		t.Errorf("ports of both documents: %v, want 8080:80 and 8081:80", web1.Ports)
	}
	if p.Name != "two" {
		t.Errorf("name: is the last document's: %q, want two", p.Name)
	}
	if p.Services["cache"] == nil || p.Services["cache"].Image != "alpine:5" || p.Services["db"] == nil {
		t.Errorf("the services the later documents add: %+v", p.Services)
	}
}

// What is not a merge of documents. Refused as docker compose refuses each, or
// read as one document.
func TestAFileWhoseDocumentsAreNotAllMappingsIsRefused(t *testing.T) {
	const web = "services:\n  web:\n    image: alpine\n"
	const db = "services:\n  db:\n    image: alpine\n"
	for _, tc := range []struct {
		name string
		body string
		// want is what the refusal has to say; empty means the file loads.
		want []string
	}{
		{"a trailing ---", web + "---\n", []string{"document 2", "is empty or not a mapping", "top-level object must be a mapping"}},
		{"a trailing --- and a comment", web + "---\n# nothing\n", []string{"document 2", "is empty or not a mapping"}},
		{"two --- in a row", web + "---\n---\n" + db, []string{"document 2", "is empty or not a mapping"}},
		{"a leading pair", "---\n---\n" + web, []string{"document 1", "is empty or not a mapping"}},
		{"only empty documents", "---\n---\n", []string{"document 1", "is empty or not a mapping"}},
		{"a later document whose services are a list", web + "---\nservices: [1]\n", []string{"line 5", "cannot unmarshal !!seq"}},
		{"a later document that is a list", web + "---\n[1]\n", []string{"document 2", "is empty or not a mapping"}},
		{"a later document that is a string", web + "---\nx\n", []string{"document 2", "is empty or not a mapping"}},

		// A later document that does not parse: docker compose refuses the file, and
		// the reader never reads past the first, so it is said here (measured: main
		// and the reader said nothing and read `web` alone).
		{"a third document that does not parse", web + "---\n" + db + "---\nservices: [\n", []string{"document 3 does not parse"}},
		{"a document start missing after a document end", "---\n...\n" + web, []string{"does not parse"}},

		// An alias to an earlier document's anchor: docker compose resolves it, and
		// each document is read by itself here.
		{"an alias to an anchor of an earlier document", "x-a: &a {image: alpine}\nservices:\n  web: *a\n---\nservices:\n  db: *a\n", []string{"document 2 refers to an anchor written in an earlier document", "-f"}},

		// The controls: one document, however it is marked.
		{"one document", web, nil},
		{"one document with a leading ---", "---\n" + web, nil},
		{"one document with a document end", web + "...\n", nil},
		{"a --- inside a string is not a document", "services:\n  web:\n    image: alpine\n    command: [\"echo\", \"---\"]\n", nil},
		{"a block scalar that holds a --- line", "services:\n  web:\n    image: alpine\n    command: |\n      echo a\n      ---\n", nil},
		// Documents that are all mappings load: the merge test above holds what they say.
		{"two documents", web + "---\n" + db, nil},
		{"two documents after a document end", web + "...\n---\n" + db, nil},
		{"a document with a leading --- and comments between", "# first\n---\n" + web + "# between\n---\n# second\n" + db, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("it loads, and was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("this file loaded, and docker compose refuses it:\n%s", tc.body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not say %q:\n%v", w, err)
				}
			}
		})
	}
}

// A failure in a later document names the line the file has, not the line in that
// document's own text: the documents are cut with their lines kept.
func TestAFailureInALaterDocumentNamesTheLineOfTheFile(t *testing.T) {
	body := "services:\n  web:\n    image: alpine\n---\nservices:\n  db:\n    image: alpine\n    image: busybox\n"
	_, err := Load(writeTemp(t, body))
	if err == nil {
		t.Fatal("db repeats image and loaded")
	}
	if !strings.Contains(err.Error(), "line 8") || !strings.Contains(err.Error(), `mapping key "image" already defined at line 7`) {
		t.Errorf("the refusal does not name the file's lines (7 and 8):\n%v", err)
	}
}

// The documents of a file come after each other in the list, ahead of the next
// `-f` file: the later `-f` file overrides the documents, as it would a file.
func TestTheDocumentsOfAFileComeBeforeTheNextFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.yaml", "services:\n  web:\n    image: alpine:1\n---\nservices:\n  web:\n    image: alpine:2\n")
	b := write("b.yaml", "services:\n  web:\n    image: alpine:3\n")
	p, err := LoadFiles([]string{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services["web"].Image; got != "alpine:3" {
		t.Errorf("the file after the two documents wins: %q, want alpine:3", got)
	}
	p, err = LoadFiles([]string{b, a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services["web"].Image; got != "alpine:2" {
		t.Errorf("the last document of the last file wins: %q, want alpine:2", got)
	}
}

// A later document may carry an `include:` as a later file may: its paths are the
// project's.
func TestALaterDocumentMayInclude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child.yaml"), []byte("services:\n  child:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(main, []byte("services:\n  web:\n    image: alpine\n---\ninclude:\n  - child.yaml\nservices:\n  db:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(main)
	if err != nil {
		t.Fatalf("a later document with an include: %v", err)
	}
	for _, name := range []string{"web", "db", "child"} {
		if p.Services[name] == nil {
			t.Errorf("service %s is missing: %v", name, p.Services)
		}
	}
}

// An included file and the file a service extends are read as one document each:
// they are not merged, and one of several documents is refused, not read as its
// first alone (the difference is in docs/compatibility.md).
func TestAnIncludedOrExtendedFileOfSeveralDocumentsIsRefused(t *testing.T) {
	const web = "services:\n  web:\n    image: alpine\n"
	const two = web + "---\nservices:\n  db:\n    image: alpine\n"
	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("an included file", func(t *testing.T) {
		dir := t.TempDir()
		child := write(t, dir, "child.yaml", two)
		main := write(t, dir, "compose.yaml", "include:\n  - child.yaml\nservices:\n    app:\n      image: alpine\n")
		_, err := Load(main)
		if err == nil || !strings.Contains(err.Error(), "holds 2 YAML documents") || !strings.Contains(err.Error(), child) {
			t.Fatalf("want the included file named as holding 2 documents, got: %v", err)
		}
	})
	t.Run("the file a service extends", func(t *testing.T) {
		dir := t.TempDir()
		base := write(t, dir, "base.yaml", two)
		main := write(t, dir, "compose.yaml", "services:\n  app:\n    extends:\n      file: base.yaml\n      service: web\n")
		_, err := Load(main)
		if err == nil || !strings.Contains(err.Error(), "holds 2 YAML documents") || !strings.Contains(err.Error(), base) {
			t.Fatalf("want the extended file named as holding 2 documents, got: %v", err)
		}
	})
}

// The file is one file in a message, however many documents it holds: what is said
// of the project names it once (two documents are not two files, and a file read
// twice is not a second).
func TestAFileOfSeveralDocumentsIsNamedOnce(t *testing.T) {
	path := writeTemp(t, "x-a: 1\n---\nx-b: 2\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("a file that defines no services loaded")
	}
	if n := strings.Count(err.Error(), path); n != 1 {
		t.Errorf("the file is named %d times, want once:\n%v", n, err)
	}
}
