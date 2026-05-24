// Package treeresolver resolves a dependency graph the npm way: each
// dependency edge gets the highest version satisfying its range, and
// when ranges conflict the resolver installs MULTIPLE versions — hoisting
// the first to top-level node_modules and nesting conflicting ones under
// the package that requires them. This is what lets gnpm install graphs
// (A→x@^1, B→x@^2) that a single-version solver would reject.
//
// It reuses the same PackageProvider as the pubgrub solver. When there
// are no version conflicts every package hoists to the top level, so the
// result is identical to the flat layout.
package treeresolver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/resolver"
	"github.com/koji-1009/gnpm/internal/semver"
)

// Placement is one installed package instance and where it goes.
type Placement struct {
	Name    string
	Version semver.Version
	// Path is the node_modules-relative location: "react" when hoisted to
	// the top level, or "a/node_modules/lodash" when nested.
	Path     string
	IsDirect bool
	// Exotic marks a git/https-sourced instance. For these the registry
	// fetch is bypassed: Tarball pins the source (https URL or
	// "git+<url>#<commit>") and VersionLabel carries the raw, possibly
	// non-semver version. The caller materializes it from the token it
	// recorded when ResolveExotic ran, keyed by Tarball.
	Exotic       bool
	Tarball      string
	VersionLabel string
}

// ExoticResolution is what a ResolveExotic callback returns for one
// git/https specifier: the package's (raw) version, its own dependency
// edges to recurse into (required and optional kept separate so an
// unresolvable optional is skipped, not fatal), and Tarball — the pinned
// source that also serves as the instance's identity for deduplication.
// All fetching I/O lives behind this callback, so the resolver stays pure.
type ExoticResolution struct {
	Version      string
	Deps         map[string]string
	OptionalDeps map[string]string
	Tarball      string
}

// Request is the resolver input.
type Request struct {
	Dependencies         map[string]string
	OptionalDependencies map[string]string
	Provider             resolver.PackageProvider
	Overrides            map[string]string
	NestedOverrides      map[string]map[string]string
	AutoInstallPeers     bool
	// BlockExoticSubdeps rejects a *transitive* git/https dependency unless
	// TrustedExotic accepts it (doc/spec.md §2.4 blockExoticSubdeps). Direct
	// exotic dependencies the user declared are never blocked.
	BlockExoticSubdeps bool
	TrustedExotic      func(specifier string) bool
	// ResolveExotic resolves a git/https specifier into a concrete package
	// through the injected fetch capability (same boundary role as
	// Provider). When nil, exotic edges are not installed — surfaced as a
	// warning rather than dropped silently.
	ResolveExotic func(specifier string) (ExoticResolution, error)
	// OnResolved, when set, is called with each registry package the moment
	// its version is finalized (the greedy walk never revises a placement),
	// letting the caller start fetching its tarball while resolution
	// continues. Like Provider, it is an injected capability — the resolver
	// owns no concurrency itself. Not called for exotic placements.
	OnResolved func(name string, version semver.Version)
}

type node struct {
	name     string
	version  semver.Version
	path     string
	direct   bool
	parent   *node
	contents map[string]*node
	// exotic instance metadata, zero for registry packages.
	exotic       bool
	tarball      string
	versionLabel string
}

type state struct {
	req           Request
	versionsCache map[string][]semver.Version
	depsCache     map[string]map[string]resolver.PackageDependencies
	exoticCache   map[string]ExoticResolution
	warnings      []string
}

type edge struct {
	requirer *node
	name     string
	rng      string
	direct   bool
	optional bool
}

// Resolve produces the full set of placements for the request, plus any
// non-fatal warnings (e.g. an exotic dep left uninstalled because no
// ResolveExotic capability was injected).
func Resolve(req Request) ([]Placement, []string, error) {
	s := &state{
		req:           req,
		versionsCache: map[string][]semver.Version{},
		depsCache:     map[string]map[string]resolver.PackageDependencies{},
		exoticCache:   map[string]ExoticResolution{},
	}
	root := &node{contents: map[string]*node{}}

	var queue []edge
	for _, name := range sortedKeys(req.Dependencies) {
		queue = append(queue, edge{root, name, req.Dependencies[name], true, false})
	}
	for _, name := range sortedKeys(req.OptionalDependencies) {
		queue = append(queue, edge{root, name, req.OptionalDependencies[name], true, true})
	}

	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		next, err := s.resolveEdge(root, e)
		if err != nil {
			return nil, nil, err
		}
		queue = append(queue, next...)
	}

	var out []Placement
	var walk func(n *node)
	walk = func(n *node) {
		for _, name := range sortedNodeKeys(n.contents) {
			c := n.contents[name]
			out = append(out, Placement{
				Name: c.name, Version: c.version, Path: c.path, IsDirect: c.direct,
				Exotic: c.exotic, Tarball: c.tarball, VersionLabel: c.versionLabel,
			})
			walk(c)
		}
	}
	walk(root)
	return out, s.warnings, nil
}

// isExoticSpec reports whether a dependency specifier is a git or https
// source rather than a semver range.
func isExoticSpec(s string) bool {
	return strings.HasPrefix(s, "git+") || strings.HasPrefix(s, "git:") ||
		strings.HasPrefix(s, "github:") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://")
}

// resolveEdge places (or reuses) one dependency edge and returns the
// edges of any newly placed node.
func (s *state) resolveEdge(root *node, e edge) ([]edge, error) {
	eff := s.effective(e.requirer, e.name, e.rng)

	// A git/https specifier is "exotic". When it is transitive,
	// blockExoticSubdeps rejects it unless trusted (direct deps the user
	// declared are always allowed). Resolution happens through the injected
	// ResolveExotic capability — the resolver itself stays pure.
	if isExoticSpec(eff) {
		if !e.direct && s.req.BlockExoticSubdeps && (s.req.TrustedExotic == nil || !s.req.TrustedExotic(eff)) {
			return nil, core.ResolutionError("blockExoticSubdeps: transitive dependency %s → %q is not on the trusted-repo allowlist", e.name, eff)
		}
		if s.req.ResolveExotic == nil {
			s.warnings = append(s.warnings, fmt.Sprintf("exotic dependency not installed: %s → %s", e.name, eff))
			return nil, nil
		}
		res, err := s.resolveExotic(eff)
		if err != nil {
			if e.optional {
				return nil, nil
			}
			return nil, err
		}
		return s.placeExotic(root, e, res), nil
	}

	parsed, err := semver.ParseRange(eff)
	if err != nil {
		return nil, nil // unparseable (non-semver) — handled elsewhere
	}

	// Reuse a visible instance: walk ancestors for the nearest one named
	// e.name. Compatible → reuse; incompatible → it shadows, so we must
	// place at/below the requirer.
	var nearest *node
	for a := e.requirer; a != nil; a = a.parent {
		if inst, ok := a.contents[e.name]; ok {
			nearest = a
			if parsed.Satisfies(inst.version) {
				return nil, nil // reuse
			}
			break
		}
	}

	versions, err := s.versions(e.name)
	if err != nil {
		if e.optional {
			return nil, nil
		}
		return nil, err
	}
	best, ok := semver.MaxSatisfying(versions, parsed)
	if !ok {
		if e.optional {
			return nil, nil
		}
		return nil, core.ResolutionError("no version of %s satisfies %s", e.name, eff)
	}
	// Prefer the `latest` dist-tag over a higher non-latest release when it
	// is in range and still a viable candidate (npm/pnpm do this so a
	// published-but-not-promoted version is not adopted automatically).
	if latest, lok := s.req.Provider.Latest(e.name); lok && parsed.Satisfies(latest) && containsVersion(versions, latest) {
		best = latest
	}

	// Placement target: top level when no ancestor holds the name, else
	// nest directly under the requirer.
	target := root
	if nearest != nil {
		target = e.requirer
	}
	if _, exists := target.contents[e.name]; exists {
		return nil, nil // already placed here (rare dep/peer overlap) — keep first
	}

	path := e.name
	if target != root {
		path = target.path + "/node_modules/" + e.name
	}
	nn := &node{name: e.name, version: best, path: path, direct: e.direct, parent: target, contents: map[string]*node{}}
	target.contents[e.name] = nn
	if s.req.OnResolved != nil {
		s.req.OnResolved(e.name, best) // version is final (greedy) → safe to fetch now
	}

	deps, err := s.deps(e.name, best)
	if err != nil {
		return nil, err
	}
	var next []edge
	for _, d := range sortedKeys(deps.Dependencies) {
		next = append(next, edge{nn, d, deps.Dependencies[d], false, false})
	}
	for _, d := range sortedKeys(deps.OptionalDependencies) {
		next = append(next, edge{nn, d, deps.OptionalDependencies[d], false, true})
	}
	if s.req.AutoInstallPeers {
		for _, d := range sortedKeys(deps.PeerDependencies) {
			if deps.OptionalPeers[d] {
				continue
			}
			next = append(next, edge{nn, d, deps.PeerDependencies[d], false, false})
		}
	}
	return next, nil
}

// resolveExotic resolves a specifier through the injected capability,
// caching by specifier so the same source is fetched once per run.
func (s *state) resolveExotic(spec string) (ExoticResolution, error) {
	if r, ok := s.exoticCache[spec]; ok {
		return r, nil
	}
	r, err := s.req.ResolveExotic(spec)
	if err != nil {
		return ExoticResolution{}, err
	}
	s.exoticCache[spec] = r
	return r, nil
}

// placeExotic places (or reuses) one resolved exotic instance and returns
// the edges of its own dependencies. Identity is the pinned source
// (res.Tarball): a visible instance from the same source is reused, a
// different one shadows and forces nesting under the requirer — mirroring
// the registry path's hoist-then-nest rule, just keyed by source instead
// of semver satisfaction.
//
// Termination: a dependency cycle re-derives the same specifier and thus
// the same Tarball, so the ancestor walk finds the matching instance and
// reuses it. Distinct sources for one name are finite (bounded by the
// graph's specifiers), so the number of placed nodes is bounded.
func (s *state) placeExotic(root *node, e edge, res ExoticResolution) []edge {
	var nearest *node
	for a := e.requirer; a != nil; a = a.parent {
		if inst, ok := a.contents[e.name]; ok {
			nearest = a
			if inst.exotic && inst.tarball == res.Tarball {
				return nil // reuse
			}
			break
		}
	}
	target := root
	if nearest != nil {
		target = e.requirer
	}
	if _, exists := target.contents[e.name]; exists {
		return nil // already placed here — keep first
	}

	path := e.name
	if target != root {
		path = target.path + "/node_modules/" + e.name
	}
	version, _ := semver.TryParse(strings.TrimPrefix(res.Version, "v"))
	nn := &node{
		name: e.name, version: version, path: path, direct: e.direct,
		parent: target, contents: map[string]*node{},
		exotic: true, tarball: res.Tarball, versionLabel: res.Version,
	}
	target.contents[e.name] = nn

	var next []edge
	for _, d := range sortedKeys(res.Deps) {
		next = append(next, edge{nn, d, res.Deps[d], false, false})
	}
	for _, d := range sortedKeys(res.OptionalDeps) {
		next = append(next, edge{nn, d, res.OptionalDeps[d], false, true})
	}
	return next
}

func (s *state) effective(requirer *node, name, declared string) string {
	if requirer != nil && requirer.name != "" {
		if scoped, ok := s.req.NestedOverrides[requirer.name]; ok {
			if r, ok := scoped[name]; ok {
				return r
			}
		}
	}
	if r, ok := s.req.Overrides[name]; ok {
		return r
	}
	return declared
}

func (s *state) versions(pkg string) ([]semver.Version, error) {
	if v, ok := s.versionsCache[pkg]; ok {
		return v, nil
	}
	v, err := s.req.Provider.Versions(pkg)
	if err != nil {
		return nil, err
	}
	s.versionsCache[pkg] = v
	return v, nil
}

func (s *state) deps(pkg string, v semver.Version) (resolver.PackageDependencies, error) {
	if m, ok := s.depsCache[pkg]; ok {
		if d, ok := m[v.String()]; ok {
			return d, nil
		}
	}
	d, err := s.req.Provider.DependenciesOf(pkg, v)
	if err != nil {
		return resolver.PackageDependencies{}, err
	}
	if s.depsCache[pkg] == nil {
		s.depsCache[pkg] = map[string]resolver.PackageDependencies{}
	}
	s.depsCache[pkg][v.String()] = d
	return d, nil
}

func containsVersion(vs []semver.Version, want semver.Version) bool {
	for _, v := range vs {
		if v.Equal(want) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedNodeKeys(m map[string]*node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
