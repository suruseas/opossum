package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A byte size or a duration that docker compose casts of a key opossum reads itself or not at all, and of an entry of a list (v5.5.1 `config`, every row
// measured, rc 1 or 0), is cast the same here: `mem_limit` and the memory of `deploy.resources` (which opossum reads, and trims a space from that docker
// compose refuses), `healthcheck.start_interval` (not read at all) and the `rate` of a `blkio_config` device (#1497). What a later file or an extending
// service writes over a bad value makes the file fine; a bad entry of a list stays whatever a later file writes; a service of an extended file that
// nothing takes is not asked; a service of the file is, a profile that leaves it out or not.
func TestAByteSizeOrDurationIsCastAsDockerComposeCastsIt(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n"
	const memBad = svc + "    mem_limit: \" 1g\"\n"
	const memGood = "services:\n  s:\n    mem_limit: 1g\n"
	const rateBad = svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"abc\"}]}\n"
	const rateGood = "services:\n  s:\n    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1m\"}]}\n"
	const baseRateBad = "services:\n  base:\n    image: x\n    blkio_config: {device_read_bps: [{path: /dev/sda, rate: abc}]}\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"mem_limit with a space before the number", map[string]string{"c.yaml": memBad}, []string{"c.yaml"}, true},
		{"mem_limit with a space after the number", map[string]string{"c.yaml": svc + "    mem_limit: \"1g \"\n"}, []string{"c.yaml"}, true},
		{"mem_limit with a space on both sides", map[string]string{"c.yaml": svc + "    mem_limit: \" 512m \"\n"}, []string{"c.yaml"}, true},
		{"mem_limit as a size", map[string]string{"c.yaml": svc + "    mem_limit: 1g\n"}, []string{"c.yaml"}, false},
		{"limits.memory with a space", map[string]string{"c.yaml": svc + "    deploy:\n      resources:\n        limits:\n          memory: \" 1g\"\n"}, []string{"c.yaml"}, true},
		{"reservations.memory with a space after", map[string]string{"c.yaml": svc + "    deploy:\n      resources:\n        reservations:\n          memory: \"1g \"\n"}, []string{"c.yaml"}, true},
		{"limits.memory bad, a later file writes a good one", map[string]string{"c.yaml": svc + "    deploy:\n      resources:\n        limits:\n          memory: \" 1g\"\n", "o.yaml": "services:\n  s:\n    deploy:\n      resources:\n        limits:\n          memory: 1g\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"mem_limit bad, a later file writes a good one", map[string]string{"c.yaml": memBad, "o.yaml": memGood}, []string{"c.yaml", "o.yaml"}, false},
		{"mem_limit good, a later file writes a bad one", map[string]string{"c.yaml": memGood + "    image: x\n", "o.yaml": "services:\n  s:\n    mem_limit: \" 1g\"\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"start_interval that is no duration", map[string]string{"c.yaml": svc + "    healthcheck: {test: [CMD, \"true\"], start_interval: \"abc\"}\n"}, []string{"c.yaml"}, true},
		{"start_interval with no unit", map[string]string{"c.yaml": svc + "    healthcheck: {test: [CMD, \"true\"], start_interval: \"10\"}\n"}, []string{"c.yaml"}, true},
		{"start_interval as a number", map[string]string{"c.yaml": svc + "    healthcheck: {test: [CMD, \"true\"], start_interval: 10}\n"}, []string{"c.yaml"}, true},
		{"start_interval as a duration", map[string]string{"c.yaml": svc + "    healthcheck: {test: [CMD, \"true\"], start_interval: \"10s\"}\n"}, []string{"c.yaml"}, false},
		{"the extended file's service is bad, the extender keeps it", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    mem_limit: \" 1g\"\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n"}, []string{"c.yaml"}, true},
		{"the extended file's service is bad, the extender writes a good one", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    mem_limit: \" 1g\"\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n    mem_limit: 1g\n"}, []string{"c.yaml"}, false},
		{"the extended file's start_interval is bad, the extender writes a good one", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    healthcheck: {test: [CMD, \"true\"], start_interval: \"abc\"}\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n    healthcheck: {start_interval: \"5s\"}\n"}, []string{"c.yaml"}, false},
		{"a service of the extended file that nothing takes", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    mem_limit: \" 1g\"\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n"}, []string{"c.yaml"}, false},
		{"a service of the file that a profile leaves out", map[string]string{"c.yaml": svc + "  o:\n    image: x\n    mem_limit: \" 1g\"\n    profiles: [p]\n"}, []string{"c.yaml"}, true},
		{"a service of the file that nothing extends", map[string]string{"c.yaml": svc + "  b:\n    image: x\n    mem_limit: \" 1g\"\n"}, []string{"c.yaml"}, true},
		{"a service of the extended file that nothing takes, with a bad blkio rate", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"abc\"}]}\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n"}, []string{"c.yaml"}, false},
		{"blkio rate that is no size", map[string]string{"c.yaml": rateBad}, []string{"c.yaml"}, true},
		{"blkio write rate that is no size", map[string]string{"c.yaml": svc + "    blkio_config: {device_write_bps: [{path: /dev/sda, rate: \"x\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio rate as a size", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1m\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate bad in the first file, a later file writes a good one", map[string]string{"c.yaml": rateBad, "o.yaml": rateGood}, []string{"c.yaml", "o.yaml"}, true},
		{"blkio rate bad in the first file, a later file resets the block", map[string]string{"c.yaml": rateBad, "o.yaml": "services:\n  s:\n    blkio_config: !reset {}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"blkio rate bad in the first file, a later file overrides the block with a good one", map[string]string{"c.yaml": rateBad, "o.yaml": "services:\n  s:\n    blkio_config: !override {device_read_bps: [{path: /dev/sda, rate: \"1m\"}]}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"blkio rate bad in the first file, a later file resets the list", map[string]string{"c.yaml": rateBad, "o.yaml": "services:\n  s:\n    blkio_config: {device_read_bps: !reset []}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"blkio rate bad in the first file, a later file overrides the list with a good one", map[string]string{"c.yaml": rateBad, "o.yaml": "services:\n  s:\n    blkio_config: {device_read_bps: !override [{path: /dev/sda, rate: \"1m\"}]}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"blkio write rate bad, a later file overrides the block with another key", map[string]string{"c.yaml": svc + "    blkio_config: {device_write_bps: [{path: /dev/sda, rate: x}]}\n", "o.yaml": "services:\n  s:\n    blkio_config: !override {weight: 10}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"blkio rate bad in the extended service, the extender resets the block", map[string]string{"base.yaml": baseRateBad, "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n    blkio_config: !reset {}\n"}, []string{"c.yaml"}, false},
		{"blkio rate bad in the extended service, the extender overrides the block with a good one", map[string]string{"base.yaml": baseRateBad, "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n    blkio_config: !override {device_read_bps: [{path: /dev/sda, rate: \"1m\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate bad in the extended service, the extender keeps it", map[string]string{"base.yaml": baseRateBad, "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: base}\n"}, []string{"c.yaml"}, true},
		{"blkio rate bad first in a list of two", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: abc}, {path: /dev/sdb, rate: \"1m\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio rate bad last in a list of two", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1m\"}, {path: /dev/sdb, rate: abc}]}\n"}, []string{"c.yaml"}, true},
		{"blkio rate empty", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio read iops rate that is no size", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_iops: [{path: /dev/sda, rate: abc}]}\n"}, []string{"c.yaml"}, true},
		{"blkio write iops rate with a space", map[string]string{"c.yaml": svc + "    blkio_config: {device_write_iops: [{path: /dev/sda, rate: \" 1m\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio iops rate with a unit docker compose has not", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_iops: [{path: /dev/sda, rate: 1ib}]}\n"}, []string{"c.yaml"}, true},
		{"blkio iops rate empty", map[string]string{"c.yaml": svc + "    blkio_config: {device_write_iops: [{path: /dev/sda, rate: \"\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio rate: a bare number as a string", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"100\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: a size with the unit mb", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1mb\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: a size with a space before the unit", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1 m\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: a size with the unit k", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1k\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: a size with the unit kib", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1kib\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: zero as a string", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"0\"}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: a bare number", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: 100}]}\n"}, []string{"c.yaml"}, false},
		{"blkio rate: a duration that is no size (seconds)", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1s\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio rate: a duration that is no size (hours)", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1h\"}]}\n"}, []string{"c.yaml"}, true},
		{"blkio iops rate as a size is taken", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_iops: [{path: /dev/sda, rate: \"1m\"}]}\n"}, []string{"c.yaml"}, false},
		{"start_interval as a float", map[string]string{"c.yaml": svc + "    healthcheck: {test: [CMD, \"true\"], start_interval: 1.5}\n"}, []string{"c.yaml"}, true},
		{"start_interval as a boolean", map[string]string{"c.yaml": svc + "    healthcheck: {test: [CMD, \"true\"], start_interval: true}\n"}, []string{"c.yaml"}, true},
		{"mem_limit with a unit only opossum reads", map[string]string{"c.yaml": svc + "    mem_limit: \"1ib\"\n"}, []string{"c.yaml"}, true},
		{"mem_limit with another unit only opossum reads", map[string]string{"c.yaml": svc + "    mem_limit: \"1bib\"\n"}, []string{"c.yaml"}, true},
		{"blkio rate good in the first file, a later file writes a bad one", map[string]string{"c.yaml": svc + "    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"1m\"}]}\n", "o.yaml": "services:\n  s:\n    blkio_config: {device_read_bps: [{path: /dev/sda, rate: \"abc\"}]}\n"}, []string{"c.yaml", "o.yaml"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if !tc.refused {
				return
			}
			// What an older version started is taken down by the commands that read a project soft, so a refusal added here must not stop them: it is kept
			// for them to say, and the read goes on (the same as every other refusal of a value, #1471).
			project, err := LoadFilesEnvDirSoft(paths, nil, "")
			if err != nil {
				t.Fatalf("the soft read stopped: %v", err)
			}
			if project.CheckValueFaults() == nil {
				t.Errorf("the soft read kept no refusal")
			}
		})
	}
}
