package installer

import (
	"context"
	"sort"
	"sync"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/store"
)

// lockMatchesPackageJSON reports whether the lockfile's root importer
// exactly mirrors package.json (so the resolver and packument refetch can
// be skipped). Non-registry *direct* specifiers and entries missing
// integrity/tarball force a re-resolve. Git entries are the exception:
// they have no integrity by nature (pinned by commit in Tarball), so under
// the hoisted linker — which materializes them uniformly via CopyFrom —
// they are accepted and re-cloned from the pinned commit in runLocked.
func (op *Operation) lockMatchesPackageJSON(pkg *project.PackageJSON, lock *lockfile.Lockfile, hoisted bool) bool {
	imp, ok := lock.Importers["."]
	if !ok {
		return false
	}
	if !sameMap(pkg.Dependencies, imp.Dependencies) {
		return false
	}
	if !op.Options.Production && !sameMap(pkg.DevDependencies, imp.DevDependencies) {
		return false
	}
	if !sameMap(pkg.OptionalDependencies, imp.OptionalDependencies) {
		return false
	}
	if !sameMap(pkg.PeerDependencies, imp.PeerDependencies) {
		return false
	}
	if hasNonRegistry(pkg.Dependencies) ||
		(!op.Options.Production && hasNonRegistry(pkg.DevDependencies)) ||
		hasNonRegistry(pkg.OptionalDependencies) {
		return false
	}
	for _, p := range lock.Packages {
		if isGitLockEntry(p) {
			if !hoisted {
				return false // isolated places git deps outside linkSpecs; re-resolve
			}
			continue // git: no integrity by nature, pinned by commit in Tarball
		}
		if p.Integrity == "" || p.Tarball == "" {
			return false
		}
	}
	return true
}

// runLocked installs straight from the lockfile: no resolver, no
// packument fetch. Missing tarballs are fetched and ingested; everything
// else is materialized from the store.
func (op *Operation) runLocked(
	ctx context.Context,
	pkg *project.PackageJSON,
	lock *lockfile.Lockfile,
	st *store.Store,
	client *registry.Client,
	cfg *npmrc.Config,
	mode project.Mode,
) (Report, error) {
	entries := make([]lockfile.LockedPackage, 0, len(lock.Packages))
	for _, p := range lock.Packages {
		entries = append(entries, p)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	// Fetch any tarballs not already in the store; always re-verify the
	// lockfile-recorded signatures so a tampered entry can't slip through
	// on a warm install. Git entries have no store tarball — they are
	// re-cloned from the pinned commit and copied into place instead.
	var mu sync.Mutex
	gitDirs := map[string]string{}
	if err := core.ForEachLimited(entries, core.HTTPConcurrency, func(p lockfile.LockedPackage) error {
		if !lockEntryMatchesPlatform(p) {
			return nil // foreign-platform optional: in the lockfile, not installed here
		}
		if isGitLockEntry(p) {
			dir, err := op.ensureGitClone(ctx, p.Tarball)
			if err != nil {
				return err
			}
			mu.Lock()
			gitDirs[p.Path] = dir
			mu.Unlock()
			return nil
		}
		if _, err := op.verifySignature(ctx, p.Name, p.Version, p.Integrity, lockedToSigs(p.Signatures)); err != nil {
			return err
		}
		if st.HasTarball(p.Integrity) {
			return nil
		}
		bytes, err := client.Tarball(ctx, p.Tarball, p.Integrity)
		if err != nil {
			return err
		}
		_, err = st.IngestTarball(bytes, p.Integrity)
		return err
	}); err != nil {
		return Report{}, err
	}
	op.prof.mark("fetch + verify (warm: ~0)")

	directNames := map[string]bool{}
	for n := range pkg.Dependencies {
		directNames[n] = true
	}
	if !op.Options.Production {
		for n := range pkg.DevDependencies {
			directNames[n] = true
		}
	}
	lockedVersion := map[string]string{}
	for _, p := range entries {
		lockedVersion[p.Name] = p.Version
	}

	specs := make([]linker.LinkSpec, 0, len(entries))
	for _, p := range entries {
		if !lockEntryMatchesPlatform(p) {
			continue // recorded in the lockfile but not for this platform
		}
		deps := map[string]string{}
		for dep := range p.Dependencies {
			if v, ok := lockedVersion[dep]; ok {
				deps[dep] = v
			}
		}
		spec := linker.LinkSpec{
			Name:         p.Name,
			Version:      p.Version,
			Dependencies: deps,
			Bin:          p.Bin,
			IsDirect:     directNames[p.Name],
			Scripts:      p.Scripts,
			Engines:      p.Engines,
			Path:         p.Path,
		}
		// Git entries copy from the clone dir; everything else materializes
		// from the store by integrity.
		if dir, ok := gitDirs[p.Path]; ok {
			spec.CopyFrom = dir
		} else {
			spec.Integrity = p.Integrity
		}
		specs = append(specs, spec)
	}

	op.prof.mark("build link specs")

	warnings, err := op.link(cfg, st, specs)
	if err != nil {
		return Report{}, err
	}
	op.prof.mark("link (materialize)")
	if err := op.applyLocalLinks(pkg); err != nil {
		return Report{}, err
	}
	if err := op.materializeConfigDeps(ctx, client, pkg); err != nil {
		return Report{}, err
	}
	if err := op.prune(specs, pkg); err != nil {
		return Report{}, err
	}
	if err := op.checkEngines(ctx, specs, &warnings); err != nil {
		return Report{}, err
	}
	op.prof.mark("local/config/engines")
	if op.scriptsEnabled() {
		sw, err := op.runLifecycleScripts(ctx, pkg, cfg, specs, op.linkerKind(cfg))
		warnings = append(warnings, sw...)
		if err != nil {
			return Report{}, err
		}
	}
	op.prof.mark("lifecycle scripts")
	if err := op.postInstallAudit(ctx, cfg, lock, &warnings); err != nil {
		return Report{}, err
	}
	for _, w := range warnings {
		op.Log.Warn("%s", w)
	}
	return Report{Warnings: warnings, Added: len(specs)}, nil
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func hasNonRegistry(deps map[string]string) bool {
	for name, raw := range deps {
		if project.ParseSpec(name, raw).Protocol != project.ProtoSemver {
			return true
		}
	}
	return false
}
