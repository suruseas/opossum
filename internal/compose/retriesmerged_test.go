package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A negative `healthcheck.retries` is refused where it stays in the project the files make, and read where a later file or an extending
// service writes the count over it or resets it, in a service nothing extends of a file that is only extended from, and in a sibling
// of one that is (`config -q`, every row measured, v5.5.1: `refused` is its rc 1): the files are asked once they are merged (#1774).
// The same file's `extends` leaves the extended service in the project, so its negative count stays and is refused. A fraction below
// zero (-0.5) is a negative number here as it is there.
func TestANegativeHealthcheckRetriesIsAskedWhereItStaysInTheProject(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
	}{
		{"retries -1, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, true},
		{"retries -1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -1, a later file resets it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"retries -1, a later file does not write it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"retries -1, a later file writes -1 (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1}
`}}, true},
		{"retries -1 in the extended file, the extender writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {retries: 4}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, false},
		{"retries -1 in the extended file, not written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, true},
		{"retries -1 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, false},
		{"retries -1, a same-file extends writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1}
  web:
    extends: bs
    healthcheck: {retries: 4}
`}}, true},
		{"retries -1, the extender resets healthcheck", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, false},
		{"retries -0.5, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, true},
		{"retries -0.5, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -0.5, a later file resets it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"retries -0.5, a later file does not write it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"retries -0.5, a later file writes -0.5 (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -0.5}
`}}, true},
		{"retries -0.5 in the extended file, the extender writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {retries: 4}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, false},
		{"retries -0.5 in the extended file, not written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, true},
		{"retries -0.5 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, false},
		{"retries -0.5, a same-file extends writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
  web:
    extends: bs
    healthcheck: {retries: 4}
`}}, true},
		{"retries -0.5, the extender resets healthcheck", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, false},
		{"retries -1e30, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}}, true},
		{"retries -1e30, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -1e30, a later file resets it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"retries -1e30, a later file does not write it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"retries -1e30, a later file writes -1e30 (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1e30}
`}}, true},
		{"retries -1e30 in the extended file, the extender writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {retries: 4}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}}, false},
		{"retries -1e30 in the extended file, not written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}}, true},
		{"retries -1e30 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}}, false},
		{"retries -1e30, a same-file extends writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
  web:
    extends: bs
    healthcheck: {retries: 4}
`}}, true},
		{"retries -1e30, the extender resets healthcheck", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1e30}
`}}, false},
		{"retries -1.5, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}}, true},
		{"retries -1.5, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -1.5, a later file resets it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"retries -1.5, a later file does not write it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"retries -1.5, a later file writes -1.5 (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1.5}
`}}, true},
		{"retries -1.5 in the extended file, the extender writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {retries: 4}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}}, false},
		{"retries -1.5 in the extended file, not written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}}, true},
		{"retries -1.5 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}}, false},
		{"retries -1.5, a same-file extends writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
  web:
    extends: bs
    healthcheck: {retries: 4}
`}}, true},
		{"retries -1.5, the extender resets healthcheck", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -1.5}
`}}, false},
		{"retries -9223372036854775809, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}}, true},
		{"retries -9223372036854775809, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -9223372036854775809, a later file resets it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"retries -9223372036854775809, a later file does not write it", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"retries -9223372036854775809, a later file writes -9223372036854775809 (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -9223372036854775809}
`}}, true},
		{"retries -9223372036854775809 in the extended file, the extender writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {retries: 4}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}}, false},
		{"retries -9223372036854775809 in the extended file, not written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}}, true},
		{"retries -9223372036854775809 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}}, false},
		{"retries -9223372036854775809, a same-file extends writes 4", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
  web:
    extends: bs
    healthcheck: {retries: 4}
`}}, true},
		{"retries -9223372036854775809, the extender resets healthcheck", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: -9223372036854775809}
`}}, false},
		{"retries \"-1\", one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: "-1"}
`}}, true},
		{"retries \"-1\", a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: "-1"}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries \"-1\", the later file writes 5 and this one is quoted as a number string (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: "-1"}
`}}, true},
		{"retries '-3', one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: '-3'}
`}}, true},
		{"retries '-3', a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: '-3'}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries '-3', the later file writes 5 and this one is quoted as a number string (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: '-3'}
`}}, true},
		{"retries \"-0.5\", one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: "-0.5"}
`}}, true},
		{"retries \"-0.5\", a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: "-0.5"}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, true},
		{"retries \"-0.5\", the later file writes 5 and this one is quoted as a number string (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: "-0.5"}
`}}, true},
		{"retries -1_0, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1_0}
`}}, true},
		{"retries -1_0, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1_0}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -1_0, the later file writes 5 and this one is quoted as a number string (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1_0}
`}}, true},
		{"retries 0x-1, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 0x-1}
`}}, true},
		{"retries 0x-1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 0x-1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, true},
		{"retries 0x-1, the later file writes 5 and this one is quoted as a number string (stays)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: 5}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 0x-1}
`}}, true},
		{"three files: only an image, retries -1, then 5", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1}
`}, {"f3.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"three files: only an image, retries -1, then reset", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1}
`}, {"f3.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"three files: retries -1 stays in the third", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -1}
`}, {"f3.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"an included file writes retries -1, a later -f file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `include: [inc.yaml]
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}, {"inc.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, false},
		{"an included file writes retries -1, nothing writes it over", []string{"f1.yaml"}, []file{{"f1.yaml", `include: [inc.yaml]
`}, {"inc.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -1}
`}}, true},
		{"three files: only an image, retries -0.5, then 5", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -0.5}
`}, {"f3.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"three files: only an image, retries -0.5, then reset", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -0.5}
`}, {"f3.yaml", `services:
  a:
    healthcheck: {retries: !reset null}
`}}, false},
		{"three files: retries -0.5 stays in the third", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: -0.5}
`}, {"f3.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"an included file writes retries -0.5, a later -f file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `include: [inc.yaml]
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}, {"inc.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, false},
		{"an included file writes retries -0.5, nothing writes it over", []string{"f1.yaml"}, []file{{"f1.yaml", `include: [inc.yaml]
`}, {"inc.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0.5}
`}}, true},
		{"retries -0x1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0x1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -0x1, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0x1}
`}}, true},
		{"retries -0o7, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0o7}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -0o7, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0o7}
`}}, true},
		{"retries -0b1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0b1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries -0b1, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: -0b1}
`}}, true},
		{"retries !!int -0x1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: !!int -0x1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries !!int -0x1, one file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: !!int -0x1}
`}}, true},
		{"healthcheck by an alias holds retries -1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `x-h: &h {test: [CMD, 'true'], retries: -1}
services:
  a:
    image: x
    healthcheck: *h
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"retries by an alias is -1, a later file writes 5", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `x-r: &r -1
services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], retries: *r}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {retries: 5}
`}}, false},
		{"healthcheck interval -1, a later file writes 5s (not a count: refused as the file is read)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], interval: -1}
`}, {"f2.yaml", `services:
  a:
    healthcheck: {interval: 5s}
`}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("the load: err %v, docker compose refuses it: %v", err, tc.refused)
			}
		})
	}
}
