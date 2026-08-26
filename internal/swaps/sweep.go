package swaps

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Mutation is one exchange written the way the sweep tool reads it.
//
// The field names are the sweep file's, not this package's: this exists to be
// marshalled into the file that tool already takes, so that finding the sites
// and running them are two programs rather than one.
type Mutation struct {
	Name     string   `json:"name"`
	File     string   `json:"file"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Packages []string `json:"packages"`
}

// Skip is a pair that could not be made into a mutation, and why.
//
// A pair dropped in silence is the failure this package was written against.
// The counts it replaced were quoted in pull requests as if everything had been
// looked at, and what had not been looked at was each time the interesting
// part. So the ones that do not become mutations come back beside the ones that
// do.
type Skip struct {
	File   string
	Line   int
	A, B   int
	Reason string
}

// Reasons a pair does not become a mutation. Both are properties of the source,
// so neither is a defect in the pair — only in what a sweep could prove about it.
const (
	// The sweep tool locates a mutation by its text and requires the text to be
	// unique in its file. Two identical calls are two places one edit would land.
	SkipNotUnique = "the call is written more than once in this file"
	// And none at all, which happens when the file changed between being parsed
	// and being read back. Not the same thing as the line above, and saying so
	// matters: told the call is written twice, a reader goes looking for the
	// copy.
	SkipGone = "the call is no longer in this file"
	// Exchanging them produces the file it started from, so nothing is mutated
	// and a green suite proves nothing. `%s and %s` given the same expression
	// twice is the usual shape.
	SkipNoChange = "exchanging them changes nothing"
)

// Sweep turns sites into mutations, and returns the pairs it could not turn.
//
// Files are read to check that each mutation names one place. A file that
// cannot be read stops the whole thing: a sweep that quietly holds fewer
// mutations than the sites it was built from is the reading this package
// exists to prevent.
func Sweep(sites []Site) ([]Mutation, []Skip, error) {
	text := map[string]string{}
	var muts []Mutation
	var skips []Skip
	for _, s := range sites {
		body, ok := text[s.File]
		if !ok {
			b, err := os.ReadFile(s.File)
			if err != nil {
				return nil, nil, err
			}
			body = string(b)
			text[s.File] = body
		}
		for _, p := range s.Exchanges {
			from, to, ok := s.Swap(p[0], p[1])
			if !ok {
				continue
			}
			switch {
			case from == to:
				skips = append(skips, Skip{s.File, s.Line, p[0], p[1], SkipNoChange})
			case strings.Count(body, from) == 0:
				skips = append(skips, Skip{s.File, s.Line, p[0], p[1], SkipGone})
			case strings.Count(body, from) > 1:
				skips = append(skips, Skip{s.File, s.Line, p[0], p[1], SkipNotUnique})
			default:
				muts = append(muts, Mutation{
					Name:     name(s, p),
					File:     s.File,
					From:     from,
					To:       to,
					Packages: []string{pkg(s.File)},
				})
			}
		}
	}
	return muts, skips, nil
}

// name is what the row says in the report, so it has to read as a claim about
// the message rather than as coordinates. The position is there too, because a
// reader looking at the row wants the line.
// The column is in it because a line can hold more than one format call — a
// Sprintf inside an Errorf is the ordinary way — and two mutations that share a
// name are two rows of a report a reader cannot tell apart.
func name(s Site, p [2]int) string {
	return s.File + ":" + strconv.Itoa(s.Line) + ":" + strconv.Itoa(s.Col) +
		" reads arguments " + strconv.Itoa(p[0]+1) + " and " + strconv.Itoa(p[1]+1) +
		" the other way round"
}

// pkg is the directory of a file, in the form the go tool takes.
//
// An absolute path is already one of those. Prefixing it would produce
// `.//private/tmp/...`, which names a directory below the working one and
// almost never exists.
func pkg(file string) string {
	d := filepath.ToSlash(filepath.Dir(file))
	if filepath.IsAbs(d) {
		return d
	}
	if d == "." {
		return "./"
	}
	return "./" + d + "/"
}
