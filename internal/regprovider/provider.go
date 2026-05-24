// Package regprovider bridges the registry Client to the resolver's
// PackageProvider interface, adding the minimum-release-age filter and a
// concurrency-safe packument cache. It lives in its own package so the
// resolver stays free of any registry/network dependency.
package regprovider

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/resolver"
	"github.com/koji-1009/gnpm/internal/semver"
	"sync"
)

// ReleaseAge configures the minimum-release-age filter (doc/spec.md §2.4).
type ReleaseAge struct {
	// Minimum is the age a version must reach before it is installable.
	// Zero disables the filter.
	Minimum time.Duration
	// Strict fails the install when every candidate is too young; when
	// false, the lowest filtered candidate is used as a fallback.
	Strict bool
	// IgnoreMissingTime admits versions whose packument lacks a publish
	// time (default true).
	IgnoreMissingTime bool
	// Exclude lists patterns (name, @scope/name, @scope/*) that bypass
	// the filter.
	Exclude []string
}

// Enabled reports whether the age filter is active.
func (r ReleaseAge) Enabled() bool { return r.Minimum > 0 }

func (r ReleaseAge) excluded(pkg string) bool {
	for _, pat := range r.Exclude {
		if matchExclude(pat, pkg) {
			return true
		}
	}
	return false
}

// Provider adapts a registry.Client to resolver.PackageProvider.
type Provider struct {
	client     *registry.Client
	ctx        context.Context
	releaseAge ReleaseAge
	now        time.Time

	mu    sync.Mutex
	cache map[string]*pkgResult

	// floors imposes a per-package minimum version (trustPolicy
	// no-downgrade); candidates below the floor are dropped.
	floors map[string]semver.Version
}

// SetFloors installs per-package minimum versions (trustPolicy).
func (p *Provider) SetFloors(floors map[string]semver.Version) { p.floors = floors }

type pkgResult struct {
	pack *registry.Packument
	err  error
	done chan struct{}
}

// New builds a Provider. now is the frozen reference time for release-age
// comparisons (zero → time.Now). ctx is used for all fetches.
func New(ctx context.Context, client *registry.Client, releaseAge ReleaseAge, now time.Time) *Provider {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !releaseAge.IgnoreMissingTime && releaseAge.Minimum == 0 {
		releaseAge.IgnoreMissingTime = true
	}
	return &Provider{
		client:     client,
		ctx:        ctx,
		releaseAge: releaseAge,
		now:        now.UTC(),
		cache:      map[string]*pkgResult{},
	}
}

func (p *Provider) packumentFor(name string) (*registry.Packument, error) {
	p.mu.Lock()
	if r, ok := p.cache[name]; ok {
		p.mu.Unlock()
		<-r.done
		return r.pack, r.err
	}
	r := &pkgResult{done: make(chan struct{})}
	p.cache[name] = r
	p.mu.Unlock()

	r.pack, r.err = p.client.Packument(p.ctx, name, p.releaseAge.Enabled())
	close(r.done)
	if r.err != nil {
		// Evict so a transient failure does not poison later lookups.
		p.mu.Lock()
		delete(p.cache, name)
		p.mu.Unlock()
	}
	return r.pack, r.err
}

// Versions implements resolver.PackageProvider, applying the release-age
// filter when enabled.
func (p *Provider) Versions(pkg string) ([]semver.Version, error) {
	pack, err := p.packumentFor(pkg)
	if err != nil {
		return nil, err
	}
	if !p.releaseAge.Enabled() || p.releaseAge.excluded(pkg) {
		return p.applyFloor(pkg, parsedVersions(pack)), nil
	}

	cutoff := p.now.Add(-p.releaseAge.Minimum)
	var mature, immature []semver.Version
	hiddenByAge := 0
	for vs := range pack.Versions {
		v, ok := semver.TryParse(vs)
		if !ok {
			continue
		}
		published, hasTime := pack.PublishTimes[vs]
		switch {
		case !hasTime:
			if p.releaseAge.IgnoreMissingTime {
				mature = append(mature, v)
			} else {
				hiddenByAge++
			}
		case published.Before(cutoff):
			mature = append(mature, v)
		default:
			hiddenByAge++
			immature = append(immature, v)
		}
	}
	if len(mature) == 0 && hiddenByAge > 0 {
		if p.releaseAge.Strict || len(immature) == 0 {
			return nil, core.NetworkError("%s: every version is younger than %d minutes (minimum-release-age hid %d candidates)",
				pkg, int(p.releaseAge.Minimum.Minutes()), hiddenByAge)
		}
		sort.Slice(immature, func(i, j int) bool { return immature[i].Less(immature[j]) })
		mature = append(mature, immature[0])
	}
	sort.Slice(mature, func(i, j int) bool { return mature[i].Less(mature[j]) })
	return p.applyFloor(pkg, mature), nil
}

// applyFloor drops candidates below the trustPolicy floor for pkg.
func (p *Provider) applyFloor(pkg string, versions []semver.Version) []semver.Version {
	floor, ok := p.floors[pkg]
	if !ok {
		return versions
	}
	out := versions[:0]
	for _, v := range versions {
		if !v.Less(floor) {
			out = append(out, v)
		}
	}
	return out
}

// DependenciesOf implements resolver.PackageProvider.
func (p *Provider) DependenciesOf(pkg string, version semver.Version) (resolver.PackageDependencies, error) {
	pack, err := p.packumentFor(pkg)
	if err != nil {
		return resolver.PackageDependencies{}, err
	}
	slice := pack.Versions[version.String()]
	if slice == nil {
		return resolver.PackageDependencies{}, core.ResolutionError("packument has no %s@%s", pkg, version)
	}
	return resolver.PackageDependencies{
		Dependencies:         slice.Dependencies,
		OptionalDependencies: slice.OptionalDependencies,
		PeerDependencies:     slice.PeerDependencies,
		OptionalPeers:        slice.OptionalPeers,
	}, nil
}

// SliceOf returns the packument version slice for tarball metadata after
// resolution, or nil when absent.
func (p *Provider) SliceOf(name string, version semver.Version) (*registry.PackumentVersion, error) {
	pack, err := p.packumentFor(name)
	if err != nil {
		return nil, err
	}
	return pack.Versions[version.String()], nil
}

// Latest implements resolver.PackageProvider: the version the `latest`
// dist-tag points to, so the resolver can prefer it over a higher
// non-latest release (npm-pick-manifest behavior).
func (p *Provider) Latest(pkg string) (semver.Version, bool) {
	pack, err := p.packumentFor(pkg)
	if err != nil {
		return semver.Version{}, false
	}
	vs, ok := pack.DistTags["latest"]
	if !ok {
		return semver.Version{}, false
	}
	return semver.TryParse(vs)
}

// ResolveDistTag maps a dist-tag (latest, next, …) to an exact-version
// range "=<version>", or "" when the tag is unknown.
func (p *Provider) ResolveDistTag(name, tag string) (string, error) {
	pack, err := p.packumentFor(name)
	if err != nil {
		return "", err
	}
	if v, ok := pack.DistTags[tag]; ok {
		return "=" + v, nil
	}
	return "", nil
}

// Warmup eagerly fetches packuments along the dependency graph rooted at
// seeds (name → declared range), overlapping network latency with the
// resolver's serial asks. Best-effort: fetch errors are ignored here and
// surface later if the resolver actually needs the package.
func (p *Provider) Warmup(seeds map[string]string) {
	const depth = 5
	var wg sync.WaitGroup
	sem := make(chan struct{}, core.HTTPConcurrency)
	var visit func(name, rng string, budget int)
	var scheduled sync.Map
	visit = func(name, rng string, budget int) {
		if budget <= 0 {
			return
		}
		if _, loaded := scheduled.LoadOrStore(name, true); loaded {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			pack, err := p.packumentFor(name)
			<-sem
			if err != nil {
				return
			}
			slice := bestSlice(pack, rng)
			if slice == nil {
				return
			}
			for dep, r := range slice.Dependencies {
				visit(dep, r, budget-1)
			}
			for dep, r := range slice.OptionalDependencies {
				visit(dep, r, budget-1)
			}
			for dep, r := range slice.PeerDependencies {
				if slice.OptionalPeers[dep] {
					continue
				}
				visit(dep, r, budget-1)
			}
		}()
	}
	for name, rng := range seeds {
		visit(name, rng, depth)
	}
	wg.Wait()
}

func bestSlice(pack *registry.Packument, rng string) *registry.PackumentVersion {
	if rng != "" {
		if parsed, err := semver.ParseRange(rng); err == nil {
			var best semver.Version
			found := false
			for vs := range pack.Versions {
				v, ok := semver.TryParse(vs)
				if !ok || !parsed.Satisfies(v) {
					continue
				}
				if !found || best.Less(v) {
					best, found = v, true
				}
			}
			if found {
				return pack.Versions[best.String()]
			}
		}
	}
	if pack.Latest() != "" {
		return pack.Versions[pack.Latest()]
	}
	return nil
}

func parsedVersions(pack *registry.Packument) []semver.Version {
	out := make([]semver.Version, 0, len(pack.Versions))
	for vs := range pack.Versions {
		if v, ok := semver.TryParse(vs); ok {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// matchExclude matches an exclude pattern against a package name:
// exact, "@scope/*" (whole scope), or a literal "@scope/name".
func matchExclude(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		scope := strings.TrimSuffix(pattern, "/*")
		return strings.HasPrefix(name, scope+"/")
	}
	return false
}
