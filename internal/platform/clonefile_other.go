//go:build !darwin

package platform

// CloneTree is unsupported off macOS; callers fall back to per-file
// hardlinks. (Linux reflink via FICLONE could be added here later.)
func CloneTree(src, dst string) error {
	return ErrCloneUnsupported
}
