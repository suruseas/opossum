package compose

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The word of a reference written inside a YAML double-quoted scalar is read as YAML reads it
// (#1706). docker compose reads the YAML first and interpolates the string that came of it, so
// `"p=${CFG:-{\"a\":1}}"` has the word `{"a":1}`; here the file is interpolated before it is
// parsed, and the word was kept as written (`p={\"a\":1}`). Every row is a word written under
// `command: ["p=${CFG:-<word>}"]` with CFG unset, and what docker compose v5.5.1 printed for it
// (`config --format json`): the value, or a refusal (go-yaml's `found unknown escape character`,
// `did not find expected hexadecimal number`, `found unexpected end of stream`, or the
// interpolation's own `invalid interpolation format` for a word with a line break).
func TestTheWordOfAReferenceInDoubleQuotesIsReadAsYAMLReadsIt(t *testing.T) {
	for _, tc := range []struct {
		word    string
		want    string
		refused bool
	}{
		{"\\\"q\\\"", "p=\"q\"", false},
		{"x\\\\y", "p=x\\y", false},
		{"a\\nb", "", true},
		{"a\\tb", "p=a\tb", false},
		{"\\x41", "p=A", false},
		{"\\u00e9", "p=é", false},
		{"\\U0001F600", "p=😀", false},
		{"\\/", "", true},
		{"\\_", "p= ", false},
		{"\\N", "p=", false},
		{"\\L", "p= ", false},
		{"\\P", "p= ", false},
		{"\\0", "p=\u0000", false},
		{"\\a", "p=\u0007", false},
		{"\\b", "p=\b", false},
		{"\\e", "p=\u001b", false},
		{"\\f", "p=\f", false},
		{"\\v", "p=\u000b", false},
		{"\\r", "p=\r", false},
		{"\\ ", "p= ", false},
		{"\\q", "", true},
		{"\\x4", "", true},
		{"\\u12", "", true},
		{"\\$", "", true},
		{"é", "p=é", false},
		{"back\\", "", true},
		{"{\\\"a\\\":1}", "p={\"a\":1}", false},
		{"${B:-\\\"z\\\"}", "p=\"z\"", false},
		{"\\\\\\\"", "p=\\\"", false},
		{"\\\\\\\\", "p=\\\\", false},
		{"a\\\\", "p=a\\", false},
		{"\\\"", "p=\"", false},
		{"\"", "", true},
		{"'", "p='", false},
		{"\\x00", "p=\u0000", false},
		{"\\u0000", "p=\u0000", false},
		{"\\xZZ", "", true},
		{"\\\t", "p=\t", false},
	} {
		t.Run(tc.word, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n    command: [\"p=${CFG:-"+tc.word+"}\"]\n"))
			if tc.refused {
				if err == nil {
					t.Fatalf("loads, command %q; docker compose refuses it", p.Services["a"].Command)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v (docker compose reads %q)", err, tc.want)
			}
			if got := p.Services["a"].Command[0]; got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

// Only a double-quoted scalar reads the word's backslashes. In a single-quoted one, a plain one and
// a block scalar they are the characters they are (docker compose v5.5.1 gave the same values for
// the same words: `\"q\"` stays `\"q\"`), and a line of a block scalar that looks like a quoted
// scalar (it begins with a quote, or has one after `: `) is text.
func TestTheWordOfAReferenceIsLeftAsWrittenWhereYAMLReadsNoEscapes(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"single quotes", `    command: ['p=${CFG:-\"q\" x\\y}']`, `p=\"q\" x\\y`},
		{"a plain scalar", "    command:\n      - p=${CFG:-x\\\\y}", `p=x\\y`},
		{"a literal block, a line like a quoted scalar", "    command:\n      - |\n        echo x: \"${CFG:-a\\\\b}\"\n", "echo x: \"a\\\\b\"\n"},
		{"a literal block, a line that begins with a quote", "    command:\n      - |\n        \"${CFG:-a\\\"b}\"\n", "\"a\\\"b\"\n"},
		{"a folded block", "    command:\n      - >\n        x: \"${CFG:-a\\\\b}\"\n", "x: \"a\\\\b\"\n"},
		{"a literal block, the second line", "    command:\n      - |\n        first line\n        \"${CFG:-a\\\"b}\"\n", "first line\n\"a\\\"b\"\n"},
		{"a block with an indentation mark and a comment", "    command:\n      - |2- # note\n        x: \"${CFG:-a\\\\b}\"\n", "x: \"a\\\\b\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n"+tc.body+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["a"].Command[0]; got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

// A value a variable holds is what it is: only the text of the file is read as YAML reads it. And
// a reference nested in the word is read in the same pass (`${B:-\"z\"}` is `"z"`), as docker compose
// reads the whole string once the YAML is done with it.
func TestOnlyTheTextOfTheFileIsReadAsYAMLReadsIt(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		env        map[string]string
		want       string
	}{
		{"a value with a backslash", `"p=${CFG:-x}"`, map[string]string{"CFG": `a\"b\\c`}, `p=a\"b\\c`},
		{"a value inside a default", `"p=${A:-${CFG}}"`, map[string]string{"CFG": `a\nb`}, `p=a\nb`},
		{"a nested reference with a quote in its word", `"p=${A:-${B:-\"z\"}}"`, nil, `p="z"`},
		{"a nested reference, the outer word escaped", `"p=${A:-\"${CFG:-x}\"}"`, map[string]string{"CFG": `v`}, `p="v"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(n string) (string, bool) { v, ok := tc.env[n]; return v, ok }
			got, err := documentValues("v: "+tc.body+"\n", lookup)
			if err != nil {
				t.Fatalf("interpolateDocument: %v", err)
			}
			if got["v"] != tc.want {
				t.Errorf("v = %q, want %q", got["v"], tc.want)
			}
		})
	}
}

// A key is not interpolated (docker compose expands the values of a mapping and never its keys): it
// is the text the YAML made of it, so a double-quoted key reads its escapes too
// (`"${A:-\"q\"}": v` is the key `${A:-"q"}`, as docker compose v5.5.1 prints it).
func TestAKeyWrittenInDoubleQuotesIsTheTextYAMLMakesOfIt(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n    environment:\n      \"${A:-\\\"q\\\"}\": v\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	env, err := p.Services["a"].ResolvedEnv()
	if err != nil {
		t.Fatalf("ResolvedEnv: %v", err)
	}
	if want := `${A:-"q"}=v`; len(env) != 1 || env[0] != want {
		t.Errorf("environment = %q, want [%q]", env, want)
	}
}

// What YAML reads in a double-quoted scalar, and what it refuses (go-yaml's words are docker
// compose's). A backslash before a line break is a continuation and is left for the fold.
func TestUnescapeDoubleQuoted(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		refused  string // a word of the refusal, "" for none
	}{
		{in: `plain`, want: `plain`},
		{in: `\"q\"`, want: `"q"`},
		{in: `a\\b`, want: `a\b`},
		{in: `\\\"`, want: `\"`},
		{in: `\t\0\a\b\v\f\r\e\ `, want: "\t\x00\a\b\v\f\r\x1b "},
		{in: "\\\t", want: "\t"},
		{in: `\N\_\L\P`, want: "\u0085\u00a0\u2028\u2029"},
		{in: `\x41\u00e9\U0001F600`, want: "Aé😀"},
		{in: `\x00\u0000`, want: "\x00\x00"},
		{in: "a\\\nb", want: "a\\\nb"},
		{in: `\q`, refused: "unknown escape"},
		{in: `\$`, refused: "unknown escape"},
		{in: `\/`, refused: "unknown escape"},
		{in: `\n`, refused: "invalid interpolation format"},
		{in: `x\`, refused: "unknown escape"},
		{in: `\x4`, refused: "hexadecimal"},
		{in: `\xZZ`, refused: "hexadecimal"},
		{in: `\x+1`, refused: "hexadecimal"},
		{in: `\u12`, refused: "hexadecimal"},
		{in: `\U0001F60`, refused: "hexadecimal"},
		{in: `\ud800`, refused: "Unicode"},
		{in: `\U00110000`, refused: "Unicode"},
		{in: `a"b`, refused: "ends the quoted value"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := unescapeDoubleQuoted(tc.in, false)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("unescapeDoubleQuoted(%q) = %q, %v; want a refusal naming %q", tc.in, got, err, tc.refused)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("unescapeDoubleQuoted(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// Where the word is not read as YAML reads it (documented in docs/compatibility.md): each row says
// what docker compose v5.5.1 gives and holds what opossum gives, so that a change to either shows.
//   - an escape that writes a brace: the reference closes at the `}` as written, where docker compose
//     counts the `}` the escape stands for (`p=ab}`).
func TestWhereTheWordOfAReferenceInDoubleQuotesIsNotReadAsYAMLReadsIt(t *testing.T) {
	for _, tc := range []struct{ name, command, docker, want string }{
		{"an escape that writes a brace", `["p=${CFG:-a\x7db}"]`, `p=ab}`, `p=a}b`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n    command: "+tc.command+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["a"].Command[0]; got != tc.want {
				t.Errorf("command = %q, want %q (docker compose gives %q)", got, tc.want, tc.docker)
			}
		})
	}
}

// What a reference's word is in each place a line can be in, as docker compose v5.5.1 reads it
// (`config --format json`; the `$$` it prints for a `$` is written as `$`). The lines of a block
// scalar are text however deep they are (a JSON document in `content: |`: the header is several lines
// up), a line that continues a plain or single-quoted value is not the start of a quoted one, and a
// key is not interpolated, so a line break in it is no break in a reference. Each row is the body of
// an `environment:` mapping, and the same with CRLF line breaks where it says so.
func TestAWordIsReadInEveryPlaceALineCanBeAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		crlf, refused bool
		want          map[string]string
	}{
		{"block, JSON line with a regex, nested two deep", "      V: |\n        {\n          \"pat\": \"${PAT:-^\\d+$$}\"\n        }\n", false, false, map[string]string{"V": "{\n  \"pat\": \"^\\d+$\"\n}\n"}},
		{"block, JSON line with a regex, nested two deep (CRLF)", "      V: |\n        {\n          \"pat\": \"${PAT:-^\\d+$$}\"\n        }\n", true, false, map[string]string{"V": "{\n  \"pat\": \"^\\d+$\"\n}\n"}},
		{"block, JSON line with a bare quote in a word", "      V: |\n        {\n          \"a\": \"${A:-\"x\"}\"\n        }\n", false, false, map[string]string{"V": "{\n  \"a\": \"\"x\"\"\n}\n"}},
		{"block, JSON line with a bare quote in a word (CRLF)", "      V: |\n        {\n          \"a\": \"${A:-\"x\"}\"\n        }\n", true, false, map[string]string{"V": "{\n  \"a\": \"\"x\"\"\n}\n"}},
		{"block, JSON line with escapes that stay as written", "      V: |\n        {\n          \"path\": \"${P:-C:\\\\temp}\", \"q\": \"${Q:-\\\"x\\\"}\"\n        }\n", false, false, map[string]string{"V": "{\n  \"path\": \"C:\\\\temp\", \"q\": \"\\\"x\\\"\"\n}\n"}},
		{"block, JSON line with escapes that stay as written (CRLF)", "      V: |\n        {\n          \"path\": \"${P:-C:\\\\temp}\", \"q\": \"${Q:-\\\"x\\\"}\"\n        }\n", true, false, map[string]string{"V": "{\n  \"path\": \"C:\\\\temp\", \"q\": \"\\\"x\\\"\"\n}\n"}},
		{"block, a shell script continued over lines", "      V: |\n        exec app \\\n          \"--root=${ROOT:-C:\\\\data}\" \\\n          \"--re=${RE:-\\w+}\"\n", false, false, map[string]string{"V": "exec app \\\n  \"--root=C:\\\\data\" \\\n  \"--re=\\w+\"\n"}},
		{"block, a shell script continued over lines (CRLF)", "      V: |\n        exec app \\\n          \"--root=${ROOT:-C:\\\\data}\" \\\n          \"--re=${RE:-\\w+}\"\n", true, false, map[string]string{"V": "exec app \\\n  \"--root=C:\\\\data\" \\\n  \"--re=\\w+\"\n"}},
		{"folded block, a deeper line with an unknown escape", "      V: >\n        x\n          y: \"${A:-\\q}\"\n", false, false, map[string]string{"V": "x\n  y: \"\\q\"\n"}},
		{"folded block, a deeper line with an unknown escape (CRLF)", "      V: >\n        x\n          y: \"${A:-\\q}\"\n", true, false, map[string]string{"V": "x\n  y: \"\\q\"\n"}},
		{"block, a deeper line that begins with a quote", "      V: |\n        a\n          \"${A:-a\\\\b}\"\n", false, false, map[string]string{"V": "a\n  \"a\\\\b\"\n"}},
		{"block, a deeper line that begins with a quote (CRLF)", "      V: |\n        a\n          \"${A:-a\\\\b}\"\n", true, false, map[string]string{"V": "a\n  \"a\\\\b\"\n"}},
		{"block with a blank line after the header", "      V: |\n\n        x: \"${A:-a\\\\b}\"\n", false, false, map[string]string{"V": "\nx: \"a\\\\b\"\n"}},
		{"block with a blank line after the header (CRLF)", "      V: |\n\n        x: \"${A:-a\\\\b}\"\n", true, false, map[string]string{"V": "\nx: \"a\\\\b\"\n"}},
		{"block header with trailing spaces", "      V: |   \n        x: \"${A:-a\\\\b}\"\n", false, false, map[string]string{"V": "x: \"a\\\\b\"\n"}},
		{"block header with trailing spaces (CRLF)", "      V: |   \n        x: \"${A:-a\\\\b}\"\n", true, false, map[string]string{"V": "x: \"a\\\\b\"\n"}},
		{"block, then a sibling key", "      X: |\n        t\n      V: \"${A:-a\\\\b}\"\n", false, false, map[string]string{"V": "a\\b", "X": "t\n"}},
		{"block, then a sibling key (CRLF)", "      X: |\n        t\n      V: \"${A:-a\\\\b}\"\n", true, false, map[string]string{"V": "a\\b", "X": "t\n"}},
		{"block with a comment on its header", "      V: | # note\n        x: \"${A:-a\\\\b}\"\n", false, false, map[string]string{"V": "x: \"a\\\\b\"\n"}},
		{"block with a comment on its header (CRLF)", "      V: | # note\n        x: \"${A:-a\\\\b}\"\n", true, false, map[string]string{"V": "x: \"a\\\\b\"\n"}},
		{"plain value continued on a line that begins with a quote", "      V: echo\n        \"${A:-\\\"x\\\"}\"\n", false, false, map[string]string{"V": "echo \"\\\"x\\\"\""}},
		{"single-quoted value continued on a line that begins with a quote", "      V: 'echo\n        \"${A:-\\\"x\\\"}\"'\n", false, false, map[string]string{"V": "echo \"\\\"x\\\"\""}},
		{"plain value continued, a backslash in the word", "      V: echo a\n        \"${A:-a\\\\b}\"\n", false, false, map[string]string{"V": "echo a \"a\\\\b\""}},
		{"a key written in double quotes with a line break escape", "      \"${A:-a\\nb}\": v\n", false, false, map[string]string{"${A:-a\nb}": "v"}},
		{"a nested reference whose default holds an escaped backslash", "      V: \"${A:-\\\"${B:-\\\\\\\\x}\\\"}\"\n", false, false, map[string]string{"V": "\"\\\\x\""}},
		{"a value with an escape that is read, the nested one too", "      V: \"${A:-${B:-\\\"z\\\"}}\"\n", false, false, map[string]string{"V": "\"z\""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "services:\n  a:\n    image: x\n    environment:\n" + tc.body
			if tc.crlf {
				doc = strings.ReplaceAll(doc, "\n", "\r\n")
			}
			p, err := Load(writeTemp(t, doc))
			if tc.refused {
				if err == nil {
					t.Fatal("loads; docker compose refuses it")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			env, err := p.Services["a"].ResolvedEnv()
			if err != nil {
				t.Fatalf("ResolvedEnv: %v", err)
			}
			got := map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				got[k] = v
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("environment = %q, want %q", got, tc.want)
			}
		})
	}
}

// The text of the file is read once: a reference nested in a default's word is read in the same
// pass, and a text that is not a YAML document (the legacy road a `.env` value takes) is not read as
// one at all — `\"` stays two characters there.
func TestTheTextOfTheFileIsReadOnceAndOnlyAsADocument(t *testing.T) {
	got, err := documentValues("v: \"p=${A:-\\\"${B:-\\\\\\\\x}\\\"}\"\n", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("interpolateDocument: %v", err)
	}
	if want := `p="\\x"`; got["v"] != want {
		t.Errorf("v = %q, want %q (docker compose: the word is read once)", got["v"], want)
	}
	text, err := interpolate([]byte(`"p=${CFG:-\"q\"}"`), func(string) (string, bool) { return "", false })
	if err != nil || string(text) != `"p=\"q\""` {
		t.Errorf("interpolate = %q, %v; a text that is not a document keeps the word as written", text, err)
	}
}

// The same for whole documents, where what is above the line matters: a block above a sibling
// mapping is over (the lines of the mapping are not the block's, however deep), a block header may
// be followed by a tab, and a scalar that is on a line of its own after `[`, `,`, `?`, `-` or a
// comment line is a quoted scalar, not a continuation. docker compose v5.5.1's `command` and
// `environment` (the `$$` it prints for a `$` is written as `$`).
func TestAWordIsReadWhereTheLinesAboveMatterAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"a block above a sibling mapping whose nested line holds the word", "services:\n  a:\n    image: x\n    command: |\n      text\n    environment:\n      V: \"${A:-a\\\\b}\"\n",
			`{"command":["text"],"environment":{"V":"a\\b"}}`},
		{"a block above a deeper sibling, nested two levels", "services:\n  a:\n    image: x\n    command: |\n      text\n    labels:\n      k: v\n    environment:\n      V: \"${A:-\\\"x\\\"}\"\n",
			`{"command":["text"],"environment":{"V":"\"x\""}}`},
		{"a block header with a tab before the indicator", "services:\n  a:\n    image: x\n    environment:\n      V:\t|\n        x: \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"x: \"a\\\\b\"\n"}}`},
		{"a flow sequence element after a comment line", "services:\n  a:\n    image: x\n    command: [\n        # note\n        \"${A:-\\\"x\\\"}\"\n      ]\n",
			`{"command":["\"x\""],"environment":{}}`},
		{"a flow sequence element after a comma line", "services:\n  a:\n    image: x\n    command: [a,\n        \"${A:-\\\"x\\\"}\"]\n",
			`{"command":["a","\"x\""],"environment":{}}`},
		{"a complex key whose scalar is on the next line", "services:\n  a:\n    image: x\n    environment:\n      ?\n        \"${A:-a\\\\b}\"\n      : v\n",
			`{"command":null,"environment":{"${A:-a\\b}":"v"}}`},
		{"a list item whose scalar is on the next line", "services:\n  a:\n    image: x\n    command:\n      -\n        \"${A:-\\\"x\\\"}\"\n",
			`{"command":["\"x\""],"environment":{}}`},
		{"a flow mapping key written in double quotes, colon with no space", "services:\n  a:\n    image: x\n    environment: {\"${A:-a\\nb}\":v}\n",
			`{"command":null,"environment":{"${A:-a\nb}":"v"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.doc))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			env, err := p.Services["a"].ResolvedEnv()
			if err != nil {
				t.Fatalf("ResolvedEnv: %v", err)
			}
			m := map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				m[k] = v
			}
			var cmd any
			if c := p.Services["a"].Command; len(c) > 0 {
				cmd = []string(c)
			}
			b, _ := json.Marshal(map[string]any{"command": cmd, "environment": m})
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
}

// Lines that look alike and are not: a quoted key after a sibling entry (the way traefik's labels
// are written, every key in quotes) is the start of a scalar, not the continuation of the value
// above it; a quoted key may have a space or a tab before its colon; a flow mapping that opens on
// its line has its keys on the next; a block header may carry a tab, a comment, a hash in its key,
// or stand alone on a line, with a `>` and its marks as well. docker compose v5.5.1's `command`,
// `environment` and `labels` (the `$$` it prints for a `$` is written as `$`).
func TestAWordIsReadAfterLinesThatLookAlikeAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"quoted keys after a sibling, in environment and labels (traefik style)", "services:\n  a:\n    image: x\n    environment:\n      FOO: bar\n      \"BAR\": \"${A:-a\\\\b}\"\n    labels:\n      \"traefik.a\": \"x\"\n      \"traefik.b\": \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"BAR":"a\\b","FOO":"bar"},"labels":{"traefik.a":"x","traefik.b":"a\\b"}}`},
		{"a quoted key whose scalar holds a reference, after a sibling", "services:\n  a:\n    image: x\n    environment:\n      \"k\": v\n      \"${Z:-z\\\\q}\" : v3\n",
			`{"command":null,"environment":{"${Z:-z\\q}":"v3","k":"v"},"labels":{}}`},
		{"a quoted key with a tab before the colon", "services:\n  a:\n    image: x\n    environment:\n      \"k\": v\n      \"${Z:-z\\\\q}\"\t: v3\n",
			`{"command":null,"environment":{"${Z:-z\\q}":"v3","k":"v"},"labels":{}}`},
		{"a flow mapping that opens on its line, a quoted key on the next", "services:\n  a:\n    image: x\n    labels: {\n      \"k\": \"${A:-a\\\\b}\"}\n",
			`{"command":null,"environment":{},"labels":{"k":"a\\b"}}`},
		{"a block header with a tab and a comment", "services:\n  a:\n    image: x\n    environment:\n      V: |\t# note\n        k: \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"k: \"a\\\\b\"\n"},"labels":{}}`},
		{"a block header whose key holds a hash", "services:\n  a:\n    image: x\n    environment:\n      \"x #y\": |\n        {\"k\": \"${A:-a\\\\b}\"}\n",
			`{"command":null,"environment":{"x #y":"{\"k\": \"a\\\\b\"}\n"},"labels":{}}`},
		{"a block indicator alone on a line, less indented than the text", "services:\n  a:\n    image: x\n    environment:\n      V:\n        |\n          k: \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"k: \"a\\\\b\"\n"},"labels":{}}`},
		{"a folded block indicator with a mark alone on a line", "services:\n  a:\n    image: x\n    environment:\n      V:\n        >-\n          k: \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"k: \"a\\\\b\""},"labels":{}}`},
		{"a block header with a tab before a folded indicator", "services:\n  a:\n    image: x\n    environment:\n      V:\t>\n        k: \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"k: \"a\\\\b\"\n"},"labels":{}}`},
		{"a plain value that goes over lines and ends in a comma, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      V: echo a,\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"echo a, \"a\\\\b\""},"labels":{}}`},
		{"a quoted key after a block mapping nested deeper, in labels", "services:\n  a:\n    image: x\n    labels:\n      a: 1\n        \n      \"${Z:-z\\\\q}\": v3\n",
			`{"command":null,"environment":{},"labels":{"${Z:-z\\q}":"v3","a":"1"}}`},
		{"a quoted key after a plain value that goes over lines", "services:\n  a:\n    image: x\n    environment:\n      A: first\n        second\n      \"${Z:-z\\\\q}\": v3\n",
			`{"command":null,"environment":{"${Z:-z\\q}":"v3","A":"first second"},"labels":{}}`},
		{"a quoted key after a double-quoted value that goes over lines", "services:\n  a:\n    image: x\n    environment:\n      A: \"first\n        second\"\n      \"${Z:-z\\\\q}\": v3\n",
			`{"command":null,"environment":{"${Z:-z\\q}":"v3","A":"first second"},"labels":{}}`},
		{"a quoted top-level key after a list", "services:\n  a:\n    image: x\n    command:\n      - foo\n    environment:\n      V: \"${A:-a\\\\b}\"\n",
			`{"command":["foo"],"environment":{"V":"a\\b"},"labels":{}}`},
		{"a single-quoted value that goes over lines and has a colon at the end of a line, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      X: 'see http:\n        \"${A:-a\\\\b}\"'\n",
			`{"command":null,"environment":{"X":"see http: \"a\\\\b\""},"labels":{}}`},
		{"a plain value that goes over lines and ends with a question mark, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      Z: what?\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"Z":"what? \"a\\\\b\""},"labels":{}}`},
		{"a plain value over lines that ends with an open bracket, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      W: echo [\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"W":"echo [ \"a\\\\b\""},"labels":{}}`},
		{"a list item that goes over lines, a quoted line after", "services:\n  a:\n    image: x\n    command:\n      - echo a\n        \"${A:-a\\\\b}\"\n",
			`{"command":["echo a \"a\\\\b\""],"environment":{},"labels":{}}`},
		{"an entry with a comment and no value, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      V: # note\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"a\\b"},"labels":{}}`},
		{"a list of mappings, a quoted key at the key column", "x-l:\n  - k: echo\n    \"${A:-\\q}\": z\nservices:\n  a:\n    image: x\n",
			"REFUSED"},
		{"a tab after the colon, a plain value over lines, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      V:\techo\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"echo \"a\\\\b\""},"labels":{}}`},
		{"a plain value over three lines, the last one quoted", "services:\n  a:\n    image: x\n    environment:\n      V: echo\n        line2\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"echo line2 \"a\\\\b\""},"labels":{}}`},
		{"a one-character plain value, a quoted line after", "services:\n  a:\n    image: x\n    environment:\n      V: a\n        \"${A:-a\\\\b}\"\n",
			`{"command":null,"environment":{"V":"a \"a\\\\b\""},"labels":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.doc))
			if tc.want == "REFUSED" {
				if err == nil {
					t.Fatal("loads; docker compose refuses it")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services["a"]
			env, err := svc.ResolvedEnv()
			if err != nil {
				t.Fatalf("ResolvedEnv: %v", err)
			}
			m := map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				m[k] = v
			}
			var cmd any
			if c := svc.Command; len(c) > 0 {
				cmd = []string(c)
			}
			lbl := map[string]string{}
			for _, kv := range svc.Labels {
				k, v, _ := strings.Cut(kv, "=")
				lbl[k] = v
			}
			b, _ := json.Marshal(map[string]any{"command": cmd, "environment": m, "labels": lbl})
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
}

// A double-quoted value that goes over several lines holds the reference on a line that does not show
// it: the parser says where the scalar is (docker compose v5.5.1: `first p="q"`).
func TestAWordInADoubleQuotedValueThatGoesOverLinesIsReadAsYAMLReadsIt(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n    command: [\"first\n      p=${CFG:-\\\"q\\\"}\"]\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := p.Services["a"].Command[0], `first p="q"`; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

// Where a line only looks like the start of a double-quoted scalar, or like an entry, and the file says
// otherwise — and the places a line-by-line guess was wrong: a quote in the middle of a plain value
// (`a,"b"`, `a:"b"`, `{"path":"…"}`), a single-quoted or plain value over several lines with lines in it
// that look like entries (`k: v`, `foo:`, `- b`, `-`), a continuation shallower than its key, an owner
// that is only an anchor or a tag, a comment at column 0, a quoted key that holds `: `, a block whose
// text is as indented as its header, a flow mapping over lines, and a bad escape that is in a word or
// outside any. Every row is docker compose v5.5.1's answer (`command`, `environment`, `labels`; the `$$`
// it prints for a `$` is written as `$`), or its refusal.
func TestAWordIsReadWhereTheParserSaysTheScalarIsAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"plain JSON-like value", "services:\n  a:\n    image: x\n    environment:\n      X: --opt={\"path\":\"${P:-C:\\\\tmp}\"}\n",
			`{"command":null,"environment":{"X":"--opt={\"path\":\"C:\\\\tmp\"}"},"labels":{}}`},
		{"plain with ,\" and a bad escape in the word", "services:\n  a:\n    image: x\n    environment:\n      X: a,\"${A:-\\d+}\"\n",
			`{"command":null,"environment":{"X":"a,\"\\d+\""},"labels":{}}`},
		{"plain with ,\" and a backslash", "services:\n  a:\n    image: x\n    environment:\n      X: echo a,\"${A:-\\\\}\"\n",
			`{"command":null,"environment":{"X":"echo a,\"\\\\\""},"labels":{}}`},
		{"plain with :\" ", "services:\n  a:\n    image: x\n    environment:\n      Y: a:\"${A:-\\\"q\\\"}\"\n",
			`{"command":null,"environment":{"Y":"a:\"\\\"q\\\"\""},"labels":{}}`},
		{"plain with - \"", "services:\n  a:\n    image: x\n    environment:\n      Z: ls - \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"Z":"ls - \"x\\\\y\""},"labels":{}}`},
		{"command string with ,\"", "services:\n  a:\n    image: x\n    command: echo a,\"${A:-\\\\}\"\n",
			`{"command":["echo","a,\\"],"environment":{},"labels":{}}`},
		{"single quoted over lines, an entry-looking line between", "services:\n  a:\n    image: x\n    environment:\n      X: 'a\n        k: v\n        \"${A:-x\\\\y}\"'\n",
			`{"command":null,"environment":{"X":"a k: v \"x\\\\y\""},"labels":{}}`},
		{"single quoted over lines, a line `foo:` between", "services:\n  a:\n    image: x\n    environment:\n      X: 'a\n        foo:\n        \"${A:-x\\\\y}\"'\n",
			`{"command":null,"environment":{"X":"a foo: \"x\\\\y\""},"labels":{}}`},
		{"plain over lines, a line `- b` between", "services:\n  a:\n    image: x\n    environment:\n      X: a\n        - b\n        \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"X":"a - b \"x\\\\y\""},"labels":{}}`},
		{"plain over lines, a line `-` between", "services:\n  a:\n    image: x\n    environment:\n      X: a\n        -\n        \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"X":"a - \"x\\\\y\""},"labels":{}}`},
		{"single quoted continuation shallower than the key", "services:\n  a:\n    image: x\n    environment:\n      X: 'x\n    \"${A:-x\\\\y}\"'\n",
			`{"command":null,"environment":{"X":"x \"x\\\\y\""},"labels":{}}`},
		{"owner line with only an anchor", "services:\n  a:\n    image: x\n    environment:\n      X: &a\n        \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"X":"x\\y"},"labels":{}}`},
		{"owner line with only a tag", "services:\n  a:\n    image: x\n    environment:\n      X: !!str\n        \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"X":"x\\y"},"labels":{}}`},
		{"a column 0 comment that looks like an entry", "# note: x\nservices:\n  a:\n    image: x\n    environment:\n      V: \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"V":"x\\y"},"labels":{}}`},
		{"a quoted key holding `: `, then a quoted line", "services:\n  a:\n    image: x\n    environment:\n      \"a: b\":\n        \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"a: b":"x\\y"},"labels":{}}`},
		{"a quoted key holding `: [`, a continuation after", "services:\n  a:\n    image: x\n    environment:\n      \"a: [x\": plain\n        \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"a: [x":"plain \"x\\\\y\""},"labels":{}}`},
		{"a block whose text has the header's indent", "services:\n  a:\n    image: x\n    environment:\n      X:\n      |\n      \"${A:-x\\\\y}\"\n",
			"REFUSED"},
		{"a multi-line double-quoted value", "services:\n  a:\n    image: x\n    command: [\"first\n      p=${CFG:-\\\"q\\\"}\"]\n",
			`{"command":["first p=\"q\""],"environment":{},"labels":{}}`},
		{"a double-quoted value with an anchor and a tag", "services:\n  a:\n    image: x\n    environment:\n      X: &a !!str \"${A:-x\\\\y}\"\n",
			`{"command":null,"environment":{"X":"x\\y"},"labels":{}}`},
		{"an alias of a double-quoted value", "services:\n  a:\n    image: x\n    environment:\n      X: &a \"${A:-x\\\\y}\"\n      Y: *a\n",
			`{"command":null,"environment":{"X":"x\\y","Y":"x\\y"},"labels":{}}`},
		{"flow mapping over lines", "services:\n  a:\n    image: x\n    environment: {\n      \"k\": \"${A:-x\\\\y}\",\n      \"j\": \"${B:-\\\"q\\\"}\"\n    }\n",
			`{"command":null,"environment":{"j":"\"q\"","k":"x\\y"},"labels":{}}`},
		{"a bad escape outside any reference", "services:\n  a:\n    image: x\n    environment:\n      X: \"a\\qb\"\n",
			"REFUSED"},
		{"a word with a bad escape, a flow value in plain elsewhere", "services:\n  a:\n    image: x\n    environment:\n      X: \"${A:-\\q}\"\n",
			"REFUSED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.doc))
			if tc.want == "REFUSED" {
				if err == nil {
					t.Fatal("loads; docker compose refuses it")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services["a"]
			env, err := svc.ResolvedEnv()
			if err != nil {
				t.Fatalf("ResolvedEnv: %v", err)
			}
			m := map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				m[k] = v
			}
			var cmd any
			if c := svc.Command; len(c) > 0 {
				cmd = []string(c)
			}
			lbl := map[string]string{}
			for _, kv := range svc.Labels {
				k, v, _ := strings.Cut(kv, "=")
				lbl[k] = v
			}
			b, _ := json.Marshal(map[string]any{"command": cmd, "environment": m, "labels": lbl})
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
}

// A reference that starts in one double-quoted scalar and ends in another (`["p=${A:-", "}"]`, a quote
// in the word that the file's own YAML closes the scalar at) is refused as docker compose refuses it
// (`p=${A:-` is no reference); and a file written with CR alone as its line break is read in the same
// places (docker compose v5.5.1: `a\b`).
func TestAReferenceThatEndsInAnotherScalarIsRefusedAndCRIsALineBreak(t *testing.T) {
	if _, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n    command: [\"p=${A:-\", \"}\"]\n")); err == nil {
		t.Error("a reference that ends in the next scalar loads; docker compose refuses it")
	}
	p, err := Load(writeTemp(t, "services:\r  a:\r    image: x\r    environment:\r      V: \"${A:-a\\\\b}\"\r      W: \"x\"\r"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	env, err := p.Services["a"].ResolvedEnv()
	if err != nil {
		t.Fatalf("ResolvedEnv: %v", err)
	}
	if want := []string{`V=a\b`, "W=x"}; !reflect.DeepEqual([]string(env), want) {
		t.Errorf("environment = %q, want %q", env, want)
	}
}

// A line break written as an escape in a word is no fault in a key (not interpolated) and is one in a
// value (docker compose v5.5.1 refuses `V: "${A:-a\nb}"`); and a column the parser counts in characters
// is the right byte where a multibyte character comes before the scalar on its line.
func TestALineBreakEscapeIsRefusedInAValueAndTheColumnIsInCharacters(t *testing.T) {
	if _, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n    environment:\n      V: \"${A:-a\\nb}\"\n")); err == nil {
		t.Error("a line break escape in the word of a reference in a value loads; docker compose refuses it")
	}
	for name, body := range map[string]string{
		"a quoted multibyte key before the value": "    environment:\n      \"ééééé\": \"${A:-a\\\\b}\"\n",
		"a multibyte word before the scalar":      "    labels: {\"ééééé\": \"x\", \"k\": \"${A:-a\\\\b}\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  a:\n    image: x\n"+body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			env, _ := p.Services["a"].ResolvedEnv()
			all := append(append([]string{}, env...), p.Services["a"].Labels...)
			found := false
			for _, kv := range all {
				if kv == `k=a\b` || kv == `ééééé=a\b` {
					found = true
				}
			}
			if !found {
				t.Errorf("environment %q, labels %q: want a\\b read once, as docker compose does", env, p.Services["a"].Labels)
			}
		})
	}
}

// The position the parser gives is a line and a column of its own counting, and the word is read where
// the scalar really is: NEL, LS and PS are line breaks to it as LF is (an earlier value holding one
// moved every later position a line down, and a quote in a later single-quoted or plain value was taken
// for a quoted scalar), a comment between an anchor or a tag and the scalar may hold quotes, an escaped
// quote inside a scalar does not close it, and CRLF is one line break however many scalars come
// before. docker compose v5.5.1's answers (`command`, `environment`, `labels`).
func TestAWordIsReadWhereTheTextHasLineBreaksAndPropertiesAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"NEL in an earlier quoted value, a later quote in a single-quoted value", "services:\n  a:\n    image: x\n    labels:\n      l: \"ab\"\n    environment:\n      A: \"plain\"\n      B: 'say\" ${U:-a\\\\b} \"'\n",
			`{"command":null,"environment":{"A":"plain","B":"say\" a\\\\b \""},"labels":{"l":"a b"}}`},
		{"LS in an earlier quoted value, a later plain value with a quote", "services:\n  a:\n    image: x\n    labels:\n      l: \"a b\"\n    environment:\n      A: \"plain\"\n      B: say\" ${U:-\\q} \"\n",
			`{"command":null,"environment":{"A":"plain","B":"say\" \\q \""},"labels":{"l":"a\u2028b"}}`},
		{"PS in an earlier quoted value, then a quoted word", "services:\n  a:\n    image: x\n    labels:\n      l: \"a b\"\n    environment:\n      A: \"${U:-\\\"q\\\"}\"\n",
			`{"command":null,"environment":{"A":"\"q\""},"labels":{"l":"a\u2029b"}}`},
		{"a comment with quotes between a tag and its scalar", "services:\n  a:\n    image: x\n    environment:\n      A: !!str # was \"${OLD:-C:\\q}\"\n        \"x\"\n",
			`{"command":null,"environment":{"A":"x"},"labels":{}}`},
		{"a comment with quotes between an anchor and its scalar", "services:\n  a:\n    image: x\n    environment:\n      A: &a # say \"hi\"\n        \"${U:-\\\"q\\\"}\"\n",
			`{"command":null,"environment":{"A":"\"q\""},"labels":{}}`},
		{"an escaped quote before a reference in the same scalar", "services:\n  a:\n    image: x\n    environment:\n      A: \"say \\\"hi\\\" ${U:-\\\\x}\"\n",
			`{"command":null,"environment":{"A":"say \"hi\" \\x"},"labels":{}}`},
		{"CRLF, a multibyte scalar, then a quoted word", "services:\r\n  a:\r\n    image: x\r\n    environment:\r\n      Z: \"日本\"\r\n      A: \"${U:-\\\"q\\\"}\"\r\n",
			`{"command":null,"environment":{"A":"\"q\"","Z":"日本"},"labels":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.doc))
			if tc.want == "REFUSED" {
				if err == nil {
					t.Fatal("loads; docker compose refuses it")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services["a"]
			env, err := svc.ResolvedEnv()
			if err != nil {
				t.Fatalf("ResolvedEnv: %v", err)
			}
			m := map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				m[k] = v
			}
			var cmd any
			if c := svc.Command; len(c) > 0 {
				cmd = []string(c)
			}
			lbl := map[string]string{}
			for _, kv := range svc.Labels {
				k, v, _ := strings.Cut(kv, "=")
				lbl[k] = v
			}
			b, _ := json.Marshal(map[string]any{"command": cmd, "environment": m, "labels": lbl})
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
}
