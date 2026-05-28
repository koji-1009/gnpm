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
// store: node_modules/.gnpm/<id>/node_modules (id = name@version, "/"→"+").
func isoStoreNM(root, id string) string {
	return filepath.Join(root, "node_modules", ".gnpm", strings.ReplaceAll(id, "/", "+"), "node_modules")
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
