package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace is one resolved workspace member.
type Workspace struct {
	Name        string
	RootPath    string
	PackageJSON *PackageJSON
}

// ResolveWorkspaces expands the workspace glob patterns relative to
// projectRoot and reads each member's package.json (doc/spec.md §8).
// Patterns support a trailing/segment `*`; a `**` segment matches any
// depth. Members without a readable package.json are skipped.
func ResolveWorkspaces(projectRoot string, patterns []string) []Workspace {
	seen := map[string]bool{}
	var out []Workspace
	for _, pattern := range patterns {
		for _, dir := range expandPattern(projectRoot, pattern) {
			if seen[dir] {
				continue
			}
			pj := filepath.Join(dir, "package.json")
			pkg, err := ReadPackageJSON(pj)
			if err != nil {
				continue
			}
			seen[dir] = true
			out = append(out, Workspace{Name: pkg.Name, RootPath: dir, PackageJSON: pkg})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// expandPattern returns directories under projectRoot matching pattern.
func expandPattern(projectRoot, pattern string) []string {
	pattern = strings.TrimSuffix(pattern, "/")
	if strings.Contains(pattern, "**") {
		return walkMatch(projectRoot, pattern)
	}
	matches, err := filepath.Glob(filepath.Join(projectRoot, filepath.FromSlash(pattern)))
	if err != nil {
		return nil
	}
	var dirs []string
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			dirs = append(dirs, m)
		}
	}
	return dirs
}

// walkMatch handles a `**` pattern by walking the tree under the static
// prefix and collecting every directory that contains a package.json.
func walkMatch(projectRoot, pattern string) []string {
	prefix := pattern
	if i := strings.Index(pattern, "**"); i >= 0 {
		prefix = strings.TrimSuffix(pattern[:i], "/")
	}
	base := filepath.Join(projectRoot, filepath.FromSlash(prefix))
	var dirs []string
	filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, "package.json")); err == nil && path != base {
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs
}

// WorkspacePatterns returns the workspace globs for the project, choosing
// pnpm-workspace.yaml#packages in pnpm mode and package.json#workspaces
// otherwise.
func WorkspacePatterns(projectRoot string, pkg *PackageJSON, mode Mode) []string {
	if mode == ModePnpm {
		if ws := ReadPnpmWorkspace(projectRoot); len(ws.Packages) > 0 {
			return ws.Packages
		}
	}
	return pkg.Workspaces
}
