package compose

// The list form of a service's `networks:` in a single file (#724, second
// item). docker compose (v5.5.0) refuses a name listed twice — `services.web.
// networks items at 0 and 1 are equal` — and an item with nothing in it —
// `services.web.networks.1 must be a string`. opossum used to pass the
// duplicate to the runtime as two `--network` flags and drop the empty item.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestANetworkListedTwiceInOneFileIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{
			"listed twice",
			"services:\n  web:\n    image: alpine\n    networks: [back, back]\nnetworks:\n  back: {}\n",
			`networks lists "back" twice (entries 1 and 2)`,
		},
		{
			"listed twice with another between",
			"services:\n  web:\n    image: alpine\n    networks: [back, front, back]\nnetworks:\n  back: {}\n  front: {}\n",
			`networks lists "back" twice (entries 1 and 3)`,
		},
		{
			"an item with nothing in it",
			"services:\n  web:\n    image: alpine\n    networks:\n      - back\n      - \nnetworks:\n  back: {}\n",
			"networks entry 2 of 2 is empty",
		},
		{
			"a quoted empty item",
			"services:\n  web:\n    image: alpine\n    networks:\n      - \"\"\nnetworks:\n  back: {}\n",
			"networks entry 1 of 1 is empty",
		},
		{
			// The empty item first: "1 of 2" reads the other way round as "2 of
			// 1", which "2 of 2" and "1 of 1" above never could — the position and
			// the count have to be told apart, not just both printed.
			"an empty item followed by a name",
			"services:\n  web:\n    image: alpine\n    networks:\n      - \n      - back\nnetworks:\n  back: {}\n",
			"networks entry 1 of 2 is empty",
		},
		{
			// The second item is an alias: its node says `n`, its value says `back`.
			"listed twice through an alias",
			"services:\n  web:\n    image: alpine\n    networks: [&n back, *n]\nnetworks:\n  back: {}\n",
			`networks lists "back" twice (entries 1 and 2)`,
		},
		{
			// The map form walks the nodes by hand, so the decoder's own
			// duplicate-key error never fires here.
			"map form with the key written twice",
			"services:\n  web:\n    image: alpine\n    networks:\n      back: {}\n      back: {}\nnetworks:\n  back: {}\n",
			`networks lists "back" twice (entries 1 and 2)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in the error, got:\n%s", tc.want, got)
			}
			if !strings.Contains(got, `service "web"`) {
				t.Errorf("the error should name the service, got:\n%s", got)
			}
		})
	}
}

// The duplicate must be in one file: the same name restated by a later -f
// file is joined into one network by the merge (#720), and that stays so.
func TestANetworkRestatedByAnotherFileIsStillOne(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n    networks: [back]\nnetworks:\n  back: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    networks: [back]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Networks, ","); got != "back" {
		t.Errorf("networks = %q, want just back", got)
	}
	// A duplicate inside a later file's own list is caught when that list is
	// the only one (nothing to merge with), as in one file.
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\nnetworks:\n  back: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    networks: [back, back]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFiles([]string{base, over}, nil); err == nil || !strings.Contains(err.Error(), `lists "back" twice`) {
		t.Errorf("want the duplicate refusal for the override file's own list, got: %v", err)
	}
	// Once two files write the service's `networks:` — the same names or
	// different ones — the merge reads both sides as maps keyed by name and
	// joins them before anything decodes the result, so a duplicate inside
	// either file is absorbed there and not reported. docker compose, which
	// validates each file first, refuses it. Pinned so the divergence is a
	// known one rather than a surprise.
	for _, tc := range []struct{ name, base, over, want string }{
		{"the later file repeats a name both list", "networks: [back]", "networks: [back, back]", "back"},
		{"the earlier file has the duplicate, the later a different name", "networks: [back, back]", "networks: [front]", "back,front"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n    "+tc.base+"\nnetworks:\n  back: {}\n  front: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(over, []byte("services:\n  web:\n    "+tc.over+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{base, over}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(p.Services["web"].Networks, ","); got != tc.want {
				t.Errorf("networks = %q, want %q", got, tc.want)
			}
		})
	}
}

// Two distinct names, and the map form, load as before.
func TestDistinctNetworksAndTheMapFormStillLoad(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    networks: [back, front]\n  db:\n    image: alpine\n    networks:\n      back: {}\nnetworks:\n  back: {}\n  front: {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Networks, ","); got != "back,front" {
		t.Errorf("web networks = %q", got)
	}
	if got := strings.Join(p.Services["db"].Networks, ","); got != "back" {
		t.Errorf("db networks = %q", got)
	}
}
