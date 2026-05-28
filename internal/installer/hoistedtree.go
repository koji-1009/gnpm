package installer

import (
	"context"
	"strings"
	"sync"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/regprovider"
	"github.com/koji-1009/gnpm/internal/semver"
	"github.com/koji-1009/gnpm/internal/store"
	"github.com/koji-1009/gnpm/internal/treeresolver"
)

// sliceInfo caches a resolved package's metadata after fetch.
type sliceInfo struct {
	tarball   string
	integrity string
	bin       map[string]string
	scripts   map[string]string
	engines   map[string]string
	deps      map[string]string
	optDeps   map[string]string
	peerDeps  map[string]string
	os        []string
	cpu       []string
	sigs      []registry.DistSignature
	hasBin    bool
	hasScript bool
	skip      bool // platform mismatch
}

type uniquePkg struct {
	name    string
	version semver.Version
}

// fetchOne downloads, verifies, and ingests one resolved package's tarball,
// returning its metadata, an optional signature warning, and an error.
// Platform-mismatched variants are reported as skip (no download).
func (op *Operation) fetchOne(
	ctx context.Context,
	provider *regprovider.Provider,
	st *store.Store,
	client *registry.Client,
	u uniquePkg,
) (sliceInfo, string, error) {
	key := u.name + "@" + u.version.String()
	slice, err := provider.SliceOf(u.name, u.version)
	if err != nil {
		return sliceInfo{}, "", err
	}
	if slice == nil || slice.Tarball == "" || slice.Integrity == "" {
		return sliceInfo{}, "", core.NetworkError("no tarball/integrity for %s", key)
	}
	info := sliceInfo{
		tarball: slice.Tarball, integrity: slice.Integrity, bin: slice.Bin,
		scripts: slice.Scripts, engines: slice.Engines, deps: slice.Dependencies,
		optDeps: slice.OptionalDependencies, peerDeps: slice.PeerDependencies,
		os: slice.OS, cpu: slice.CPU, sigs: slice.Signatures,
		hasBin: slice.HasBin, hasScript: slice.HasInstallScript,
	}
	// Platform-mismatched optional deps are recorded in the lockfile (so it
	// stays cross-platform — npm/pnpm do this) but not downloaded or linked
	// here; the matching platform installs them from the same lockfile.
	if !platformMatches(slice) {
		info.skip = true
		return info, "", nil
	}
	bytes, err := client.Tarball(ctx, slice.Tarball, slice.Integrity)
	if err != nil {
		return sliceInfo{}, "", err
	}
	warn, err := op.verifySignature(ctx, u.name, u.version.String(), slice.Integrity, toSigs(slice.Signatures))
	if err != nil {
		return sliceInfo{}, "", err
	}
	if _, err := st.IngestTarball(bytes, slice.Integrity); err != nil {
		return sliceInfo{}, "", err
	}
	return info, warn, nil
}

// assembleHoisted expands placements into per-location link specs and
// lockfile entries (keyed by node_modules path). It walks placements in
// their deterministic order, so the result is independent of download
// completion order; platform-skipped packages are dropped.
func assembleHoisted(
	placements []treeresolver.Placement,
	infos map[string]sliceInfo,
	aliasByPackage map[string]string,
	versionAtPath map[string]string,
) ([]linker.LinkSpec, map[string]lockfile.LockedPackage) {
	var linkSpecs []linker.LinkSpec
	lockPackages := map[string]lockfile.LockedPackage{}

	for _, p := range placements {
		info, ok := infos[p.Name+"@"+p.Version.String()]
		if !ok {
			continue
		}
		path := p.Path
		// A direct top-level npm: alias installs under its logical name.
		if alias, ok := aliasByPackage[p.Name]; ok && p.Path == p.Name {
			path = alias
		}
		edges := map[string]string{}
		for d := range info.deps {
			if v, ok := resolveEdgeVersion(path, d, versionAtPath); ok {
				edges[d] = v
			}
		}
		for d := range info.optDeps {
			if v, ok := resolveEdgeVersion(path, d, versionAtPath); ok {
				edges[d] = v
			}
		}
		// Always record the lockfile entry (keeps the lockfile
		// cross-platform); only link the packages for this platform.
		lockPackages[path] = lockfile.LockedPackage{
			Name: p.Name, Version: p.Version.String(), Path: path,
			Tarball: info.tarball, Integrity: info.integrity,
			Dependencies: info.deps, OptionalDependencies: info.optDeps, PeerDependencies: info.peerDeps,
			OS: info.os, CPU: info.cpu, HasBin: info.hasBin, HasInstallScript: info.hasScript,
			Bin: info.bin, Scripts: info.scripts, Engines: info.engines,
			Signatures: lockSignatures(info.sigs),
		}
		if info.skip {
			continue // platform mismatch: recorded above, not linked
		}
		linkSpecs = append(linkSpecs, linker.LinkSpec{
			Name: p.Name, Version: p.Version.String(), Integrity: info.integrity,
			Bin: info.bin, Scripts: info.scripts, Engines: info.engines,
			IsDirect: p.IsDirect, Path: path, Dependencies: edges,
			LinkAlias: aliasByPackage[p.Name],
		})
	}
	return linkSpecs, lockPackages
}

// versionsByPath maps every placement's install path to its version (the
// exotic version label for git/https instances), so a consumer's dependency
// edges can be resolved to concrete versions. Shared by the registry and
// exotic assembly so an exotic package's edges resolve against the same tree.
func versionsByPath(placements []treeresolver.Placement, aliasByPackage map[string]string) map[string]string {
	out := make(map[string]string, len(placements))
	for _, p := range placements {
		pp := p.Path
		if alias, ok := aliasByPackage[p.Name]; ok && p.Path == p.Name {
			pp = alias
		}
		v := p.Version.String()
		if p.Exotic {
			v = p.VersionLabel
		}
		out[pp] = v
	}
	return out
}

// resolveEdgeVersion finds the version a dependency edge resolves to from a
// consumer at consumerPath: the nearest node_modules walking up from that path,
// then the top level — mirroring Node's resolution over the hoisted tree.
func resolveEdgeVersion(consumerPath, dep string, versionAtPath map[string]string) (string, bool) {
	for prefix := consumerPath; ; {
		if v, ok := versionAtPath[prefix+"/node_modules/"+dep]; ok {
			return v, true
		}
		i := strings.LastIndex(prefix, "/node_modules/")
		if i < 0 {
			break
		}
		prefix = prefix[:i]
	}
	v, ok := versionAtPath[dep] // hoisted to the top level
	return v, ok
}

// tarballFetcher is a bounded worker pool that downloads + verifies +
// ingests tarballs streamed from the resolver (via OnResolved) while
// resolution and packument prefetch are still in flight. It deduplicates by
// name@version (a version may be placed at several paths), records the
// first error and cancels the run, and collects metadata into infos for a
// later deterministic assembly.
type tarballFetcher struct {
	op       *Operation
	ctx      context.Context
	cancel   context.CancelFunc
	provider *regprovider.Provider
	st       *store.Store
	client   *registry.Client

	ch chan uniquePkg
	wg sync.WaitGroup

	mu       sync.Mutex
	seen     map[string]bool
	infos    map[string]sliceInfo
	warnings []string
	err      error
}

func (op *Operation) newTarballFetcher(
	ctx context.Context,
	provider *regprovider.Provider,
	st *store.Store,
	client *registry.Client,
) *tarballFetcher {
	cctx, cancel := context.WithCancel(ctx)
	f := &tarballFetcher{
		op: op, ctx: cctx, cancel: cancel, provider: provider, st: st, client: client,
		ch:    make(chan uniquePkg, core.HTTPConcurrency*4),
		seen:  map[string]bool{},
		infos: map[string]sliceInfo{},
	}
	for i := 0; i < core.HTTPConcurrency; i++ {
		f.wg.Add(1)
		go f.worker()
	}
	return f
}

// submit hands a finalized package to the pool. It selects on ctx so a
// cancelled run (after a fetch error) never blocks the resolver on a full
// channel.
func (f *tarballFetcher) submit(name string, version semver.Version) {
	select {
	case f.ch <- uniquePkg{name, version}:
	case <-f.ctx.Done():
	}
}

func (f *tarballFetcher) worker() {
	defer f.wg.Done()
	for u := range f.ch {
		key := u.name + "@" + u.version.String()
		f.mu.Lock()
		dup := f.seen[key]
		if !dup {
			f.seen[key] = true
		}
		f.mu.Unlock()
		if dup {
			continue
		}
		info, warn, err := f.op.fetchOne(f.ctx, f.provider, f.st, f.client, u)
		f.mu.Lock()
		if err != nil && f.err == nil {
			f.err = err
			f.cancel()
		}
		if warn != "" {
			f.warnings = append(f.warnings, warn)
		}
		f.infos[key] = info
		f.mu.Unlock()
	}
}

func (f *tarballFetcher) closeAndWait() {
	close(f.ch)
	f.wg.Wait()
	f.cancel() // release the derived context
}
