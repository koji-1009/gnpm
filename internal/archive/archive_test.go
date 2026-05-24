package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// BuildTarball builds a gzipped tar from a set of named entries, each
// prefixed with "package/". Each entry's mode is written verbatim into
// the tar header. Exported for reuse by other packages' tests.
func BuildTarball(t *testing.T, files map[string]tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, e := range files {
		hdr := &tar.Header{Name: "package/" + name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.link
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	body string
	mode int64
	link string
}

func TestExtract(t *testing.T) {
	tarball := BuildTarball(t, map[string]tarEntry{
		"package.json":  {body: `{"name":"demo"}`, mode: 0o644},
		"bin/cli.js":    {body: "#!/usr/bin/env node\n", mode: 0o755},
		"lib/index.js":  {body: "module.exports={}", mode: 0o644},
		"link-to-index": {link: "lib/index.js"},
	})
	dest := t.TempDir()
	entries, err := Extract(bytes.NewReader(tarball), dest)
	if err != nil {
		t.Fatal(err)
	}
	var files, links int
	for _, e := range entries {
		if e.IsLink {
			links++
			continue
		}
		files++
	}
	if files != 3 || links != 1 {
		t.Fatalf("got %d files / %d links, want 3 / 1", files, links)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "package.json")); string(b) != `{"name":"demo"}` {
		t.Errorf("package.json content = %q", b)
	}
	info, err := os.Stat(filepath.Join(dest, "bin/cli.js"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("bin/cli.js should be executable")
	}
}

func TestSanitizerRejectsTraversal(t *testing.T) {
	s := NewSanitizer(t.TempDir())
	bad := []string{"../escape", "../../etc/passwd", "/abs/path", "a/../../b", "with\x00nul"}
	for _, name := range bad {
		if _, err := s.Resolve(name); err == nil {
			t.Errorf("Resolve(%q) should be rejected", name)
		}
	}
	if _, err := s.Resolve("lib/nested/ok.js"); err != nil {
		t.Errorf("Resolve of a normal path should succeed: %v", err)
	}
}
