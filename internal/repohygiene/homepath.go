package repohygiene

import "regexp"

// Captured output from a real run is kept in this repository, and a run is taken
// on somebody's machine. What the machine puts in that output — the path to a
// person's home directory, and whatever their account is called — is not part of
// what the run shows, and it is not something to publish on their behalf. Two of
// these reached a commit before anyone noticed; both were removed by hand, by a
// reader who happened to look.
//
// So captures are redacted before they land, and this is the check that the
// redaction happened. It is a narrow one, and worth stating exactly:
//
//   - It finds the shape of a home directory on macOS, which is where this
//     project is developed and where every capture is taken. `/Users/` followed
//     by a name is that shape and nothing else.
//   - It does not find `/home/…`. Images legitimately put a service's home there
//     — `/home/postgres/pgdata` is in a fixture in this repository — and the
//     shape alone cannot tell that from a person's home on a Linux machine.
//   - **It does not find a bare account name.** A user column in `lsof` output is
//     a word; without knowing which word, nothing distinguishes it from any
//     other. That was one of the two that got through, and this check would not
//     have caught it. Reading a capture before committing it is still the only
//     thing that catches that one.
//
// Redaction writes `<user>`, which is why angle brackets are not part of a name
// here: the redacted form has to pass.
var homePath = regexp.MustCompile(`/Users/[A-Za-z0-9_-]+`)

// HomeLeak returns the home directory path found in content, or "" if there is
// none. The whole file is read: a capture puts the path wherever the command it
// recorded happened to print it.
func HomeLeak(content []byte) string {
	return string(homePath.Find(content))
}
