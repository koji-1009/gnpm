package installer

import (
	"os"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/platform"
	"github.com/koji-1009/gnpm/internal/project"
)

// linkWorkspaces wires a monorepo's members after the root install
// (doc/spec.md §8). Every member's registry dependencies are hoisted to
// the root node_modules; this step gives each member its own
// node_modules populated with symlinks to those hoisted packages, plus
// reciprocal workspace-to-workspace symlinks. The root node_modules also
// gets a <memberName> → member symlink for convenience.
func (op *Operation) linkWorkspaces(members []project.Workspace, linkSpecs []linker.LinkSpec) error {
	if len(members) == 0 {
		return nil
	}
	rootNM := filepath.Join(op.ProjectRoot, "node_modules")
	installed := map[string]bool{}
	for _, s := range linkSpecs {
		installed[s.TopLevelName()] = true
	}
	memberByName := map[string]project.Workspace{}
	for _, m := range members {
		memberByName[m.Name] = m
	}

	// 1) <root>/node_modules/<memberName> → member dir.
	for _, m := range members {
		if m.Name == "" {
			continue
		}
		if err := symlinkIfAbsent(filepath.Join(rootNM, filepath.FromSlash(m.Name)), m.RootPath); err != nil {
			return err
		}
	}

	// 2) Each member's own node_modules.
	for _, m := range members {
		memberNM := filepath.Join(m.RootPath, "node_modules")
		if err := os.MkdirAll(memberNM, 0o755); err != nil {
			return core.IOError("creating %s", memberNM).Wrap(err)
		}
		deps := map[string]string{}
		for k, v := range m.PackageJSON.Dependencies {
			deps[k] = v
		}
		if !op.Options.Production {
			for k, v := range m.PackageJSON.DevDependencies {
				deps[k] = v
			}
		}
		for k, v := range m.PackageJSON.OptionalDependencies {
			deps[k] = v
		}
		for logical, raw := range deps {
			spec := project.ParseSpec(logical, raw)
			linkPath := filepath.Join(memberNM, filepath.FromSlash(spec.LogicalName))
			// workspace-to-workspace (workspace: protocol, or a dep named
			// after another member).
			if target, ok := memberByName[spec.PackageName]; ok && (spec.Protocol == project.ProtoWorkspace || spec.Protocol == project.ProtoSemver) {
				if err := symlinkIfAbsent(linkPath, target.RootPath); err != nil {
					return err
				}
				continue
			}
			if spec.Protocol != project.ProtoSemver {
				continue // file/link/git/https/catalog handled elsewhere
			}
			// Otherwise link to the root-hoisted package.
			if installed[spec.PackageName] {
				if err := symlinkIfAbsent(linkPath, filepath.Join(rootNM, filepath.FromSlash(spec.PackageName))); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func symlinkIfAbsent(linkPath, target string) error {
	if _, err := os.Lstat(linkPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return core.IOError("mkdir for symlink %s", linkPath).Wrap(err)
	}
	rel, err := filepath.Rel(filepath.Dir(linkPath), target)
	if err != nil {
		rel = target
	}
	if err := platform.CreateDirSymlink(linkPath, rel); err != nil {
		return core.IOError("symlinking %s", linkPath).Wrap(err)
	}
	return nil
}
