package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/workspacestate"
)

// fakeReg is an in-memory npm registry over httptest.
type fakeReg struct {
	srv        *httptest.Server
	packuments map[string]map[string]any // name → packument
	tarballs   map[string][]byte         // path → bytes
}

func newFakeReg(t *testing.T) *fakeReg {
	r := &fakeReg{packuments: map[string]map[string]any{}, tarballs: map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		if tb, ok := r.tarballs[path]; ok {
			w.Write(tb)
			return
		}
		name := strings.TrimPrefix(path, "/")
		name = strings.ReplaceAll(name, "%2f", "/")
		if p, ok := r.packuments[name]; ok {
			json.NewEncoder(w).Encode(p)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// add registers name@version with the given deps and optional bin.
func (r *fakeReg) add(t *testing.T, name, version string, deps map[string]string, bin map[string]string) {
	files := map[string]string{
		"package.json": fmt.Sprintf(`{"name":%q,"version":%q}`, name, version),
		"index.js":     "module.exports = " + strconv.Quote(name),
	}
	for _, p := range bin {
		files[p] = "#!/usr/bin/env node\n"
	}
	tarball := makeTarball(t, files)
	sum := sha512.Sum512(tarball)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	tarPath := "/" + name + "/-/" + lastSeg(name) + "-" + version + ".tgz"
	r.tarballs[tarPath] = tarball

	p := r.packuments[name]
	if p == nil {
		p = map[string]any{"name": name, "dist-tags": map[string]any{}, "versions": map[string]any{}}
		r.packuments[name] = p
	}
	ver := map[string]any{
		"name": name, "version": version,
		"dist": map[string]any{"tarball": r.srv.URL + tarPath, "integrity": integrity},
	}
	if len(deps) > 0 {
		ver["dependencies"] = toAnyMap(deps)
	}
	if len(bin) > 0 {
		ver["bin"] = toAnyMap(bin)
	}
	p["versions"].(map[string]any)[version] = ver
	p["dist-tags"].(map[string]any)["latest"] = version
}

func TestInstallEndToEnd(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	reg.add(t, "lib", "2.0.0", map[string]string{"leaf": "^1.0.0"}, nil)
	reg.add(t, "app", "1.0.0", map[string]string{"lib": "^2.0.0"}, map[string]string{"app": "bin/app.js"})

	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"app":"^1.0.0"}}`)

	op := newOp(t, root)
	report, err := op.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 3 {
		t.Errorf("added = %d, want 3", report.Added)
	}

	for _, name := range []string{"app", "lib", "leaf"} {
		if _, err := os.Stat(filepath.Join(root, "node_modules", name, "package.json")); err != nil {
			t.Errorf("node_modules/%s missing: %v", name, err)
		}
	}
	// bin shim for app (named app.cmd on Windows).
	if _, err := os.Stat(filepath.Join(root, "node_modules", ".bin", binShimName("app"))); err != nil {
		t.Errorf(".bin/app shim missing: %v", err)
	}
	// lockfile written with three packages.
	lock, err := lockfile.Read(root, reg.srv.URL)
	if err != nil || lock == nil {
		t.Fatalf("lockfile read: %v", err)
	}
	if len(lock.Packages) != 3 {
		t.Errorf("lockfile has %d packages, want 3", len(lock.Packages))
	}
	if lock.Importers["."].Dependencies["app"] != "^1.0.0" {
		t.Errorf("importer deps wrong: %v", lock.Importers["."])
	}
	// workspace state written.
	if st, _ := workspacestate.Read(root); st == nil || st.Hash == "" {
		t.Error("workspace state not written")
	}
}

func TestInstallPrunesExtraneous(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	reg.add(t, "lib", "2.0.0", map[string]string{"leaf": "^1.0.0"}, nil)
	reg.add(t, "app", "1.0.0", map[string]string{"lib": "^2.0.0"}, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"app":"^1.0.0"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"app", "lib", "leaf"} {
		if _, err := os.Stat(filepath.Join(root, "node_modules", n)); err != nil {
			t.Fatalf("%s should be installed: %v", n, err)
		}
	}
	// Drop the dependency; reinstall must prune app, lib, and leaf.
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0"}`)
	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"app", "lib", "leaf"} {
		if _, err := os.Stat(filepath.Join(root, "node_modules", n)); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", n)
		}
	}
}

func TestInstallMultipleVersions(t *testing.T) {
	// A needs x@^1, B needs x@^2 → gnpm must install BOTH (pubgrub would
	// have failed). x@1 hoists to top; x@2 nests under B.
	reg := newFakeReg(t)
	reg.add(t, "x", "1.5.0", nil, nil)
	reg.add(t, "x", "2.3.0", nil, nil)
	reg.add(t, "A", "1.0.0", map[string]string{"x": "^1.0.0"}, nil)
	reg.add(t, "B", "1.0.0", map[string]string{"x": "^2.0.0"}, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"A":"^1.0.0","B":"^1.0.0"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatalf("multi-version graph should install, not fail: %v", err)
	}
	top, _ := os.ReadFile(filepath.Join(root, "node_modules", "x", "package.json"))
	if !strings.Contains(string(top), `"1.5.0"`) {
		t.Errorf("top-level x should be 1.5.0, got %s", top)
	}
	nested, err := os.ReadFile(filepath.Join(root, "node_modules", "B", "node_modules", "x", "package.json"))
	if err != nil {
		t.Fatalf("nested x for B not installed: %v", err)
	}
	if !strings.Contains(string(nested), `"2.3.0"`) {
		t.Errorf("nested x should be 2.3.0, got %s", nested)
	}
	// The lockfile records both at their distinct paths.
	lock, _ := lockfile.Read(root, reg.srv.URL)
	if _, ok := lock.Packages["x"]; !ok {
		t.Error("lockfile missing top-level x")
	}
	if _, ok := lock.Packages["B/node_modules/x"]; !ok {
		t.Error("lockfile missing nested x path")
	}
}

func TestInstallOptimisticShortCircuit(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Delete node_modules to prove the short-circuit does NOT re-link
	// (it returns before touching the store) when state matches.
	os.RemoveAll(filepath.Join(root, "node_modules", "leaf"))
	// Re-read: state still present, hash unchanged → short-circuit.
	report, err := newOp(t, root).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 0 {
		t.Errorf("expected optimistic short-circuit (0 added), got %d", report.Added)
	}
}

func TestInstallFrozenLockfileDivergence(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`)

	// No lockfile yet + frozen → resolution diverges from (absent) lock.
	op := newOp(t, root)
	op.Options.FrozenLockfile = true
	op.Options.OptimisticRepeatInstall = false
	_, err := op.Run(context.Background())
	if err == nil {
		t.Fatal("expected frozen-lockfile divergence error")
	}
	if core.ExitCodeFor(err) != core.ExitUsage {
		t.Errorf("frozen divergence should be a usage error (64), got %d", core.ExitCodeFor(err))
	}
}

// --- helpers ----------------------------------------------------------

// drainNodeDetect blocks until the background node-version detection (which
// writes node-version.json into the cache root) has finished, so it can't
// race t.TempDir cleanup.
func drainNodeDetect(op *Operation) {
	if op.nodeVer != nil {
		op.nodeVer()
	}
}

// jsonStr escapes s for embedding inside a JSON string literal. Windows
// temp paths contain backslashes, which are JSON (and JSON-string) escape
// characters, so a raw path produces invalid package.json.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1]) // strip the surrounding quotes
}

// binShimName is the .bin entry filename for this OS (Windows shims are
// <name>.cmd / .ps1; elsewhere it is the bare name).
func binShimName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".cmd"
	}
	return name
}

func newOp(t *testing.T, root string) *Operation {
	t.Helper()
	opts := DefaultOptions()
	opts.StoreRoot = filepath.Join(t.TempDir(), "store")
	opts.CacheRoot = filepath.Join(t.TempDir(), "cache")
	return &Operation{ProjectRoot: root, Options: opts, Log: core.NewLogger("test", core.LevelError), Version: "test"}
}

func TestInstallLinkDependency(t *testing.T) {
	reg := newFakeReg(t)
	root := t.TempDir()
	localLib := filepath.Join(t.TempDir(), "local-lib")
	if err := os.MkdirAll(localLib, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(localLib, "package.json"), []byte(`{"name":"local-lib","version":"1.0.0"}`), 0o644)

	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"local-lib":"link:`+jsonStr(localLib)+`"}}`)
	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "node_modules", "local-lib")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("link not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("node_modules/local-lib should be a symlink")
	}
	if b, _ := os.ReadFile(filepath.Join(linkPath, "package.json")); !strings.Contains(string(b), "local-lib") {
		t.Errorf("symlink does not resolve to the local package: %q", b)
	}
}

func TestInstallConfigDependencies(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "shared-config", "1.0.0", nil, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","gnpm":{"configDependencies":{"shared-config":"1.0.0"}}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", ".gnpm-config", "shared-config", "package.json")); err != nil {
		t.Errorf("config dependency not materialized under .gnpm-config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", "shared-config")); !os.IsNotExist(err) {
		t.Error("config dependency should not be in regular node_modules")
	}
}

func TestInstallHTTPSDependency(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	// The remote tarball declares a registry dependency, which must be
	// resolved and hoisted (not left as a broken leaf).
	tarball := makeTarball(t, map[string]string{"package.json": `{"name":"remote","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`})
	reg.tarballs["/remote.tgz"] = tarball
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"remote":"`+reg.srv.URL+`/remote.tgz"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", "remote", "package.json")); err != nil {
		t.Errorf("https tarball dependency not materialized: %v", err)
	}
	// Its transitive dependency must be installed at top level.
	if _, err := os.Stat(filepath.Join(root, "node_modules", "leaf", "package.json")); err != nil {
		t.Errorf("https dependency's transitive dep (leaf) not resolved: %v", err)
	}
	// The lockfile must pin the tarball's integrity for reproducibility.
	lock, _ := lockfile.Read(root, reg.srv.URL)
	pinned := false
	for _, p := range lock.Packages {
		if p.Tarball == reg.srv.URL+"/remote.tgz" && strings.HasPrefix(p.Integrity, "sha512-") {
			pinned = true
		}
	}
	if !pinned {
		t.Error("https dependency not pinned by integrity in the lockfile")
	}
}

func TestInstallGitDependency(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Build a local git repo to clone via git+file://.
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false") // avoid host signing setup hanging
	// The git package declares a registry dependency to verify transitive
	// resolution of git deps.
	os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"gitdep","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`), 0o644)
	run("add", ".")
	run("commit", "-q", "--no-gpg-sign", "-m", "init")

	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	root := t.TempDir()
	home := t.TempDir() // isolate ~/.gnpm/git
	t.Setenv("HOME", home)
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"gitdep":"git+file://`+jsonStr(repo)+`"}}`)

	op := newOp(t, root)
	if _, err := op.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", "gitdep", "package.json")); err != nil {
		t.Errorf("git dependency not materialized: %v", err)
	}
	// gitdep is a real directory (not a symlink), so Node resolution
	// reaches the top-level node_modules where its transitive dep lives.
	if info, _ := os.Lstat(filepath.Join(root, "node_modules", "gitdep")); info != nil && info.Mode()&os.ModeSymlink != 0 {
		t.Error("git dependency should be a real directory, not a symlink")
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", "leaf", "package.json")); err != nil {
		t.Errorf("git dependency's transitive dep (leaf) not resolved: %v", err)
	}
	// The lockfile must pin the resolved commit SHA for reproducibility.
	lock, _ := lockfile.Read(root, reg.srv.URL)
	pinned := false
	for _, p := range lock.Packages {
		if strings.HasPrefix(p.Tarball, "git+file://") && strings.Contains(p.Tarball, "#") {
			commit := p.Tarball[strings.LastIndexByte(p.Tarball, '#')+1:]
			if len(commit) == 40 { // full SHA-1
				pinned = true
			}
		}
	}
	if !pinned {
		t.Error("git dependency not pinned to a resolved commit SHA in the lockfile")
	}
}

func TestInstallTransitiveExoticDependency(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	// A remote tarball that itself depends on a registry package.
	remoteTar := makeTarball(t, map[string]string{"package.json": `{"name":"remote","version":"2.0.0","dependencies":{"leaf":"^1.0.0"}}`})
	reg.tarballs["/remote.tgz"] = remoteTar
	// A *registry* package "host" whose dependency is the remote tarball —
	// i.e. a transitive exotic dependency, which must be fetched, its deps
	// resolved, and materialized (not warned-and-skipped).
	reg.add(t, "host", "1.0.0", map[string]string{"remote": reg.srv.URL + "/remote.tgz"}, nil)

	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"host":"^1.0.0"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// host (registry), remote (transitive exotic), and remote's own
	// transitive registry dep leaf must all be installed.
	for _, name := range []string{"host", "remote", "leaf"} {
		if _, err := os.Stat(filepath.Join(root, "node_modules", name, "package.json")); err != nil {
			t.Errorf("node_modules/%s missing: %v", name, err)
		}
	}
	// remote is a real directory, not a symlink, so Node resolution reaches
	// the hoisted leaf.
	if info, _ := os.Lstat(filepath.Join(root, "node_modules", "remote")); info != nil && info.Mode()&os.ModeSymlink != 0 {
		t.Error("transitive exotic dep should be a real directory, not a symlink")
	}
	// The lockfile pins the transitive exotic by integrity.
	lock, _ := lockfile.Read(root, reg.srv.URL)
	pinned := false
	for _, p := range lock.Packages {
		if p.Tarball == reg.srv.URL+"/remote.tgz" && strings.HasPrefix(p.Integrity, "sha512-") {
			pinned = true
		}
	}
	if !pinned {
		t.Error("transitive exotic dependency not pinned by integrity in the lockfile")
	}
}

func TestInstallTransitiveGitLockedFastPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Local git repo for the transitive git dep; it depends on a registry leaf.
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"gitdep","version":"1.0.0","dependencies":{"leaf":"^1.0.0"}}`), 0o644)
	run("add", ".")
	run("commit", "-q", "--no-gpg-sign", "-m", "init")

	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	// A registry package whose dependency is the git repo (transitive git).
	reg.add(t, "host", "1.0.0", map[string]string{"gitdep": "git+file://" + repo}, nil)

	root := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gnpm/git
	writeProject(t, root, reg.srv.URL, `{"name":"demo","version":"1.0.0","dependencies":{"host":"^1.0.0"}}`)

	op := newOp(t, root) // one op → store/cache persist across both installs
	if _, err := op.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainNodeDetect(op) // let the async node-version cache write finish before cleanup
	for _, n := range []string{"host", "gitdep", "leaf"} {
		if _, err := os.Stat(filepath.Join(root, "node_modules", n, "package.json")); err != nil {
			t.Fatalf("first install: node_modules/%s missing: %v", n, err)
		}
	}

	// Prove the locked fast path handles the transitive git entry: drop
	// node_modules, take the registry down, and reinstall frozen. Registry
	// tarballs are in the store and the commit is cloned, so a true locked
	// install needs no network — a full re-resolve would refetch the host
	// packument and fail.
	reg.srv.Close()
	if err := os.RemoveAll(filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	op.Options.FrozenLockfile = true
	if _, err := op.Run(context.Background()); err != nil {
		t.Fatalf("frozen locked reinstall of a transitive git dep should succeed offline: %v", err)
	}
	drainNodeDetect(op)
	for _, n := range []string{"host", "gitdep", "leaf"} {
		if _, err := os.Stat(filepath.Join(root, "node_modules", n, "package.json")); err != nil {
			t.Errorf("locked reinstall: node_modules/%s missing: %v", n, err)
		}
	}
	if info, _ := os.Lstat(filepath.Join(root, "node_modules", "gitdep")); info != nil && info.Mode()&os.ModeSymlink != 0 {
		t.Error("gitdep should be a real directory under the locked fast path, not a symlink")
	}
}

func TestInstallWorkspaces(t *testing.T) {
	reg := newFakeReg(t)
	reg.add(t, "leaf", "1.0.0", nil, nil)
	root := t.TempDir()
	writeProject(t, root, reg.srv.URL, `{"name":"mono","version":"1.0.0","workspaces":["packages/*"]}`)

	mkMember := func(name, body string) {
		dir := filepath.Join(root, "packages", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644)
	}
	mkMember("lib", `{"name":"lib","version":"1.0.0"}`)
	mkMember("app", `{"name":"app","version":"1.0.0","dependencies":{"lib":"workspace:*","leaf":"^1.0.0"}}`)

	if _, err := newOp(t, root).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// leaf is hoisted to the root.
	if _, err := os.Stat(filepath.Join(root, "node_modules", "leaf", "package.json")); err != nil {
		t.Errorf("leaf not hoisted to root: %v", err)
	}
	// app's node_modules links the workspace sibling lib and the hoisted leaf.
	appNM := filepath.Join(root, "packages", "app", "node_modules")
	if b, _ := os.ReadFile(filepath.Join(appNM, "lib", "package.json")); !strings.Contains(string(b), `"lib"`) {
		t.Errorf("app/node_modules/lib does not resolve to the workspace sibling: %q", b)
	}
	if _, err := os.Stat(filepath.Join(appNM, "leaf", "package.json")); err != nil {
		t.Errorf("app/node_modules/leaf not linked to hoisted leaf: %v", err)
	}
}

func writeProject(t *testing.T, root, registryURL, pkgJSON string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// Point the project's registry at the fake server (also forces npm mode).
	if err := os.WriteFile(filepath.Join(root, ".npmrc"), []byte("registry="+registryURL+"/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		mode := int64(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func toAnyMap(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func lastSeg(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}
