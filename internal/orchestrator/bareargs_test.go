package orchestrator_test

// A build arg written as a bare `NAME` (`args: [A]`, or `A:` with nothing
// after it) takes the host shell's value, and is left out when the shell
// has none — as docker compose passes it. Apple's builder does not read
// the shell: `container build --build-arg A` gave the Dockerfile an empty
// A with A exported (measured, container 1.3.1, 2026-09-07), where
// `container run -e A` reads it. So the resolution is opossum's. Left out
// when unset matters too: passed as written, an unset `A` overwrote a
// Dockerfile's `ARG A=default` with the empty string.

import (
	"bytes"
	"os"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestABareBuildArgTakesTheShellsValueAndIsLeftOutWhenUnset(t *testing.T) {
	t.Setenv("OPOSSUM_TEST_ARG_SET", "fromshell")
	t.Setenv("OPOSSUM_TEST_ARG_EQ", "x=y")
	// Unset for real: t.Setenv would set it to "", which is a value.
	if v, ok := os.LookupEnv("OPOSSUM_TEST_ARG_UNSET"); ok {
		t.Setenv("OPOSSUM_TEST_ARG_UNSET", v) // restores it after the test
		os.Unsetenv("OPOSSUM_TEST_ARG_UNSET")
	}
	for _, tc := range []struct{ name, args, want, absent string }{
		{"set in the shell", "OPOSSUM_TEST_ARG_SET", "--build-arg OPOSSUM_TEST_ARG_SET=fromshell", ""},
		{"a value holding an equals sign", "OPOSSUM_TEST_ARG_EQ", "--build-arg OPOSSUM_TEST_ARG_EQ=x=y", ""},
		{"unset in the shell", "OPOSSUM_TEST_ARG_UNSET", "", "OPOSSUM_TEST_ARG_UNSET"},
		{"written with a value", "B=fromfile", "--build-arg B=fromfile", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
			p := project("demo", map[string]*compose.Service{
				"api": {Build: &compose.Build{Context: "/ctx", Args: compose.Environment{tc.args}}},
			})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if err := o.Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			var build string
			for _, l := range log() {
				if len(l) > 5 && l[:5] == "build" {
					build = l
				}
			}
			if build == "" {
				t.Fatalf("no build in %v", log())
			}
			if tc.want != "" && !contains(build, tc.want) {
				t.Errorf("want %q in the build line, got %q", tc.want, build)
			}
			if tc.absent != "" && contains(build, tc.absent) {
				t.Errorf("an unset bare arg should be left out, got %q", build)
			}
		})
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
