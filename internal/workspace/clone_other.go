//go:build !darwin

package workspace

import "golang.org/x/sys/unix"

// cloneAPFS is unavailable off darwin — clonefile(2) is macOS-only. It reports
// "not supported" so cloneOrCopy falls back to a plain copy. opossum targets
// macOS; this stub only keeps the package building and its filesystem logic
// testable on other platforms (Linux CI).
func cloneAPFS(src, dst string) error {
	return unix.ENOTSUP
}
