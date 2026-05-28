package linker

import (
	"os"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/platform"
	"github.com/koji-1009/gnpm/internal/store"
)

// IsolatedLinker materializes each package under
// node_modules/.gnpm/<id>/node_modules/<name> and symlinks its declared
// dependencies into that private node_modules, mirroring pnpm. Direct
// dependencies are symlinked into the top-level node_modules.
type IsolatedLinker struct {
	Store *store.Store
}

func (l IsolatedLinker) gnpmRoot(projectRoot string) string {
	return filepath.Join(projectRoot, "node_modules", ".gnpm")
}

func (l IsolatedLinker) pkgDir(projectRoot string, spec LinkSpec) string {
	return filepath.Join(l.gnpmRoot(projectRoot), safeID(spec.ID()), "node_modules", filepath.FromSlash(spec.Name))
}

// Link materializes and wires the package graph.
func (l IsolatedLinker) Link(projectRoot string, packages []LinkSpec) error {
	nodeModules := filepath.Join(projectRoot, "node_modules")
	binDir := filepath.Join(nodeModules, ".bin")
	if err := os.MkdirAll(l.gnpmRoot(projectRoot), 0o755); err != nil {
		return core.IOError("creating node_modules/.gnpm").Wrap(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return core.IOError("creating node_modules/.bin").Wrap(err)
	}

	if err := core.ForEachLimited(packages, core.DefaultParallelism(), func(spec LinkSpec) error {
		dest := l.pkgDir(projectRoot, spec)
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		// git/https-sourced packages are copied from the clone/extract dir;
		// registry ones are materialized from the content-addressable store.
		if spec.CopyFrom != "" {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return core.IOError("creating parent for %s", spec.Name).Wrap(err)
			}
			return platform.CopyTreeExcluding(spec.CopyFrom, dest, ".git")
		}
		return l.Store.Materialize(spec.Integrity, dest)
	}); err != nil {
		return err
	}

	byID := map[string]LinkSpec{}
	for _, s := range packages {
		byID[s.ID()] = s
	}

	// Wire each package's dependencies into its private node_modules.
	for _, spec := range packages {
		ownNM := filepath.Join(l.gnpmRoot(projectRoot), safeID(spec.ID()), "node_modules")
		for depName, depVer := range spec.Dependencies {
			dep, ok := byID[depName+"@"+depVer]
			if !ok {
				continue
			}
			linkPath := filepath.Join(ownNM, filepath.FromSlash(depName))
			if exists(linkPath) {
				continue
			}
			target := l.pkgDir(projectRoot, dep)
			rel, err := filepath.Rel(filepath.Dir(linkPath), target)
			if err != nil {
				rel = target
			}
			if err := platform.CreateDirSymlink(linkPath, rel); err != nil {
				return core.IOError("symlinking dependency %s", depName).Wrap(err)
			}
		}
	}

	// Top-level symlinks for direct dependencies.
	for _, spec := range packages {
		if !spec.IsDirect {
			continue
		}
		linkPath := filepath.Join(nodeModules, filepath.FromSlash(spec.TopLevelName()))
		if exists(linkPath) {
			continue
		}
		target := l.pkgDir(projectRoot, spec)
		rel, err := filepath.Rel(filepath.Dir(linkPath), target)
		if err != nil {
			rel = target
		}
		if err := platform.CreateDirSymlink(linkPath, rel); err != nil {
			return core.IOError("top-level symlink for %s", spec.TopLevelName()).Wrap(err)
		}
	}

	// Bin shims pointing into each package's materialized dir.
	for _, spec := range packages {
		for binName, binPath := range spec.Bin {
			source := filepath.Join(l.pkgDir(projectRoot, spec), filepath.FromSlash(binPath))
			if err := writeBinShim(source, filepath.Join(binDir, binName)); err != nil {
				return err
			}
		}
	}
	return nil
}

// LinkedPath returns the materialized root of spec in the isolated layout.
func (l IsolatedLinker) LinkedPath(projectRoot string, spec LinkSpec) string {
	return l.pkgDir(projectRoot, spec)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
