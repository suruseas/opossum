package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// `mem_limit` and `shm_size` are read as docker compose v5.5.1 reads them where a size is written as a
// bare number: the number YAML reads (`04000000` is 1048576, `0x100000` and `0o4000000` the same,
// `010` is eight), where the digits were read as text — `04000000` was four million bytes, a memory
// limit four times the one docker compose gives, and `0x100000` was refused. A size written as a string
// (`"04000000"` is four million, `"512m"`, `"1.5g"`) is read as it was. The answers are docker
// compose's (measured with `config`; memory in MiB rounded up, as the runtime takes it, shm in bytes).
// Not here, and read as they were: a bare number with a fraction or an exponent (`1.5`, `1e6`), which
// docker compose drops with no word, leaving no limit; a size padded with blanks, which it refuses.
func TestBareSizesAreTheNumberYAMLReads(t *testing.T) {
	for _, tc := range []struct {
		yaml, mem, shm string
		noShm          bool // not asked for shm_size: `0` is no size there, and opossum refuses it (a separate difference)
	}{
		{"04000000", "1M", "1048576", false},
		{"0x100000", "1M", "1048576", false},
		{"0X100000", "1M", "1048576", false},
		{"0o4000000", "1M", "1048576", false},
		{"0b100000000000000000000", "1M", "1048576", false},
		{"1_048_576", "1M", "1048576", false},
		{"1048576", "1M", "1048576", false},
		{"+1048576", "1M", "1048576", false},
		{"1_5", "1M", "15", false},
		{"1_", "1M", "1", false},
		{"0x_10", "1M", "16", false},
		{"010", "1M", "8", false},
		{"0x10", "1M", "16", false},
		{"00", "", "", true},
		{"0", "", "", true},
		{"512", "1M", "512", false},
		{"1", "1M", "1", false},
		{"!!int \"010\"", "1M", "8", false},
		{"!!int 010", "1M", "8", false},
		{"!!str 010", "1M", "10", false},
		{"0x100000000", "4096M", "4294967296", false},
		{"040000000000", "4096M", "4294967296", false},
		{"0x400000", "4M", "4194304", false},
		{"0o20000000", "4M", "4194304", false},
		{"010000000", "2M", "2097152", false},
		{"0x1F00000", "31M", "32505856", false},
		{"\"04000000\"", "4M", "4000000", false},
		{"\"512m\"", "512M", "536870912", false},
		{"\"1.5g\"", "1536M", "1610612736", false},
		{"\"1gb\"", "1024M", "1073741824", false},
		{"\"1G\"", "1024M", "1073741824", false},
		{"\"1048576\"", "1M", "1048576", false},
		{"\"010\"", "1M", "10", false},
		{"\"0x10\"", "REFUSE", "REFUSE", false},
		{"\"abc\"", "REFUSE", "REFUSE", false},
	} {
		t.Run("mem_limit/"+tc.yaml, func(t *testing.T) {
			p, err := Load(writeSizeFile(t, fmt.Sprintf("services:\n  a:\n    image: x\n    mem_limit: %s\n", tc.yaml)))
			got := "REFUSE"
			if err == nil {
				mem, _, rerr := p.Services["a"].Resources()
				if rerr != nil {
					t.Fatalf("Resources after a load that passed: %v", rerr)
				}
				got = mem
			}
			if got != tc.mem {
				t.Errorf("mem_limit: %s: %q, want %q (err %v)", tc.yaml, got, tc.mem, err)
			}
		})
		if tc.noShm {
			continue
		}
		t.Run("shm_size/"+tc.yaml, func(t *testing.T) {
			p, err := Load(writeSizeFile(t, fmt.Sprintf("services:\n  a:\n    image: x\n    shm_size: %s\n", tc.yaml)))
			got := "REFUSE"
			if err == nil {
				got = string(p.Services["a"].ShmSize)
			}
			if got != tc.shm {
				t.Errorf("shm_size: %s: %q, want %q (err %v)", tc.yaml, got, tc.shm, err)
			}
		})
	}
}

func writeSizeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// What is left as it was, and differs from docker compose v5.5.1 (which drops each of these with no word, so
// the service has no limit): an integer past 64 bits (`0x8000000000000000`) is refused, as it was, and a number
// written with a fraction or an exponent is read as the text it is. A row each, so that neither is changed by
// the reading above in passing.
func TestBareSizesThatDockerComposeDropsAreReadAsTheyWere(t *testing.T) {
	for _, tc := range []struct{ yaml, mem, shm string }{
		{"0x8000000000000000", "REFUSE", "REFUSE"},
		{"1.5", "1M", "1"},
		{"1e6", "REFUSE", "REFUSE"},
	} {
		t.Run("mem_limit/"+tc.yaml, func(t *testing.T) {
			p, err := Load(writeSizeFile(t, fmt.Sprintf("services:\n  a:\n    image: x\n    mem_limit: %s\n", tc.yaml)))
			got := "REFUSE"
			if err == nil {
				got, _, _ = p.Services["a"].Resources()
			}
			if got != tc.mem {
				t.Errorf("mem_limit: %s: %q, want %q (err %v)", tc.yaml, got, tc.mem, err)
			}
		})
		t.Run("shm_size/"+tc.yaml, func(t *testing.T) {
			p, err := Load(writeSizeFile(t, fmt.Sprintf("services:\n  a:\n    image: x\n    shm_size: %s\n", tc.yaml)))
			got := "REFUSE"
			if err == nil {
				got = string(p.Services["a"].ShmSize)
			}
			if got != tc.shm {
				t.Errorf("shm_size: %s: %q, want %q (err %v)", tc.yaml, got, tc.shm, err)
			}
		})
	}
}

// `mem_limit` and `deploy.resources.limits.memory` must agree, and agree when the one is a bare number: the
// verdict follows the number YAML reads, as docker compose gives it (measured, v5.5.1). `04000000` and "1048576"
// agree (they were refused, as four million and 1048576), `04000000` and "4000000" do not (they were read).
func TestBareMemLimitAgreesWithDeployMemoryByTheNumberYAMLReads(t *testing.T) {
	for _, tc := range []struct{ mem, deploy, want string }{
		{"0x100000", "\"1m\"", "ok"},
		{"04000000", "\"1048576\"", "ok"},
		{"04000000", "\"4000000\"", "REFUSE"},
		{"0x100000", "\"2m\"", "REFUSE"},
	} {
		t.Run(tc.mem+" and "+tc.deploy, func(t *testing.T) {
			_, err := Load(writeSizeFile(t, fmt.Sprintf("services:\n  a:\n    image: x\n    mem_limit: %s\n    deploy:\n      resources:\n        limits:\n          memory: %s\n", tc.mem, tc.deploy)))
			got := "ok"
			if err != nil {
				got = "REFUSE"
			}
			if got != tc.want {
				t.Errorf("mem_limit %s with memory %s: %s, want %s (err %v)", tc.mem, tc.deploy, got, tc.want, err)
			}
		})
	}
}
