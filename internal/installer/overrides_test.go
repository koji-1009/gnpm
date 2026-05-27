package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/koji-1009/gnpm/internal/project"
)

// TestEffectiveOverridesSources checks that dependency overrides are honored
// from all three pnpm-compatible sources and merged with the right precedence:
// package.json `overrides` (npm) < package.json `pnpm.overrides` < the
// monorepo-wide pnpm-workspace.yaml `overrides`.
func TestEffectiveOverridesSources(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{
		"name": "root", "version": "1.0.0",
		"overrides": {"a": "1.0.0", "shared": "from-npm"},
		"pnpm": {"overrides": {"b": "2.0.0", "shared": "from-pnpm"}}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"),
		[]byte("overrides:\n  c: 3.0.0\n  shared: from-workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkg, err := project.ReadPackageJSON(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	// package.json layer: pnpm.overrides wins over top-level overrides.
	if pkg.Overrides["shared"] != "from-pnpm" {
		t.Errorf("package.json shared = %q, want from-pnpm (pnpm.overrides wins)", pkg.Overrides["shared"])
	}
	if pkg.Overrides["b"] != "2.0.0" {
		t.Errorf("pnpm.overrides b not honored: %q", pkg.Overrides["b"])
	}

	op := &Operation{ProjectRoot: dir}
	got, _ := op.effectiveOverrides(pkg)
	want := map[string]string{"a": "1.0.0", "b": "2.0.0", "c": "3.0.0", "shared": "from-workspace"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("override %q = %q, want %q (workspace wins on conflict)", k, got[k], v)
		}
	}
}
