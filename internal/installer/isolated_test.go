package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koji-1009/gnpm/internal/lockfile"
)

// isolatedProject writes a project pinned to the fake registry and selects the
// isolated (pnpm-style) linker.
func isolatedProject(t *testing.T, root, registryURL, pkgJSON string) {
	t.Helper()
	writeProject(t, root, registryURL, pkgJSON)
	if err := os.WriteFile(filepath.Join(root, ".npmrc"),
		[]byte("registry="+registryURL+"/\nnode-linker=isolated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// pnpm mode (pnpm-lock.yaml) + the isolated linker is the realistic pnpm
	// setup; node-linker selects the linker, the workspace file selects the mode.
	if err := os.WriteFile(filepath.Join(root, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isoStoreNM returns a package instance's private node_modules in the virtual
// store: node_modules/.pnpm/<id>/node_modules (id = name@version, "/"→"+").
func isoStoreNM(root, id string) string {
	return filepath.Join(root, "node_modules", ".pnpm", strings.ReplaceAll(id, "/", "+"), "node_modules")
}

// TestIsolatedTransitive checks the isolated layout wires each package's
// dependencies into its own private node_modules (strict pnpm-style nesting):
// root→app→lib→leaf, each link resolvable through the virtual store.
func TestIsolatedTransitive(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	reg.add(t, "lib", "2.0.0", map[string]string{"leaf": "^1.0.0"}, nil)
	reg.add(t, "app", "1.0.0", map[string]string{"lib": "^2.0.0"}, nil)
	root := t.TempDir()
	isolatedProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"app":"^1.0.0"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatalf("isolated install failed: %v", err)
	}
	// Direct dep app is symlinked at the top level.
	if _, err := os.Stat(filepath.Join(root, "node_modules", "app", "package.json")); err != nil {
		t.Errorf("top-level app not linked: %v", err)
	}
	// app sees lib; lib sees leaf — each in the consumer's private node_modules.
	if _, err := os.Stat(filepath.Join(isoStoreNM(root, "app@1.0.0"), "lib", "package.json")); err != nil {
		t.Errorf("app cannot see lib: %v", err)
	}
	if _, err := os.Stat(filepath.Join(isoStoreNM(root, "lib@2.0.0"), "leaf", "package.json")); err != nil {
		t.Errorf("lib cannot see leaf: %v", err)
	}
	// Strictness: app must NOT see leaf (it doesn't declare it).
	if _, err := os.Stat(filepath.Join(isoStoreNM(root, "app@1.0.0"), "leaf")); err == nil {
		t.Error("isolated layout leaked leaf into app (should be strict)")
	}
}

// TestIsolatedReproducible checks a second install with the lockfile present
// reproduces the install without error or churn.
func TestIsolatedReproducible(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	reg.add(t, "lib", "2.0.0", map[string]string{"leaf": "^1.0.0"}, nil)
	root := t.TempDir()
	isolatedProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"lib":"^2.0.0"}}`)

	op := newOp(t, root)
	op.Options.OptimisticRepeatInstall = false // force a real second pass
	if _, err := op.Run(context.Background()); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	lock1, err := os.ReadFile(filepath.Join(root, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatalf("no pnpm-lock.yaml after install: %v", err)
	}
	// Re-resolve from scratch with the lockfile present: must succeed and not
	// rewrite the lockfile.
	os.RemoveAll(filepath.Join(root, "node_modules"))
	op2 := newOp(t, root)
	op2.Options.OptimisticRepeatInstall = false
	if _, err := op2.Run(context.Background()); err != nil {
		t.Fatalf("reinstall failed: %v", err)
	}
	lock2, _ := os.ReadFile(filepath.Join(root, "pnpm-lock.yaml"))
	if string(lock1) != string(lock2) {
		t.Errorf("lockfile changed across reinstall (non-reproducible):\n--- first ---\n%s\n--- second ---\n%s", lock1, lock2)
	}
	// Sanity: the parsed lockfile resolves both packages.
	lf, _ := lockfile.Read(root, reg.srv.URL)
	if _, ok := lf.Packages["lib@2.0.0"]; !ok {
		t.Error("lockfile missing lib@2.0.0")
	}
}

// TestNodeLinkerModeDefault checks node-linker defaults to each ecosystem's
// own default when unset: isolated (.pnpm store) in pnpm mode, hoisted (flat)
// in npm mode.
func TestNodeLinkerModeDefault(t *testing.T) {
	t.Run("pnpm mode -> isolated", func(t *testing.T) {
		reg := newFakeReg(t)
		reg.add(t, "leaf", "1.0.0", nil, nil)
		root := t.TempDir()
		writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`)
		// pnpm mode, but NO node-linker set.
		if err := os.WriteFile(filepath.Join(root, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := newOp(t, root).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "node_modules", ".pnpm", "leaf@1.0.0", "node_modules", "leaf", "package.json")); err != nil {
			t.Errorf("pnpm mode should default to the isolated .pnpm store: %v", err)
		}
	})
	t.Run("npm mode -> hoisted", func(t *testing.T) {
		reg := newFakeReg(t)
		reg.add(t, "leaf", "1.0.0", nil, nil)
		root := t.TempDir()
		writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`)
		if _, err := newOp(t, root).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "node_modules", ".pnpm")); err == nil {
			t.Error("npm mode should not create a .pnpm store")
		}
		if _, err := os.Stat(filepath.Join(root, "node_modules", "leaf", "package.json")); err != nil {
			t.Errorf("npm mode should hoist leaf to the flat node_modules: %v", err)
		}
	})
}

// TestWorkspacePnpmImporters checks that a multi-package pnpm workspace writes
// a shared pnpm-lock.yaml with a per-member importer section (pnpm's
// structure), not just the root importer.
func TestWorkspacePnpmImporters(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"mono","version":"1.0.0"}`)
	// pnpm mode + workspace globs (read from pnpm-workspace.yaml in pnpm mode).
	if err := os.WriteFile(filepath.Join(root, "pnpm-workspace.yaml"), []byte("packages:\n  - packages/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(name, body string) {
		dir := filepath.Join(root, "packages", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("lib", `{"name":"lib","version":"1.0.0"}`)
	mk("app", `{"name":"app","version":"1.0.0","dependencies":{"leaf":"^1.0.0","lib":"workspace:*"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatalf("no pnpm-lock.yaml: %v", err)
	}
	p, err := lockfile.ParsePnpm(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".", "packages/app", "packages/lib"} {
		if _, ok := p.Importers[want]; !ok {
			got := make([]string, 0, len(p.Importers))
			for k := range p.Importers {
				got = append(got, k)
			}
			t.Errorf("pnpm-lock.yaml missing importer %q; has %v", want, got)
		}
	}
	if app, ok := p.Importers["packages/app"]; ok {
		if _, ok := app.Dependencies["leaf"]; !ok {
			t.Error("packages/app importer should record its leaf dependency")
		}
		// The workspace sibling is recorded as a link: version (pnpm's form),
		// so pnpm accepts the lockfile instead of seeing the dep as missing.
		if dep, ok := app.Dependencies["lib"]; !ok {
			t.Error("packages/app importer missing the workspace dependency lib")
		} else if dep.Version != "link:../lib" {
			t.Errorf("workspace dep lib version = %q, want link:../lib", dep.Version)
		}
	}
}
