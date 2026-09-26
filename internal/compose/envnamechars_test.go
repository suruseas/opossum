package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The characters a variable's name may have in an env file, as docker compose
// (v5.5.1, `config`, measured 2026-09-26) reads them: it refuses ASCII
// punctuation outside `_ . - [ ]` and the control characters other than the
// tab, the vertical tab and the form feed, saying `unexpected character "/" in
// variable name`. A name with a `/` or a `$` used to go on into the container's
// environment as written.
//
// The set below is the measurement, one character at a time between two letters
// (`A<c>B=1`): every printable ASCII character and every ASCII control
// character. A space has its own words (`key cannot contain a space`), and a
// character past ASCII is not asked about here.
const refusedInName = "!\"#$%&'()*+,/;<>?@\\^`{|}~"

func TestAnEnvFileNameIsHeldToWhatDockerComposeTakes(t *testing.T) {
	for c := rune(0x21); c < 0x7f; c++ {
		refused := strings.ContainsRune(refusedInName, c)
		t.Run(fmt.Sprintf("%q", string(c)), func(t *testing.T) {
			_, err := loadEnvOf(t, "", fmt.Sprintf("A%cB=1\nZ=2\n", c), "")
			want := fmt.Sprintf("unexpected character %q in variable name", string(c))
			switch {
			case refused && (err == nil || !strings.Contains(err.Error(), want)):
				t.Fatalf("want a refusal saying %s, got %v", want, err)
			case !refused && err != nil:
				t.Fatalf("want the name taken, as docker compose takes it, got %v", err)
			}
		})
	}
}

func TestAnEnvFileNameWithAControlCharacter(t *testing.T) {
	for c := rune(1); c < 0x20; c++ {
		if c == '\n' { // the end of a line, not a character in it
			continue
		}
		// A lone carriage return is kept in a name (only `\r\n` at the end of a
		// line is a line ending), and docker compose takes it.
		taken := c == '\t' || c == '\v' || c == '\f' || c == '\r'
		t.Run(fmt.Sprintf("%U", c), func(t *testing.T) {
			_, err := loadEnvOf(t, "", fmt.Sprintf("A%cB=1\nZ=2\n", c), "")
			if taken && err != nil {
				t.Fatalf("want it taken, got %v", err)
			}
			if !taken && (err == nil || !strings.Contains(err.Error(), "unexpected character")) {
				t.Fatalf("want it refused, got %v", err)
			}
		})
	}
	t.Run("DEL", func(t *testing.T) {
		if _, err := loadEnvOf(t, "", "A\x7fB=1\nZ=2\n", ""); err == nil || !strings.Contains(err.Error(), "unexpected character") {
			t.Fatalf("want it refused, got %v", err)
		}
	})
}

// What is not asked: letters past ASCII and a no-break space are taken by docker
// compose, and stay taken. (It refuses symbols, emoji and some spaces there,
// which this does not — a known difference.)
func TestAnEnvFileNameWithACharacterPastAscii(t *testing.T) {
	for _, name := range []string{"é", "日本", "A B"} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadEnvOf(t, "", name+"=1\nZ=2\n", ""); err != nil {
				t.Fatalf("want the name taken, got %v", err)
			}
		})
	}
}

// Where the name is: after `export`, and in the project's `.env` (which docker
// compose reads with the same rule), and the value is not in the message — an
// env file is where secrets are, and the line is not quoted back.
func TestTheRefusalOfANameSaysWhereAndNotWhat(t *testing.T) {
	_, err := loadEnvOf(t, "", "export A/B=hunter2\nZ=2\n", "")
	if err == nil || !strings.Contains(err.Error(), `app.env:1: unexpected character "/" in variable name`) {
		t.Fatalf("want the place and the character, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the refusal quotes the value back: %v", err)
	}
	_, err = loadEnvOf(t, "", "A=1\n", "A/B=1\n")
	if err == nil || !strings.Contains(err.Error(), ".env:1: unexpected character") {
		t.Fatalf("want the project's .env held to the same rule, got %v", err)
	}
}

// The line the refusal names is the line the name is on: not the first, and not
// the one a multi-line value began on. The name is on the second line, and
// on the line after a quoted value that spans three.
func TestTheRefusalNamesTheLineTheNameIsOn(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"the second line", "Z=1\nA/B=1\n", "app.env:2:"},
		{"after a value that spans lines", "Z=\"one\ntwo\nthree\"\nA/B=1\n", "app.env:4:"},
		{"after a comment and a blank line", "# c\n\nA/B=1\n", "app.env:3:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadEnvOf(t, "", tc.body, "")
			if err == nil || !strings.Contains(err.Error(), tc.want+" unexpected character") {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// The other two ways an env file is read hold a name to the same rule: the file
// `--env-file` names is parsed by the same reader as the project's `.env`.
func TestAnEnvFileGivenByTheFlagIsHeldToTheSameRule(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yaml"), "services: {web: {image: alpine}}\n")
	write(t, filepath.Join(dir, "other.list"), "A/B=1\n")
	p, err := LoadFilesEnvDir([]string{filepath.Join(dir, "compose.yaml")}, []string{filepath.Join(dir, "other.list")}, dir)
	if err == nil {
		err = p.CheckDeclaredNames() // the name is kept for the commands that start or print something
	}
	if err == nil || !strings.Contains(err.Error(), "other.list:1: unexpected character") {
		t.Fatalf("want the file the flag names refused, got %v", err)
	}
}

// What is read past is only the name: the line goes, and every line after it is
// read as it would be without it — including a value that opens a quote on the
// refused line and closes it on a later one, which is walked to its end and not
// left for its second line to be read as a line of its own. The refused name is
// no variable; the ones beside it are.
func TestAProjectDotEnvIsReadPastALineWhoseNameItRefuses(t *testing.T) {
	for _, tc := range []struct{ name, dotenv, line string }{
		{"a symbol", "A/B=1\nOK=1\n", ".env:1:"},
		{"a space", "A B=1\nOK=1\n", ".env:1:"},
		{"a symbol whose value spans lines", "A/B=\"one\ntwo\nthree\"\nOK=1\n", ".env:1:"},
		{"a space whose value spans lines", "A B='one\ntwo'\nOK=1\n", ".env:1:"},
		{"the refused line last", "OK=1\nA/B=1\n", ".env:2:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, ".env"), tc.dotenv)
			if v, err := EnvValue(dir, nil, "OK"); err != nil || v != "1" {
				t.Errorf("OK = %q, %v; want 1 — a line after the refused one was not read", v, err)
			}
			if v, _ := EnvValue(dir, nil, "A/B"); v != "" {
				t.Errorf("the refused name is a variable holding %q", v)
			}
			write(t, filepath.Join(dir, "compose.yaml"), "services: {web: {image: alpine}}\n")
			p, err := Load(filepath.Join(dir, "compose.yaml"))
			if err != nil {
				t.Fatalf("the load stopped at the refused name: %v", err)
			}
			if err := p.CheckDeclaredNames(); err == nil || !strings.Contains(err.Error(), tc.line) {
				t.Errorf("want the refusal kept for the commands that start something, naming %s, got %v", tc.line, err)
			}
		})
	}
}

// The same for the second `.env` a project reads when COMPOSE_FILE chose a compose
// file in another directory than the one it runs in: it is a `.env` of the
// project too, and one that a project may have been started on.
func TestTheSecondDotEnvIsReadPastToo(t *testing.T) {
	work, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "compose.yaml"), "services: {web: {image: alpine}}\n")
	write(t, filepath.Join(home, ".env"), "A/B=1\nOK=1\n")
	p, err := LoadFilesEnvDir([]string{filepath.Join(home, "compose.yaml")}, nil, work)
	if err != nil {
		t.Fatalf("the load stopped at the refused name: %v", err)
	}
	if err := p.CheckDeclaredNames(); err == nil || !strings.Contains(err.Error(), ".env:1:") {
		t.Errorf("want the refusal kept, naming the line, got %v", err)
	}
}

// A quote that never closes is not read past, on a refused name or a fine one: it
// takes the rest of the file, and a line in it (COMPOSE_PROJECT_NAME, say) with it,
// so a command that goes on would act on a project the file did not name.
func TestAnUnterminatedQuoteIsNotReadPastOnARefusedName(t *testing.T) {
	for _, tc := range []struct{ name, dotenv string }{
		{"a symbol", "A/B=\"x\nCOMPOSE_PROJECT_NAME=prod\n"},
		{"a space", "A B='x\nCOMPOSE_PROJECT_NAME=prod\n"},
		{"a name that is fine", "OK=\"x\nCOMPOSE_PROJECT_NAME=prod\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, ".env"), tc.dotenv)
			if _, err := EnvValue(dir, nil, "COMPOSE_PROJECT_NAME"); err == nil || !strings.Contains(err.Error(), "unterminated quoted value") {
				t.Errorf("want the quote that never closes refused, got %v", err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), "services: {web: {image: alpine}}\n")
			if _, err := Load(filepath.Join(dir, "compose.yaml")); err == nil || !strings.Contains(err.Error(), "unterminated quoted value") {
				t.Errorf("the load: want the quote that never closes refused, got %v", err)
			}
		})
	}
}

// A project the compose file includes has a `.env` of its own, read at a level
// under this project's, and a name it refuses is read past like this project's:
// the four commands that take a project down have to be able to read the file.
func TestAnIncludedProjectsDotEnvIsReadPastToo(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "include:\n  - sub/compose.yaml\nservices:\n  web:\n    image: alpine\n")
	write(t, filepath.Join(dir, "sub", "compose.yaml"), "services:\n  db:\n    image: alpine\n")
	write(t, filepath.Join(dir, "sub", ".env"), "A/B=1\nOK=1\n")
	p, err := Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatalf("the load stopped at the included project's refused name: %v", err)
	}
	if err := p.CheckDeclaredNames(); err == nil || !strings.Contains(err.Error(), "sub/.env:1:") {
		t.Errorf("want the refusal kept, naming the included project's line, got %v", err)
	}
}

// And a project the included project includes in turn: its refused name lands in
// the same place, however deep it is.
func TestAProjectIncludedInTurnIsReadPastToo(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "include:\n  - a/compose.yaml\nservices:\n  web:\n    image: alpine\n")
	write(t, filepath.Join(dir, "a", "compose.yaml"), "include:\n  - b/compose.yaml\nservices:\n  mid:\n    image: alpine\n")
	write(t, filepath.Join(dir, "a", "b", "compose.yaml"), "services:\n  db:\n    image: alpine\n")
	write(t, filepath.Join(dir, "a", "b", ".env"), "A B=1\n")
	p, err := Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatalf("the load stopped at the refused name two includes down: %v", err)
	}
	if err := p.CheckDeclaredNames(); err == nil || !strings.Contains(err.Error(), "b/.env:1:") {
		t.Errorf("want the refusal kept, naming the line, got %v", err)
	}
}
