package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestIngestAndMaterialize(t *testing.T) {
	// Two files with identical content must deduplicate to one store file.
	tarball := buildTarball(t, map[string]string{
		"package.json": `{"name":"demo","version":"1.0.0"}`,
		"a.js":         "same",
		"b.js":         "same",
		"bin/cli.js":   "#!/usr/bin/env node",
	})
	integrity := sri(tarball)

	s := New(t.TempDir())
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	if s.HasTarball(integrity) {
		t.Fatal("store should start empty")
	}
	m, err := s.IngestTarball(tarball, integrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 4 {
		t.Fatalf("manifest has %d files, want 4", len(m.Files))
	}
	if !s.HasTarball(integrity) {
		t.Error("HasTarball should be true after ingest")
	}

	// Dedup: a.js and b.js share one content path.
	var aHash, bHash string
	for _, f := range m.Files {
		switch f.RelPath {
		case "a.js":
			aHash = f.SHA512Hex
		case "b.js":
			bHash = f.SHA512Hex
		}
	}
	if aHash == "" || aHash != bHash {
		t.Errorf("identical files should dedup: a=%q b=%q", aHash, bHash)
	}
	if _, err := os.Stat(s.Layout.FilePath(aHash)); err != nil {
		t.Errorf("content file missing in store: %v", err)
	}

	// Idempotent re-ingest.
	if _, err := s.IngestTarball(tarball, integrity); err != nil {
		t.Errorf("re-ingest should be a no-op: %v", err)
	}

	// Materialize into node_modules-like dest.
	dest := filepath.Join(t.TempDir(), "demo")
	if err := s.Materialize(integrity, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "package.json")); string(b) != `{"name":"demo","version":"1.0.0"}` {
		t.Errorf("materialized package.json = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.js")); string(b) != "same" {
		t.Errorf("materialized a.js = %q", b)
	}
}

func TestKeyForRejectsBadIntegrity(t *testing.T) {
	if _, err := KeyFor("not-an-integrity-literal!!"); err == nil {
		t.Error("KeyFor should reject malformed integrity")
	}
	k, err := KeyFor(sri([]byte("x")))
	if err != nil || len(k) < 10 {
		t.Errorf("KeyFor valid integrity: key=%q err=%v", k, err)
	}
}

func sri(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func buildTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		mode := int64(0o644)
		if filepath.Dir(name) == "bin" {
			mode = 0o755
		}
		hdr := &tar.Header{Name: "package/" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
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
