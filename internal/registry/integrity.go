// Package registry talks to the npm registry: packument fetch with
// caching and revalidation, and tarball fetch with subresource-integrity
// verification. It also defines the packument data model the resolver
// consumes.
package registry

import (
	"crypto/subtle"
	"encoding/base64"
	"hash"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
)

// Integrity is a parsed subresource-integrity value `<algorithm>-<base64>`
// (e.g. "sha512-…"). npm tarballs carry one in their `dist.integrity`.
type Integrity struct {
	Algorithm    string
	DigestBase64 string
}

// ParseIntegrity parses an SRI literal. The algorithm is lowercased; the
// base64 digest is kept verbatim.
func ParseIntegrity(s string) (Integrity, error) {
	dash := strings.IndexByte(s, '-')
	if dash <= 0 || dash == len(s)-1 {
		return Integrity{}, core.IntegrityError("invalid integrity literal: %q", s)
	}
	return Integrity{
		Algorithm:    strings.ToLower(s[:dash]),
		DigestBase64: s[dash+1:],
	}, nil
}

// Encode renders the integrity in canonical `<algorithm>-<base64>` form.
func (in Integrity) Encode() string {
	return in.Algorithm + "-" + in.DigestBase64
}

// Hasher returns a streaming hash for the integrity's algorithm.
func (in Integrity) Hasher() (hash.Hash, error) {
	h, ok := core.NewHasher(in.Algorithm)
	if !ok {
		return nil, core.IntegrityError("unsupported integrity algorithm: %q", in.Algorithm)
	}
	return h, nil
}

// Matches reports whether digest (the raw hash output) equals this
// integrity's digest, compared in constant time.
func (in Integrity) Matches(digest []byte) bool {
	want, err := base64.StdEncoding.DecodeString(in.DigestBase64)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want, digest) == 1
}

// Verify checks that bytes hash to this integrity, returning an
// IntegrityError on mismatch.
func (in Integrity) Verify(bytes []byte) error {
	h, err := in.Hasher()
	if err != nil {
		return err
	}
	h.Write(bytes)
	sum := h.Sum(nil)
	if !in.Matches(sum) {
		return &core.Error{
			Kind:     core.KindIntegrity,
			Message:  "integrity mismatch (" + in.Algorithm + ")",
			Expected: in.Encode(),
			Actual:   in.Algorithm + "-" + base64.StdEncoding.EncodeToString(sum),
		}
	}
	return nil
}
