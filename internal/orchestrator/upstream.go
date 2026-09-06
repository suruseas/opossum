package orchestrator

import "github.com/suruseas/opossum/internal/runtime"

// UpstreamWordings is what this package matches against text `container`
// printed. Every entry is a row of the wording table in
// testdata/real-cli-output.md, and the test that reads both holds them to each
// other — see runtime.UpstreamWording for the rule, and for what a match
// without a declaration looks like from there (nothing). Add here whenever a
// new string from upstream is matched on, and add the row.
//
// The image-side signatures (initdb's lost+found, an entrypoint's chown) are
// not here: their upstream is an image, not `container`. They are declared
// in ImageWordings, each bound to a capture whose header names the image
// tag and the date it was taken (the same three-way hold, with the date
// standing in for the runtime version).
var UpstreamWordings = []runtime.UpstreamWording{
	{Site: "runErrorHint", Wording: "does not support required platforms"},
	{Site: "runErrorHint", Wording: "Error: platform linux/arm64"},
	{Site: "runErrorHint", Wording: "failed to resolve"},
	{Site: "runErrorHint", Wording: "in rootfs"},
	{Site: "runErrorHint", Wording: "Address already in use"},
	{Site: "isStorageAttachmentError", Wording: "VZErrorDomain"},
	{Site: "isStorageAttachmentError", Wording: "Code=2"},
	{Site: "isStorageAttachmentError", Wording: "storage device attachment is invalid"},
}
