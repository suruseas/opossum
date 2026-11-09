package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

func TestDiagCodeIsCarriedBesideTheWords(t *testing.T) {
	coded := codedf(codeRuntimeAbsent, "no %s", "runtime")
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"coded", coded, string(codeRuntimeAbsent)},
		{"wrapped", fmt.Errorf("up: %w", coded), string(codeRuntimeAbsent)},
		{"the outer code of two", withCode(codeHostPortInUse, fmt.Errorf("x: %w", coded)), string(codeHostPortInUse)},
		{"a subnet change", &subnetChangedError{coded}, string(codeRuntimeAbsent)},
		{"one of several", errorList{msg: "two failed", errs: []error{errors.New("first"), coded}}, string(codeRuntimeAbsent)},
		{"plain", errors.New("plain"), ""},
		{"a project's value that looks like one", fmt.Errorf("service %q failed", "[OPSM-999] x"), ""},
		{"an empty code does not hide one inside", withCode("", coded), string(codeRuntimeAbsent)},
		{"an empty code keeps the error plain", withCode("", errors.New("plain")), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiagCode(tc.err); got != tc.want {
				t.Errorf("DiagCode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodedfSaysWhatTheSentenceSaidBefore(t *testing.T) {
	inner := errors.New("inner")
	err := codedf(codeRuntimeAbsent, "a %s: %w", "b", inner)
	want := fmt.Sprintf("[%s] a b: inner", codeRuntimeAbsent)
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, inner) {
		t.Error("the wrapped error was lost")
	}
}

func TestHintCodeReadsOnlyTheOpening(t *testing.T) {
	for _, tc := range []struct {
		hint string
		want diagCode
	}{
		{"\n  → [OPSM-412] this image has no build", "OPSM-412"},
		{"\n  → the words have no code [OPSM-412]", ""},
		{"[OPSM-412] not a hint", ""},
		{"", ""},
	} {
		if got := hintCode(tc.hint); got != tc.want {
			t.Errorf("hintCode(%q) = %q, want %q", tc.hint, got, tc.want)
		}
	}
}

func TestAWarningGoesToWarnWhereItIsSet(t *testing.T) {
	var out bytes.Buffer
	o := New(&compose.Project{Name: "p"}, &runtime.Runtime{}, "opossum", &out)
	var code, message string
	o.Warn = func(c, m string) { code, message = c, m }
	o.warnf(codeIgnoredField, "service %q: x\n", "web")
	if code != string(codeIgnoredField) || message != `service "web": x` {
		t.Errorf("Warn got %q, %q", code, message)
	}
	if out.Len() != 0 {
		t.Errorf("the text line was printed too: %q", out.String())
	}
	o.Warn = nil
	o.warnf(codeIgnoredField, "service %q: x\n", "web")
	if want := fmt.Sprintf("warning: [%s] service \"web\": x\n", codeIgnoredField); out.String() != want {
		t.Errorf("text = %q, want %q", out.String(), want)
	}
}

// A code written into the sentence is read by nobody: DiagCode would say "". This
// looks for the three common spellings. It does not find a code assembled by other
// means, such as a hint appended to a wrapped error, which withCode has to carry.
func TestTheCommonSpellingsOfACodeInASentenceAreRefused(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{`fmt.Errorf("[%s]`, `fmt.Errorf("[OPSM`, `errors.New("[OPSM`} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s builds a coded error with %s; use codedf so that it carries its code", f, bad)
			}
		}
	}
}
