package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `deploy.replicas` of a service taken from another file by `extends` is read after it is merged with the service
// that extends it, as docker compose reads it (#1560; `config -q`, every row measured, v5.5.1): a count that is no whole
// number (`true`, `1.5`, `~`, `.inf`, `.nan`) is a count docker compose casts in the merged service, so it passes where the
// extending service writes the count over it (`replicas: 3`, `!override`, `!reset`, or resets the whole of `deploy`), and
// is refused where it stays. A string that is no whole number (`"two"`, `""`) is refused in the file that writes it
// whatever is written over it. A list or a mapping is read where the count is overridden or reset, as a count that is no whole number
// is, and refused where it stays and where a scalar is written over it without a tag, as docker compose refuses the pair (#1776).
func TestTheReplicasOfAnExtendedServiceAreAskedAfterItIsMerged(t *testing.T) {
	for _, tc := range []struct {
		replicas, own, want string
	}{
		{"true", "deploy: {replicas: 3}", "read"},
		{"true", "deploy: {replicas: !override 3}", "read"},
		{"true", "deploy: !override {replicas: 3}", "read"},
		{"true", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"true", "deploy: !reset null", "read"},
		{"true", "deploy: {replicas: !reset null}", "read"},
		{"true", "deploy: {replicas: true}", "REFUSE"},
		{"true", "x-a: 1", "REFUSE"},
		{"1.5", "deploy: {replicas: 3}", "read"},
		{"1.5", "deploy: {replicas: !override 3}", "read"},
		{"1.5", "deploy: !override {replicas: 3}", "read"},
		{"1.5", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"1.5", "deploy: !reset null", "read"},
		{"1.5", "deploy: {replicas: !reset null}", "read"},
		{"1.5", "deploy: {replicas: true}", "REFUSE"},
		{"1.5", "x-a: 1", "REFUSE"},
		{"~", "deploy: {replicas: 3}", "read"},
		{"~", "deploy: {replicas: !override 3}", "read"},
		{"~", "deploy: !override {replicas: 3}", "read"},
		{"~", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"~", "deploy: !reset null", "read"},
		{"~", "deploy: {replicas: !reset null}", "read"},
		{"~", "deploy: {replicas: true}", "REFUSE"},
		{"~", "x-a: 1", "REFUSE"},
		{".inf", "deploy: {replicas: 3}", "read"},
		{".inf", "deploy: {replicas: !override 3}", "read"},
		{".inf", "deploy: !override {replicas: 3}", "read"},
		{".inf", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{".inf", "deploy: !reset null", "read"},
		{".inf", "deploy: {replicas: !reset null}", "read"},
		{".inf", "deploy: {replicas: true}", "REFUSE"},
		{".inf", "x-a: 1", "REFUSE"},
		{".nan", "deploy: {replicas: 3}", "read"},
		{".nan", "deploy: {replicas: !override 3}", "read"},
		{".nan", "deploy: !override {replicas: 3}", "read"},
		{".nan", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{".nan", "deploy: !reset null", "read"},
		{".nan", "deploy: {replicas: !reset null}", "read"},
		{".nan", "deploy: {replicas: true}", "REFUSE"},
		{".nan", "x-a: 1", "REFUSE"},
		{"\"two\"", "deploy: {replicas: 3}", "REFUSE"},
		{"\"two\"", "deploy: {replicas: !override 3}", "REFUSE"},
		{"\"two\"", "deploy: !override {replicas: 3}", "REFUSE"},
		{"\"two\"", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"\"two\"", "deploy: !reset null", "REFUSE"},
		{"\"two\"", "deploy: {replicas: !reset null}", "REFUSE"},
		{"\"two\"", "deploy: {replicas: true}", "REFUSE"},
		{"\"two\"", "x-a: 1", "REFUSE"},
		{"\"\"", "deploy: {replicas: 3}", "REFUSE"},
		{"\"\"", "deploy: {replicas: !override 3}", "REFUSE"},
		{"\"\"", "deploy: !override {replicas: 3}", "REFUSE"},
		{"\"\"", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"\"\"", "deploy: !reset null", "REFUSE"},
		{"\"\"", "deploy: {replicas: !reset null}", "REFUSE"},
		{"\"\"", "deploy: {replicas: true}", "REFUSE"},
		{"\"\"", "x-a: 1", "REFUSE"},
		{"\"2\"", "deploy: {replicas: 3}", "read"},
		{"\"2\"", "deploy: {replicas: !override 3}", "read"},
		{"\"2\"", "deploy: !override {replicas: 3}", "read"},
		{"\"2\"", "deploy: {resources: {limits: {cpus: \"1\"}}}", "read"},
		{"\"2\"", "deploy: !reset null", "read"},
		{"\"2\"", "deploy: {replicas: !reset null}", "read"},
		{"\"2\"", "deploy: {replicas: true}", "REFUSE"},
		{"\"2\"", "x-a: 1", "read"},
		{"2", "deploy: {replicas: 3}", "read"},
		{"2", "deploy: {replicas: !override 3}", "read"},
		{"2", "deploy: !override {replicas: 3}", "read"},
		{"2", "deploy: {resources: {limits: {cpus: \"1\"}}}", "read"},
		{"2", "deploy: !reset null", "read"},
		{"2", "deploy: {replicas: !reset null}", "read"},
		{"2", "deploy: {replicas: true}", "REFUSE"},
		{"2", "x-a: 1", "read"},
		{"2.0", "deploy: {replicas: 3}", "read"},
		{"2.0", "deploy: {replicas: !override 3}", "read"},
		{"2.0", "deploy: !override {replicas: 3}", "read"},
		{"2.0", "deploy: {resources: {limits: {cpus: \"1\"}}}", "read"},
		{"2.0", "deploy: !reset null", "read"},
		{"2.0", "deploy: {replicas: !reset null}", "read"},
		{"2.0", "deploy: {replicas: true}", "REFUSE"},
		{"2.0", "x-a: 1", "read"},
		{"[1]", "deploy: {replicas: 3}", "REFUSE"},
		{"[1]", "deploy: {replicas: !override 3}", "read"},
		{"[1]", "deploy: !override {replicas: 3}", "read"},
		{"[1]", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"[1]", "deploy: !reset null", "read"},
		{"[1]", "deploy: {replicas: !reset null}", "read"},
		{"[1]", "deploy: {replicas: true}", "REFUSE"},
		{"[1]", "x-a: 1", "REFUSE"},
		{"{a: 1}", "deploy: {replicas: 3}", "REFUSE"},
		{"{a: 1}", "deploy: {replicas: !override 3}", "read"},
		{"{a: 1}", "deploy: !override {replicas: 3}", "read"},
		{"{a: 1}", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"{a: 1}", "deploy: !reset null", "read"},
		{"{a: 1}", "deploy: {replicas: !reset null}", "read"},
		{"{a: 1}", "deploy: {replicas: true}", "REFUSE"},
		{"{a: 1}", "x-a: 1", "REFUSE"},
		{"-1", "deploy: {replicas: 3}", "read"},
		{"-1", "deploy: {replicas: !override 3}", "read"},
		{"-1", "deploy: !override {replicas: 3}", "read"},
		{"-1", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"-1", "deploy: !reset null", "read"},
		{"-1", "deploy: {replicas: !reset null}", "read"},
		{"-1", "deploy: {replicas: true}", "REFUSE"},
		{"-1", "x-a: 1", "REFUSE"},
		{"\"-1\"", "deploy: {replicas: 3}", "read"},
		{"\"-1\"", "deploy: {replicas: !override 3}", "read"},
		{"\"-1\"", "deploy: !override {replicas: 3}", "read"},
		{"\"-1\"", "deploy: {resources: {limits: {cpus: \"1\"}}}", "REFUSE"},
		{"\"-1\"", "deploy: !reset null", "read"},
		{"\"-1\"", "deploy: {replicas: !reset null}", "read"},
		{"\"-1\"", "deploy: {replicas: true}", "REFUSE"},
		{"\"-1\"", "x-a: 1", "REFUSE"},
	} {
		t.Run(tc.replicas+" / "+tc.own, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "f1.yaml"), []byte("services:\n  base:\n    image: x\n    deploy:\n      replicas: "+tc.replicas+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte("services:\n  b:\n    extends: {file: f1.yaml, service: base}\n    "+tc.own+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(main)
			got := "read"
			if err != nil {
				got = "REFUSE"
			}
			if got != tc.want {
				t.Errorf("replicas %s under %s: %s, want %s (err %v)", tc.replicas, tc.own, got, tc.want, err)
			}
		})
	}
}

// A refusal of a count that stays names the file it came from, and says so when that service extends another in turn.
func TestTheRefusalOfAnExtendedReplicasNamesTheFileItCameFrom(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
		not   string
	}{
		{"one hop", map[string]string{"f1.yaml": "services:\n  base:\n    image: x\n    deploy:\n      replicas: true\n"}, "f1.yaml", "or a file it extends"},
		{"two hops", map[string]string{
			"f1.yaml": "services:\n  base:\n    extends: {file: f2.yaml, service: deep}\n",
			"f2.yaml": "services:\n  deep:\n    image: x\n    deploy:\n      replicas: true\n"}, "or a file it extends", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte("services:\n  b:\n    extends: {file: f1.yaml, service: base}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(main)
			if err == nil {
				t.Fatal("a count that stays and is no whole number was read")
			}
			msg := err.Error()
			if !strings.Contains(msg, "f1.yaml") || !strings.Contains(msg, "services.base.deploy.replicas") || !strings.Contains(msg, tc.want) {
				t.Errorf("the refusal is %q, want it to name f1.yaml and services.base.deploy.replicas and say %q", msg, tc.want)
			}
			if tc.not != "" && strings.Contains(msg, tc.not) {
				t.Errorf("the refusal is %q, which says %q of a value in the file it names", msg, tc.not)
			}
		})
	}
}

// Through two files: the count is asked once, of the service the file takes, after every hop is merged — so the
// service of the middle, or the one on top, writing it over is enough (measured, v5.5.1), and a hop in the middle is not
// asked on its own.
func TestTheReplicasOfAServiceExtendedThroughTwoFilesAreAskedAfterAllHopsAreMerged(t *testing.T) {
	far := "services:\n  deep:\n    image: x\n    deploy:\n      replicas: true\n"
	mid := func(extra string) string {
		return "services:\n  base:\n    extends: {file: f2.yaml, service: deep}\n" + extra
	}
	top := func(extra string) string {
		return "services:\n  b:\n    extends: {file: f1.yaml, service: base}\n" + extra
	}
	for _, tc := range []struct {
		name, mid, top string
		refused        bool
	}{
		{"nothing writes it over", mid(""), top(""), true},
		{"the one on top writes it over", mid(""), top("    deploy: {replicas: 3}\n"), false},
		{"the one in the middle writes it over", mid("    deploy: {replicas: 3}\n"), top(""), false},
		{"the one in the middle resets deploy", mid("    deploy: !reset null\n"), top(""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{"f1.yaml": tc.mid, "f2.yaml": far, "compose.yaml": tc.top} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Load(filepath.Join(dir, "compose.yaml"))
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// A service the extended file itself extends in turn (a chain inside that file) is asked the same way: after the merge, and
// not on its own in the file that holds it. A count written over in the middle or on top is read; one that stays is refused (#1560;
// measured, v5.5.1).
func TestTheReplicasOfAChainInAnExtendedFileAreAskedAfterTheMerge(t *testing.T) {
	for _, tc := range []struct {
		name, f1, top, f2 string
		refused           bool
	}{
		{"chain, true, written over", "services:\n    common:\n      image: x\n      deploy:\n        replicas: true\n    base:\n      extends: common\n", "    deploy: {replicas: 3}\n", "", false},
		{"chain, 1.5, written over", "services:\n    common:\n      image: x\n      deploy:\n        replicas: 1.5\n    base:\n      extends: common\n", "    deploy: {replicas: 3}\n", "", false},
		{"chain, .inf, written over", "services:\n    common:\n      image: x\n      deploy:\n        replicas: .inf\n    base:\n      extends: common\n", "    deploy: {replicas: 3}\n", "", false},
		{"chain, true, stays", "services:\n    common:\n      image: x\n      deploy:\n        replicas: true\n    base:\n      extends: common\n", "", "", true},
		{"chain, 1.5, deploy reset", "services:\n    common:\n      image: x\n      deploy:\n        replicas: 1.5\n    base:\n      extends: common\n", "    deploy: !reset null\n", "", false},
		{"chain, true, the middle writes it over", "services:\n    common:\n      image: x\n      deploy:\n        replicas: true\n    base:\n      extends: common\n      deploy: {replicas: 2}\n", "", "", false},
		{"two files, a chain in the far one, written over", "services:\n  base:\n    extends: {file: f2.yaml, service: deep}\n", "    deploy: {replicas: 3}\n", "services:\n  c2:\n    image: x\n    deploy:\n      replicas: true\n  deep:\n    extends: c2\n", false},
		{"two files, a chain in the far one, stays", "services:\n  base:\n    extends: {file: f2.yaml, service: deep}\n", "", "services:\n  c2:\n    image: x\n    deploy:\n      replicas: true\n  deep:\n    extends: c2\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"f1.yaml": tc.f1, "compose.yaml": "services:\n  b:\n    extends: {file: f1.yaml, service: base}\n" + tc.top}
			if tc.f2 != "" {
				files["f2.yaml"] = tc.f2
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Load(filepath.Join(dir, "compose.yaml"))
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// A load that goes on past a fault (the commands that take a project down) keeps the refusal of a count that stays, as a
// warning, and does not stop: the value is read as the file says.
func TestTheRefusalOfAnExtendedReplicasIsKeptAsAWarningWhereTheLoadGoesOn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f1.yaml"), []byte("services:\n  base:\n    image: x\n    deploy:\n      replicas: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(main, []byte("services:\n  b:\n    extends: {file: f1.yaml, service: base}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFilesEnvDirSoft([]string{main}, nil, dir)
	if err != nil {
		t.Fatalf("the load stopped: %v", err)
	}
	if err := p.CheckValueFaults(); err == nil || !strings.Contains(err.Error(), "deploy.replicas") {
		t.Fatalf("the fault kept is %v, want the refusal of the count that stays", err)
	}
}
