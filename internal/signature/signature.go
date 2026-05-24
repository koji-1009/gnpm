// Package signature verifies registry-attached ECDSA P-256 signatures
// over "<name>@<version>:<integrity>" against the registry's published
// signing keys (doc/spec.md §2.4 signaturePolicy), using Go's
// crypto/ecdsa — no bundled native crypto library.
package signature

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/koji-1009/gnpm/internal/core"
)

// Policy is the signaturePolicy setting.
type Policy int

const (
	PolicyNone Policy = iota
	PolicyWeak
	PolicyStrict
)

// ParsePolicy maps a setting value to a Policy. ok is false for an
// unrecognized value.
func ParsePolicy(s string) (Policy, bool) {
	switch s {
	case "none", "":
		return PolicyNone, true
	case "weak":
		return PolicyWeak, true
	case "strict":
		return PolicyStrict, true
	default:
		return PolicyNone, false
	}
}

// Result is the outcome of verifying a tarball's signatures.
type Result int

const (
	// ResultAbsent: no signature was attached.
	ResultAbsent Result = iota
	// ResultValid: a signature verified against a known key.
	ResultValid
	// ResultInvalid: a signature was present but none verified.
	ResultInvalid
)

// Signature is one (keyid, sig) pair, sig being base64(DER(ECDSA{r,s})).
type Signature struct {
	KeyID string
	Sig   string
}

// KeyStore fetches and caches a registry's ECDSA public keys.
type KeyStore struct {
	Registry  string
	HTTP      *http.Client
	AuthToken string

	mu     sync.Mutex
	keys   map[string]*ecdsa.PublicKey
	loaded bool
	err    error
}

// NewKeyStore builds a KeyStore for a registry.
func NewKeyStore(registry string, httpClient *http.Client, authToken string) *KeyStore {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &KeyStore{Registry: registry, HTTP: httpClient, AuthToken: authToken, keys: map[string]*ecdsa.PublicKey{}}
}

// WithKeys builds a KeyStore pre-seeded with keys, for tests.
func WithKeys(keys map[string]*ecdsa.PublicKey) *KeyStore {
	return &KeyStore{keys: keys, loaded: true}
}

func (ks *KeyStore) load(ctx context.Context) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if ks.loaded {
		return ks.err
	}
	ks.loaded = true
	endpoint := strings.TrimRight(ks.Registry, "/") + "/-/npm/v1/keys"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		ks.err = err
		return err
	}
	if ks.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+ks.AuthToken)
	}
	resp, err := ks.HTTP.Do(req)
	if err != nil {
		ks.err = core.NetworkError("fetching registry keys: %v", err)
		return ks.err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		ks.err = core.NetworkError("registry keys returned %d", resp.StatusCode)
		return ks.err
	}
	data, _ := io.ReadAll(resp.Body)
	var doc struct {
		Keys []struct {
			KeyID string `json:"keyid"`
			Key   string `json:"key"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		ks.err = core.NetworkError("parsing registry keys: %v", err)
		return ks.err
	}
	for _, k := range doc.Keys {
		pub, err := parseECDSAKey(k.Key)
		if err != nil {
			continue
		}
		ks.keys[k.KeyID] = pub
	}
	return nil
}

// Verify checks the signatures over "<name>@<version>:<integrity>".
func (ks *KeyStore) Verify(ctx context.Context, name, version, integrity string, sigs []Signature) (Result, error) {
	if len(sigs) == 0 {
		return ResultAbsent, nil
	}
	if !ks.loaded {
		if err := ks.load(ctx); err != nil {
			return ResultInvalid, err
		}
	}
	message := []byte(name + "@" + version + ":" + integrity)
	digest := sha256.Sum256(message)
	for _, s := range sigs {
		key := ks.keys[s.KeyID]
		if key == nil {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			continue
		}
		if ecdsa.VerifyASN1(key, digest[:], der) {
			return ResultValid, nil
		}
	}
	return ResultInvalid, nil
}

// Enforce applies the policy to a verification result, returning a
// warning string (non-fatal) or a fatal IntegrityError.
func Enforce(policy Policy, name, version string, result Result) (warning string, err error) {
	switch policy {
	case PolicyNone:
		return "", nil
	case PolicyWeak:
		switch result {
		case ResultInvalid:
			return "", core.IntegrityError("signature verification failed for %s@%s", name, version)
		default:
			return "", nil
		}
	case PolicyStrict:
		switch result {
		case ResultValid:
			return "", nil
		case ResultAbsent:
			return "", core.IntegrityError("no signature for %s@%s (signature-policy=strict)", name, version)
		default:
			return "", core.IntegrityError("signature verification failed for %s@%s", name, version)
		}
	}
	return "", nil
}

func parseECDSAKey(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, core.IntegrityError("registry key is not ECDSA")
	}
	return ec, nil
}
