package orchestrator

// ImageWording is one string this package matches against text an image's
// own entrypoint printed — Postgres's initdb, a chown in redis's or mongo's
// startup. Its upstream is not `container` but the image, so it goes quiet
// the day the image is updated, even with no runtime change (#480), and a
// `:latest` tag makes that happen without anyone here doing anything.
//
// Each declaration names the capture under testdata/error-wordings that last
// showed the wording, and the test that reads both holds them to each other:
// the literal is still in the code at the site, the capture still carries the
// wording, and the capture's header names the image tag and the date it was
// taken — the "last matched" the runtime-side table records for `container`.
type ImageWording struct {
	// Site names the function (or file) whose body holds the literal.
	Site string
	// Wording is the text matched, exactly as one literal at the site — or,
	// when InCode is set, the text a capture shows while the code carries
	// only pieces of it (a regexp).
	Wording string
	// InCode names pieces of the matching literal's source text when the
	// wording is not itself one literal.
	InCode []string
	// Image is the image family the wording comes from, as the capture's
	// header names it (postgres, redis, valkey, mongo, clickhouse).
	Image string
	// Capture is the file under testdata/error-wordings whose header names
	// the image tag and date, and whose body carries the wording.
	Capture string
}

// ImageWordings is what this package matches against text an image printed.
// Add an entry whenever a new string from an image's entrypoint is matched
// on, with the capture that shows it; re-capture when the image is updated
// and the date in the header moves.
var ImageWordings = []ImageWording{
	// OPSM-110: postgres 18's entrypoint refusing an old cluster at the new
	// layout — captured on 2026-08-23 (container 1.2.2); the image's words,
	// not the runtime's, so the capture stands until the image is retaken.
	{Site: "pgVersionedLayoutHint", Wording: "(unused mount/volume)", Image: "postgres", Capture: "pg18-named-old-datadir.txt"},
	{Site: "pgVersionedLayoutHint", Wording: "pg_upgrade", Image: "postgres", Capture: "pg18-named-old-datadir.txt"},
	{Site: "initdbNotEmptyHint", Wording: "initdb:", Image: "postgres", Capture: "pg17-external-volume-lostfound-131.txt"},
	{Site: "initdbNotEmptyHint", Wording: "lost+found", Image: "postgres", Capture: "pg17-external-volume-lostfound-131.txt"},
	{Site: "chownCrashHint", Wording: "chown", Image: "postgres", Capture: "pg17-bind-old-datadir-131.txt"},
	{Site: "chownCrashHint", Wording: "Operation not permitted", Image: "postgres", Capture: "pg17-bind-old-datadir-131.txt"},
	{Site: "chownCrashHint", Wording: "Operation not permitted", Image: "redis", Capture: "redis-chown-131.txt"},
	{Site: "chownCrashHint", Wording: "Operation not permitted", Image: "mongo", Capture: "mongo-chown-131.txt"},
	{Site: "chownCrashHint", Wording: "Operation not permitted", Image: "clickhouse", Capture: "clickhouse-chown-131.txt"},
	// chownPathRE pulls the path out of both spellings: `chown: .:` (redis)
	// and `chown: changing ownership of '…':` (mongo, and — since a 2026
	// image — clickhouse, which used to print `chown: /var/lib/clickhouse/:`).
	{Site: "chownfailure.go", Wording: "chown: .: Operation not permitted", InCode: []string{"chown:", `\s+([^:\n]+):`}, Image: "redis", Capture: "redis-chown-131.txt"},
	{Site: "chownfailure.go", Wording: "chown: changing ownership of '/data/db': Operation not permitted", InCode: []string{"chown:", "'([^']+)'"}, Image: "mongo", Capture: "mongo-chown-131.txt"},
	{Site: "chownfailure.go", Wording: "chown: changing ownership of '/var/lib/clickhouse/': Operation not permitted", InCode: []string{"chown:", "'([^']+)'"}, Image: "clickhouse", Capture: "clickhouse-chown-131.txt"},
}
