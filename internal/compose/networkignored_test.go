package compose

import (
	"slices"
	"testing"
)

// Per-network settings opossum reads past — `ipam` under a top-level
// declaration, `aliases`/`ipv4_address` under a service's map-form entry —
// used to be dropped without a word: `config` listed every ignored volume
// field but no network field, so an alias that never resolved was discovered
// at runtime rather than announced (#714). Both sides now report each dropped
// key the way volumes do.
func TestDroppedNetworkFieldsAreNamed(t *testing.T) {
	p := writeProject(t, `
services:
  web:
    image: app:1
    networks:
      back:
        aliases: [db-alias]
        ipv4_address: 10.0.0.5
      front:
        x-note: kept quiet
  plain:
    image: app:1
    networks: [back]
networks:
  back:
    ipam:
      config:
        - subnet: 10.0.0.0/24
    driver: bridge
    x-team: platform
  front:
    internal: true
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, want := range []string{"networks.back.driver"} {
		if !slices.Contains(proj.Unsupported, want) {
			t.Errorf("top-level %s should be reported as ignored, got %v", want, proj.Unsupported)
		}
	}
	// `ipam` is acted on now (its subnet goes to the runtime), so it is not
	// listed whole; only the keys under it opossum reads past would be.
	for _, quiet := range []string{"networks.back.ipam", "networks.back.x-team", "networks.front.internal", "networks"} {
		if slices.Contains(proj.Unsupported, quiet) {
			t.Errorf("%s is acted on or an extension and must not be reported, got %v", quiet, proj.Unsupported)
		}
	}
	web := proj.Services["web"].Unsupported
	for _, want := range []string{"networks.back.aliases", "networks.back.ipv4_address"} {
		if !slices.Contains(web, want) {
			t.Errorf("service-level %s should be reported, got %v", want, web)
		}
	}
	if slices.Contains(web, "networks.front.x-note") {
		t.Errorf("an x- key under a network entry is an extension, not a dropped setting: %v", web)
	}
	// The list form carries no settings and must stay quiet, or every project
	// that names its networks would be told something was ignored.
	if plain := proj.Services["plain"].Unsupported; len(plain) != 0 {
		t.Errorf("a list-form networks: has nothing to report, got %v", plain)
	}
}
