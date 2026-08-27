package repohygiene_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The real-conformance target selects its tests by name: `go test -run` with a
// pattern that matches nothing is a warning and exit 0, so a rename on either
// side would leave a target that runs nothing and reports success — the one
// outcome that suite promises not to produce. This binds the two sides: the
// pattern the Makefile passes must select at least one test function that
// actually exists in the file the target points at.
func TestTheRealConformanceTargetSelectsTestsThatExist(t *testing.T) {
	root := repoRoot(t)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	recipe := regexp.MustCompile(`(?m)^\tOPOSSUM_REAL_RUNTIME=1 .*go test (\S+) -run '([^']+)'.*$`).
		FindStringSubmatch(string(mk))
	if recipe == nil {
		t.Fatal("the Makefile no longer has the real-conformance recipe in the shape this reads " +
			"(OPOSSUM_REAL_RUNTIME=1 … go test <pkg> -run '<pattern>') — if the target moved or " +
			"changed shape, move this check with it; without it, a drifted -run pattern runs " +
			"nothing and exits 0")
	}
	pkg, pattern := recipe[1], recipe[2]

	dir := filepath.Join(root, filepath.FromSlash(pkg))
	names, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no test files under %s (the package the target points at): %v", pkg, err)
	}
	fn := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	sel, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("the -run pattern %q does not compile: %v", pattern, err)
	}
	var all []string
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range fn.FindAllStringSubmatch(string(b), -1) {
			all = append(all, m[1])
			if sel.MatchString(m[1]) {
				return // the target selects at least this one — it measures
			}
		}
	}
	t.Errorf("the -run pattern %q selects none of the %d test functions under %s — "+
		"`make real-conformance` would run nothing and exit 0. Rename the pattern or the tests "+
		"so they meet again.", pattern, len(all), pkg)
}
