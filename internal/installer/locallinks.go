package installer

import (
	"os"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/platform"
	"github.com/koji-1009/gnpm/internal/project"
)

// applyLocalLinks materializes file: and link: dependencies as symlinks
// into node_modules/<name>, pointing at the local source directory
// (doc/spec.md §3). Both protocols are linked rather than copied; the
// linked package's own node_modules satisfies its transitive imports via
// standard Node resolution.
func (op *Operation) applyLocalLinks(pkg *project.PackageJSON) error {
	nodeModules := filepath.Join(op.ProjectRoot, "node_modules")
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
	for logical, raw := range merged {
		spec := project.ParseSpec(logical, raw)
		if spec.Protocol != project.ProtoFile && spec.Protocol != project.ProtoLink {
			continue
		}
		target := spec.Range
		if !filepath.IsAbs(target) {
			target = filepath.Join(op.ProjectRoot, target)
		}
		linkPath := filepath.Join(nodeModules, filepath.FromSlash(logical))
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			return core.IOError("mkdir for local link %s", logical).Wrap(err)
		}
		rel, err := filepath.Rel(filepath.Dir(linkPath), target)
		if err != nil {
			rel = target
		}
		if err := platform.CreateDirSymlink(linkPath, rel); err != nil {
			return core.IOError("linking %s → %s", logical, target).Wrap(err)
		}
	}
	return nil
}
