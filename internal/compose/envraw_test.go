package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadEnvOf writes body as app.env beside a compose file whose one service
// reads it under the given env_file entry (`format: raw`, or the default
// with format == ""), and returns the service's resolved environment. envText
// is the project's `.env`, when given.
func loadEnvOf(t *testing.T, format, body, envText string) (Environment, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if envText != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envText), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entry := "{path: app.env}"
	if format != "" {
		entry = "{path: app.env, format: " + format + "}"
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  web:\n    image: alpine\n    env_file:\n      - "+entry+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		return nil, err
	}
	return proj.Services["web"].ResolvedEnv()
}

// Each row is one rule of docker compose's (v5.5.1) `format: raw` reading,
// measured on 2026-09-18, with the default reading of the same file beside
// it where the two part. `raw` holds what the container gets (a file's keys
// come out sorted) — or the text the refusal must carry, under `rawErr`.
func TestEnvFileFormatRawHandsValuesOverAsWritten(t *testing.T) {
	unsetHostVars(t, "RAWT", "NOPE", "A", "B", "C", "D")
	for _, tc := range []struct {
		name    string
		file    string
		raw     []string // the environment under format: raw
		rawErr  string   // or what its refusal says
		dotenv  []string // the environment under the default reading
		dotErr  string
		project string // the project's .env, when the rule needs one
	}{
		{name: "quotes, $ and ${} stay as written",
			file:    "Q=\"x y\"\nD=$RAWT\nS='a $RAWT'\nE=${RAWT}\nF=${NOPE:-x}\n",
			project: "RAWT=/home/x\n",
			raw:     []string{"D=$RAWT", "E=${RAWT}", "F=${NOPE:-x}", "Q=\"x y\"", "S='a $RAWT'"},
			dotenv:  []string{"D=/home/x", "E=/home/x", "F=x", "Q=x y", "S=a $RAWT"}},
		{name: "a blank line and a # line are skipped, a # inside a value is kept",
			file:   "A=1\n\n# note\n   # note\n   #B=2\nC=1 # c\n",
			raw:    []string{"A=1", "C=1 # c"},
			dotenv: []string{"A=1", "C=1"}},
		{name: "a line with no = is skipped, KEY: VALUE among them",
			file:   "JUSTNAME\nA:1\nB=2\n",
			raw:    []string{"B=2"},
			dotErr: "no `=`"},
		{name: "blanks before the name go, blanks after the value stay",
			file:   "   A=1   \nB=2\n",
			raw:    []string{"A=1   ", "B=2"},
			dotenv: []string{"A=1", "B=2"}},
		{name: "a value's own = and an empty value",
			file:   "A=b=c\nB=\n",
			raw:    []string{"A=b=c", "B="},
			dotenv: []string{"A=b=c", "B="}},
		{name: "a quote left open is not continued onto the next line",
			file:   "A=\"x\ny\"\nB=2\n",
			raw:    []string{"A=\"x", "B=2"},
			dotenv: []string{"A=x\ny", "B=2"}},
		{name: "a byte-order mark",
			file:   "\ufeffA=1\nB=2\n",
			raw:    []string{"A=1", "B=2"},
			dotenv: []string{"A=1", "B=2"}},
		{name: "a second byte-order mark is part of the name",
			file:   "\ufeff\ufeffA=1\nB=2\n",
			raw:    []string{"B=2", "\ufeffA=1"},
			dotErr: "byte-order mark"},
		{name: "a byte-order mark on the second line is part of the name",
			file:   "A=1\n\ufeffB=2\n",
			raw:    []string{"A=1", "\ufeffB=2"},
			dotErr: "byte-order mark"},
		{name: "a # after a blank of any kind is a comment",
			file:   " #A=1\n\v#B=2\n\u00a0#C=3\nD=4\n",
			raw:    []string{"D=4"},
			dotenv: []string{"D=4"}},
		{name: "a carriage return after the name is part of it",
			file:   "A\r=1\nB=2\n",
			raw:    []string{"A\r=1", "B=2"},
			dotenv: []string{"A=1", "B=2"}},
		{name: "CRLF",
			file:   "A=1\r\nB=2\r\n",
			raw:    []string{"A=1", "B=2"},
			dotenv: []string{"A=1", "B=2"}},
		// The blank that a name may not hold is a space or a tab; a vertical
		// tab, a form feed, a carriage return, a no-break space or an
		// ideographic space inside or after the name is part of it, and any
		// of them in front of the name is dropped (docker compose v5.5.1,
		// measured 2026-09-18).
		{name: "a tab inside the name",
			file:   "A\tB=1\nB=2\n",
			rawErr: "variable \"A\\tB\" contains whitespace",
			dotenv: []string{"A\tB=1", "B=2"}},
		{name: "a tab after the name",
			file:   "A\t=1\nB=2\n",
			rawErr: "variable \"A\\t\" contains whitespace",
			dotenv: []string{"A=1", "B=2"}},
		{name: "a vertical tab inside the name",
			file:   "A\vB=1\nB=2\n",
			raw:    []string{"A\vB=1", "B=2"},
			dotenv: []string{"A\vB=1", "B=2"}},
		{name: "a form feed after the name",
			file:   "A\f=1\nB=2\n",
			raw:    []string{"A\f=1", "B=2"},
			dotenv: []string{"A=1", "B=2"}},
		{name: "a no-break space inside the name",
			file:   "A\u00a0B=1\nB=2\n",
			raw:    []string{"A\u00a0B=1", "B=2"},
			dotenv: []string{"A\u00a0B=1", "B=2"}},
		{name: "blanks of every kind before the name are dropped",
			file:   "\tA=1\n\vB=2\n\u00a0C=3\n\u3000D=4\n",
			raw:    []string{"A=1", "B=2", "C=3", "D=4"},
			dotenv: []string{"A=1", "B=2", "C=3", "D=4"}},
		{name: "export is not a prefix but a blank in the name",
			file:   "export A=1\nB=2\n",
			rawErr: `variable "export A" contains whitespace`,
			dotenv: []string{"A=1", "B=2"}},
		// The default reading takes a name with a blank inside, and refuses
		// one that is empty; docker compose does the reverse (#1149). The
		// default column says what opossum does today.
		{name: "a blank inside the name",
			file:   "A B=1\nB=2\n",
			rawErr: `variable "A B" contains whitespace`,
			dotenv: []string{"A B=1", "B=2"}},
		{name: "a blank after the name",
			file:   "A =1\nB=2\n",
			rawErr: `variable "A " contains whitespace`,
			dotenv: []string{"A=1", "B=2"}},
		{name: "no name before the =",
			file:   "=1\nB=2\n",
			rawErr: "no variable name",
			dotErr: "empty variable name"},
		{name: "a ${} in a raw value is not resolved from the project's .env either",
			file:    "D=${A}\n",
			project: "A=9\n",
			raw:     []string{"D=${A}"},
			dotenv:  []string{"D=9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loadEnvOf(t, "raw", tc.file, tc.project)
			if tc.rawErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.rawErr) {
					t.Errorf("raw: want a refusal saying %q, got env %v, err %v", tc.rawErr, got, err)
				}
			} else if err != nil {
				t.Errorf("raw: %v", err)
			} else if !reflect.DeepEqual([]string(got), tc.raw) {
				t.Errorf("raw: got %q, want %q", got, tc.raw)
			}
			got, err = loadEnvOf(t, "", tc.file, tc.project)
			if tc.dotErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.dotErr) {
					t.Errorf("default: want a refusal saying %q, got env %v, err %v", tc.dotErr, got, err)
				}
			} else if err != nil {
				t.Errorf("default: %v", err)
			} else if !reflect.DeepEqual([]string(got), tc.dotenv) {
				t.Errorf("default: got %q, want %q", got, tc.dotenv)
			}
		})
	}
}

// `format:` itself, against whether the entry's file is read at all. The
// default and `format: ""` read the same; `raw` is the only other reading;
// any other word is refused where the file is read — so an entry whose file
// is absent under `required: false`, or a service a profile keeps out of
// the run, is not refused for it; `RAW` is another word. A `format:` that is
// not a string (left empty, `null`, a number) is refused at the load, as
// docker compose (v5.5.1) refuses it (`must be a string`).
func TestEnvFileFormatIsReadWhereTheFileIs(t *testing.T) {
	file := "Q=\"x\"\nA:1\n"
	for _, tc := range []struct {
		format string
		want   []string
		err    string
		atLoad bool // the refusal is the load's, not the read's
	}{
		{"", []string{"A=1", "Q=x"}, "", false},
		{`""`, []string{"A=1", "Q=x"}, "", false},
		{"raw", []string{"Q=\"x\""}, "", false},
		{"RAW", nil, `unsupported env_file format "RAW" for app.env`, false},
		{"foo", nil, `unsupported env_file format "foo"`, false},
		{"null", nil, "format must be a string", true},
		{"1", nil, "format must be a string", true},
		{"[]", nil, "cannot unmarshal !!seq into string", true},
		{"{}", nil, "cannot unmarshal !!map into string", true},
	} {
		t.Run("format "+tc.format+", the file read", func(t *testing.T) {
			got, err := loadEnvOf(t, tc.format, file, "")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v, %v", tc.err, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual([]string(got), tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
		// The same entry whose file is not read: absent under `required:
		// false`, or on a service a profile keeps out. Only a `format` that
		// is not a string is refused, and at the load.
		for _, sit := range []struct{ name, compose string }{
			{"absent under required: false", "services:\n  web:\n    image: alpine\n    env_file:\n      - {path: nope.env, required: false, format: FORMAT}\n"},
			{"a service a profile keeps out", "services:\n  web:\n    image: alpine\n    profiles: [x]\n    env_file:\n      - {path: nope.env, format: FORMAT}\n  db:\n    image: alpine\n"},
		} {
			if tc.format == "" {
				continue
			}
			t.Run("format "+tc.format+", "+sit.name, func(t *testing.T) {
				p := writeTemp(t, strings.ReplaceAll(sit.compose, "FORMAT", tc.format))
				proj, err := Load(p)
				if tc.atLoad {
					if err == nil || !strings.Contains(err.Error(), tc.err) {
						t.Fatalf("want the load refused saying %q, got %v", tc.err, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("the load should not be refused for a file it does not read: %v", err)
				}
				if sit.name == "absent under required: false" {
					if env, err := proj.Services["web"].ResolvedEnv(); err != nil || len(env) != 0 {
						t.Errorf("want an empty environment and no error, got %v, %v", env, err)
					}
				}
			})
		}
	}
	// Reading it means it is no longer named among the ignored fields.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  web:\n    image: alpine\n    env_file:\n      - {path: app.env, format: raw}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := proj.Services["web"].Unsupported; len(got) != 0 {
		t.Errorf("format is read, yet listed as ignored: %v", got)
	}
}

// A refusal under `format: raw` names the file and the line, and does not
// read the line back: the value is where secrets live.
func TestARawEnvFileRefusalNamesThePlaceNotTheContents(t *testing.T) {
	const secret = "ghp-canary-do-not-print"
	for _, tc := range []struct{ name, file, wantIn string }{
		{"a blank in the name", "A B=" + secret + "\n", "contains whitespace"},
		{"no name in front of the =", "=" + secret + "\n", "no variable name"},
		{"on the second line", "OK=1\n=" + secret + "\n", "app.env:2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadEnvOf(t, "raw", tc.file, "")
			if err == nil {
				t.Fatal("the env file was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("a different failure got there first, so this checked nothing: %v", err)
			}
			line := "app.env:1"
			if tc.name == "on the second line" {
				line = "app.env:2"
			}
			if !strings.Contains(err.Error(), line) {
				t.Errorf("the error should name the file and line to open (%s): %v", line, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error read the file's contents back:\n%v", err)
			}
		})
	}
}

// Files are read in order, whatever their format: a later file's `${A}` sees
// an earlier raw file's A, an earlier file cannot see a later one's, and the
// project's `.env` outranks both for `${}` while the container still gets the
// file's own value (docker compose v5.5.1, measured 2026-09-18).
func TestEnvFilesOfBothFormatsAreReadInOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries string // the env_file list, as YAML lines
		project string
		want    []string
	}{
		{"raw first, the default file sees its value",
			"      - {path: raw.env, format: raw}\n      - plain.env\n", "",
			[]string{"A=1", "B=1"}},
		{"the default file first, the raw file's value is not yet there",
			"      - plain.env\n      - {path: raw.env, format: raw}\n", "",
			[]string{"B=", "A=1"}},
		{"raw first with the project's .env: the container gets the file's A, ${A} the .env's",
			"      - {path: raw.env, format: raw}\n      - plain.env\n", "A=9\n",
			[]string{"A=1", "B=9"}},
		// The raw file's line is split at its FIRST `=`: what the later file
		// sees under ${A} says where the split was.
		{"a later ${} sees the raw value split at the first =",
			"      - {path: eq.env, format: raw}\n      - plain.env\n", "",
			[]string{"A=b=c", "B=b=c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{"raw.env": "A=1\n", "plain.env": "B=${A}\n", "eq.env": "A=b=c\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.project != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.project), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte("services:\n  web:\n    image: alpine\n    env_file:\n"+tc.entries), 0o644); err != nil {
				t.Fatal(err)
			}
			proj, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := proj.Services["web"].ResolvedEnv()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual([]string(got), tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// `format` reached through an alias or a merge key is the value it names,
// for the type check too: `format: *fmt` and `<<: *base` with a null or a
// number are refused at the load as a written one is, a `raw` so reached
// reads raw, a `foo` so reached is refused where the file is read, a key
// written beside the merge outranks it, and among several merged mappings
// the first that has the key wins (docker compose v5.5.1, measured
// 2026-09-18).
func TestEnvFileFormatThroughAnAliasOrAMergeKey(t *testing.T) {
	const file = "Q=\"x y\"\nA:1\n"
	rawOut := []string{"Q=\"x y\""}
	for _, tc := range []struct {
		name, head, entry string
		want              []string
		err               string
	}{
		{"an alias to raw", "x-fmt: &fmt raw\n", "{path: app.env, format: *fmt}", rawOut, ""},
		{"a merge of a mapping with raw", "x-base: &base {format: raw}\n", "{<<: *base, path: app.env}", rawOut, ""},
		{"an alias to null", "x-fmt: &fmt null\n", "{path: app.env, format: *fmt}", nil, "format must be a string"},
		{"a merge of a mapping with null", "x-base: &base {format: null}\n", "{<<: *base, path: app.env}", nil, "format must be a string"},
		{"an alias to a number", "x-fmt: &fmt 1\n", "{path: app.env, format: *fmt}", nil, "format must be a string"},
		{"an alias to foo", "x-fmt: &fmt foo\n", "{path: app.env, format: *fmt}", nil, `unsupported env_file format "foo"`},
		{"a merge with null and raw written beside it", "x-base: &base {format: null}\n", "{<<: *base, path: app.env, format: raw}", rawOut, ""},
		{"two merged mappings, raw first", "x-a: &a {format: raw}\nx-b: &b {format: null}\n", "{<<: [*a, *b], path: app.env}", rawOut, ""},
		{"two merged mappings, null first", "x-a: &a {format: null}\nx-b: &b {format: raw}\n", "{<<: [*a, *b], path: app.env}", nil, "format must be a string"},
		{"an alias to a number on a file not read", "x-fmt: &fmt 1\n", "{path: nope.env, required: false, format: *fmt}", nil, "format must be a string"},
		{"the key itself an alias, to raw", "x-k: &k format\n", "{path: app.env, *k : raw}", rawOut, ""},
		{"the key itself an alias, to null", "x-k: &k format\n", "{path: app.env, *k : null}", nil, "format must be a string"},
		{"no path and a null format: the path is asked for first", "", "{format: null}", nil, "has no path"},
		// With a list or a mapping the decode fails before the path is
		// looked at, in YAML's words; docker compose asks for the path
		// first (#1153, with `required: []`, which main has read this
		// way all along).
		{"no path and a list format: the decode fails first", "", "{format: []}", nil, "cannot unmarshal !!seq into string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(file), 0o644); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte(tc.head+"services:\n  web:\n    image: alpine\n    env_file:\n      - "+tc.entry+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var got Environment
			proj, err := Load(p)
			if err == nil {
				got, err = proj.Services["web"].ResolvedEnv()
			}
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v, %v", tc.err, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual([]string(got), tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A file listed more than once is read at every listing here — its values
// standing where they last appear, each listing's `required` and `format`
// counting — where docker compose (v5.5.1, measured 2026-09-18) reads it
// once, where first listed, with its last listing's attributes (#1152). A
// known difference: a row is red when opossum stops giving its answer, and
// `docker` records what docker compose gave when measured, for the reader
// of the failure (nothing here runs docker compose).
func TestAnEnvFileListedTwiceIsReadAtEveryListing(t *testing.T) {
	files := map[string]string{"app.env": "Q=\"x y\"\nA:1\n", "x.env": "A=1\nK=fromX\n", "a.env": "K=fromA\n", "p.env": "B=${A}\n"}
	for _, tc := range []struct {
		name, entries string
		want          []string // opossum
		err           string
		docker        string // docker compose's answer, where it differs
	}{
		{"the same file twice", "[app.env, app.env]", []string{"A=1", "Q=x y"}, "", ""},
		{"raw first, the default last", "[{path: app.env, format: raw}, app.env]", []string{"Q=x y", "A=1"}, "", "A=1 Q=x y (read once, as the default)"},
		{"the default first, raw last", "[app.env, {path: app.env, format: raw}]", []string{"A=1", "Q=\"x y\""}, "", `Q="x y" only (read once, raw)`},
		{"an unknown format first, the default last", "[{path: app.env, format: foo}, app.env]", nil, `unsupported env_file format "foo"`, "A=1 Q=x y (the first listing's format does not count)"},
		{"the default first, an unknown format last", "[app.env, {path: app.env, format: foo}]", nil, `unsupported env_file format "foo"`, ""},
		{"required first, required: false last, no file", "[nope.env, {path: nope.env, required: false}]", nil, "not found", "no error (the last listing's required counts)"},
		{"required: false first, required last, no file", "[{path: nope.env, required: false}, nope.env]", nil, "not found", ""},
		{"a file listed again", "[x.env, a.env, x.env]", []string{"A=1", "K=fromX"}, "", "K=fromA (kept at its first place)"},
		{"a later ${} sees the file", "[x.env, p.env, x.env]", []string{"A=1", "K=fromX", "B=1"}, "", ""},
		{"a file listed again is read again", "[p.env, x.env, p.env]", []string{"B=1", "A=1", "K=fromX"}, "", "B= (not read again after x.env)"},
		{"a path spelt two ways", "[{path: ./app.env, format: raw}, app.env]", []string{"Q=x y", "A=1"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetHostVars(t, "A", "B", "K", "Q")
			dir := t.TempDir()
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte("services:\n  web:\n    image: alpine\n    env_file: "+tc.entries+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			proj, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := proj.Services["web"].ResolvedEnv()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v, %v (docker compose: %s)", tc.err, got, err, tc.docker)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual([]string(got), tc.want) {
				t.Errorf("got %q, want %q (docker compose: %s)", got, tc.want, tc.docker)
			}
		})
	}
	// Across `-f` files the listings are joined and read in that order, each
	// with its own format (docker compose folds them: the second file's
	// format at the first file's place).
	for _, tc := range []struct {
		name  string
		first string
		want  []string
	}{
		{"the default in the first file, raw in the second", "b.yaml", []string{"A=1", "Q=\"x y\""}},
		{"raw in the first file, the default in the second", "a.yaml", []string{"Q=x y", "A=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetHostVars(t, "A", "Q")
			dir := t.TempDir()
			for name, body := range map[string]string{
				"app.env": files["app.env"],
				"b.yaml":  "services:\n  web:\n    image: alpine\n    env_file: [app.env]\n",
				"a.yaml":  "services:\n  web:\n    image: alpine\n    env_file:\n      - {path: app.env, format: raw}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			second := map[string]string{"b.yaml": "a.yaml", "a.yaml": "b.yaml"}[tc.first]
			proj, err := LoadFiles([]string{filepath.Join(dir, tc.first), filepath.Join(dir, second)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := proj.Services["web"].ResolvedEnv()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual([]string(got), tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
