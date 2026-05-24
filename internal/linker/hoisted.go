package linker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/platform"
	"github.com/koji-1009/gnpm/internal/store"
)

// HoistedLinker materializes each package at its resolved node_modules
// path (npm's flat-with-nesting layout): top-level packages land at
// node_modules/<name>, and version-conflicting copies at
// node_modules/<parent>/node_modules/<name>. Bin shims are written into
// the .bin of the node_modules directory the package sits in.
type HoistedLinker struct {
	Store *store.Store
}

// Link materializes packages by their InstallPath and writes bin shims.
func (l HoistedLinker) Link(projectRoot string, packages []LinkSpec) ([]string, error) {
	nodeModules := filepath.Join(projectRoot, "node_modules")
	if err := os.MkdirAll(filepath.Join(nodeModules, ".bin"), 0o755); err != nil {
		return nil, core.IOError("creating node_modules/.bin").Wrap(err)
	}

	// Deduplicate by install path (the tree resolver already assigns each
	// instance a distinct path; this guards against accidental repeats).
	seen := map[string]bool{}
	specs := make([]LinkSpec, 0, len(packages))
	for _, s := range packages {
		p := s.InstallPath()
		if seen[p] {
			continue
		}
		seen[p] = true
		specs = append(specs, s)
	}

	// Materialize shallowest-first: a parent must exist before its nested
	// node_modules is populated, otherwise a parent's recursive clonefile
	// (which requires a non-existent destination) would wipe a child that
	// a concurrent task already placed under it.
	for _, depth := range sortedDepths(specs) {
		group := specsAtDepth(specs, depth)
		if err := core.ForEachLimited(group, core.DefaultParallelism(), func(spec LinkSpec) error {
			dest := filepath.Join(nodeModules, filepath.FromSlash(spec.InstallPath()))
			if _, err := os.Stat(dest); err == nil {
				return nil // already materialized
			}
			if spec.CopyFrom != "" {
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return core.IOError("creating parent for %s", spec.InstallPath()).Wrap(err)
				}
				return platform.CopyTreeExcluding(spec.CopyFrom, dest, ".git")
			}
			return l.Store.Materialize(spec.Integrity, dest)
		}); err != nil {
			return nil, err
		}
	}

	for _, spec := range specs {
		binDir := binDirFor(nodeModules, spec.InstallPath())
		for binName, binPath := range spec.Bin {
			source := filepath.Join(nodeModules, filepath.FromSlash(spec.InstallPath()), filepath.FromSlash(binPath))
			if err := writeBinShim(source, filepath.Join(binDir, binName)); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

// LinkedPath returns the materialized root of spec.
func (HoistedLinker) LinkedPath(projectRoot string, spec LinkSpec) string {
	return filepath.Join(projectRoot, "node_modules", filepath.FromSlash(spec.InstallPath()))
}

// pathDepth counts the nesting level of an install path (0 = top level).
func pathDepth(relPath string) int {
	return strings.Count(relPath, "/node_modules/")
}

// sortedDepths returns the distinct nesting depths present, ascending.
func sortedDepths(specs []LinkSpec) []int {
	set := map[int]bool{}
	for _, s := range specs {
		set[pathDepth(s.InstallPath())] = true
	}
	out := make([]int, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Ints(out)
	return out
}

func specsAtDepth(specs []LinkSpec, depth int) []LinkSpec {
	var out []LinkSpec
	for _, s := range specs {
		if pathDepth(s.InstallPath()) == depth {
			out = append(out, s)
		}
	}
	return out
}

// binDirFor returns the .bin directory of the node_modules that directly
// contains the package at relPath (relative to the top-level
// node_modules). Scoped names still bin into that node_modules/.bin, not
// the scope directory.
func binDirFor(nodeModules, relPath string) string {
	if i := strings.LastIndex(relPath, "/node_modules/"); i >= 0 {
		return filepath.Join(nodeModules, filepath.FromSlash(relPath[:i]), "node_modules", ".bin")
	}
	return filepath.Join(nodeModules, ".bin")
}
