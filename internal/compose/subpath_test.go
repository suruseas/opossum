package compose

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Which mounts ask for a part of a volume, as docker compose reads them
// (v5.5.1, measured 2026-09-20): a named volume's non-empty `volume.subpath`
// mounts only what is under it; an empty one mounts the whole volume; a bind's
// or a tmpfs's `volume.subpath` is read past. Only the first is recorded — the
// rest keep being listed among the ignored fields, as before.
func TestVolumeSubpathsAreTheNamedVolumeMountsOfAPart(t *testing.T) {
	for _, tc := range []struct {
		name, vols string
		want       []string
	}{
		{"a named volume's subpath", "      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n", []string{"data:/data (subpath sub)"}},
		{"a named volume's subpath beside nocopy", "      - {type: volume, source: data, target: /data, volume: {subpath: sub, nocopy: true}}\n", []string{"data:/data (subpath sub)"}},
		{"a deeper subpath", "      - {type: volume, source: data, target: /data, volume: {subpath: a/b}}\n", []string{"data:/data (subpath a/b)"}},
		{"an empty subpath is the whole volume", "      - {type: volume, source: data, target: /data, volume: {subpath: \"\"}}\n", nil},
		{"no subpath", "      - {type: volume, source: data, target: /data}\n", nil},
		{"a bind's volume.subpath is read past", "      - {type: bind, source: ./shared, target: /data, volume: {subpath: sub}}\n", nil},
		{"a tmpfs's volume.subpath is read past", "      - {type: tmpfs, target: /data, volume: {subpath: sub}}\n", nil},
		{"the short form has none", "      - data:/data\n", nil},
		{"two of them, in the order written", "      - {type: volume, source: data, target: /a, volume: {subpath: x}}\n      - data:/plain\n      - {type: volume, source: data, target: /b, volume: {subpath: y}}\n", []string{"data:/a (subpath x)", "data:/b (subpath y)"}},
		{"through an alias", "      - *m\n", []string{"data:/data (subpath sub)"}},
		// The whole volume in other words: docker compose mounts all of it.
		{"a subpath of .", "      - {type: volume, source: data, target: /data, volume: {subpath: .}}\n", nil},
		{"a subpath of ./", "      - {type: volume, source: data, target: /data, volume: {subpath: ./}}\n", nil},
		{"a subpath that comes back up", "      - {type: volume, source: data, target: /data, volume: {subpath: sub/..}}\n", nil},
		// Two entries at one target are one mount, the later kept.
		{"a subpath then the whole at its target", "      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n      - data:/data\n", nil},
		{"the whole then a subpath at its target", "      - data:/data\n      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n", []string{"data:/data (subpath sub)"}},
		{"a subpath then the whole at its target with a slash", "      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n      - data:/data/\n", nil},
		{"a subpath at a slashed target then the whole", "      - {type: volume, source: data, target: /data/, volume: {subpath: sub}}\n      - data:/data\n", nil},
		// A long-form tmpfs at the target is a mount there too: the later one.
		{"a subpath then a long-form tmpfs at its target", "      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n      - {type: tmpfs, target: /data}\n", nil},
		{"a long-form tmpfs then a subpath at its target", "      - {type: tmpfs, target: /data}\n      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n", []string{"data:/data (subpath sub)"}},
		// An anonymous volume's, which docker compose refuses (`must not set
		// Subpath when using anonymous volumes`), refused here too.
		{"an anonymous volume's subpath", "      - {type: volume, target: /data, volume: {subpath: sub}}\n", []string{"an anonymous volume at /data (subpath sub)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "x-m: &m {type: volume, source: data, target: /data, volume: {subpath: sub}}\nservices:\n  app:\n    image: alpine:3.20\n    volumes:\n" + tc.vols + "volumes:\n  data: {}\n"
			p, err := Load(writeTemp(t, body))
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["app"]
			var got []string
			for _, v := range svc.VolumeSubpaths {
				got = append(got, v.String())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			// Still listed among the ignored fields wherever the key is
			// written, as it was — the empty one included: the mount is read,
			// the subpath is not acted on.
			listed := slices.ContainsFunc(svc.Unsupported, func(u string) bool { return strings.HasSuffix(u, ".volume.subpath") })
			if wantListed := strings.Contains(tc.vols, "subpath: "); !strings.HasPrefix(tc.name, "through an alias") && listed != wantListed {
				t.Errorf("listed among the ignored: %v, want %v (%v)", listed, wantListed, svc.Unsupported)
			}
		})
	}
}

// The whole list through an alias reads the same as written in place.
func TestVolumeSubpathsThroughAnAliasedList(t *testing.T) {
	body := "x-vs: &vs\n  - {type: volume, source: data, target: /data, volume: {subpath: sub}}\nservices:\n  app:\n    image: alpine:3.20\n    volumes: *vs\nvolumes:\n  data: {}\n"
	p, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if subs := p.Services["app"].VolumeSubpaths; len(subs) != 1 || subs[0].String() != "data:/data (subpath sub)" {
		t.Errorf("got %v", subs)
	}
}

// A service that borrows a holder's mounts with volumes_from borrows a mount
// of a part along with them — unless it mounts that path itself — so a
// command that starts the borrower alone reads it (docker compose shows the
// borrower only what is under the holder's subpath).
func TestVolumeSubpathsComeAlongWithVolumesFrom(t *testing.T) {
	for _, tc := range []struct {
		name, app string
		want      []string
	}{
		{"borrowed", "    volumes_from: [holder]\n", []string{"data:/data (subpath sub)"}},
		{"borrowed, the path mounted whole here", "    volumes_from: [holder]\n    volumes:\n      - data:/data\n", nil},
		{"borrowed beside a mount of its own elsewhere", "    volumes_from: [holder]\n    volumes:\n      - data:/other\n", []string{"data:/data (subpath sub)"}},
		// Two holders at one target: the later one's mount is the one borrowed
		// (docker compose v5.5.1, measured 2026-09-20).
		{"the part, then another holder's whole at its target", "    volumes_from: [holder, whole]\n", nil},
		{"another holder's whole, then the part", "    volumes_from: [whole, holder]\n", []string{"data:/data (subpath sub)"}},
		// Through a holder that borrows it in turn.
		{"borrowed along a chain", "    volumes_from: [middle]\n", []string{"data:/data (subpath sub)"}},
		// A holder whose target is written with a slash.
		{"borrowed from a slashed target", "    volumes_from: [slashed]\n", []string{"data:/data/ (subpath sub)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  holder:\n    image: alpine:3.20\n    volumes:\n      - {type: volume, source: data, target: /data, volume: {subpath: sub}}\n      - data:/whole\n  whole:\n    image: alpine:3.20\n    volumes:\n      - data:/data\n  middle:\n    image: alpine:3.20\n    volumes_from: [holder]\n  slashed:\n    image: alpine:3.20\n    volumes:\n      - {type: volume, source: data, target: /data/, volume: {subpath: sub}}\n  app:\n    image: alpine:3.20\n" + tc.app + "volumes:\n  data: {}\n"
			p, err := Load(writeTemp(t, body))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, v := range p.Services["app"].VolumeSubpaths {
				got = append(got, v.String())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Which of the service's own `volumes:` entries each recorded mount is, as the
// ignored fields number them (1 for the first), and 0 for one borrowed with
// `volumes_from` — the holder's entry, not one of the borrower's. Written out
// per row, not counted from the fixture.
func TestAVolumeSubpathKnowsItsOwnEntry(t *testing.T) {
	const named = "      - {type: volume, source: data, target: %s, volume: {subpath: sub}}\n"
	for _, tc := range []struct {
		name, vols string
		from       bool
		want       []int
	}{
		{"the first", fmt.Sprintf(named, "/a"), false, []int{1}},
		{"after a bind's", "      - {type: bind, source: ./s, target: /b, volume: {subpath: sub}}\n" + fmt.Sprintf(named, "/a"), false, []int{2}},
		{"over a whole mount at its target", "      - data:/a\n" + fmt.Sprintf(named, "/a"), false, []int{2}},
		{"first and third", fmt.Sprintf(named, "/a") + "      - data:/plain\n" + fmt.Sprintf(named, "/c"), false, []int{1, 3}},
		{"the later of two at one target", fmt.Sprintf(named, "/a") + fmt.Sprintf(named, "/a"), false, []int{2}},
		{"through an alias", "      - data:/plain\n      - *m\n", false, []int{2}},
		{"borrowed", "      - data:/plain\n", true, []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "x-m: &m {type: volume, source: data, target: /m, volume: {subpath: sub}}\nservices:\n  holder:\n    image: alpine:3.20\n    volumes:\n      - data:/x\n" + fmt.Sprintf(named, "/h") + "  app:\n    image: alpine:3.20\n"
			if tc.from {
				body += "    volumes_from: [holder]\n"
			}
			body += "    volumes:\n" + tc.vols + "volumes:\n  data: {}\n"
			p, err := Load(writeTemp(t, body))
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, v := range p.Services["app"].VolumeSubpaths {
				got = append(got, v.Entry)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("entries = %v, want %v", got, tc.want)
			}
		})
	}
}
