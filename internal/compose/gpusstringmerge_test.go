package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A string for `gpus` (`all`, or any word) that a later file, or a service extending one of another file, writes over a `gpus` that is already there is refused, as docker compose refuses it
// (`cannot override services.web.gpus`); a list written over either merges, and a `!override` or a `!reset` takes the earlier value out first. Every row is measured (docker compose v5.5.1, `config -q`, #2036). A
// service that extends one of its own file, and a hop through a file that is only extended from, are left out of the refusal (docker compose reads those); the four rows of a list under `all` in the
// same file, and the same under a later `-f` file, are not in this table (opossum reads them). A string that comes in through the extends of a later `-f` file is not that file's own: docker compose puts it under the earlier file's value (rows "later -f extends ...").
func TestAStringForGpusWrittenOverGpusIsRefusedAsDockerComposeRefusesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"single | none | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"single | none | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: All
`}, []string{"a.yaml"}, true},
		{"single | none | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: "all"
`}, []string{"a.yaml"}, false},
		{"single | none | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"single | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"single | none | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"single | none | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"single | none | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"single | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"single | none | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"single | none | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: ~
`}, []string{"a.yaml"}, true},
		{"single | none | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"second -f | none | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | none | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | none | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | none | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | all | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | all | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | all | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [{count: 1}] | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [{count: 1}] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [{count: 1}] | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [{count: 1}] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [{count: 1}] | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [] | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [] | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !reset [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !reset [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !reset [] | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !reset [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !override all | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !override all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !override all | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"second -f | !override all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"second -f | !override all | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f | none | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | none | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | none | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | none | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | all | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | all | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | all | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [{count: 1}] | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [{count: 1}] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [{count: 1}] | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [{count: 1}] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [{count: 1}] | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [] | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [] | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !reset [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !reset [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !reset [] | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !reset [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !reset []
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !override all | All", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: All
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !override all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: "all"
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !override all | abc", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: abc
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"third -f | !override all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | !override all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | !reset all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset all
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | !reset []", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | ~", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: ~
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"third -f | !override all | !reset null", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: !override all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  web:
    gpus: !reset null
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"extends file | none | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extends file | none | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extends file | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | none | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extends file | none | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends file | all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, true},
		{"extends file | all | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, true},
		{"extends file | all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, true},
		{"extends file | all | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, true},
		{"extends file | all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | all | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, true},
		{"extends file | [{count: 1}] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, true},
		{"extends file | [{count: 1}] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, true},
		{"extends file | [{count: 1}] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, true},
		{"extends file | [{count: 1}] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [{count: 1}] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends file | [] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, true},
		{"extends file | [] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, true},
		{"extends file | [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, true},
		{"extends file | [] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, true},
		{"extends file | [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, true},
		{"extends file | !reset [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, true},
		{"extends file | !reset [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !reset [] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, true},
		{"extends file | !reset [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, true},
		{"extends file | !override all | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, true},
		{"extends file | !override all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, true},
		{"extends file | !override all | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, true},
		{"extends file | !override all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends file | !override all | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | none | all", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends same file | none | All", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: All
`}, []string{"a.yaml"}, true},
		{"extends same file | none | \"all\"", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: "all"
`}, []string{"a.yaml"}, false},
		{"extends same file | none | abc", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"extends same file | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | none | []", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends same file | none | !override all", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | none | !reset all", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"extends same file | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | none | !reset []", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends same file | none | ~", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: ~
`}, []string{"a.yaml"}, true},
		{"extends same file | none | !reset null", map[string]string{"a.yaml": `services:
  x:
    image: xi
  web:
    extends: x
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"extends same file | all | all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends same file | all | All", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: All
`}, []string{"a.yaml"}, true},
		{"extends same file | all | \"all\"", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: "all"
`}, []string{"a.yaml"}, false},
		{"extends same file | all | abc", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"extends same file | all | [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | all | []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends same file | all | !override all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | all | !reset all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"extends same file | all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | all | !reset []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends same file | all | ~", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: ~
`}, []string{"a.yaml"}, false},
		{"extends same file | all | !reset null", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: all
  web:
    extends: x
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | All", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: All
`}, []string{"a.yaml"}, true},
		{"extends same file | [{count: 1}] | abc", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"extends same file | [{count: 1}] | [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | !override all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | !reset all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | !reset []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | ~", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: ~
`}, []string{"a.yaml"}, false},
		{"extends same file | [{count: 1}] | !reset null", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
  web:
    extends: x
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | All", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: All
`}, []string{"a.yaml"}, true},
		{"extends same file | [] | abc", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"extends same file | [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | !override all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | !reset all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | !reset []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | ~", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: ~
`}, []string{"a.yaml"}, false},
		{"extends same file | [] | !reset null", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: []
  web:
    extends: x
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | All", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: All
`}, []string{"a.yaml"}, true},
		{"extends same file | !reset [] | \"all\"", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: "all"
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | abc", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"extends same file | !reset [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | !override all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | !reset all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | !reset []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends same file | !reset [] | ~", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: ~
`}, []string{"a.yaml"}, true},
		{"extends same file | !reset [] | !reset null", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !reset []
  web:
    extends: x
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | All", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: All
`}, []string{"a.yaml"}, true},
		{"extends same file | !override all | \"all\"", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: "all"
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | abc", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: abc
`}, []string{"a.yaml"}, true},
		{"extends same file | !override all | [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | !override all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | !reset all", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: !reset all
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: !override [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | !reset []", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | ~", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: ~
`}, []string{"a.yaml"}, false},
		{"extends same file | !override all | !reset null", map[string]string{"a.yaml": `services:
  x:
    image: xi
    gpus: !override all
  web:
    extends: x
    gpus: !reset null
`}, []string{"a.yaml"}, false},
		{"extends chain | none | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | none | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extends chain | none | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extends chain | all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | all | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, true},
		{"extends chain | all | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [{count: 1}] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, true},
		{"extends chain | [{count: 1}] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | [] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, true},
		{"extends chain | [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !reset [] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, true},
		{"extends chain | !reset [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends chain | !override all | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, true},
		{"extends chain | !override all | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml"}, false},
		{"extends then -f | none | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | none | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | none | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | none | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | none | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | all | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | all | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | all | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [{count: 1}] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [{count: 1}] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [{count: 1}] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [{count: 1}] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [{count: 1}] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !reset [] | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !reset [] | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !reset [] | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !reset [] | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !override all | All", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: All
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !override all | \"all\"", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: "all"
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !override all | abc", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: abc
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extends then -f | !override all | [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | !override all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | !reset all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset all
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | !override [{count: 1}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !override [{count: 1}]
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | !reset []", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | ~", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: ~
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extends then -f | !override all | !reset null", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "b.yaml": `services:
  web:
    gpus: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    gpus: !override all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"root over a chain | [{count: 1}] | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
    gpus: all
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, true},
		{"root over a chain | [{count: 1}] | [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
    gpus: [{count: 2}]
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    gpus: [{count: 1}]
`}, []string{"a.yaml"}, false},
		{"root over a chain | all | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
    gpus: all
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, true},
		{"root over a chain | all | [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
    gpus: [{count: 2}]
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    gpus: all
`}, []string{"a.yaml"}, false},
		{"root over a chain | none | all", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
    gpus: all
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"root over a chain | none | [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b1.yaml, service: m}
    gpus: [{count: 2}]
`, "b1.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"later -f extends other file | none | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends chain | none | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: mid}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
  mid:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends same file | none | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  base:
    image: wi
    gpus: all
  web:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f extends other file | none | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`, "d.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`}, []string{"a.yaml", "b.yaml", "d.yaml"}, false},
		{"later -f extends other file + own list | none | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: [{count: 3}]
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends other file + own string | none | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: all
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"later -f extends other file | none | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends chain | none | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: mid}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
  mid:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends same file | none | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
  web:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f extends other file | none | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`, "d.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`}, []string{"a.yaml", "b.yaml", "d.yaml"}, false},
		{"later -f extends other file + own list | none | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: [{count: 3}]
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends other file + own string | none | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: all
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"later -f extends other file | [{count: 1}] | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends chain | [{count: 1}] | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: mid}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
  mid:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f extends other file | [{count: 1}] | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`, "d.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`}, []string{"a.yaml", "b.yaml", "d.yaml"}, false},
		{"later -f extends other file + own list | [{count: 1}] | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: [{count: 3}]
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends other file + own string | [{count: 1}] | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: all
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"later -f extends other file | [{count: 1}] | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends chain | [{count: 1}] | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: mid}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
  mid:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends same file | [{count: 1}] | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
  web:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f extends other file | [{count: 1}] | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`, "d.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`}, []string{"a.yaml", "b.yaml", "d.yaml"}, false},
		{"later -f extends other file + own list | [{count: 1}] | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: [{count: 3}]
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends other file + own string | [{count: 1}] | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: [{count: 1}]
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: all
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"later -f extends other file | all | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends chain | all | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: mid}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
  mid:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f extends other file | all | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`, "d.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`}, []string{"a.yaml", "b.yaml", "d.yaml"}, false},
		{"later -f extends other file + own list | all | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: [{count: 3}]
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends other file + own string | all | src all", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: all
`, "c.yaml": `services:
  base:
    image: wi
    gpus: all
`}, []string{"a.yaml", "b.yaml"}, true},
		{"later -f extends other file | all | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends chain | all | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: mid}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
  mid:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends same file | all | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
  web:
    extends: base
`}, []string{"a.yaml", "b.yaml"}, false},
		{"third -f extends other file | all | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    labels: {a: b}
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`, "d.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
`}, []string{"a.yaml", "b.yaml", "d.yaml"}, false},
		{"later -f extends other file + own list | all | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: [{count: 3}]
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"later -f extends other file + own string | all | src [{count: 2}]", map[string]string{"a.yaml": `services:
  web:
    image: wi
    gpus: all
`, "b.yaml": `services:
  web:
    extends: {file: c.yaml, service: base}
    gpus: all
`, "c.yaml": `services:
  base:
    image: wi
    gpus: [{count: 2}]
`}, []string{"a.yaml", "b.yaml"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths := make([]string, len(tc.order))
			for i, f := range tc.order {
				paths[i] = filepath.Join(dir, f)
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
