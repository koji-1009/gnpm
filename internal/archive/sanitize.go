// Package archive extracts npm tarballs (gzip + tar) with path-traversal
// sanitization, stripping the conventional leading "package/" directory.
package archive

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
)

// Sanitizer validates tar entry names against a fixed destination root.
// Construct once per archive and reuse Resolve per entry.
type Sanitizer struct {
	destAbs string
}

// NewSanitizer builds a Sanitizer rooted at destination.
func NewSanitizer(destination string) *Sanitizer {
	abs, err := filepath.Abs(destination)
	if err != nil {
		abs = filepath.Clean(destination)
	}
	return &Sanitizer{destAbs: abs}
}

// Resolve returns the safe absolute path for a tar entry name, or an
// IoError when the name attempts traversal (".."), is absolute, carries
// a Windows drive letter, or embeds a NUL byte.
func (s *Sanitizer) Resolve(entryName string) (string, error) {
	if strings.IndexByte(entryName, 0) >= 0 {
		return "", core.IOError("archive entry contains NUL byte: %q", entryName)
	}
	if strings.HasPrefix(entryName, "/") || hasDriveLetter(entryName) {
		return "", core.IOError("archive entry has absolute path: %q", entryName)
	}
	normalized := path.Clean(entryName)
	if normalized == ".." || hasDotDotSegment(normalized) {
		return "", core.IOError("archive entry escapes destination: %q", entryName)
	}
	joined := filepath.Join(s.destAbs, filepath.FromSlash(normalized))
	rel, err := filepath.Rel(s.destAbs, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", core.IOError("archive entry escapes destination: %q", entryName)
	}
	return joined, nil
}

func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func hasDriveLetter(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	isLetter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	return isLetter && s[1] == ':' && (s[2] == '/' || s[2] == '\\')
}
