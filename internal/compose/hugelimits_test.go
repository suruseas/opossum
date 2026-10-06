package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A memory limit and a CPU count past what container takes are the most it takes (#1746, #1744). docker compose reads them (`config -q`
// is 0 for every row, v5.5.1), and an upper limit that large is no limit. The count of bytes was cut to an int64 on the way to MiB, which
// wraps past 2^63 and gave `-m -8796093022207M`; a count of CPUs past an int became the largest int. container 1.5.0, measured: `-m` up
// to 8796093022207M (a MiB short of 2^63 bytes) is read, 8796093022208M drops the runtime's connection and more digits stop the CLI; `-c` up to
// 100000000 is read (as the host's count), 1000000000 and 2147483647 cannot be written to the cgroup and 9223372036854775807 drops the connection.
func TestAMemoryLimitAndACPUCountPastWhatContainerTakesAreTheMostItTakes(t *testing.T) {
	for _, tc := range []struct{ key, value, mem, cpu string }{
		{"mem_limit", "512m", "512M", ""},
		{"mem_limit", "1500000", "2M", ""},   // rounded up to a MiB
		{"mem_limit", "1048576.5", "1M", ""}, // the bytes are cut to a whole number, as docker compose cuts them
		{"mem_limit", "1.0000001m", "1M", ""},
		{"mem_limit", `"0.5"`, "", ""}, // less than a byte is no limit
		{"mem_limit", "8796093022206m", "8796093022206M", ""},
		{"mem_limit", "8796093022207m", "8796093022207M", ""},
		{"mem_limit", "8796093022208m", "8796093022207M", ""},
		{"mem_limit", "9223372036854775807", "8796093022207M", ""},
		{"mem_limit", "9223372036854775808", "8796093022207M", ""},
		{"mem_limit", "18446744073709551615", "8796093022207M", ""},
		{"mem_limit", "99999999999999999999", "8796093022207M", ""},
		{"mem_limit", `"9223372036854775807"`, "8796093022207M", ""},
		{"mem_limit", "8589934592g", "8796093022207M", ""},
		{"mem_limit", "8388608t", "8796093022207M", ""},
		{"cpus", "4", "", "4"},
		{"cpus", "1.5", "", "2"}, // rounded up to a whole CPU
		{"cpus", "0.1", "", "1"},
		{"cpus", "99999000", "", "99999000"},
		{"cpus", "100000000", "", "100000000"},
		{"cpus", "1e8", "", "100000000"},
		{"cpus", "100000001", "", "100000000"},
		{"cpus", "99999999999", "", "100000000"},
		{"cpus", "9223372036854775807", "", "100000000"},
		{"cpus", "99999999999999999999", "", "100000000"},
		{"cpus", "3.4028235e38", "", "100000000"},
		{"cpus", `"99999999999999999999"`, "", "100000000"},
		{"deploy.limits.memory", `"9223372036854775807"`, "8796093022207M", ""},
		{"deploy.limits.cpus", `"99999999999"`, "", "100000000"},
	} {
		t.Run(tc.key+" "+tc.value, func(t *testing.T) {
			body := "services:\n  a:\n    image: alpine\n"
			switch tc.key {
			case "deploy.limits.memory":
				body += "    deploy:\n      resources:\n        limits:\n          memory: " + tc.value + "\n"
			case "deploy.limits.cpus":
				body += "    deploy:\n      resources:\n        limits:\n          cpus: " + tc.value + "\n"
			default:
				body += "    " + tc.key + ": " + tc.value + "\n"
			}
			path := filepath.Join(t.TempDir(), "compose.yaml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			mem, cpu, err := p.Services["a"].Resources()
			if err != nil {
				t.Fatalf("Resources: %v", err)
			}
			if mem != tc.mem || cpu != tc.cpu {
				t.Errorf("-m %q -c %q, want -m %q -c %q", mem, cpu, tc.mem, tc.cpu)
			}
		})
	}
}
