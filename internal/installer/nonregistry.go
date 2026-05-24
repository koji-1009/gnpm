package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/project"
)

// directDeps returns the project's direct dependencies (deps + dev unless
// production + optional) as logical name → raw specifier.
func (op *Operation) directDeps(pkg *project.PackageJSON) map[string]string {
	merged := map[string]string{}
	for k, v := range pkg.Dependencies {
		merged[k] = v
	}
	if !op.Options.Production {
		for k, v := range pkg.DevDependencies {
			merged[k] = v
		}
	}
	for k, v := range pkg.OptionalDependencies {
		merged[k] = v
	}
	return merged
}

// findHTTPSLock returns the integrity the existing lockfile pins for an
// https URL, if any (for reproducibility verification). The lockfile is a
// map, and the same URL may now occupy several paths (a transitive exotic
// hoisted and nested), so selection is deterministic: the smallest
// integrity among matches.
func findHTTPSLock(existing *lockfile.Lockfile, url string) (string, bool) {
	if existing == nil {
		return "", false
	}
	best, found := "", false
	for _, p := range existing.Packages {
		if p.Tarball == url && p.Integrity != "" && (!found || p.Integrity < best) {
			best, found = p.Integrity, true
		}
	}
	return best, found
}

// findGitLock returns the commit SHA the existing lockfile pins for a git
// clone URL, if any. Selection is deterministic (smallest commit among
// matches) since the same clone URL may occupy several lockfile paths.
func findGitLock(existing *lockfile.Lockfile, cloneURL string) (string, bool) {
	if existing == nil {
		return "", false
	}
	prefix := "git+" + cloneURL + "#"
	best, found := "", false
	for _, p := range existing.Packages {
		if commit, ok := strings.CutPrefix(p.Tarball, prefix); ok && commit != "" && (!found || commit < best) {
			best, found = commit, true
		}
	}
	return best, found
}

func httpGet(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, core.NetworkError("building request for %s: %v", rawURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, core.NetworkError("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, core.NetworkError("GET %s failed (%d)", rawURL, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func gitClone(ctx context.Context, cloneURL, ref, dir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return core.Usage("git dependency requires the `git` CLI on PATH")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return core.IOError("creating git cache dir").Wrap(err)
	}
	if out, err := exec.CommandContext(ctx, "git", "clone", "--quiet", cloneURL, dir).CombinedOutput(); err != nil {
		// Remove any partial clone so a failed fetch can't poison the cache
		// (a leftover dir would make later installs skip the clone and then
		// fail on a broken repo).
		os.RemoveAll(dir)
		return core.NetworkError("git clone %s failed: %s", cloneURL, strings.TrimSpace(string(out)))
	}
	if ref != "" {
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "checkout", "--quiet", ref).CombinedOutput(); err != nil {
			os.RemoveAll(dir)
			return core.NetworkError("git checkout %s failed: %s", ref, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// isGitLockEntry reports whether a lockfile package is a git dependency:
// pinned by commit in Tarball ("git+<url>#<commit>") with no registry
// integrity.
func isGitLockEntry(p lockfile.LockedPackage) bool {
	return p.Integrity == "" && strings.HasPrefix(p.Tarball, "git+")
}

// ensureGitClone makes sure the commit pinned by a "git+<url>#<commit>"
// lockfile tarball is present in the shared git cache (cloning it if not),
// and returns the clone directory. The cache key matches fetchExotic so a
// commit cloned during a full install is reused by a locked install.
func (op *Operation) ensureGitClone(ctx context.Context, tarball string) (string, error) {
	cloneURL, commit := parseGitURL(tarball)
	if commit == "" {
		return "", core.LockfileError("git lockfile entry has no pinned commit: %q", tarball)
	}
	key := sha256Hex(cloneURL + "#" + commit)[:16]
	dir := filepath.Join(homeDir(), ".gnpm", "git", key)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := gitClone(ctx, cloneURL, commit, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func gitHead(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", core.IOError("git rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// parseGitURL normalizes a git specifier into a clone URL and an optional
// ref. Handles github:owner/repo#ref, git+https://...#ref, and git://.
func parseGitURL(raw string) (cloneURL, ref string) {
	if hash := strings.LastIndexByte(raw, '#'); hash >= 0 {
		ref = raw[hash+1:]
		raw = raw[:hash]
	}
	switch {
	case strings.HasPrefix(raw, "github:"):
		cloneURL = "https://github.com/" + strings.TrimPrefix(raw, "github:") + ".git"
	case strings.HasPrefix(raw, "git+"):
		cloneURL = strings.TrimPrefix(raw, "git+")
	default:
		cloneURL = raw
	}
	return cloneURL, ref
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
