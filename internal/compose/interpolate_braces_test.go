package compose

import (
	"reflect"
	"strings"
	"testing"
)

// The `}` that closes `${VAR:-word}` and `${VAR:+word}` when the word has a `{` of its own (#1631).
// docker compose counts every `{` it meets as an opening and does not look at the character
// after it, so `${E:-{x}}` is one reference with the word `{x}`, and `${E:-{{x}}}` closes one
// `}` early. opossum counted only `${`, so it closed at the first `}` and left the rest of the
// word behind as text (`ev}` where docker gives `ev`). Every value below is what docker compose
// v5.5.1 printed for `P${E<op>word}S` in an `environment` value — E unset, set to nothing, and
// set to `ev`, with B set to `bv` (captured by running `docker compose config` over each
// template; no value was worked out by hand — except that `config` prints a literal `$` as `$$`,
// which is taken back to `$` here).
func TestTheWordOfAReferenceMayHaveBracesOfItsOwn(t *testing.T) {
	for _, tc := range []struct {
		word, op          string
		unset, empty, set string
	}{
		{`x`, `:-`, `PxS`, `PxS`, `PevS`},
		{`x`, `:+`, `PS`, `PS`, `PxS`},
		{`x`, `-`, `PxS`, `PS`, `PevS`},
		{`x`, `+`, `PS`, `PxS`, `PxS`},
		{`{x}`, `:-`, `P{x}S`, `P{x}S`, `PevS`},
		{`{x}`, `:+`, `PS`, `PS`, `P{x}S`},
		{`{x}`, `-`, `P{x}S`, `PS`, `PevS`},
		{`{x}`, `+`, `PS`, `P{x}S`, `P{x}S`},
		{`{`, `:-`, `P{S`, `P{S`, `PevS`},
		{`{`, `:+`, `PS`, `PS`, `P{S`},
		{`{`, `-`, `P{S`, `PS`, `PevS`},
		{`{`, `+`, `PS`, `P{S`, `P{S`},
		{`}`, `:-`, `P}S`, `P}S`, `Pev}S`},
		{`}`, `:+`, `P}S`, `P}S`, `P}S`},
		{`}`, `-`, `P}S`, `P}S`, `Pev}S`},
		{`}`, `+`, `P}S`, `P}S`, `P}S`},
		{`{x}y`, `:-`, `P{x}yS`, `P{x}yS`, `PevS`},
		{`{x}y`, `:+`, `PS`, `PS`, `P{x}yS`},
		{`{x}y`, `-`, `P{x}yS`, `PS`, `PevS`},
		{`{x}y`, `+`, `PS`, `P{x}yS`, `P{x}yS`},
		{`a{b`, `:-`, `Pa{bS`, `Pa{bS`, `PevS`},
		{`a{b`, `:+`, `PS`, `PS`, `Pa{bS`},
		{`a{b`, `-`, `Pa{bS`, `PS`, `PevS`},
		{`a{b`, `+`, `PS`, `Pa{bS`, `Pa{bS`},
		{`{a}{b}`, `:-`, `P{a}{b}S`, `P{a}{b}S`, `PevS`},
		{`{a}{b}`, `:+`, `PS`, `PS`, `P{a}{b}S`},
		{`{a}{b}`, `-`, `P{a}{b}S`, `PS`, `PevS`},
		{`{a}{b}`, `+`, `PS`, `P{a}{b}S`, `P{a}{b}S`},
		{`{{x}}`, `:-`, `P{{x}}S`, `P{{x}}S`, `Pev}S`},
		{`{{x}}`, `:+`, `P}S`, `P}S`, `P{{x}}S`},
		{`{{x}}`, `-`, `P{{x}}S`, `P}S`, `Pev}S`},
		{`{{x}}`, `+`, `P}S`, `P{{x}}S`, `P{{x}}S`},
		{`\}`, `:-`, `P\}S`, `P\}S`, `Pev}S`},
		{`\}`, `:+`, `P}S`, `P}S`, `P\}S`},
		{`\}`, `-`, `P\}S`, `P}S`, `Pev}S`},
		{`\}`, `+`, `P}S`, `P\}S`, `P\}S`},
		{`\{x\}`, `:-`, `P\{x\}S`, `P\{x\}S`, `PevS`},
		{`\{x\}`, `:+`, `PS`, `PS`, `P\{x\}S`},
		{`\{x\}`, `-`, `P\{x\}S`, `PS`, `PevS`},
		{`\{x\}`, `+`, `PS`, `P\{x\}S`, `P\{x\}S`},
		{`"{x}"`, `:-`, `P"{x}"S`, `P"{x}"S`, `PevS`},
		{`"{x}"`, `:+`, `PS`, `PS`, `P"{x}"S`},
		{`"{x}"`, `-`, `P"{x}"S`, `PS`, `PevS`},
		{`"{x}"`, `+`, `PS`, `P"{x}"S`, `P"{x}"S`},
		{`${B}`, `:-`, `PbvS`, `PbvS`, `PevS`},
		{`${B}`, `:+`, `PS`, `PS`, `PbvS`},
		{`${B}`, `-`, `PbvS`, `PS`, `PevS`},
		{`${B}`, `+`, `PS`, `PbvS`, `PbvS`},
		{`${B:-{x}}`, `:-`, `PbvS`, `PbvS`, `PevS`},
		{`${B:-{x}}`, `:+`, `PS`, `PS`, `PbvS`},
		{`${B:-{x}}`, `-`, `PbvS`, `PS`, `PevS`},
		{`${B:-{x}}`, `+`, `PS`, `PbvS`, `PbvS`},
		{`$${x}`, `:-`, `P${x}S`, `P${x}S`, `PevS`},
		{`$${x}`, `:+`, `PS`, `PS`, `P${x}S`},
		{`$${x}`, `-`, `P${x}S`, `PS`, `PevS`},
		{`$${x}`, `+`, `PS`, `P${x}S`, `P${x}S`},
		{`{${B}}`, `:-`, `P{bv}S`, `P{bv}S`, `PevS`},
		{`{${B}}`, `:+`, `PS`, `PS`, `P{bv}S`},
		{`{${B}}`, `-`, `P{bv}S`, `PS`, `PevS`},
		{`{${B}}`, `+`, `PS`, `P{bv}S`, `P{bv}S`},
		{`{"a":1}`, `:-`, `P{"a":1}S`, `P{"a":1}S`, `PevS`},
		{`{"a":1}`, `:+`, `PS`, `PS`, `P{"a":1}S`},
		{`{"a":1}`, `-`, `P{"a":1}S`, `PS`, `PevS`},
		{`{"a":1}`, `+`, `PS`, `P{"a":1}S`, `P{"a":1}S`},
		{`{}`, `:-`, `P{}S`, `P{}S`, `PevS`},
		{`{}`, `:+`, `PS`, `PS`, `P{}S`},
		{`{}`, `-`, `P{}S`, `PS`, `PevS`},
		{`{}`, `+`, `PS`, `P{}S`, `P{}S`},
		{`{{{x}}}`, `:-`, `P{{{x}}}S`, `P{{{x}}}S`, `Pev}S`},
		{`{{{x}}}`, `:+`, `P}S`, `P}S`, `P{{{x}}}S`},
		{`{x}{y}{z}`, `:-`, `P{x}{y}{z}S`, `P{x}{y}{z}S`, `PevS`},
		{`{x}{y}{z}`, `:+`, `PS`, `PS`, `P{x}{y}{z}S`},
		{`{{x}{y}}`, `:-`, `P{{x}{y}}S`, `P{{x}{y}}S`, `Pev}S`},
		{`{{x}{y}}`, `:+`, `P}S`, `P}S`, `P{{x}{y}}S`},
		{`a{{b}}c`, `:-`, `Pa{{b}c}S`, `Pa{{b}c}S`, `Pevc}S`},
		{`a{{b}}c`, `:+`, `Pc}S`, `Pc}S`, `Pa{{b}c}S`},
		{`{{}}`, `:-`, `P{{}}S`, `P{{}}S`, `Pev}S`},
		{`{{}}`, `:+`, `P}S`, `P}S`, `P{{}}S`},
		{`{ {x} }`, `:-`, `P{ {x} }S`, `P{ {x} }S`, `PevS`},
		{`{ {x} }`, `:+`, `PS`, `PS`, `P{ {x} }S`},
		{`x{{y}}`, `:-`, `Px{{y}}S`, `Px{{y}}S`, `Pev}S`},
		{`x{{y}}`, `:+`, `P}S`, `P}S`, `Px{{y}}S`},
		{`{{x}`, `:-`, `P{{x}S`, `P{{x}S`, `PevS`},
		{`{{x}`, `:+`, `PS`, `PS`, `P{{x}S`},
		{`{x}}`, `:-`, `P{x}}S`, `P{x}}S`, `Pev}S`},
		{`{x}}`, `:+`, `P}S`, `P}S`, `P{x}}S`},
		{`{x}}}`, `:-`, `P{x}}}S`, `P{x}}}S`, `Pev}}S`},
		{`{x}}}`, `:+`, `P}}S`, `P}}S`, `P{x}}}S`},
		{`}{`, `:-`, `P{}S`, `P{}S`, `Pev{}S`},
		{`}{`, `:+`, `P{}S`, `P{}S`, `P{}S`},
		{`{}{`, `:-`, `P{}{S`, `P{}{S`, `PevS`},
		{`{}{`, `:+`, `PS`, `PS`, `P{}{S`},
		{`{{`, `:-`, `P{{S`, `P{{S`, `PevS`},
		{`{{`, `:+`, `PS`, `PS`, `P{{S`},
		{`}}`, `:-`, `P}}S`, `P}}S`, `Pev}}S`},
		{`}}`, `:+`, `P}}S`, `P}}S`, `P}}S`},
		{`{{{`, `:-`, `P{{{S`, `P{{{S`, `PevS`},
		{`{{{`, `:+`, `PS`, `PS`, `P{{{S`},
		{`{x}{`, `:-`, `P{x}{S`, `P{x}{S`, `PevS`},
		{`{x}{`, `:+`, `PS`, `PS`, `P{x}{S`},
		{`{{x}}}`, `:-`, `P{{x}}}S`, `P{{x}}}S`, `Pev}}S`},
		{`{{x}}}`, `:+`, `P}}S`, `P}}S`, `P{{x}}}S`},
		{`{{x}}{y}`, `:-`, `P{{x}{y}}S`, `P{{x}{y}}S`, `Pev{y}}S`},
		{`{{x}}{y}`, `:+`, `P{y}}S`, `P{y}}S`, `P{{x}{y}}S`},
		{`{x}{{y}}`, `:-`, `P{x}{{y}}S`, `P{x}{{y}}S`, `Pev}S`},
		{`{x}{{y}}`, `:+`, `P}S`, `P}S`, `P{x}{{y}}S`},
		{`{x{y}z}`, `:-`, `P{x{y}z}S`, `P{x{y}z}S`, `PevS`},
		{`{x{y}z}`, `:+`, `PS`, `PS`, `P{x{y}z}S`},
		{`{}}x`, `:-`, `P{}}xS`, `P{}}xS`, `PevS`},
		{`{}}x`, `:+`, `PS`, `PS`, `P{}}xS`},
		{`{}x`, `:-`, `P{}xS`, `P{}xS`, `PevS`},
		{`{}x`, `:+`, `PS`, `PS`, `P{}xS`},
		{`a{}b`, `:-`, `Pa{}bS`, `Pa{}bS`, `PevS`},
		{`a{}b`, `:+`, `PS`, `PS`, `Pa{}bS`},
		{`{}}`, `:-`, `P{}}S`, `P{}}S`, `PevS`},
		{`{}}`, `:+`, `PS`, `PS`, `P{}}S`},
		{`{{{x}`, `:-`, `P{{{x}S`, `P{{{x}S`, `PevS`},
		{`{{{x}`, `:+`, `PS`, `PS`, `P{{{x}S`},
		{`{{{{x}`, `:-`, `P{{{{x}S`, `P{{{{x}S`, `PevS`},
		{`{{{{x}`, `:+`, `PS`, `PS`, `P{{{{x}S`},
		{`{x`, `:-`, `P{xS`, `P{xS`, `PevS`},
		{`{x`, `:+`, `PS`, `PS`, `P{xS`},
		{`{{x`, `:-`, `P{{xS`, `P{{xS`, `PevS`},
		{`{{x`, `:+`, `PS`, `PS`, `P{{xS`},
		{`x{`, `:-`, `Px{S`, `Px{S`, `PevS`},
		{`x{`, `:+`, `PS`, `PS`, `Px{S`},
		{`{}{}`, `:-`, `P{}{}S`, `P{}{}S`, `PevS`},
		{`{}{}`, `:+`, `PS`, `PS`, `P{}{}S`},
		{`{}{}}`, `:-`, `P{}{}}S`, `P{}{}}S`, `PevS`},
		{`{}{}}`, `:+`, `PS`, `PS`, `P{}{}}S`},
		{`}x{`, `:-`, `Px{}S`, `Px{}S`, `Pevx{}S`},
		{`}x{`, `:+`, `Px{}S`, `Px{}S`, `Px{}S`},
		{`{y}{}}z`, `:-`, `P{y}{}}zS`, `P{y}{}}zS`, `PevS`},
		{`{y}{}}z`, `:+`, `PS`, `PS`, `P{y}{}}zS`},
	} {
		tmpl := "P${E" + tc.op + tc.word + "}S"
		for _, st := range []struct {
			name, val string
			set       bool
			want      string
		}{
			{"unset", "", false, tc.unset},
			{"empty", "", true, tc.empty},
			{"set", "ev", true, tc.set},
		} {
			t.Run(tmpl+" with E "+st.name, func(t *testing.T) {
				lookup := func(name string) (string, bool) {
					switch name {
					case "E":
						return st.val, st.set
					case "B":
						return "bv", true
					}
					return "", false
				}
				got, err := interpolate([]byte(tmpl), lookup)
				if err != nil {
					t.Fatalf("interpolate(%q) = %v; docker compose gives %q", tmpl, err, st.want)
				}
				if string(got) != st.want {
					t.Errorf("interpolate(%q) with E %s = %q, want %q (docker compose v5.5.1)", tmpl, st.name, got, st.want)
				}
			})
		}
	}
}

// The count that never gets back to zero takes the last `}` of the value, and a second reference
// in that value is inside it: docker compose refuses (when the word is used) `V: 'P${E:-{S} ${B:-b}'` as an invalid
// interpolation format (measured, v5.5.1, B set or not), and so does this — where main read the
// two references apart and gave `P{S b`.
func TestAnOpenBraceSwallowsTheReferenceAfterIt(t *testing.T) {
	lookup := func(name string) (string, bool) { return "bv", name == "B" }
	doc := "a: 'P${E:-{S} ${B:-b}'\n"
	if got, err := documentValues(doc, lookup); err == nil {
		t.Errorf("interpolateDocument(%q) = %v, want the reference refused, as docker compose refuses it", doc, got)
	}
	// Refused when the word is used: with E set it is not, and the value is the variable's, as in
	// docker compose (`Pev`).
	set := func(name string) (string, bool) { return "ev", name == "E" }
	if got, err := documentValues(doc, set); err != nil || got["a"] != "Pev" {
		t.Errorf("interpolateDocument(%q) with E set = %v, %v; want Pev (docker compose v5.5.1)", doc, got, err)
	}
}

// documentValues expands doc as a compose file is (the way a reference's YAML value is known) and
// reads the top-level scalars back.
func documentValues(doc string, lookup varLookup) (map[string]string, error) {
	d, err := interpolateDocument([]byte(doc), lookup)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := d.into(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// A reference written across lines with a `\` continuation is still one reference, and one with
// no `}` on its own line is closed as it was before the word's own `{` counted. The rows with E
// set tell the two closings apart: a word that has a `{` of its own and a continuation closes
// one `}` later than the old count did (`ev`, not `ev}`).
func TestAReferenceAcrossLinesIsStillClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want map[string]string
		setE bool
	}{
		"a continuation, the word has a brace pair": {"a: \"${E:-\\\n   {x}}\"\n", map[string]string{"a": "{x}"}, false},
		"a continuation after the word's own {":     {"a: \"${E:-{\\\n   x}}\"\n", map[string]string{"a": "ev"}, true},
		"the same, written with CRLF":               {"a: \"${E:-{\\\r\n   x}}\"\r\n", map[string]string{"a": "ev"}, true},
		"a continuation before the closing }":       {"a: \"${E:-x\\\n   y}\"\n", map[string]string{"a": "xy"}, false},
		"no } on the first line, no continuation":   {"a: \"${E:-x\n   y}\"\n", map[string]string{"a": "x y"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			lookup := func(n string) (string, bool) { return "ev", tc.setE && n == "E" }
			got, err := documentValues(tc.doc, lookup)
			// Where the lines fold is a difference of its own (a default's line break is kept, as
			// before): what is compared is where the reference closed.
			for k, v := range got {
				got[k] = strings.Join(strings.Fields(v), " ")
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("interpolateDocument(%q) = %v, %v; want %v", tc.doc, got, err, tc.want)
			}
		})
	}
}

// The count is made inside the YAML value that holds the reference, as docker compose makes it
// (after parsing): a `}` after the closing quote, in a comment, or of a flow mapping is not
// the word's, and a value that goes on over several lines is one value. Every row is what
// docker compose v5.5.1 printed for the same file (`config --format json`); the first
// version of the rule, which read to the last `}` of the line, broke the first nine of them.
func TestTheWordIsReadInsideItsOwnYAMLValue(t *testing.T) {
	const head = "services:\n  a:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		env        map[string]string
		want       map[string]string
	}{
		{"a flow mapping, quoted, {} default", "    environment: {CFG: \"${CFG:-{}}\"}\n", nil, map[string]string{"CFG": "{}"}},
		{"the same with CFG set", "    environment: {CFG: \"${CFG:-{}}\"}\n", map[string]string{"CFG": "ev"}, map[string]string{"CFG": "ev"}},
		{"a flow mapping, a word whose { is not closed", "    environment: {A: \"${X:-{y}\"}\n", nil, map[string]string{"A": "{y"}},
		{"a flow mapping with two keys", "    environment: {A: \"${E:-{x}\", B: \"${B:-y}\"}\n", nil, map[string]string{"A": "{x", "B": "y"}},
		{"a quoted value and a comment with braces", "    environment:\n      CFG: \"${CFG:-{}}\"  # see {docs}\n", nil, map[string]string{"CFG": "{}"}},
		{"a quoted value and a comment with a }", "    environment:\n      CFG: \"${CFG:-{}}\"  # default is }\n", nil, map[string]string{"CFG": "{}"}},
		{"a plain value and a comment with braces", "    environment:\n      CFG: ${CFG:-x{y}}  # see {docs}\n", nil, map[string]string{"CFG": "x{y}"}},
		{"the same with CFG set", "    environment:\n      CFG: ${CFG:-{}}  # see {docs}\n", map[string]string{"CFG": "ev"}, map[string]string{"CFG": "ev"}},
		{"a reference written in a comment, open", "    environment:\n      V: v  # e.g. ${E:-{x} or ${B:-y}\n", nil, map[string]string{"V": "v"}},
		{"the same with A set", "    environment:\n      V: \"${A:-${B}\n        rest}\"\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "av"}},
		{"a JSON default over two lines, A set", "    environment:\n      V: \"${A:-{\\\"a\\\": 1,\n        \\\"b\\\": 2}}\"\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"a JSON default in single quotes, A set", "    environment:\n      V: '${A:-{\"a\":1}}'\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"{} in single quotes, A set", "    environment:\n      V: '${A:-{}}'\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"{} plain, A set", "    environment:\n      V: ${A:-{}}\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"a tag before the quoted value, A set", "    environment:\n      V: !!str \"${A:-{x}}\"\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"an anchor before the quoted value, A set", "    environment:\n      V: &v \"${A:-{x}}\"\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"a quote in the middle of a plain value is not a quote", "    environment:\n      V: it's ${A:-{x}} and}\n", map[string]string{"A": "av"}, map[string]string{"V": "it's av and}"}},
		{"a doubled quote in a single-quoted value", "    environment:\n      V: 'it''s ${A:-{x}} and}'\n", map[string]string{"A": "av"}, map[string]string{"V": "it's av and}"}},
		{"an escaped quote in a double-quoted value", "    environment:\n      V: \"say \\\"hi\\\" ${A:-{x}} and}\"\n", map[string]string{"A": "av"}, map[string]string{"V": "say \"hi\" av and}"}},
		{"a flow mapping with an escaped quote before the reference, A set", "    environment: {V: \"say \\\"hi\\\" ${A:-{}}\", W: \"1\"}\n", map[string]string{"A": "av"}, map[string]string{"V": "say \"hi\" av", "W": "1"}},
		{"a flow mapping with a doubled quote before the reference, A set", "    environment: {V: 'it''s ${A:-{}}', W: '1'}\n", map[string]string{"A": "av"}, map[string]string{"V": "it's av", "W": "1"}},
		{"a plain value and a comment ending in }, A set", "    environment:\n      CFG: ${A:-{}}  # default is }\n", map[string]string{"A": "av"}, map[string]string{"CFG": "av"}},
		{"a flow mapping with a tag before the quoted value, A set", "    environment: {V: !!str \"${A:-{}}\", W: \"1\"}\n", map[string]string{"A": "av"}, map[string]string{"V": "av", "W": "1"}},
		{"a doubled quote after the reference in a single-quoted value, A set", "    environment:\n      V: '${A:-{}} it''s}'\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"a plain value, A unset, and a comment ending in }", "    environment:\n      CFG: ${A:-a{}}  # default is }\n", nil, map[string]string{"CFG": "a{}"}},
		{"a nested reference over two plain lines, A set", "    environment:\n      V: ${A:-${B}\n        rest}\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "av"}},
		{"a nested reference in a folded block, A set", "    environment:\n      V: >\n        ${A:-${B}\n        rest}\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "av\n"}},
		{"a nested reference and a # in a literal block, A set", "    environment:\n      V: |\n        echo ${A:-${B} # note}\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "echo av\n"}},
		{"a nested reference and a quote in a literal block, A set", "    environment:\n      V: |\n        echo x: \"${A:-${B}\" more}\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "echo x: \"av\n"}},
		{"a [ in a plain value is text, not a flow sequence", "    environment:\n      V: a[ ${A:-{}} # }\n", map[string]string{"A": "av"}, map[string]string{"V": "a[ av"}},
		{"a quote in the middle of a plain value, A set", "    environment:\n      V: x\"${A:-{}}\" }\n", map[string]string{"A": "av"}, map[string]string{"V": "x\"av"}},
		{"a # inside a plain word is not a comment", "    environment:\n      V: a#b${A:-{}}c}\n", map[string]string{"A": "av"}, map[string]string{"V": "a#bav"}},
		{"a # after a slash is not a comment", "    environment:\n      V: http://h/#${A:-{x}}\n", map[string]string{"A": "av"}, map[string]string{"V": "http://h/#av"}},
		{"an anchor before the quoted value, and a comment", "    environment:\n      V: &a \"${A:-{}} # }\"\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}},
		{"a tab before the comment", "    environment:\n      V: ${A:-a{}}\t# }\n", nil, map[string]string{"V": "a{}"}},
		{"a later { in the file does not make a nested reference a braced one", "    environment:\n      V: ${A:-${B}\n        rest}\n      W: \"{x}\"\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "av", "W": "{x}"}},
		// A quote opens a scalar after `{` as it does after `:` (#1708): the word ends where the quote does,
		// so a `}` or a ` #` in the rest of the quote is the word's and not a comment's or a mapping's.
		{"a quoted key right after `{` of a flow mapping, a `, ` inside it, and the mapping's `}` after the value", "    environment: {\"x, \": \"${E:-{x}\"}\n", nil, map[string]string{"x, ": "{x"}},
		{"a double-quoted value ending in an escaped backslash, then another quoted value", "    environment: {V: \"a\\\\\", W: \"${E:-{x}\"}\n", nil, map[string]string{"V": "a\\", "W": "{x"}},
		{"a tag, a tab, then the quoted value with a ` #` inside", "    environment:\n      V: !!str\t\"${E:-{x} # }\"\n", nil, map[string]string{"V": "{x} # "}},
		{"a comment after a tab", "    environment:\n      V: v\t# ${E:-{S} ${B:-b}\n", nil, map[string]string{"V": "v"}},
		{"a comment line at column 0", "    environment:\n# e.g. ${E:-{S} ${B:-b}\n      V: v\n", nil, map[string]string{"V": "v"}},
		{"a ] and a [ before the reference are text", "    environment:\n      V: a] - [ ${E:-{}} # }\n", map[string]string{"E": "ev"}, map[string]string{"V": "a] - [ ev"}},
		{"CR as the only line break, a comment line before the keys", "    environment:\r      # top}\r      V: 'P${E:-{S}'\r      W: 'plain}'\r", nil, map[string]string{"V": "P{S", "W": "plain}"}},
		{"CR as the only line break, a comment line with a } before the value, E set", "    environment:\r      # top}\r      V: 'P${E:-{}}'\r      W: 'plain}'\r", map[string]string{"E": "ev"}, map[string]string{"V": "Pev", "W": "plain}"}},
		{"a quote that a literal block opens and never closes (single), A set", "    environment:\n      V: |\n        echo x: '${A:-{}} more\n      W: 'z}'\n", map[string]string{"A": "av"}, map[string]string{"V": "echo x: 'av more\n", "W": "z}"}},
		{"CR as the only line break, a plain value, A set", "    environment:\r      V: ${A:-{}}\r      W: 'plain}'\r", map[string]string{"A": "av"}, map[string]string{"V": "av", "W": "plain}"}},
		{"CR as the only line break", "    environment:\r      V: 'P${E:-{S}'\r      W: 'plain}'\r", nil, map[string]string{"V": "P{S", "W": "plain}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(n string) (string, bool) { v, ok := tc.env[n]; return v, ok }
			doc := strings.ReplaceAll(head, "\n", "\n")
			if strings.Contains(tc.body, "\r") {
				doc = strings.ReplaceAll(head, "\n", "\r")
			}
			d, err := interpolateDocument([]byte(doc+tc.body), lookup)
			if err != nil {
				t.Fatalf("interpolateDocument: %v", err)
			}
			var parsed struct {
				Services map[string]struct {
					Environment map[string]string `yaml:"environment"`
				} `yaml:"services"`
			}
			if err := d.into(&parsed); err != nil {
				t.Fatalf("the interpolated document does not read back: %v\n%s", err, d.raw)
			}
			if env := parsed.Services["a"].Environment; !reflect.DeepEqual(env, tc.want) {
				t.Errorf("environment = %v, want %v (docker compose v5.5.1)\n%s", env, tc.want, d.raw)
			}
		})
	}
}

// Text that is not a YAML document — a `.env` value, a default read again — is one value: nothing
// in it is a quote or a comment, and the last `}` is the value's own. The values are what docker
// compose v5.5.1 gave for the same text as a quoted `.env` value (`A="…"`), X unset or `xv`.
func TestTextThatIsNotAYAMLDocumentIsOneValue(t *testing.T) {
	for _, tc := range []struct {
		name, text, unset, set string
	}{
		{"a } and a comment mark in the value", `${X:-{}} # }`, `{}} # `, `xv`},
		{"a quote and a } after the reference", `x: '${X:-{}}' }`, `x: '{}}' `, `x: 'xv`},
	} {
		for _, st := range []struct {
			name string
			set  bool
			want string
		}{{"unset", false, tc.unset}, {"set", true, tc.set}} {
			t.Run(tc.name+", X "+st.name, func(t *testing.T) {
				lookup := func(n string) (string, bool) { return "xv", st.set && n == "X" }
				got, err := interpolate([]byte(tc.text), lookup)
				if err != nil || string(got) != st.want {
					t.Errorf("interpolate(%q) = %q, %v; want %q (docker compose v5.5.1)", tc.text, got, err, st.want)
				}
			})
		}
	}
}

// A quote begins a scalar after `,`, `[`, `-` and at the start of a line, as after `:` — each of
// the shapes below puts the reference in a quoted item whose closing quote is what ends the
// value (docker compose v5.5.1: `av` for each, with A set).
func TestTheWordInASequenceItemIsReadInsideItsQuotes(t *testing.T) {
	const head = "services:\n  a:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"after a comma in a flow sequence", "    command: [a, \"${A:-{}}\"]\n", []string{"a", "av"}},
		{"right after the [", "    command: [\"${A:-{}}\"]\n", []string{"av"}},
		{"after a comma in a flow sequence, and a } item after it", "    command: [a, \"${A:-{}}\", \"}\"]\n", []string{"a", "av", "}"}},
		{"right after the [, and a } item after it", "    command: [\"${A:-{}}\", \"}\"]\n", []string{"av", "}"}},
		{"after the - of a block sequence, and a comment", "    command:\n      - \"${A:-{}} # }\"\n", []string{"av"}},
		{"at the start of a line in a flow sequence", "    command: [\n      \"${A:-{}} # }\" ]\n", []string{"av"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(n string) (string, bool) { return "av", n == "A" }
			d, err := interpolateDocument([]byte(head+tc.body), lookup)
			if err != nil {
				t.Fatalf("interpolateDocument: %v", err)
			}
			var parsed struct {
				Services map[string]struct {
					Command []string `yaml:"command"`
				} `yaml:"services"`
			}
			if err := d.into(&parsed); err != nil {
				t.Fatalf("does not read back: %v\n%s", err, d.raw)
			}
			if got := parsed.Services["a"].Command; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("command = %v, want %v\n%s", got, tc.want, d.raw)
			}
		})
	}
}

// A reference whose word has no `{` of its own is read exactly as it was before the rule: whatever
// else is in the text — quotes, comments, line breaks, a `}` further on. A fixed-seed generator
// writes tens of thousands of fragments from the characters that mattered in the cases above, keeps
// those whose reference has no bare `{`, and checks both ways of reading (as a document and as
// one value) against the old count.
func TestAReferenceWithNoBraceOfItsOwnIsReadAsBefore(t *testing.T) {
	// The `{` and the `${}` are in it so that a bare `{` turns up after a reference (outside its word) and a
	// `${` meets the `}` right after it (#1708): the shapes in which reading the value's extent would move the
	// closing of a word with no `{` of its own.
	alphabet := []string{"${", "${", "}", "}", "$", "a", "B", ":-", ":+", "x", " ", " # ", "'", "\"", "\\", "\n", "\r\n", ": ", "- ", "[", "]", ",", "!!str ", "&a ", "{", "${}", "${A:-"}
	seed := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 { // xorshift: no dependence on a library's sequence
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	checked := 0
	for n := 0; n < 80000; n++ {
		var b strings.Builder
		b.WriteString("v: ")
		for k := 0; k < 2+int(next()%10); k++ {
			b.WriteString(alphabet[next()%uint64(len(alphabet))])
		}
		text := b.String()
		from := strings.Index(text, "${")
		if from < 0 {
			continue
		}
		from += 2
		old := matchBraceAcrossLines(text[from:])
		// Not the implementation's own hasBareBrace (the gate under test): a `{` that is not the one of a `${`.
		if old < 0 || strings.Contains(strings.ReplaceAll(text[from:from+old], "${", ""), "{") {
			continue
		}
		checked++
		for _, inDocument := range []bool{true, false} {
			if got := matchBrace(text, from, inDocument); got != old {
				t.Fatalf("matchBrace(%q, %d, %v) = %d, want %d (the old count): a word with no `{` of its own is read as before", text, from, inDocument, got, old)
			}
		}
	}
	if checked < 2000 {
		t.Fatalf("only %d fragments had a reference with no bare `{`; the generator has stopped covering the case", checked)
	}
}

// Where the value runs over several lines and the word has a `{` of its own, the extent of the value
// is not read (a plain, folded or literal value that goes on after the reference's line, or a quote
// opened on an earlier line), and the reference is closed as it always was. That is not what docker
// compose gives, and it is not worse than before: each row says what docker compose v5.5.1 gives
// and holds what opossum gave before this rule, so that a change to either shows.
func TestWhereTheValueCannotBeReadTheReferenceIsClosedAsBefore(t *testing.T) {
	const head = "services:\n  a:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		env        map[string]string
		docker     map[string]string // docker compose v5.5.1; not asserted
		want       map[string]string // opossum, as before
	}{
		{"a word whose { is not closed, no } on its line", "    environment:\n      V: ${A:-{x\n        y}}\n", map[string]string{"A": "av"}, map[string]string{"V": "av"}, map[string]string{"V": "av}"}},
		{"a nested reference and a word with a { of its own over two lines", "    environment:\n      V: ${A:-${B}\n        {x}}\n", map[string]string{"A": "av", "B": "bv"}, map[string]string{"V": "av"}, map[string]string{"V": "av}"}},
		{"a single quote that a literal block opens and nothing closes", "    environment:\n      V: |\n        echo x: '${A:-{}} more\n      W: z}\n", map[string]string{"A": "av"},
			map[string]string{"V": "echo x: 'av more\n", "W": "z}"}, map[string]string{"V": "echo x: 'av} more\n", "W": "z}"}},
		{"a quote that a literal block never closes", "    environment:\n      V: |\n        echo x: \"${A:-{}} more\n      W: 'z}'\n", map[string]string{"A": "av"},
			map[string]string{"V": "echo x: \"av more\n", "W": "z}"}, map[string]string{"V": "echo x: \"av} more\n", "W": "z}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(n string) (string, bool) { v, ok := tc.env[n]; return v, ok }
			d, err := interpolateDocument([]byte(head+tc.body), lookup)
			if err != nil {
				t.Fatalf("interpolateDocument: %v", err)
			}
			var parsed struct {
				Services map[string]struct {
					Environment map[string]string `yaml:"environment"`
				} `yaml:"services"`
			}
			if err := d.into(&parsed); err != nil {
				t.Fatalf("does not read back: %v\n%s", err, d.raw)
			}
			if env := parsed.Services["a"].Environment; !reflect.DeepEqual(env, tc.want) {
				t.Errorf("environment = %v, want %v (as before; docker compose gives %v)", env, tc.want, tc.docker)
			}
		})
	}
}

// Where the value is a plain scalar over several lines, or a quote opened on an earlier line, the extent of it is read
// from the reference's own line only. A word whose braces do not balance on that line is closed at the last `}` of
// it — not at the first `}` that balances the `${` openings, as the rows above say of the rows that go past the line,
// and not where docker compose closes it, which reads the folded value (#1707; a docs sentence said "as before" of
// this, which it is not: the value is `ev }}` here and was `ev} }}` before the rule, and docker compose gives `ev}`).
// Each row holds what opossum gives and says what docker compose v5.5.1 gives (not asserted), so that a change to
// either shows.
func TestAWordWhoseBracesDoNotBalanceOnTheLineOfAMultiLineValueIsClosedAtTheLastBraceOfTheLine(t *testing.T) {
	const head = "services:\n  a:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		docker     map[string]string // docker compose v5.5.1; not asserted
		want       map[string]string // opossum
	}{
		{"a plain value over two lines, three { and two } on the line", "    environment:\n      V: a\n       ${E:-{{{x}}\n       }}\n",
			map[string]string{"V": "a ev}"}, map[string]string{"V": "a ev }}"}},
		{"a quote opened on the line before", "    environment:\n      V: \"a\n       b: '${E:-{{{x}}' }}\"\n",
			map[string]string{"V": "a b: 'ev}"}, map[string]string{"V": "a b: 'ev' }}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(n string) (string, bool) { return "ev", n == "E" }
			d, err := interpolateDocument([]byte(head+tc.body), lookup)
			if err != nil {
				t.Fatalf("interpolateDocument: %v", err)
			}
			var parsed struct {
				Services map[string]struct {
					Environment map[string]string `yaml:"environment"`
				} `yaml:"services"`
			}
			if err := d.into(&parsed); err != nil {
				t.Fatalf("does not read back: %v\n%s", err, d.raw)
			}
			if env := parsed.Services["a"].Environment; !reflect.DeepEqual(env, tc.want) {
				t.Errorf("environment = %v, want %v (docker compose gives %v)", env, tc.want, tc.docker)
			}
		})
	}
}

// The gate is what keeps a word with no `{` of its own as it was: the value's extent is not read for
// it, and the `{` of a nested `${` is not counted as the word's own (#1708). Each row gives the index
// the old count (the `}` that balances the `${` openings) closes at; reading the value as a word with
// braces is, for the first rows, a different index (`${A:-${}} x}` closes at 6, and at 9 when the
// `{` of the `${` is counted as an opening and the `}` after it skipped).
func TestAWordWithNoBraceOfItsOwnIsClosedWhereTheOldCountClosesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string // the reference's text, from its `${`
		want int    // where its `}` is, counted from after the `${`
	}{
		{"a nested reference whose `{` meets a `}`", "v: ${A:-${}} x}", 6},
		{"the same with a bare `{` further on, outside the reference", "v: ${A:-${}} x} {y}", 6},
		{"the same with a bare `{` on a later line", "v: ${A:-${}} x}\nw: {y}", 6},
		{"a nested reference whose `{` meets a `{`", "v: ${A:-${{}} x}", 7},
	} {
		from := strings.Index(tc.text, "${") + 2
		for _, inDocument := range []bool{true, false} {
			if got := matchBrace(tc.text, from, inDocument); got != tc.want {
				t.Errorf("%s: matchBrace(%q, %d, %v) = %d, want %d", tc.name, tc.text, from, inDocument, got, tc.want)
			}
		}
	}
}

// A quote opens a scalar after `?` (a complex key) as it does after `:` (#1708). A key is not
// expanded as a value is, so the reading is asked for directly: the word of `? "${E:-{x} # }"` closes at
// the `}` before the closing quote, where, if the quote were not one, the ` #` would end the value there.
func TestAQuoteAfterAQuestionMarkOpensAScalar(t *testing.T) {
	const text = "? \"${E:-{x} # }\""
	from := strings.Index(text, "${") + 2
	want := strings.LastIndex(text, "}") - from
	if got := matchBrace(text, from, true); got != want {
		t.Errorf("matchBrace(%q, %d, true) = %d, want %d (the `}` before the closing quote)", text, from, got, want)
	}
}

// And a word that does begin with a `{` (`${{x}} y}`) has one of its own: it is the word's `{`, not the
// `${`'s, so the count that reads the value takes it (the `}`s close at 3, where the old count stopped at 2).
func TestAWordThatBeginsWithABraceHasOneOfItsOwn(t *testing.T) {
	const text = "v: ${{x}} y}"
	from := strings.Index(text, "${") + 2
	for _, inDocument := range []bool{true, false} {
		if got := matchBrace(text, from, inDocument); got != 3 {
			t.Errorf("matchBrace(%q, %d, %v) = %d, want 3", text, from, inDocument, got)
		}
	}
}
