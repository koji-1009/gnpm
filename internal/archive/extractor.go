package archive

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/platform"
)

// Entry describes one extracted tarball member.
type Entry struct {
	// RelPath is the entry path with the leading "package/" stripped,
	// using forward slashes.
	RelPath string
	// AbsPath is the written file's absolute path (files only).
	AbsPath string
	Size    int64
	// IsLink marks a symlink/hardlink entry, recorded but not
	// materialized; Target holds its link name.
	IsLink bool
	Target string
}

// Extract reads a gzipped tar from r and writes its members under dest,
// stripping the leading "package/" directory. It returns metadata for
// each emitted entry. Symlinks/hardlinks are recorded but not created
// (the store treats them as advisory). The executable bit from the tar
// header is preserved; setuid/setgid are dropped.
func Extract(r io.Reader, dest string) ([]Entry, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, core.IOError("creating extract dir").Wrap(err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, core.IntegrityError("gzip: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	san := NewSanitizer(dest)
	var emitted []Entry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, core.IntegrityError("tar: %v", err)
		}
		rel := stripPackagePrefix(hdr.Name)
		if rel == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			abs, err := san.Resolve(rel)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return nil, core.IOError("mkdir %s", abs).Wrap(err)
			}
			continue
		case tar.TypeSymlink, tar.TypeLink:
			emitted = append(emitted, Entry{RelPath: rel, IsLink: true, Target: hdr.Linkname})
			continue
		case tar.TypeReg:
			abs, err := san.Resolve(rel)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return nil, core.IOError("mkdir parent of %s", abs).Wrap(err)
			}
			n, err := writeFile(tr, abs)
			if err != nil {
				return nil, err
			}
			if platform.IsExecMode(hdr.Mode) {
				if err := platform.ChmodExecutable(abs); err != nil {
					return nil, core.IOError("chmod %s", abs).Wrap(err)
				}
			}
			emitted = append(emitted, Entry{RelPath: rel, AbsPath: abs, Size: n})
		default:
			// Skip character/block devices, FIFOs, etc.
			continue
		}
	}
	return emitted, nil
}

func writeFile(r io.Reader, dest string) (int64, error) {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, core.IOError("creating %s", dest).Wrap(err)
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, core.IOError("writing %s", dest).Wrap(err)
	}
	return n, nil
}

func stripPackagePrefix(name string) string {
	const prefix = "package/"
	if s, ok := strings.CutPrefix(name, prefix); ok {
		return s
	}
	// Some tarballs use a different top dir; npm only strips "package/".
	return name
}
