package treeresolver

import (
	"sort"
	"testing"

	"github.com/koji-1009/gnpm/internal/resolver"
)

func deps(d map[string]string) resolver.PackageDependencies {
	return resolver.PackageDependencies{Dependencies: d}
}

// placementSet renders placements as "name@version@path" for assertions.
func placementSet(ps []Placement) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name+"@"+p.Version.String()+"@"+p.Path)
	}
	sort.Strings(out)
	return out
}

func TestTreeNoConflictAllHoisted(t *testing.T) {
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
		"a":    {"1.0.0": deps(map[string]string{"b": "^1.0.0"})},
		"b":    {"1.0.0": deps(map[string]string{"leaf": "^1.0.0"})},
		"leaf": {"1.0.0": deps(nil)},
	})
	got, _, err := Resolve(Request{Dependencies: map[string]string{"a": "^1.0.0"}, Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	// All hoisted to top level: path == name.
	for _, pl := range got {
		if pl.Path != pl.Name {
			t.Errorf("%s should be hoisted to top level, got path %q", pl.Name, pl.Path)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected 3 placements, got %d: %v", len(got), placementSet(got))
	}
}

func TestTreeConflictNests(t *testing.T) {
	// A needs x@^1, B needs x@^2 → both versions must be installed.
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
		"A": {"1.0.0": deps(map[string]string{"x": "^1.0.0"})},
		"B": {"1.0.0": deps(map[string]string{"x": "^2.0.0"})},
		"x": {"1.5.0": deps(nil), "2.3.0": deps(nil)},
	})
	got, _, err := Resolve(Request{Dependencies: map[string]string{"A": "^1.0.0", "B": "^1.0.0"}, Provider: p})
	if err != nil {
		t.Fatalf("conflict graph should resolve (not error): %v", err)
	}
	set := placementSet(got)
	// A processed before B, so x@1.5.0 hoists to top; B's x@^2 nests.
	want := map[string]bool{
		"A@1.0.0@A":                true,
		"B@1.0.0@B":                true,
		"x@1.5.0@x":                true,
		"x@2.3.0@B/node_modules/x": true,
	}
	if len(set) != len(want) {
		t.Fatalf("placements = %v, want %v", set, want)
	}
	for _, s := range set {
		if !want[s] {
			t.Errorf("unexpected placement %q (set=%v)", s, set)
		}
	}
}

func TestTreeReuseCompatible(t *testing.T) {
	// Both A and B accept x@^1 → single shared top-level x.
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
		"A": {"1.0.0": deps(map[string]string{"x": "^1.0.0"})},
		"B": {"1.0.0": deps(map[string]string{"x": ">=1.2.0"})},
		"x": {"1.5.0": deps(nil)},
	})
	got, _, err := Resolve(Request{Dependencies: map[string]string{"A": "^1.0.0", "B": "^1.0.0"}, Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	xs := 0
	for _, pl := range got {
		if pl.Name == "x" {
			xs++
			if pl.Path != "x" {
				t.Errorf("x should be the single hoisted copy, got %q", pl.Path)
			}
		}
	}
	if xs != 1 {
		t.Errorf("expected one shared x, got %d", xs)
	}
}

func TestTreeBlockExoticSubdeps(t *testing.T) {
	// A registry package with a transitive git dependency.
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
		"A": {"1.0.0": deps(map[string]string{"evil": "git+https://example.com/evil.git"})},
	})
	req := func(block bool, trusted func(string) bool) Request {
		return Request{
			Dependencies:       map[string]string{"A": "^1.0.0"},
			Provider:           p,
			BlockExoticSubdeps: block,
			TrustedExotic:      trusted,
		}
	}
	// Blocked + untrusted → hard error.
	if _, _, err := Resolve(req(true, nil)); err == nil {
		t.Error("blockExoticSubdeps should reject an untrusted transitive exotic dep")
	}
	// Blocked + trusted → allowed (surfaced as a warning, not installed).
	_, warns, err := Resolve(req(true, func(string) bool { return true }))
	if err != nil {
		t.Errorf("trusted exotic should not error: %v", err)
	}
	if len(warns) == 0 {
		t.Error("expected a warning that the exotic dep was not installed")
	}
	// Not blocked → no error, but surfaced (not silently dropped).
	_, warns, err = Resolve(req(false, nil))
	if err != nil || len(warns) == 0 {
		t.Errorf("unblocked exotic should warn, not error: warns=%v err=%v", warns, err)
	}
}

func TestTreeExoticResolvedTransitively(t *testing.T) {
	// A registry package depends on a git URL; that exotic package declares
	// its own registry dependency, which must resolve and hoist to the top.
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
		"A":    {"1.0.0": deps(map[string]string{"tool": "git+https://example.com/tool.git#v2"})},
		"leaf": {"1.0.0": deps(nil)},
	})
	calls := 0
	resolveExotic := func(spec string) (ExoticResolution, error) {
		calls++
		return ExoticResolution{
			Version: "2.0.0",
			Deps:    map[string]string{"leaf": "^1.0.0"},
			Tarball: "git+https://example.com/tool.git#abc123",
		}, nil
	}
	got, warns, err := Resolve(Request{
		Dependencies:  map[string]string{"A": "^1.0.0"},
		Provider:      p,
		ResolveExotic: resolveExotic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("no warnings expected when the exotic dep resolves: %v", warns)
	}
	if calls != 1 {
		t.Errorf("ResolveExotic should be called once, got %d", calls)
	}
	byName := map[string]Placement{}
	for _, pl := range got {
		byName[pl.Name] = pl
	}
	tool, ok := byName["tool"]
	if !ok {
		t.Fatal("exotic package 'tool' was not placed")
	}
	if !tool.Exotic || tool.Tarball != "git+https://example.com/tool.git#abc123" || tool.VersionLabel != "2.0.0" {
		t.Errorf("tool placement missing exotic metadata: %+v", tool)
	}
	if leaf, ok := byName["leaf"]; !ok || leaf.Path != "leaf" {
		t.Errorf("the exotic's transitive dep 'leaf' should hoist to top level, got %+v", leaf)
	}
}

func TestTreeDirectExoticNotBlocked(t *testing.T) {
	// blockExoticSubdeps targets transitive deps only: a direct exotic dep
	// the user declared must still resolve even when blocking is on and the
	// repo is untrusted.
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{})
	resolveExotic := func(spec string) (ExoticResolution, error) {
		return ExoticResolution{Version: "1.0.0", Tarball: "git+https://x/tool.git#c"}, nil
	}
	got, _, err := Resolve(Request{
		Dependencies:       map[string]string{"tool": "git+https://x/tool.git"},
		Provider:           p,
		BlockExoticSubdeps: true,
		TrustedExotic:      func(string) bool { return false },
		ResolveExotic:      resolveExotic,
	})
	if err != nil {
		t.Fatalf("a direct exotic dep must not be blocked by blockExoticSubdeps: %v", err)
	}
	if len(got) != 1 || !got[0].Exotic || got[0].Path != "tool" {
		t.Errorf("direct exotic placement wrong: %+v", got)
	}
}

func TestTreeExoticOptionalDepSkipped(t *testing.T) {
	// An exotic package's optionalDependency that cannot be resolved must
	// be skipped, not fail the whole resolution.
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{})
	resolveExotic := func(spec string) (ExoticResolution, error) {
		return ExoticResolution{
			Version:      "1.0.0",
			OptionalDeps: map[string]string{"missing": "^1.0.0"},
			Tarball:      "git+https://x/tool.git#c",
		}, nil
	}
	got, _, err := Resolve(Request{
		Dependencies:  map[string]string{"tool": "git+https://x/tool.git"},
		Provider:      p,
		ResolveExotic: resolveExotic,
	})
	if err != nil {
		t.Fatalf("an unresolvable optional exotic dep must not fail resolution: %v", err)
	}
	for _, pl := range got {
		if pl.Name == "missing" {
			t.Error("the missing optional dep should not be placed")
		}
	}
}

func TestTreePrefersLatestDistTag(t *testing.T) {
	// x has 1.0.0 and a higher 1.1.0, but `latest` points at 1.0.0 (the
	// maintainer published 1.1.0 without promoting it). npm/pnpm pick the
	// latest-tag version when it satisfies, not the highest — gnpm must too.
	mk := func() *resolver.InMemoryProvider {
		return resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
			"a": {"1.0.0": deps(map[string]string{"x": "^1.0.0"})},
			"x": {"1.0.0": deps(nil), "1.1.0": deps(nil)},
		})
	}
	versionOf := func(ps []Placement, name string) string {
		for _, p := range ps {
			if p.Name == name {
				return p.Version.String()
			}
		}
		return ""
	}

	// With latest=1.0.0, the higher 1.1.0 must NOT be adopted.
	p := mk()
	p.SetLatest("x", "1.0.0")
	got, _, err := Resolve(Request{Dependencies: map[string]string{"a": "^1.0.0"}, Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	if v := versionOf(got, "x"); v != "1.0.0" {
		t.Errorf("with latest=1.0.0, x should resolve to 1.0.0, got %q", v)
	}

	// Without a latest tag, the default is highest-satisfying (1.1.0).
	got2, _, err := Resolve(Request{Dependencies: map[string]string{"a": "^1.0.0"}, Provider: mk()})
	if err != nil {
		t.Fatal(err)
	}
	if v := versionOf(got2, "x"); v != "1.1.0" {
		t.Errorf("with no latest tag, x should resolve to highest 1.1.0, got %q", v)
	}
}

func TestTreeCycleTerminates(t *testing.T) {
	p := resolver.NewInMemoryProvider(map[string]map[string]resolver.PackageDependencies{
		"a": {"1.0.0": deps(map[string]string{"b": "^1.0.0"})},
		"b": {"1.0.0": deps(map[string]string{"a": "^1.0.0"})},
	})
	got, _, err := Resolve(Request{Dependencies: map[string]string{"a": "^1.0.0"}, Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("cycle should yield 2 placements, got %d", len(got))
	}
}
