package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/koji-1009/gnpm/internal/npmrc"
)

func TestPackumentFetchAndRevalidate(t *testing.T) {
	var hits, conditional int32
	body := []byte(`{"name":"demo","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"demo","version":"1.0.0","dist":{"tarball":"https://x/demo.tgz","integrity":"sha512-abc"}}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("ETag", `"v1"`)
		// No Cache-Control → never fresh → always revalidate.
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&conditional, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	cfg := npmrc.New(map[string]string{"registry": srv.URL + "/"})
	c := NewClient(Options{Config: cfg})

	p1, err := c.Packument(context.Background(), "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Latest() != "1.0.0" {
		t.Errorf("latest = %q", p1.Latest())
	}
	// Second call revalidates and gets a 304, returning the cached body.
	p2, err := c.Packument(context.Background(), "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Versions["1.0.0"] == nil {
		t.Error("revalidated packument lost versions")
	}
	if hits != 2 {
		t.Errorf("server hits = %d, want 2", hits)
	}
	if conditional != 1 {
		t.Errorf("conditional 304 count = %d, want 1", conditional)
	}
}

func TestPackumentRetriesTransient(t *testing.T) {
	var hits int32
	body := []byte(`{"name":"demo","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"demo","version":"1.0.0"}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 twice
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	c := NewClient(Options{Config: npmrc.New(map[string]string{"registry": srv.URL + "/"})})
	p, err := c.Packument(context.Background(), "demo", false)
	if err != nil {
		t.Fatalf("should succeed after retries: %v", err)
	}
	if p.Latest() != "1.0.0" {
		t.Errorf("latest = %q", p.Latest())
	}
	if hits != 3 {
		t.Errorf("expected 3 attempts (2x503 + 1x200), got %d", hits)
	}
}

func TestPackumentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewClient(Options{Config: npmrc.New(map[string]string{"registry": srv.URL + "/"})})
	_, err := c.Packument(context.Background(), "ghost", false)
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestScopedPackumentURLEncoding(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"name":"@scope/pkg","dist-tags":{},"versions":{}}`))
	}))
	defer srv.Close()
	c := NewClient(Options{Config: npmrc.New(map[string]string{"registry": srv.URL + "/"})})
	if _, err := c.Packument(context.Background(), "@scope/pkg", false); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/@scope%2fpkg" {
		t.Errorf("scoped path = %q, want /@scope%%2fpkg", gotPath)
	}
}

func TestTarballFetchVerifies(t *testing.T) {
	tarball := smallTarball(t)
	good := sriOf(tarball)
	served := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&served, 1)
		w.Write(tarball)
	}))
	defer srv.Close()

	cache := NewCache(t.TempDir())
	c := NewClient(Options{Config: npmrc.New(nil), Cache: cache})

	b, err := c.Tarball(context.Background(), srv.URL+"/demo.tgz", good)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, tarball) {
		t.Error("tarball bytes differ")
	}
	// Second fetch is served from cache (no new server hit).
	if _, err := c.Tarball(context.Background(), srv.URL+"/demo.tgz", good); err != nil {
		t.Fatal(err)
	}
	if served != 1 {
		t.Errorf("server served %d times, want 1 (cache hit)", served)
	}

	// Wrong integrity is rejected.
	bad := "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
	if _, err := c.Tarball(context.Background(), srv.URL+"/demo.tgz", bad); err == nil {
		t.Error("expected integrity mismatch error")
	}
}

func smallTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte(`{"name":"demo"}`)
	tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write(content)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sriOf(b []byte) string {
	sum := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}
