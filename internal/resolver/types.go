// Package resolver is gnpm's version solver: a Pubgrub implementation
// with conflict-driven learning, plus the npm extensions (overrides,
// optional dependencies, peer dependencies, dist-tag handling) the
// install path needs. It depends only on the semver package and a
// PackageProvider abstraction over the registry.
package resolver

import "github.com/koji-1009/gnpm/internal/semver"

// Term is a constraint on a package: positive (must be satisfied) or
// negative (must not be satisfied).
type Term struct {
	Package    string
	Range      semver.NpmRange
	IsPositive bool
}

// Invert flips the term's polarity.
func (t Term) Invert() Term {
	return Term{Package: t.Package, Range: t.Range, IsPositive: !t.IsPositive}
}

// Incompatibility is a conjunction of terms that cannot all hold at once;
// Cause is a human-readable reason for conflict explanations.
type Incompatibility struct {
	Terms []Term
	Cause string
}

// PackageDependencies are the declarations of one candidate version.
// OptionalDependencies are propagated as constraints (npm semantics for
// platform-specific native siblings); the install path's platform filter
// then drops variants that don't match the host.
type PackageDependencies struct {
	Dependencies         map[string]string
	OptionalDependencies map[string]string
	PeerDependencies     map[string]string
	// OptionalPeers are peers flagged {optional:true}; the solver must
	// not force-install them.
	OptionalPeers map[string]bool
}

// PackageProvider supplies resolver inputs, typically wrapping a registry
// client and packument cache.
type PackageProvider interface {
	// Versions returns all known versions for package, in any order.
	Versions(pkg string) ([]semver.Version, error)
	// DependenciesOf returns the declarations of pkg@version.
	DependenciesOf(pkg string, version semver.Version) (PackageDependencies, error)
	// Latest returns the version the `latest` dist-tag points to, if known.
	// npm/pnpm prefer this version when it satisfies the requested range,
	// even if a higher version was published — a maintainer can publish
	// without promoting to `latest`, and those releases must not be adopted
	// automatically. The resolver only applies it when the version is also a
	// viable candidate (so release-age / floor filters still win).
	Latest(pkg string) (semver.Version, bool)
}

// Request is the solver input: the root's direct deps plus a provider.
type Request struct {
	Dependencies         map[string]string
	OptionalDependencies map[string]string
	Provider             PackageProvider

	// Preferred biases the solver toward already-locked versions.
	Preferred map[string]semver.Version
	// Overrides force a range wherever a package appears.
	Overrides map[string]string
	// NestedOverrides force a range only under a given parent.
	NestedOverrides map[string]map[string]string
	// AutoInstallPeers pulls missing peers in (npm@7+ behavior).
	AutoInstallPeers bool
	// OnDecide fires when the solver commits to package@version, letting
	// the install path start the tarball fetch in parallel. It runs in
	// the solver hot loop; any work it starts must be fire-and-forget,
	// and a later backtrack past the decision is harmless.
	OnDecide func(pkg string, version semver.Version)
}

// Result is the solved flat assignment of package → chosen version.
type Result struct {
	Assignments map[string]semver.Version
}
