package lockfile

import (
	"sort"
	"strings"
	"testing"
)

// TestPeerContextSnapshotKeys checks that generated pnpm snapshot keys carry
// pnpm's peer-context suffixes: a package's own resolved peers, plus peers that
// bubble up from its dependency subtree (unless it provides them itself), with
// a package never listing itself as its own peer context.
func TestPeerContextSnapshotKeys(t *testing.T) {
	dep := func(names ...string) map[string]string {
		m := map[string]string{}
		for _, n := range names {
			m[n] = "*"
		}
		return m
	}
	pkg := func(name string, deps, peers map[string]string) LockedPackage {
		return LockedPackage{Name: name, Version: "1.0.0", Dependencies: deps, PeerDependencies: peers}
	}
	lf := &Lockfile{
		Version:   SchemaVersion,
		Importers: map[string]Importer{".": {Dependencies: dep("plugin")}},
		Packages: map[string]LockedPackage{
			"plugin@1.0.0":     pkg("plugin", dep("parser"), dep("eslint")), // own eslint + bubbled typescript
			"parser@1.0.0":     pkg("parser", dep("estree"), dep("eslint")), // own eslint + bubbled typescript
			"estree@1.0.0":     pkg("estree", nil, dep("typescript")),       // own typescript
			"eslint@1.0.0":     pkg("eslint", dep("utils"), nil),            // utils peers eslint → self, excluded → flat
			"utils@1.0.0":      pkg("utils", nil, dep("eslint")),            // own eslint
			"typescript@1.0.0": pkg("typescript", nil, nil),                 // flat
		},
	}

	out := LockfileToPnpm(lf)
	got := make([]string, 0, len(out.Snapshots))
	for k := range out.Snapshots {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{
		"eslint@1.0.0", // self-peer (via utils) must NOT appear in its own context
		"estree@1.0.0(typescript@1.0.0)",
		"parser@1.0.0(eslint@1.0.0)(typescript@1.0.0)",
		"plugin@1.0.0(eslint@1.0.0)(typescript@1.0.0)", // parser is a dep, not a peer → not in context
		"typescript@1.0.0",
		"utils@1.0.0(eslint@1.0.0)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("snapshot keys mismatch:\n got=%v\nwant=%v", got, want)
	}
}
