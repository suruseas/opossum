package runtime

import "testing"

// `chown: .:` is all a Redis container says when it cannot take its data
// directory. The image is what turns that into a path.
func TestImageWorkingDirReadsWhereTheProcessStarts(t *testing.T) {
	for _, tc := range []struct {
		fixture, want string
		wantOK        bool
	}{
		{"../../testdata/image-inspect/redis-7-alpine.json", "/data", true},
		// Declaring nothing is an answer: the image was there and said nothing,
		// which is not the same as not being able to ask.
		{"../../testdata/image-inspect/redis-redis-stack-server.json", "", true},
		{"../../testdata/image-inspect/postgres17.json", "/", true},
		// Not on the machine yet — the case `up --from-docker-compose` meets,
		// since it writes the overlay before anything is pulled.
		{"", "", false},
		// Variants are the same image for different machines. One of them saying
		// nothing is not the image saying nothing — a check that reads only the
		// first would answer "" for an image that declares a directory.
		{"../../testdata/image-inspect/two-variants-second-declares-workdir.json", "/data", true},
	} {
		got, ok := imageInspectShim(t, tc.fixture).ImageWorkingDir("redis")
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: ImageWorkingDir = (%q, %v), want (%q, %v)", tc.fixture, got, ok, tc.want, tc.wantOK)
		}
	}
}
