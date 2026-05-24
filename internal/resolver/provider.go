package resolver

import (
	"sort"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/semver"
)

// InMemoryProvider is a fixture provider for tests.
type InMemoryProvider struct {
	data   map[string]map[string]PackageDependencies
	latest map[string]string // optional pkg → `latest` dist-tag version
}

// NewInMemoryProvider builds a provider from package → version → deps.
func NewInMemoryProvider(data map[string]map[string]PackageDependencies) *InMemoryProvider {
	return &InMemoryProvider{data: data}
}

// SetLatest records the `latest` dist-tag version for pkg (test helper).
func (p *InMemoryProvider) SetLatest(pkg, version string) {
	if p.latest == nil {
		p.latest = map[string]string{}
	}
	p.latest[pkg] = version
}

// Latest implements PackageProvider; absent unless SetLatest was called.
func (p *InMemoryProvider) Latest(pkg string) (semver.Version, bool) {
	vs, ok := p.latest[pkg]
	if !ok {
		return semver.Version{}, false
	}
	v, err := semver.Parse(vs)
	if err != nil {
		return semver.Version{}, false
	}
	return v, true
}

// Versions returns the sorted known versions for pkg.
func (p *InMemoryProvider) Versions(pkg string) ([]semver.Version, error) {
	m, ok := p.data[pkg]
	if !ok {
		return nil, nil
	}
	out := make([]semver.Version, 0, len(m))
	for vs := range m {
		v, err := semver.Parse(vs)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out, nil
}

// DependenciesOf returns the declarations for pkg@version.
func (p *InMemoryProvider) DependenciesOf(pkg string, version semver.Version) (PackageDependencies, error) {
	m, ok := p.data[pkg]
	if !ok {
		return PackageDependencies{}, core.ResolutionError("no entry for %s", pkg)
	}
	deps, ok := m[version.String()]
	if !ok {
		return PackageDependencies{}, core.ResolutionError("no entry for %s@%s", pkg, version)
	}
	return deps, nil
}
