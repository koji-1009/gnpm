package platform

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// ErrCloneUnsupported is returned by CloneTree on platforms without a
// clonefile primitive, signaling the caller to fall back to hardlinks.
var ErrCloneUnsupported = errors.New("clonefile not supported on this platform")

// Hardlink links src to dst, falling back to a byte copy when the link
// syscall fails (cross-device EXDEV, or a filesystem that disallows
// links). The destination's parent directory must already exist.
func Hardlink(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	// Any link failure (EXDEV, EPERM on some filesystems) → copy.
	return CopyFile(src, dst)
}

// CopyFile copies src to dst, preserving the source's permission bits.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// OpenFile honors umask, so re-apply the exact source mode.
	return os.Chmod(dst, info.Mode().Perm())
}

// CopyTreeExcluding recursively copies src into dst, skipping any
// directory whose base name equals exclude (e.g. ".git" for a git clone).
// Symlinks are preserved (recreated pointing at the same target), since
// package contents can legitimately contain them; other irregular files
// (fifos, sockets, devices) are skipped.
func CopyTreeExcluding(src, dst, exclude string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		dest := filepath.Join(dst, rel)
		if d.IsDir() {
			if d.Name() == exclude {
				return filepath.SkipDir
			}
			return os.MkdirAll(dest, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dest)
			return os.Symlink(target, dest)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return CopyFile(path, dest)
	})
}

// ChmodExecutable adds the user/group/other execute bits to path,
// preserving its other permission bits. npm ships native binaries with
// the exec bit set; the store honors it so node_modules/.bin works.
func ChmodExecutable(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.Chmod(path, info.Mode().Perm()|0o111)
}

// CreateDirSymlink creates a symlink at linkPath pointing at target. On
// Windows a directory symlink is attempted; callers needing junctions
// can layer them later. Any pre-existing entry at linkPath is removed
// first.
func CreateDirSymlink(linkPath, target string) error {
	_ = os.Remove(linkPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}

// IsExecMode reports whether a tar/file mode has any execute bit set.
func IsExecMode(mode int64) bool {
	return mode&0o111 != 0
}
