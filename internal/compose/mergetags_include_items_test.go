package compose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A `!reset` or `!override` written on a network, volume, config or secret by its name is read over an earlier `-f` file (the item is gone, or stands
// whole) and not over an `include`: docker compose merges the item with the included one as it is written there, and still reads a tag on a key under
// it (#1735; every row is docker compose v5.5.1's `config --format json` for the same files; the volume is the one the service mounts by its long form).
func TestATagOnANetworkVolumeConfigOrSecretOverAnIncludeAndOverAnEarlierFile(t *testing.T) {
	const inc = "networks:\n  n: {driver: bridge, labels: {a: b}}\nvolumes:\n  v: {driver: local, name: vv, labels: {a: b}}\nconfigs:\n  c: {content: hello}\nsecrets:\n  k: {file: ./k.txt}\n"
	const svc = "services:\n  web:\n    image: x\n    networks: [n]\n    volumes:\n      - {type: volume, source: v, target: /v}\n    configs: [c]\n    secrets: [k]\n"
	for _, tc := range []struct {
		name, over string
		include    bool   // the file is the parent that includes the other, and not a later `-f` file
		want       string // what the project holds: n's labels, c's content, k's file; or "error"
	}{
		{"include: network !reset", "networks:\n  n: !reset null\n", true, "n=a=b c=hello k=k.txt"},
		{"include: network !override", "networks:\n  n: !override {labels: {c: d}}\n", true, "n=a=b,c=d c=hello k=k.txt"},
		{"include: network labels !reset", "networks:\n  n:\n    labels: !reset null\n", true, "n= c=hello k=k.txt"},
		{"include: network labels !override", "networks:\n  n:\n    labels: !override {c: d}\n", true, "n=c=d c=hello k=k.txt"},
		{"include: volume !reset", "volumes:\n  v: !reset null\n", true, "n=a=b c=hello k=k.txt"},
		{"include: volume !override", "volumes:\n  v: !override {external: false}\n", true, "n=a=b c=hello k=k.txt"},
		{"include: config !reset", "configs:\n  c: !reset null\n", true, "n=a=b c=hello k=k.txt"},
		{"include: config !override", "configs:\n  c: !override {content: other}\n", true, "n=a=b c=other k=k.txt"},
		{"include: config content !reset", "configs:\n  c:\n    content: !reset null\n    file: ./k.txt\n", true, "n=a=b c= k=k.txt"},
		{"include: secret !reset", "secrets:\n  k: !reset null\n", true, "n=a=b c=hello k=k.txt"},
		{"include: secret !override", "secrets:\n  k: !override {file: ./k2.txt}\n", true, "n=a=b c=hello k=k2.txt"},
		{"include: secret file !override", "secrets:\n  k:\n    file: !override ./k2.txt\n", true, "n=a=b c=hello k=k2.txt"},
		// What a replace and a merge leave different: the included volume keeps its `name`, and a config of another form is refused where the two forms meet.
		{"include: volume !override keeps the included name", "volumes:\n  v: !override {labels: {c: d}}\n", true, "n=a=b c=hello k=k.txt v=vv"},
		{"include: config !override with another form", "configs:\n  c: !override {file: ./k2.txt}\n", true, "error"},
		{"-f: network !reset", "networks:\n  n: !reset null\n", false, "error"},
		{"-f: network !override", "networks:\n  n: !override {labels: {c: d}}\n", false, "n=c=d c=hello k=k.txt"},
		{"-f: network labels !reset", "networks:\n  n:\n    labels: !reset null\n", false, "n= c=hello k=k.txt"},
		{"-f: volume !reset", "volumes:\n  v: !reset null\n", false, "error"},
		{"-f: config !reset", "configs:\n  c: !reset null\n", false, "error"},
		{"-f: config !override", "configs:\n  c: !override {content: other}\n", false, "n=a=b c=other k=k.txt"},
		{"-f: secret !reset", "secrets:\n  k: !reset null\n", false, "error"},
		{"-f: secret !override", "secrets:\n  k: !override {file: ./k2.txt}\n", false, "n=a=b c=hello k=k2.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{"k.txt": "s", "k2.txt": "s2"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.include {
				write("inc.yaml", inc)
				write("p.yaml", "include:\n  - inc.yaml\n"+svc+tc.over)
				paths = []string{filepath.Join(dir, "p.yaml")}
			} else {
				write("base.yaml", inc+svc)
				write("p.yaml", tc.over)
				paths = []string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "p.yaml")}
			}
			project, err := LoadFiles(paths, nil)
			if tc.want == "error" {
				if err == nil {
					t.Fatal("docker compose refuses it (the service refers to what the tag took out), and it was read")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			labels := append([]string(nil), project.Networks["n"].Labels...)
			sort.Strings(labels)
			content := ""
			if c, ok := project.Configs["c"]; ok && c.Content != nil {
				content = *c.Content
			}
			got := "n=" + strings.Join(labels, ",") + " c=" + content + " k=" + filepath.Base(project.Secrets["k"].File)
			if strings.HasSuffix(tc.want, " v=vv") {
				got += " v=" + project.Volumes["v"].Name
			}
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
