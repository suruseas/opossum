package compose

import (
	"slices"
	"strings"
	"testing"
)

// The names in `build.additional_contexts`, as a failed build names them:
// the keys of a mapping — through an alias or a merge key, as the decoder
// reads them — or the part before `=` of a list's `name=context`. Nothing is
// refused (the key is not acted on, and it never was), and it stays listed
// among the ignored fields, as it was.
func TestAdditionalContextsAreReadForTheirNames(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        []string
	}{
		{"a mapping", "{sharedlib: ./shared}", []string{"sharedlib"}},
		{"two in a mapping, in the order written", "{b: ./shared, a: docker-image://alpine:3.20}", []string{"b", "a"}},
		{"a list", "[sharedlib=./shared]", []string{"sharedlib"}},
		{"an empty context still names one", "{sharedlib: \"\"}", []string{"sharedlib"}},
		{"an empty mapping", "{}", nil},
		{"an empty list", "[]", nil},
		{"the whole value through an alias", "*ac", []string{"sharedlib"}},
		{"a list item through an alias", "[*item]", []string{"lib"}},
		// Through a merge key the merged names count, and `<<` is not one.
		{"a merge key beside a name", "{<<: *ac, b: ./x}", []string{"sharedlib", "b"}},
		{"a merge key of an empty mapping", "{<<: *none}", nil},
		{"a merge key of a list of mappings", "{<<: [*ac, *two]}", []string{"sharedlib", "second"}},
		// A key spelt `<<` in quotes is a name, not a merge.
		{"a quoted << is a name", "{\"<<\": ./x}", []string{"<<"}},
		{"a key through an alias", "{*k : ./shared}", []string{"lib"}},
		// Shapes docker compose refuses are read here as far as they go, as
		// they were: nothing to name, nothing refused.
		{"a list item without =", "[sharedlib]", nil},
		{"a scalar", "sharedlib", nil},
		{"null", "null", nil},
		{"a number in a list", "[1]", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "x-ac: &ac {sharedlib: ./shared}\nx-two: &two {second: ./x}\nx-none: &none {}\nx-item: &item lib=./shared\nx-k: &k lib\nservices:\n  app:\n    image: app-img:1\n    build:\n      context: .\n      additional_contexts: " + tc.value + "\n"
			p, err := Load(writeTemp(t, body))
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["app"]
			if got := []string(svc.Build.AdditionalContexts); !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !slices.ContainsFunc(svc.Unsupported, func(u string) bool { return strings.Contains(u, "additional_contexts") }) {
				t.Errorf("additional_contexts is not acted on, so it stays listed as ignored: %v", svc.Unsupported)
			}
		})
	}
}
