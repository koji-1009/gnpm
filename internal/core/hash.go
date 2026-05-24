package core

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
)

// SHA256Hex returns the lowercase hex SHA-256 of b. Used for the
// workspace-state fingerprint (doc/spec.md §4.3.1) and the dlx cache key
// (§10.1).
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SHA512Hex returns the lowercase hex SHA-512 of b. The content store
// keys files by this digest.
func SHA512Hex(b []byte) string {
	sum := sha512.Sum512(b)
	return hex.EncodeToString(sum[:])
}

// NewHasher returns a streaming hash for the named SRI algorithm, and
// ok=false for an unsupported name so callers can surface a clean error
// rather than panic on attacker-controlled integrity strings.
func NewHasher(algorithm string) (hash.Hash, bool) {
	switch algorithm {
	case "sha512":
		return sha512.New(), true
	case "sha384":
		return sha512.New384(), true
	case "sha256":
		return sha256.New(), true
	case "sha1":
		return sha1.New(), true
	default:
		return nil, false
	}
}
