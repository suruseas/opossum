package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestHowFailuresAreReportedIsDecidedByFlagThenVariable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		env     string
		want    bool
		refused string
	}{
		{"neither", []string{"up"}, "", false, ""},
		{"flag json, no variable", []string{"--error-format", "json", "up"}, "", true, ""},
		{"no flag, variable json", []string{"up"}, "json", true, ""},
		{"flag text wins over variable json", []string{"--error-format", "text", "up"}, "json", false, ""},
		{"flag json wins over variable text", []string{"--error-format", "json", "up"}, "text", true, ""},
		{"an empty variable is unset", []string{"up"}, "", false, ""},
		{"a valid flag does not look at the variable", []string{"--error-format", "json", "up"}, "yaml", true, ""},
		{"a bad flag", []string{"--error-format", "yaml", "up"}, "", false, `--error-format must be text or json, not "yaml"`},
		{"a bad flag beside a good variable", []string{"--error-format", "yaml", "up"}, "json", false, `--error-format must be text or json, not "yaml"`},
		{"a bad variable", []string{"up"}, "yaml", false, `OPOSSUM_ERROR_FORMAT must be text or json, not "yaml"`},
		{"the supervisor's log stays text", []string{"__supervise"}, "json", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == errorFormatEnv {
					return tc.env
				}
				return ""
			}
			got, err := reportsJSON(tc.args, getenv)
			if tc.refused != "" {
				if err == nil || err.Error() != tc.refused {
					t.Fatalf("err = %v, want %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("reportsJSON = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestErrorFormatIsFoundWhereTheFlagIsAFlag(t *testing.T) {
	const j = "json"
	for _, tc := range []struct {
		name  string
		args  []string
		value string
		found bool
	}{
		{"before the command", []string{"--error-format", j, "up"}, j, true},
		{"after the command", []string{"up", "--error-format", j}, j, true},
		{"with an equals sign", []string{"up", "--error-format=json"}, j, true},
		{"after a persistent flag's value", []string{"-p", "demo", "--error-format", j, "up"}, j, true},
		{"after a long flag's value", []string{"--project-name", "demo", "--error-format", j, "up"}, j, true},
		{"after a local flag's value", []string{"up", "--wait-timeout", "30", "--error-format", j}, j, true},
		{"the value of another flag is not the flag", []string{"-p", "--error-format", "up"}, "", false},
		{"after a boolean flag", []string{"--verbose", "--error-format", j, "up"}, j, true},
		{"after a short boolean flag", []string{"up", "-d", "--error-format", j}, j, true},
		{"after a long flag with its value attached", []string{"--project-name=x", "--error-format", j, "up"}, j, true},
		{"after a short flag with its value attached", []string{"-pdemo", "--error-format", j, "up"}, j, true},
		{"after an inherited short flag on run", []string{"run", "-p", "x", "--error-format", j, "web"}, j, true},
		{"after an inherited long flag on run", []string{"run", "--project-name", "x", "--error-format", j, "web"}, j, true},
		{"after an unknown flag", []string{"up", "--bogus", "--error-format", j}, j, true},
		{"after a service of a command that keeps its flags", []string{"up", "web", "--error-format", j}, j, true},
		{"the last one counts", []string{"--error-format", "text", "up", "--error-format", j}, j, true},
		{"not after the double dash", []string{"up", "--", "--error-format", j}, "", false},
		{"before the service of run", []string{"run", "--error-format", j, "web", "true"}, j, true},
		{"not after the service of run", []string{"run", "web", "--error-format", j}, "", false},
		{"not after the service of exec", []string{"exec", "web", "--error-format", j}, "", false},
		{"a run flag's value is not the service", []string{"run", "--audit-format", "json", "--error-format", j, "web"}, j, true},
		{"no value left", []string{"up", "--error-format"}, "", true},
		{"absent", []string{"up", "web"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, found := errorFormatInArgs(tc.args)
			if value != tc.value || found != tc.found {
				t.Errorf("errorFormatInArgs = %q, %v, want %q, %v", value, found, tc.value, tc.found)
			}
		})
	}
}

func TestAFailureIsReportedAsTheLastLineOfJSON(t *testing.T) {
	err := orchestrator.ErrRuntimeAbsent()
	var buf bytes.Buffer
	reportFailure(&buf, true, err, 1)
	line := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("not one line: %q", line)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"schema": 1.0, "level": "error", "source": "opossum", "code": orchestrator.DiagCode(err), "exit": 1.0} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if msg, _ := got["message"].(string); strings.HasPrefix(msg, "[") || !strings.HasPrefix(msg, "the `container` CLI") {
		t.Errorf("message = %q, want it without the code at the start", msg)
	}
}

func TestReportFailureKeepsWhatTheMessageSays(t *testing.T) {
	wrapped := errors.New("up: [OPSM-101] the rest")
	var buf bytes.Buffer
	reportFailure(&buf, true, wrapped, 1)
	var got diagnosticLine
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "" {
		t.Errorf("code = %q, want none: nothing marked this error, and the words are not the mark", got.Code)
	}
	if got.Message != "up: [OPSM-101] the rest" {
		t.Errorf("message = %q", got.Message)
	}
	buf.Reset()
	coded := fmt.Errorf("up: %w", orchestrator.ErrRuntimeAbsent())
	reportFailure(&buf, true, coded, 1)
	got = diagnosticLine{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := coded.Error(); got.Code == "" || got.Message != want {
		t.Errorf("code %q, message %q: a code further in stays, as the message said it", got.Code, got.Message)
	}
	buf.Reset()
	reportFailure(&buf, true, errors.New("a & b\nsecond line"), 1)
	if !strings.Contains(buf.String(), `"message":"a & b\nsecond line"`) {
		t.Errorf("want the ampersand as it is, the new line kept, and no indentation added: %s", buf.String())
	}
}

// A script reads these keys. An empty code is still a key.
func TestALineHasThePromisedKeysAndTypes(t *testing.T) {
	number, text := "number", "string"
	kind := func(v any) string {
		switch v.(type) {
		case float64:
			return number
		case string:
			return text
		}
		return fmt.Sprintf("%T", v)
	}
	check := func(t *testing.T, raw []byte, want map[string]string) map[string]any {
		t.Helper()
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Errorf("keys = %v, want %v", got, want)
		}
		for key, typ := range want {
			v, ok := got[key]
			if !ok {
				t.Errorf("no %q in %s", key, raw)
			} else if kind(v) != typ {
				t.Errorf("%q is a %s, want a %s", key, kind(v), typ)
			}
		}
		return got
	}
	failure := map[string]string{"schema": number, "level": text, "source": text, "code": text, "message": text, "exit": number}
	t.Run("a failure with no code", func(t *testing.T) {
		var buf bytes.Buffer
		reportFailure(&buf, true, errors.New("plain"), 1)
		got := check(t, buf.Bytes(), failure)
		if got["code"] != "" || got["message"] != "plain" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("a failure with a code", func(t *testing.T) {
		var buf bytes.Buffer
		err := orchestrator.ErrRuntimeAbsent()
		reportFailure(&buf, true, err, 1)
		got := check(t, buf.Bytes(), failure)
		if got["code"] != orchestrator.DiagCode(err) || got["code"] == "" {
			t.Errorf("got %v", got)
		}
		if got["message"] == "" {
			t.Errorf("no message: %v", got)
		}
	})
	t.Run("a warning", func(t *testing.T) {
		var buf bytes.Buffer
		writeWarningJSON(&buf, "OPSM-502", "ignored")
		check(t, buf.Bytes(), map[string]string{"schema": number, "level": text, "source": text, "code": text, "message": text})
	})
}

func TestAWarningIsALineWithoutAnExit(t *testing.T) {
	var buf bytes.Buffer
	writeWarningJSON(&buf, "OPSM-502", "ignored")
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["level"] != "warning" || got["code"] != "OPSM-502" || got["message"] != "ignored" || got["source"] != "opossum" {
		t.Errorf("got %v", got)
	}
	if _, has := got["exit"]; has {
		t.Errorf("a warning has no exit: %v", got)
	}
}

func runWithErrorFormat(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(opossumBin, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(),
		"OPOSSUM_CONTAINER_BIN="+fakeShimBin,
		"INSPECT_PROJECT_FROM_NAME=1",
		"FAKE_LOG="+filepath.Join(t.TempDir(), "invocations.log"),
		"XDG_STATE_HOME="+t.TempDir(),
		"OPOSSUM_CRASH_GRACE=0"), env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatalf("opossum did not run: %v", err)
	}
	return o.String(), e.String(), code
}

func errorFormatProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(`name: ef
services:
  web:
    image: alpine
    dns_search: example.com
  app:
    image: alpine
    depends_on:
      migrate: {condition: service_completed_successfully}
  migrate:
    image: alpine
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestTheLastLineOfStderrSaysTheExitOfTheProcess(t *testing.T) {
	dir := errorFormatProject(t)
	for _, tc := range []struct {
		name   string
		args   []string
		env    []string
		exit   int
		source string
	}{
		{"a one-off that exits 3", []string{"run", "--rm", "web", "sh", "-c", "exit 3"}, []string{"FAKE_EXIT=--name web-run.=3"}, 3, "container"},
		{"a one-off that exits 1", []string{"run", "--rm", "web", "false"}, []string{"FAKE_EXIT=--name web-run.=1"}, 1, "container"},
		{"an exec that exits 7", []string{"exec", "web", "sh", "-c", "exit 7"}, []string{"FAKE_EXIT=exec web.=7"}, 7, "container"},
		{"opossum's own failure", []string{"run", "--rm", "nosuch", "true"}, nil, 1, "opossum"},
		{"a dependency that failed", []string{"run", "--rm", "app", "true"}, []string{"FAKE_EXIT=--name migrate.=3"}, 1, "opossum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runWithErrorFormat(t, dir, append(tc.env, "OPOSSUM_ERROR_FORMAT=json"), tc.args...)
			if code != tc.exit {
				t.Fatalf("exit %d, want %d\n%s", code, tc.exit, stderr)
			}
			var got diagnosticLine
			if err := json.Unmarshal([]byte(lastLine(stderr)), &got); err != nil {
				t.Fatalf("the last line of stderr is not JSON: %v\n%s", err, stderr)
			}
			if got.Level != "error" || got.Source != tc.source || got.Exit == nil || *got.Exit != code {
				t.Errorf("got %+v, want source %s and exit %d", got, tc.source, code)
			}
		})
	}
}

func TestAFailureToParseIsReportedByTheFormatItAskedFor(t *testing.T) {
	dir := errorFormatProject(t)
	for _, tc := range []struct {
		name string
		args []string
		env  []string
		json bool
	}{
		{"the variable", []string{"up", "--bogus"}, []string{"OPOSSUM_ERROR_FORMAT=json"}, true},
		{"the flag before it", []string{"--error-format", "json", "up", "--bogus"}, nil, true},
		{"the flag after it", []string{"up", "--bogus", "--error-format", "json"}, nil, true},
		{"text by default", []string{"up", "--bogus"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runWithErrorFormat(t, dir, tc.env, tc.args...)
			if code != 1 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			line := lastLine(stderr)
			if tc.json != strings.HasPrefix(line, `{"schema":1,`) {
				t.Errorf("json = %v, but the last line is %q", tc.json, line)
			}
			if !tc.json && !strings.HasPrefix(line, "opossum: ") {
				t.Errorf("want the text form, got %q", line)
			}
		})
	}
}

// `--help --format json` promises exit 0, so a bad setting beside it does not fail it.
func TestAnInvalidErrorFormatDoesNotFailTheHelpJSON(t *testing.T) {
	dir := errorFormatProject(t)
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{"variable", []string{"--help", "--format", "json"}, []string{"OPOSSUM_ERROR_FORMAT=yaml"}},
		{"flag", []string{"--help", "--format", "json", "--error-format", "yaml"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runWithErrorFormat(t, dir, tc.env, tc.args...)
			if code != 0 || !strings.HasPrefix(stdout, "{") {
				t.Errorf("exit %d\nstdout: %.80s\nstderr: %s", code, stdout, stderr)
			}
		})
	}
}

// `up --from-docker-compose --dry-run` plans against the adapted project with a
// second orchestrator, and its warnings are the ones that project prints.
func TestACodedWarningAfterTheProjectIsReloadedIsStillJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yaml"), `name: ef
services:
  db:
    image: postgres:16
    dns_search: example.com
    volumes:
      - data:/var/lib/postgresql/data
volumes:
  data: {}
`)
	stdout, stderr, _ := runWithErrorFormat(t, dir, []string{"OPOSSUM_ERROR_FORMAT=json"}, "--verbose", "up", "--from-docker-compose", "--no-build", "--dry-run")
	if !strings.Contains(stderr, "OPSM-101") {
		t.Fatalf("this fixture should find a change to plan with, which is what reloads the project; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, `"level":"warning"`) || !strings.Contains(stderr, `"code":"OPSM-502"`) {
		t.Errorf("no JSON warning on stderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "warning: [OPSM-502]") {
		t.Errorf("the reloaded project printed the warning as text:\n%s", stdout)
	}
}

func TestAnInvalidErrorFormatIsRefusedAsText(t *testing.T) {
	dir := errorFormatProject(t)
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{"flag", []string{"--error-format", "yaml", "ps"}, nil},
		{"variable", []string{"ps"}, []string{"OPOSSUM_ERROR_FORMAT=yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runWithErrorFormat(t, dir, tc.env, tc.args...)
			if code != 1 || !strings.HasPrefix(stderr, "opossum: ") || strings.Contains(stderr, `"schema"`) {
				t.Errorf("exit %d, stderr %q", code, stderr)
			}
		})
	}
}

func TestACodedWarningMovesToStderrOnceWhenAskedForJSON(t *testing.T) {
	dir := errorFormatProject(t)
	for _, args := range [][]string{
		{"--verbose", "up", "-d", "web"},
		{"--verbose", "run", "--rm", "web", "true"},
	} {
		t.Run(args[1], func(t *testing.T) {
			stdout, stderr, _ := runWithErrorFormat(t, dir, []string{"OPOSSUM_ERROR_FORMAT=json"}, args...)
			var n int
			for _, line := range strings.Split(stderr, "\n") {
				if strings.Contains(line, `"level":"warning"`) && strings.Contains(line, `"code":"OPSM-502"`) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("want the warning on one line of stderr, got %d:\n%s", n, stderr)
			}
			if strings.Contains(stdout, "OPSM-502") || strings.Contains(stderr, "warning: [OPSM-502]") {
				t.Errorf("the warning was also printed as text:\nstdout: %s\nstderr: %s", stdout, stderr)
			}
			textOut, textErr, _ := runWithErrorFormat(t, dir, nil, args...)
			if !strings.Contains(textOut+textErr, "warning: [OPSM-502]") {
				t.Errorf("by default the warning stays a line of text, got:\nstdout: %s\nstderr: %s", textOut, textErr)
			}
		})
	}
}

type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout is closed") }

// The help JSON is written before anything else is read, on a path of its own,
// and a failure to write it is reported like every other failure.
func TestAFailureToWriteTheHelpJSONIsReportedByTheFormatItAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		json bool
	}{
		{"the flag", []string{"--help", "--format", "json", "--error-format", "json"}, "", true},
		{"the variable", []string{"--help", "--format", "json"}, "json", true},
		{"text by default", []string{"--help", "--format", "json"}, "", false},
		{"text when the setting is invalid", []string{"--help", "--format", "json", "--error-format", "yaml"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(errorFormatEnv, tc.env)
			var stderr bytes.Buffer
			if code := runCLI(tc.args, refusingWriter{}, &stderr); code != 1 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			line := lastLine(stderr.String())
			if !tc.json {
				if line != "opossum: stdout is closed" {
					t.Errorf("want the text form, got %q", line)
				}
				return
			}
			var got diagnosticLine
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatalf("%v in %q", err, line)
			}
			if got.Level != "error" || got.Source != "opossum" || got.Message != "stdout is closed" || got.Exit == nil || *got.Exit != 1 {
				t.Errorf("got %+v", got)
			}
		})
	}
}

// The search for the flag stops at a service only for the commands whose parser
// stops there too, and the list of them is written by hand. A word after the
// first argument is asked of each command's own parser: one that stops does not
// look at it, so an unknown flag there is not refused.
func TestTheCommandsThatStopAtTheirFirstArgumentAreTheOnesTheSearchStopsAt(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		t.Run(c.CommandPath(), func(t *testing.T) {
			stops := c.Flags().Parse([]string{"service", "--not-a-flag-of-any-command"}) == nil
			if stops != passesThrough[c.Name()] {
				t.Errorf("its parser stops at the first argument: %v; passesThrough says %v", stops, passesThrough[c.Name()])
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
}
