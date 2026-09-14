package runtime

import "testing"

// `volume ls` on container 1.4.1 prints a NAME / TYPE / DRIVER / OPTIONS header
// first (testdata/real-cli-output.md). A volume named NAME is found in a row
// below the header and never in the header itself; a list without a header
// (one name per line) is read row by row.
func TestVolumeInListSkipsOnlyTheHeader(t *testing.T) {
	const header = "NAME                                  TYPE       DRIVER  OPTIONS"
	for _, tc := range []struct {
		name, out, volume string
		want              bool
	}{
		{"the header only", header + "\n", "NAME", false},
		{"a volume named NAME below the header", header + "\nNAME  named  local\n", "NAME", true},
		{"a volume below the header", header + "\nvz_tmp  named  local\n", "vz_tmp", true},
		{"a volume that is not there", header + "\nvz_tmp  named  local\n", "vz", false},
		{"a name that holds it", header + "\nvz_tmp2  named  local\n", "vz_tmp", false},
		{"another case of it", header + "\nvz_tmp  named  local\n", "VZ_TMP", false},
		{"names only, first line", "demo_data\nother\n", "demo_data", true},
		{"names only, second line", "other\ndemo_data\n", "demo_data", true},
		{"a volume named name in a later row", "other\nname\n", "name", true},
		{"nothing", "", "demo_data", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := volumeInList(tc.out, tc.volume); got != tc.want {
				t.Errorf("volumeInList(%q, %q) = %v, want %v", tc.out, tc.volume, got, tc.want)
			}
		})
	}
}
