package linker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/koji-1009/gnpm/internal/store"
)

func TestHoistedLink(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Initialize(); err != nil {
		t.Fatal(err)
	}
	tarball := buildTarball(t, map[string]tf{
		"package.json": {body: `{"name":"demo","version":"1.0.0"}`},
		"bin/cli.js":   {body: "#!/usr/bin/env node", mode: 0o755},
	})
	integrity := sri(tarball)
	if _, err := st.IngestTarball(tarball, integrity); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	l := HoistedLinker{Store: st}
	specs := []LinkSpec{{
		Name: "demo", Version: "1.0.0", Integrity: integrity, IsDirect: true,
		Bin: map[string]string{"demo": "bin/cli.js"},
	}}
	if _, err := l.Link(root, specs); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(filepath.Join(root, "node_modules", "demo", "package.json")); err != nil || string(b) != `{"name":"demo","version":"1.0.0"}` {
		t.Errorf("materialized package.json wrong: %q (err=%v)", b, err)
	}
	if runtime.GOOS != "windows" {
		shim := filepath.Join(root, "node_modules", ".bin", "demo")
		info, err := os.Stat(shim)
		if err != nil {
			t.Fatalf("bin shim missing: %v", err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Error("bin shim not executable")
		}
	}
}

func TestHoistedNestedMultiVersion(t *testing.T) {
	// Two versions of the same package at distinct paths both materialize
	// (one hoisted to top level, one nested for a conflict).
	st := store.New(t.TempDir())
	st.Initialize()
	mk := func(v string) string {
		tb := buildTarball(t, map[string]tf{"package.json": {body: `{"name":"dup","version":"` + v + `"}`}})
		integ := sri(tb)
		st.IngestTarball(tb, integ)
		return integ
	}
	specs := []LinkSpec{
		{Name: "dup", Version: "1.0.0", Integrity: mk("1.0.0"), Path: "dup"},
		{Name: "dup", Version: "2.0.0", Integrity: mk("2.0.0"), Path: "B/node_modules/dup"},
	}
	root := t.TempDir()
	if _, err := (HoistedLinker{Store: st}).Link(root, specs); err != nil {
		t.Fatal(err)
	}
	top, _ := os.ReadFile(filepath.Join(root, "node_modules", "dup", "package.json"))
	if !bytes.Contains(top, []byte(`"1.0.0"`)) {
		t.Errorf("top-level dup should be 1.0.0, got %s", top)
	}
	nested, _ := os.ReadFile(filepath.Join(root, "node_modules", "B", "node_modules", "dup", "package.json"))
	if !bytes.Contains(nested, []byte(`"2.0.0"`)) {
		t.Errorf("nested dup should be 2.0.0, got %s", nested)
	}
}

type tf struct {
	body string
	mode int64
}

func buildTarball(t *testing.T, files map[string]tf) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, f := range files {
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: "package/" + name, Mode: mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		tw.WriteHeader(hdr)
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sri(b []byte) string {
	sum := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}
