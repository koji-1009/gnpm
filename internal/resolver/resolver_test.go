package resolver

import (
	"testing"

	"github.com/koji-1009/gnpm/internal/semver"
)

// dep is shorthand for a dependency map.
type dep = map[string]string

func deps(d dep) PackageDependencies { return PackageDependencies{Dependencies: d} }

func solve(t *testing.T, req Request) map[string]string {
	t.Helper()
	if req.Provider == nil {
		t.Fatal("nil provider")
	}
	res, err := NewSolver(req).Solve()
	if err != nil {
		t.Fatalf("solve failed: %v", err)
	}
	out := map[string]string{}
	for k, v := range res.Assignments {
		out[k] = v.String()
	}
	return out
}

func TestResolveBasic(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": deps(nil), "1.2.0": deps(nil), "2.0.0": deps(nil)},
	})
	got := solve(t, Request{Dependencies: dep{"a": "^1.0.0"}, Provider: p})
	if got["a"] != "1.2.0" {
		t.Errorf("a = %q, want 1.2.0 (highest in range)", got["a"])
	}
}

func TestResolveTransitive(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": deps(dep{"b": "^1.0.0"})},
		"b": {"1.0.0": deps(dep{"c": "^1.0.0"}), "1.5.0": deps(dep{"c": "^1.0.0"})},
		"c": {"1.0.0": deps(nil), "1.1.0": deps(nil)},
	})
	got := solve(t, Request{Dependencies: dep{"a": "1.0.0"}, Provider: p})
	if got["a"] != "1.0.0" || got["b"] != "1.5.0" || got["c"] != "1.1.0" {
		t.Errorf("got %v", got)
	}
}

func TestResolveBacktrack(t *testing.T) {
	// root → a@* and b@*; a needs c@1, b needs c@2 only for its newest
	// version. The solver must backtrack b to a version compatible with c.
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": deps(dep{"c": "1.0.0"})},
		"b": {
			"2.0.0": deps(dep{"c": "2.0.0"}),
			"1.0.0": deps(dep{"c": "1.0.0"}),
		},
		"c": {"1.0.0": deps(nil), "2.0.0": deps(nil)},
	})
	got := solve(t, Request{Dependencies: dep{"a": "*", "b": "*"}, Provider: p})
	if got["c"] != "1.0.0" {
		t.Errorf("c = %q, want 1.0.0 (forced by a)", got["c"])
	}
	if got["b"] != "1.0.0" {
		t.Errorf("b = %q, want 1.0.0 (backtracked to satisfy c)", got["b"])
	}
}

func TestResolveUnsatisfiable(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": deps(dep{"c": "1.0.0"})},
		"b": {"1.0.0": deps(dep{"c": "2.0.0"})},
		"c": {"1.0.0": deps(nil), "2.0.0": deps(nil)},
	})
	_, err := NewSolver(Request{Dependencies: dep{"a": "1.0.0", "b": "1.0.0"}, Provider: p}).Solve()
	if err == nil {
		t.Fatal("expected unsatisfiable resolution to fail")
	}
}

func TestResolveOverrides(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": deps(dep{"b": "^1.0.0"})},
		"b": {"1.0.0": deps(nil), "1.5.0": deps(nil), "2.0.0": deps(nil)},
	})
	// Flat override forces b to 2.0.0 even though a asked for ^1.
	got := solve(t, Request{Dependencies: dep{"a": "1.0.0"}, Overrides: dep{"b": "2.0.0"}, Provider: p})
	if got["b"] != "2.0.0" {
		t.Errorf("b = %q, want 2.0.0 (override)", got["b"])
	}
}

func TestResolveOptionalDepBestEffort(t *testing.T) {
	// a's optional dep `native` has no satisfying version → skipped, no error.
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": {OptionalDependencies: dep{"native": "^9.9.9"}}},
	})
	got := solve(t, Request{Dependencies: dep{"a": "1.0.0"}, Provider: p})
	if got["a"] != "1.0.0" {
		t.Errorf("a = %q", got["a"])
	}
	if _, ok := got["native"]; ok {
		t.Error("unsatisfiable optional dep should be skipped")
	}
}

func TestResolveAutoInstallPeers(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"plugin": {"1.0.0": {PeerDependencies: dep{"host": "^2.0.0"}}},
		"host":   {"2.0.0": deps(nil), "2.1.0": deps(nil)},
	})
	got := solve(t, Request{Dependencies: dep{"plugin": "1.0.0"}, AutoInstallPeers: true, Provider: p})
	if got["host"] != "2.1.0" {
		t.Errorf("host = %q, want 2.1.0 (auto-installed peer)", got["host"])
	}
}

func TestResolveOptionalPeerNotForced(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"plugin": {"1.0.0": {PeerDependencies: dep{"sass": "*"}, OptionalPeers: map[string]bool{"sass": true}}},
	})
	got := solve(t, Request{Dependencies: dep{"plugin": "1.0.0"}, AutoInstallPeers: true, Provider: p})
	if _, ok := got["sass"]; ok {
		t.Error("optional peer should not be force-installed")
	}
}

func TestResolvePreferred(t *testing.T) {
	p := NewInMemoryProvider(map[string]map[string]PackageDependencies{
		"a": {"1.0.0": deps(nil), "1.1.0": deps(nil), "1.2.0": deps(nil)},
	})
	// Without a preference the solver picks the highest (1.2.0); with a
	// preference for 1.1.0 it should keep that.
	got := solve(t, Request{
		Dependencies: dep{"a": "^1.0.0"},
		Preferred:    map[string]semver.Version{"a": semver.MustParse("1.1.0")},
		Provider:     p,
	})
	if got["a"] != "1.1.0" {
		t.Errorf("a = %q, want 1.1.0 (preferred)", got["a"])
	}
}
