package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
)

// Cache is the on-disk store for slim packuments and verified tarball
// bytes. Layout:
//
//	<root>/packuments/<safe-name>.json       slim packument
//	<root>/packuments/<safe-name>.meta.json  etag / last-modified / freshUntil
//	<root>/tarballs/<safe-integrity>.tgz     verified tarball bytes
//
// Storing the slim packument (not the verbatim multi-MB registry body)
// cuts cache size and warm-install parse time dramatically.
type Cache struct {
	Root string
}

// NewCache returns a cache rooted at root (typically ~/.gnpm/cache).
func NewCache(root string) *Cache { return &Cache{Root: root} }

func (c *Cache) packumentDir() string { return filepath.Join(c.Root, "packuments") }
func (c *Cache) tarballDir() string   { return filepath.Join(c.Root, "tarballs") }

func (c *Cache) packumentPath(name string) string {
	return filepath.Join(c.packumentDir(), safeName(name)+".json")
}
func (c *Cache) metaPath(name string) string {
	return filepath.Join(c.packumentDir(), safeName(name)+".meta.json")
}
func (c *Cache) tarballPath(integrity string) string {
	return filepath.Join(c.tarballDir(), normalizeIntegrity(integrity)+".tgz")
}

// Initialize creates the cache directory skeleton.
func (c *Cache) Initialize() error {
	for _, d := range []string{c.packumentDir(), c.tarballDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return core.IOError("creating cache dir %s", d).Wrap(err)
		}
	}
	return nil
}

// CachedPackument is a packument plus its revalidation metadata.
type CachedPackument struct {
	Packument    *Packument
	Etag         string
	LastModified string
	FreshUntil   time.Time // zero = unknown, always revalidate
}

// IsFresh reports whether the cached body is still within its
// Cache-Control freshness window.
func (cp *CachedPackument) IsFresh() bool {
	return !cp.FreshUntil.IsZero() && time.Now().UTC().Before(cp.FreshUntil)
}

type packumentMeta struct {
	Etag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	FreshUntil   string `json:"freshUntil,omitempty"`
}

// ReadPackument loads a cached packument, or (nil, nil) on a miss. A
// malformed entry is treated as a miss.
func (c *Cache) ReadPackument(name string) (*CachedPackument, error) {
	body, err := os.ReadFile(c.packumentPath(name))
	if err != nil {
		return nil, nil
	}
	p, err := ParsePackument(body)
	if err != nil {
		return nil, nil
	}
	cp := &CachedPackument{Packument: p}
	if metaBytes, err := os.ReadFile(c.metaPath(name)); err == nil {
		var meta packumentMeta
		if json.Unmarshal(metaBytes, &meta) == nil {
			cp.Etag = meta.Etag
			cp.LastModified = meta.LastModified
			if meta.FreshUntil != "" {
				if t, err := time.Parse(time.RFC3339, meta.FreshUntil); err == nil {
					cp.FreshUntil = t.UTC()
				}
			}
		}
	}
	return cp, nil
}

// WritePackument persists the slim packument and its revalidation
// metadata.
func (c *Cache) WritePackument(cp *CachedPackument) error {
	if err := os.MkdirAll(c.packumentDir(), 0o755); err != nil {
		return core.IOError("mkdir packument cache").Wrap(err)
	}
	body, err := cp.Packument.MarshalSlim()
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.packumentPath(cp.Packument.Name), body, 0o644); err != nil {
		return core.IOError("writing packument cache").Wrap(err)
	}
	meta := packumentMeta{Etag: cp.Etag, LastModified: cp.LastModified}
	if !cp.FreshUntil.IsZero() {
		meta.FreshUntil = cp.FreshUntil.UTC().Format(time.RFC3339)
	}
	if meta != (packumentMeta{}) {
		if mb, err := json.Marshal(meta); err == nil {
			_ = os.WriteFile(c.metaPath(cp.Packument.Name), mb, 0o644)
		}
	}
	return nil
}

// ReadTarball returns cached tarball bytes for integrity, or (nil, false).
func (c *Cache) ReadTarball(integrity string) ([]byte, bool) {
	b, err := os.ReadFile(c.tarballPath(integrity))
	if err != nil {
		return nil, false
	}
	return b, true
}

// WriteTarball stores verified tarball bytes under integrity.
func (c *Cache) WriteTarball(integrity string, bytes []byte) error {
	if err := os.MkdirAll(c.tarballDir(), 0o755); err != nil {
		return core.IOError("mkdir tarball cache").Wrap(err)
	}
	if err := os.WriteFile(c.tarballPath(integrity), bytes, 0o644); err != nil {
		return core.IOError("writing tarball cache").Wrap(err)
	}
	return nil
}

func safeName(name string) string {
	return strings.ReplaceAll(name, "/", "+")
}

// normalizeIntegrity turns an SRI string into a filename-safe token by
// dropping the algorithm prefix and remapping the base64 characters that
// are unsafe in paths.
func normalizeIntegrity(integrity string) string {
	body := integrity
	if dash := strings.IndexByte(integrity, '-'); dash > 0 {
		body = integrity[dash+1:]
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '/':
			b.WriteByte('_')
		case '+':
			b.WriteByte('-')
		case '=':
			// drop padding
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String()
}
