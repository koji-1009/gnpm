package installer

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/project"
)

// prune removes top-level node_modules entries that are no longer part of
// the install (extraneous packages left by a removed dependency), so the
// tree matches the resolved set — npm/pnpm do this. Hidden entries
// (.bin, .gnpm, .gnpm-config) are preserved, and scope directories are
// pruned member-by-member.
func (op *Operation) prune(linkSpecs []linker.LinkSpec, pkg *project.PackageJSON) error {
	nm := filepath.Join(op.ProjectRoot, "node_modules")
	entries, err := os.ReadDir(nm)
	if err != nil {
		return nil // no node_modules yet
	}

	kept := map[string]bool{}
	for _, s := range linkSpecs {
		kept[s.TopLevelName()] = true
	}
	// Direct non-registry deps materialize under their logical name.
	for logical, raw := range op.directDeps(pkg) {
		switch project.ParseSpec(logical, raw).Protocol {
		case project.ProtoFile, project.ProtoLink, project.ProtoGit, project.ProtoHTTPS, project.ProtoWorkspace:
			kept[logical] = true
		}
	}
	// Workspace members get a convenience symlink at the root.
	for _, m := range project.ResolveWorkspaces(op.ProjectRoot, project.WorkspacePatterns(op.ProjectRoot, pkg, project.DetectMode(op.ProjectRoot))) {
		if m.Name != "" {
			kept[m.Name] = true
		}
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // .bin / .gnpm / .gnpm-config and other metadata
		}
		if strings.HasPrefix(name, "@") {
			op.pruneScope(filepath.Join(nm, name), name, kept)
			continue
		}
		if !kept[name] {
			if err := os.RemoveAll(filepath.Join(nm, name)); err != nil {
				return core.IOError("pruning %s", name).Wrap(err)
			}
		}
	}
	return nil
}

// pruneScope removes stale packages inside a @scope directory and drops
// the scope dir when it becomes empty.
func (op *Operation) pruneScope(scopeDir, scope string, kept map[string]bool) {
	subs, err := os.ReadDir(scopeDir)
	if err != nil {
		return
	}
	for _, s := range subs {
		full := scope + "/" + s.Name()
		if !kept[full] {
			os.RemoveAll(filepath.Join(scopeDir, s.Name()))
		}
	}
	if remaining, _ := os.ReadDir(scopeDir); len(remaining) == 0 {
		os.Remove(scopeDir)
	}
}
