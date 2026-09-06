package doctor

import "github.com/suruseas/opossum/internal/runtime"

// UpstreamWordings is what this package matches against the output of
// `container system df` and `container builder status`. Every entry is a row
// of the wording table in testdata/real-cli-output.md; see
// runtime.UpstreamWording for the rule. One of these is matched by a regexp
// rather than by one literal, so it names the piece of the regexp that
// carries it; the other two are tokens compared whole.
var UpstreamWordings = []runtime.UpstreamWording{
	{Site: "parseReclaimable", Wording: "GB (", InCode: []string{`(B|KB|MB|GB|TB|PB)\s+\(`}},
	{Site: "parseBuilder", Wording: "running"},
	{Site: "parseBuilder", Wording: "MB"},
}
