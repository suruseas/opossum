package compose

import (
	"reflect"
	"testing"
)

func TestShellSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`sh -c "echo hi"`, []string{"sh", "-c", "echo hi"}},
		{`echo hello world`, []string{"echo", "hello", "world"}},
		{`sh -c 'echo a; sleep 1; echo b'`, []string{"sh", "-c", "echo a; sleep 1; echo b"}},
		{`  spaced   out  `, []string{"spaced", "out"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{`nginx -g "daemon off;"`, []string{"nginx", "-g", "daemon off;"}},
		{`echo "a\"b"`, []string{"echo", `a"b`}},         // escaped quote inside double quotes
		{`echo it\ works`, []string{"echo", "it works"}}, // escaped space joins a word
		{``, nil},
		{`   `, nil},
	}
	for _, c := range cases {
		got, err := shellSplit(c.in)
		if err != nil {
			t.Errorf("shellSplit(%q) error: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("shellSplit(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestShellSplitUnbalanced(t *testing.T) {
	for _, in := range []string{`echo "unterminated`, `echo 'nope`, `trailing\`} {
		if _, err := shellSplit(in); err == nil {
			t.Errorf("shellSplit(%q) should error on unbalanced input", in)
		}
	}
}

func TestLoadCommandStringIsShellSplit(t *testing.T) {
	// The exact case that broke on the real runtime (issue #20): a string command
	// must reach the runtime as argv, not one opaque argument.
	p, err := Load(writeTemp(t, `
services:
  migrate:
    image: alpine
    command: sh -c "echo 'running'; sleep 1"
  web:
    image: web
    command: ["nginx", "-g", "daemon off;"]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := []string(p.Services["migrate"].Command); !reflect.DeepEqual(got, []string{"sh", "-c", "echo 'running'; sleep 1"}) {
		t.Errorf("string command = %#v, want shell-split argv", got)
	}
	// List form is still taken verbatim.
	if got := []string(p.Services["web"].Command); !reflect.DeepEqual(got, []string{"nginx", "-g", "daemon off;"}) {
		t.Errorf("list command = %#v, want verbatim", got)
	}
}

func TestLoadEntrypointParsing(t *testing.T) {
	// entrypoint shares command's parsing: string form is shell-split, list form
	// is verbatim.
	p, err := Load(writeTemp(t, `
services:
  a:
    image: x
    entrypoint: /docker-entrypoint.sh --debug
    command: serve
  b:
    image: y
    entrypoint: ["/bin/tini", "--"]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := []string(p.Services["a"].Entrypoint); !reflect.DeepEqual(got, []string{"/docker-entrypoint.sh", "--debug"}) {
		t.Errorf("string entrypoint = %#v, want shell-split", got)
	}
	if got := []string(p.Services["a"].Command); !reflect.DeepEqual(got, []string{"serve"}) {
		t.Errorf("command alongside entrypoint = %#v", got)
	}
	if got := []string(p.Services["b"].Entrypoint); !reflect.DeepEqual(got, []string{"/bin/tini", "--"}) {
		t.Errorf("list entrypoint = %#v, want verbatim", got)
	}
}

func TestLoadCommandUnbalancedQuoteFails(t *testing.T) {
	_, err := Load(writeTemp(t, `
services:
  db:
    image: postgres
    command: sh -c "oops
`))
	if err == nil {
		t.Fatal("expected Load to fail on an unbalanced quote in a string command")
	}
}
