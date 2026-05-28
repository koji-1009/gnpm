package linker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/koji-1009/gnpm/internal/store"
)

// buildTarballRaw builds a tiny package tarball without a *testing.T, so it can
// be reused from benchmarks.
func buildTarballRaw(files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// benchGraph ingests n synthetic packages (each with depsPer dependency edges)
// into a store and returns their link specs, modelling an isolated install of a
// dense dependency graph.
func benchGraph(b *testing.B, st *store.Store, n, depsPer int) []LinkSpec {
	b.Helper()
	integ := make([]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("pkg%d", i)
		tb := buildTarballRaw(map[string]string{
			"package.json": `{"name":"` + name + `","version":"1.0.0"}`,
			"index.js":     "module.exports = " + fmt.Sprint(i),
			"lib/util.js":  "exports.x = 1",
		})
		integ[i] = sri(tb)
		if _, err := st.IngestTarball(tb, integ[i]); err != nil {
			b.Fatal(err)
		}
	}
	specs := make([]LinkSpec, n)
	for i := 0; i < n; i++ {
		deps := map[string]string{}
		for k := 1; k <= depsPer && k < n; k++ {
			deps[fmt.Sprintf("pkg%d", (i+k)%n)] = "1.0.0"
		}
		specs[i] = LinkSpec{
			Name: fmt.Sprintf("pkg%d", i), Version: "1.0.0", Integrity: integ[i],
			Path: fmt.Sprintf("pkg%d", i), IsDirect: i < 10, Dependencies: deps,
		}
	}
	return specs
}

func BenchmarkIsolatedLink(b *testing.B) {
	for _, n := range []int{50, 500, 2000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			st := store.New(b.TempDir())
			if err := st.Initialize(); err != nil {
				b.Fatal(err)
			}
			specs := benchGraph(b, st, n, 6)
			l := IsolatedLinker{Store: st}
			base := b.TempDir()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root := filepath.Join(base, fmt.Sprintf("r%d", i))
				if err := l.Link(root, specs); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
