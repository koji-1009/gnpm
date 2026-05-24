// Package store is gnpm's content-addressable package store. Tarballs
// are extracted once; every file is deduplicated by its SHA-512 under
// files/, and an index records the file list per tarball. Materializing
// a package into node_modules hardlinks each file from the store (with a
// copy fallback), the pnpm model.
package store

import (
	"encoding/base64"
	"path/filepath"
	"strings"

	"github.com/koji-1009/gnpm/internal/registry"
)

// Layout resolves paths within the store:
//
//	<root>/v1/files/<aa>/<sha512hex>     deduplicated file content
//	<root>/v1/index/<aa>/<key>.json      per-tarball file manifest
//	<root>/v1/tmp/                        extraction scratch
type Layout struct {
	Root string
}

func (l Layout) base() string         { return filepath.Join(l.Root, "v1") }
func (l Layout) FilesDir() string     { return filepath.Join(l.base(), "files") }
func (l Layout) IndexDir() string     { return filepath.Join(l.base(), "index") }
func (l Layout) TmpDir() string       { return filepath.Join(l.base(), "tmp") }
func (l Layout) ExtractedDir() string { return filepath.Join(l.base(), "extracted") }

// ExtractedPackageDir is the ready-to-clone tree for a tarball store key:
// each file inside is a hardlink onto its files/<sha> twin, so a recursive
// clonefile materializes the package in one syscall.
func (l Layout) ExtractedPackageDir(key string) string {
	return filepath.Join(l.ExtractedDir(), prefix(key), key)
}

// FilePath is the content-addressed path for a file's SHA-512 hex.
func (l Layout) FilePath(sha512Hex string) string {
	return filepath.Join(l.FilesDir(), prefix(sha512Hex), sha512Hex)
}

// IndexPath is the manifest path for a tarball store key.
func (l Layout) IndexPath(key string) string {
	return filepath.Join(l.IndexDir(), prefix(key), key+".json")
}

func prefix(hex string) string {
	if len(hex) >= 2 {
		return hex[:2]
	}
	return "xx"
}

// KeyFor derives a filesystem-safe store key from a tarball's SRI
// integrity by hex-encoding its digest. This avoids the base64 "/" "+"
// "=" characters that would otherwise break path construction.
func KeyFor(integritySRI string) (string, error) {
	in, err := registry.ParseIntegrity(integritySRI)
	if err != nil {
		return "", err
	}
	digest, err := base64.StdEncoding.DecodeString(in.DigestBase64)
	if err != nil {
		return "", err
	}
	return in.Algorithm + "-" + toHex(digest), nil
}

const hexdigits = "0123456789abcdef"

func toHex(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, c := range b {
		sb.WriteByte(hexdigits[c>>4])
		sb.WriteByte(hexdigits[c&0x0f])
	}
	return sb.String()
}
