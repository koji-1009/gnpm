package installer

import (
	"os"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/workspacestate"
)

// WorkspaceUpToDate reports whether node_modules matches the project's
// declared dependencies and lockfile, by comparing the freshly computed
// fingerprint against the recorded workspace state. Used by run / exec
// for verifyDepsBeforeRun. A missing package.json or state yields
// (false, nil).
func WorkspaceUpToDate(root string) (bool, error) {
	pkg, err := project.ReadPackageJSON(filepath.Join(root, "package.json"))
	if err != nil {
		return false, nil
	}
	mode := project.DetectMode(root)
	lockBytes, _ := os.ReadFile(filepath.Join(root, lockfile.ProjectLockfileName(mode)))

	major := ""
	if rt := pkg.DevEnginesRuntime; rt != nil && rt.Name == "node" {
		major = workspacestate.MajorString(rt.Version)
	}
	engineKey := workspacestate.EngineKey(major)

	hash := workspacestate.ComputeHash(workspacestate.HashInput{
		Dependencies:         pkg.Dependencies,
		DevDependencies:      pkg.DevDependencies,
		OptionalDependencies: pkg.OptionalDependencies,
		PeerDependencies:     pkg.PeerDependencies,
		LockfileFingerprint:  fingerprint(lockBytes),
		EngineKey:            engineKey,
	})
	rec, _ := workspacestate.Read(root)
	return workspacestate.Matches(rec, hash, engineKey), nil
}
