//go:build darwin

package workspace

import "golang.org/x/sys/unix"

// cloneAPFS clones a directory hierarchy with clonefile(2) — copy-on-write, so
// it's near-instant and shares blocks until the two diverge. dst must not exist.
// On a non-APFS or cross-device path it returns ENOTSUP/EXDEV, which cloneOrCopy
// turns into a plain-copy fallback.
func cloneAPFS(src, dst string) error {
	return unix.Clonefile(src, dst, 0)
}
