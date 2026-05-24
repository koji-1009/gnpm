package store

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"github.com/koji-1009/gnpm/internal/archive"
	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/platform"
)

var extractedCounter atomic.Uint64

// uniqueSuffix produces a process-unique staging-dir suffix.
func uniqueSuffix() string {
	return strconv.Itoa(os.Getpid()) + "." + strconv.FormatUint(extractedCounter.Add(1), 10)
}

// StoredFile is one file in a tarball's manifest.
type StoredFile struct {
	RelPath   string `json:"path"`
	SHA512Hex string `json:"sha512"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
}

// Manifest is the per-tarball index: the integrity it was ingested from
// and the list of deduplicated files.
type Manifest struct {
	Integrity string       `json:"tarball"`
	Files     []StoredFile `json:"files"`
}

// Store is a filesystem-backed content-addressable store.
type Store struct {
	Layout Layout
}

// New returns a store rooted at root.
func New(root string) *Store { return &Store{Layout: Layout{Root: root}} }

// Initialize creates the store's directory skeleton.
func (s *Store) Initialize() error {
	for _, d := range []string{s.Layout.FilesDir(), s.Layout.IndexDir(), s.Layout.TmpDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return core.IOError("creating store dir %s", d).Wrap(err)
		}
	}
	return nil
}

// HasTarball reports whether the tarball for integritySRI is already
// ingested.
func (s *Store) HasTarball(integritySRI string) bool {
	key, err := KeyFor(integritySRI)
	if err != nil {
		return false
	}
	_, err = os.Stat(s.Layout.IndexPath(key))
	return err == nil
}

// ReadManifest returns the manifest for integritySRI, or (nil, nil) when
// the tarball is not in the store.
func (s *Store) ReadManifest(integritySRI string) (*Manifest, error) {
	key, err := KeyFor(integritySRI)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.Layout.IndexPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, core.IOError("reading store index").Wrap(err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, core.IOError("parsing store index").Wrap(err)
	}
	return &m, nil
}

// IngestTarball extracts and stores the tarball bytes under integritySRI.
// It is idempotent: an already-ingested tarball returns its existing
// manifest. The caller is responsible for having verified the bytes
// against integritySRI before ingest.
func (s *Store) IngestTarball(data []byte, integritySRI string) (*Manifest, error) {
	if existing, err := s.ReadManifest(integritySRI); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if err := s.Initialize(); err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp(s.Layout.TmpDir(), "ext-")
	if err != nil {
		return nil, core.IOError("creating extract tmp").Wrap(err)
	}
	defer os.RemoveAll(tmp)

	entries, err := archive.Extract(bytes.NewReader(data), tmp)
	if err != nil {
		return nil, err
	}

	files := make([]StoredFile, 0, len(entries))
	for _, e := range entries {
		if e.IsLink || e.AbsPath == "" {
			continue
		}
		sf, err := s.intern(e, tmp)
		if err != nil {
			return nil, err
		}
		files = append(files, sf)
	}

	manifest := &Manifest{Integrity: integritySRI, Files: files}
	if err := s.writeIndex(integritySRI, manifest); err != nil {
		return nil, err
	}
	// Build the ready-to-clone tree so Materialize can clonefile it in one
	// syscall (best-effort; per-file hardlink still works without it).
	s.populateExtracted(integritySRI, manifest)
	return manifest, nil
}

// populateExtracted builds extracted/<key>/ as a tree of hardlinks onto
// the content store, via a staging dir renamed into place. Best-effort:
// failures leave Materialize to fall back to per-file hardlinks.
func (s *Store) populateExtracted(integritySRI string, m *Manifest) {
	key, err := KeyFor(integritySRI)
	if err != nil {
		return
	}
	dir := s.Layout.ExtractedPackageDir(key)
	if _, err := os.Stat(dir); err == nil {
		return
	}
	staging := dir + ".tmp." + uniqueSuffix()
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return
	}
	for _, f := range m.Files {
		target := filepath.Join(staging, filepath.FromSlash(f.RelPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			os.RemoveAll(staging)
			return
		}
		if err := platform.Hardlink(s.Layout.FilePath(f.SHA512Hex), target); err != nil {
			os.RemoveAll(staging)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		os.RemoveAll(staging)
		return
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging) // lost a race; the winner's tree is fine
	}
}

// intern hashes one extracted file and moves it onto its content path.
func (s *Store) intern(e archive.Entry, tmpRoot string) (StoredFile, error) {
	hexsum, err := sha512File(e.AbsPath)
	if err != nil {
		return StoredFile{}, err
	}
	info, err := os.Stat(e.AbsPath)
	if err != nil {
		return StoredFile{}, core.IOError("stat extracted file").Wrap(err)
	}
	dest := s.Layout.FilePath(hexsum)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return StoredFile{}, core.IOError("mkdir store bucket").Wrap(err)
		}
		// Same filesystem (tmp and files share the store root), so
		// rename is atomic; a concurrent ingest of identical content
		// simply wins the race and either copy is correct.
		if err := os.Rename(e.AbsPath, dest); err != nil {
			if _, statErr := os.Stat(dest); statErr != nil {
				if cErr := platform.CopyFile(e.AbsPath, dest); cErr != nil {
					return StoredFile{}, core.IOError("interning file").Wrap(cErr)
				}
			}
		}
	}
	rel := filepath.ToSlash(mustRel(tmpRoot, e.AbsPath))
	return StoredFile{RelPath: rel, SHA512Hex: hexsum, Size: e.Size, Mode: uint32(info.Mode().Perm())}, nil
}

func (s *Store) writeIndex(integritySRI string, m *Manifest) error {
	key, err := KeyFor(integritySRI)
	if err != nil {
		return err
	}
	path := s.Layout.IndexPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.IOError("mkdir index bucket").Wrap(err)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return core.IOError("encoding index").Wrap(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return core.IOError("writing index tmp").Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Lost a race with a concurrent ingest of the same tarball.
		if _, statErr := os.Stat(path); statErr == nil {
			os.Remove(tmp)
			return nil
		}
		return core.IOError("committing index").Wrap(err)
	}
	return nil
}

// Materialize reconstructs the package tree for integritySRI into dest.
// On macOS it clonefiles the prebuilt extracted tree in one syscall;
// otherwise (or on clone failure) it falls back to per-file hardlinks.
func (s *Store) Materialize(integritySRI, dest string) error {
	// Fast path: recursive clonefile of the prebuilt tree.
	if key, err := KeyFor(integritySRI); err == nil {
		ext := s.Layout.ExtractedPackageDir(key)
		if info, err := os.Stat(ext); err == nil && info.IsDir() {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err == nil {
				if err := platform.CloneTree(ext, dest); err == nil {
					return nil
				}
				// Clone failed (EXDEV / unsupported): clear any partial
				// dest and fall through to per-file hardlinks.
				os.RemoveAll(dest)
			}
		}
	}

	manifest, err := s.ReadManifest(integritySRI)
	if err != nil {
		return err
	}
	if manifest == nil {
		return core.IOError("no store index for %s", integritySRI)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return core.IOError("creating %s", dest).Wrap(err)
	}
	// Pre-create directories (dedup), then link files in parallel.
	dirs := map[string]struct{}{}
	for _, f := range manifest.Files {
		dirs[filepath.Dir(filepath.Join(dest, filepath.FromSlash(f.RelPath)))] = struct{}{}
	}
	for d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return core.IOError("mkdir %s", d).Wrap(err)
		}
	}
	return core.ForEachLimited(manifest.Files, core.DefaultParallelism(), func(f StoredFile) error {
		target := filepath.Join(dest, filepath.FromSlash(f.RelPath))
		if err := platform.Hardlink(s.Layout.FilePath(f.SHA512Hex), target); err != nil {
			return core.IOError("linking %s", target).Wrap(err)
		}
		return nil
	})
}

func sha512File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", core.IOError("opening %s for hash", path).Wrap(err)
	}
	defer f.Close()
	h, _ := core.NewHasher("sha512")
	if _, err := io.Copy(h, f); err != nil {
		return "", core.IOError("hashing %s", path).Wrap(err)
	}
	return toHex(h.Sum(nil)), nil
}

func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return filepath.Base(target)
	}
	return rel
}
