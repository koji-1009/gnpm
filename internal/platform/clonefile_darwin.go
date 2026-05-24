//go:build darwin

package platform

import "golang.org/x/sys/unix"

// CloneTree recursively clones the directory tree at src to dst using
// APFS clonefile(2): one syscall copies the whole tree copy-on-write, so
// materializing a package costs a single call instead of N per-file
// hardlinks. dst must not already exist and must share a volume with src
// (else clonefile returns EXDEV and the caller falls back to hardlinks).
func CloneTree(src, dst string) error {
	return unix.Clonefile(src, dst, 0)
}
