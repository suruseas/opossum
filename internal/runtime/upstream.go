package runtime

// UpstreamWording is one string this repository matches against text that
// some other program printed — `container`, the builder, a database image's
// entrypoint. Such a match goes quiet the day upstream rephrases, and nothing
// in the code notices; the wording table in testdata/real-cli-output.md is
// where each is written down with the capture that last showed it.
//
// The table used to be bound to the code by nobody. A row could be thinned to
// one short word, or a new match added without a row, and every check stayed
// green. So each package that matches upstream text declares what it matches,
// next to the code, and a test holds the declarations and the table to each
// other: every declared wording is a row's, every row's wording is declared,
// and every declaration names a literal that is still in the code.
//
// What this does not do: notice a match written without a declaration. A
// `strings.Contains` against upstream text that nobody declares is invisible
// here, as it was before — declaring is the author's duty, and the doc on
// each package's list says so.
type UpstreamWording struct {
	// Site names the function, or file, the table's "一致させている場所"
	// column names for this wording — the last token the row carries in
	// backticks. The test reads that function's body (and the file-level
	// var/const of its file, where a regexp lives), or that whole file.
	Site string
	// Wording is the upstream text, exactly as the table's wording column
	// carries it in backticks.
	Wording string
	// InCode names a piece of the source text of the literal(s) that do the
	// matching, when the wording is not itself one literal — a regexp, say.
	// Empty means a literal's value at the site is the wording, exactly.
	InCode []string
}

// UpstreamWordings is what this package matches against upstream text. Add a
// row to the table in testdata/real-cli-output.md alongside any addition
// here; the test that reads both says which side is missing what.
var UpstreamWordings = []UpstreamWording{
	{Site: "buildhint.go", Wording: "unable to read root manifest"},
	{Site: "buildhint.go", Wording: "rpc error: code = Unavailable"},
	{Site: "buildhint.go", Wording: "No space left on device"},
	// One predicate, two rows in the table (volumes and images): both cite
	// resourceInUse, which is where the match is.
	{Site: "resourceInUse", Wording: "in use"},
}
