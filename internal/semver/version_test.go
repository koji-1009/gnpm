package semver

import "testing"

// Ported from node-semver v7 test fixtures.
// https://github.com/npm/node-semver/blob/v7/test/fixtures/

func TestNodeSemverInclude(t *testing.T) {
	cases := [][2]string{
		{"1.0.0 - 2.0.0", "1.2.3"},
		{"^1.2.3+build", "1.2.3"},
		{"^1.2.3+build", "1.3.0"},
		{"1.2.3-pre+asdf - 2.4.3-pre+asdf", "1.2.3"},
		{"1.2.3-pre+asdf - 2.4.3-pre+asdf", "1.2.3-pre.2"},
		{"1.2.3-pre+asdf - 2.4.3-pre+asdf", "2.4.3-alpha"},
		{"1.2.3+asdf - 2.4.3+asdf", "1.2.3"},
		{"1.0.0", "1.0.0"},
		{">=*", "0.2.4"},
		{"", "1.0.0"},
		{"*", "1.2.3"},
		{">=1.0.0", "1.0.0"},
		{">=1.0.0", "1.0.1"},
		{">=1.0.0", "1.1.0"},
		{">1.0.0", "1.0.1"},
		{">1.0.0", "1.1.0"},
		{"<=2.0.0", "2.0.0"},
		{"<=2.0.0", "1.9999.9999"},
		{"<=2.0.0", "0.2.9"},
		{"<2.0.0", "1.9999.9999"},
		{"<2.0.0", "0.2.9"},
		{">= 1.0.0", "1.0.0"},
		{">=  1.0.0", "1.0.1"},
		{">=   1.0.0", "1.1.0"},
		{"> 1.0.0", "1.0.1"},
		{">  1.0.0", "1.1.0"},
		{"<=   2.0.0", "2.0.0"},
		{"<= 2.0.0", "1.9999.9999"},
		{"<=  2.0.0", "0.2.9"},
		{"<    2.0.0", "1.9999.9999"},
		{"<\t2.0.0", "0.2.9"},
		{">=0.1.97", "0.1.97"},
		{"0.1.20 || 1.2.4", "1.2.4"},
		{">=0.2.3 || <0.0.1", "0.0.0"},
		{">=0.2.3 || <0.0.1", "0.2.3"},
		{">=0.2.3 || <0.0.1", "0.2.4"},
		{"||", "1.3.4"},
		{"2.x.x", "2.1.3"},
		{"1.2.x", "1.2.3"},
		{"1.2.x || 2.x", "2.1.3"},
		{"1.2.x || 2.x", "1.2.3"},
		{"x", "1.2.3"},
		{"2.*.*", "2.1.3"},
		{"1.2.*", "1.2.3"},
		{"1.2.* || 2.*", "2.1.3"},
		{"1.2.* || 2.*", "1.2.3"},
		{"2", "2.1.2"},
		{"2.3", "2.3.1"},
		{"~0.0.1", "0.0.1"},
		{"~0.0.1", "0.0.2"},
		{"~x", "0.0.9"},
		{"~2", "2.0.9"},
		{"~2.4", "2.4.0"},
		{"~2.4", "2.4.5"},
		{"~>3.2.1", "3.2.2"},
		{"~1", "1.2.3"},
		{"~>1", "1.2.3"},
		{"~> 1", "1.2.3"},
		{"~1.0", "1.0.2"},
		{"~ 1.0", "1.0.2"},
		{"~ 1.0.3", "1.0.12"},
		{">=1", "1.0.0"},
		{">= 1", "1.0.0"},
		{"<1.2", "1.1.1"},
		{"< 1.2", "1.1.1"},
		{"~v0.5.4-pre", "0.5.5"},
		{"~v0.5.4-pre", "0.5.4"},
		{"=0.7.x", "0.7.2"},
		{"<=0.7.x", "0.7.2"},
		{">=0.7.x", "0.7.2"},
		{"<=0.7.x", "0.6.2"},
		{"~1.2.1 >=1.2.3", "1.2.3"},
		{"~1.2.1 =1.2.3", "1.2.3"},
		{"~1.2.1 1.2.3", "1.2.3"},
		{"~1.2.1 >=1.2.3 1.2.3", "1.2.3"},
		{"~1.2.1 1.2.3 >=1.2.3", "1.2.3"},
		{">=1.2.1 1.2.3", "1.2.3"},
		{"1.2.3 >=1.2.1", "1.2.3"},
		{">=1.2.3 >=1.2.1", "1.2.3"},
		{">=1.2.1 >=1.2.3", "1.2.3"},
		{">=1.2", "1.2.8"},
		{"^1.2.3", "1.8.1"},
		{"^0.1.2", "0.1.2"},
		{"^0.1", "0.1.2"},
		{"^0.0.1", "0.0.1"},
		{"^1.2", "1.4.2"},
		{"^1.2 ^1", "1.4.2"},
		{"^1.2.3-alpha", "1.2.3-pre"},
		{"^1.2.0-alpha", "1.2.0-pre"},
		{"^0.0.1-alpha", "0.0.1-beta"},
		{"^0.0.1-alpha", "0.0.1"},
		{"^0.1.1-alpha", "0.1.1-beta"},
		{"^x", "1.2.3"},
		{"x - 1.0.0", "0.9.7"},
		{"x - 1.x", "0.9.7"},
		{"1.0.0 - x", "1.9.7"},
		{"1.x - x", "1.9.7"},
		{"<=7.x", "7.9.9"},
	}
	for _, c := range cases {
		r, err := ParseRange(c[0])
		if err != nil {
			t.Errorf("ParseRange(%q) error: %v", c[0], err)
			continue
		}
		v := MustParse(c[1])
		if !r.Satisfies(v) {
			t.Errorf("%q should include %q but did not", c[0], c[1])
		}
	}
}

func TestNodeSemverExclude(t *testing.T) {
	cases := [][2]string{
		{"1.0.0 - 2.0.0", "2.2.3"},
		{"1.2.3+asdf - 2.4.3+asdf", "1.2.3-pre.2"},
		{"1.2.3+asdf - 2.4.3+asdf", "2.4.3-alpha"},
		{"^1.2.3+build", "2.0.0"},
		{"^1.2.3+build", "1.2.0"},
		{"^1.2.3", "1.2.3-pre"},
		{"^1.2", "1.2.0-pre"},
		{">1.2", "1.3.0-beta"},
		{"<=1.2.3", "1.2.3-beta"},
		{"^1.2.3", "1.2.3-beta"},
		{"=0.7.x", "0.7.0-asdf"},
		{">=0.7.x", "0.7.0-asdf"},
		{"<=0.7.x", "0.7.0-asdf"},
		{"1", "2.0.0-beta"},
		{"<1", "1.0.0-beta"},
		{"< 1", "1.0.0-beta"},
		{"=0.7.x", "0.8.2"},
		{">=0.7.x", "0.6.2"},
		{"<0.7.x", "0.7.2"},
		{"<1.2.3", "1.2.3-beta"},
		{"=1.2.3", "1.2.3-beta"},
		{">1.2", "1.2.8"},
		{"^0.0.1", "0.0.2-alpha"},
		{"^0.0.1", "0.0.2"},
		{"^1.2.3", "2.0.0-alpha"},
		{"^1.2.3", "1.2.2"},
		{"^1.2", "1.1.9"},
		{"*", "v1.2.3-foo"},
		{"blerg", "1.2.3"},
		{"git+https://user:password0123@github.com/foo", "123.0.0"},
		{"^1.2.3", "2.0.0-pre"},
	}
	for _, c := range cases {
		r, err := ParseRange(c[0])
		if err != nil {
			// Unparseable range counts as "excludes" per node-semver.
			continue
		}
		v, err := Parse(c[1])
		if err != nil {
			continue // unparseable version excludes
		}
		if r.Satisfies(v) {
			t.Errorf("%q should exclude %q but included it", c[0], c[1])
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha.2", -1},
		{"1.0.0-alpha.10", "1.0.0-alpha.9", 1},
		{"1.0.0-alpha.beta", "1.0.0-alpha.1", 1},
		{"1.0.0-beta", "1.0.0-alpha.beta", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"2.0.0", "1.9.9", 1},
	}
	for _, c := range cases {
		got := sign(MustParse(c.a).Compare(MustParse(c.b)))
		if got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

func TestParseRejectsBadVersions(t *testing.T) {
	bad := []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "v1.2.3", "x.y.z", "1.2.3-beta..1"}
	for _, s := range bad {
		if v, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded: %v", s, v)
		}
	}
}

func TestRangeAlgebra(t *testing.T) {
	// Intersect.
	a := MustParseRange(">=1.0.0 <2.0.0")
	b := MustParseRange(">=1.5.0 <3.0.0")
	inter := a.Intersect(b)
	if !inter.Satisfies(MustParse("1.7.0")) || inter.Satisfies(MustParse("1.2.0")) {
		t.Errorf("intersection wrong: canonical=%s", inter.Canonical())
	}

	// Subset.
	if !MustParseRange("^1.2.0").Subset(MustParseRange(">=1.0.0 <2.0.0")) {
		t.Error("^1.2.0 should be a subset of >=1.0.0 <2.0.0")
	}
	if MustParseRange(">=1.0.0 <3.0.0").Subset(MustParseRange("^1.0.0")) {
		t.Error(">=1.0.0 <3.0.0 should not be a subset of ^1.0.0")
	}

	// Subset must hold across an OR decomposition that merges back.
	if !MustParseRange(">=1.0.0").Subset(MustParseRange(">=1.0.0 <1.5.0 || >=1.5.0")) {
		t.Error(">=1.0.0 should be a subset of its own OR split")
	}

	// IsEmpty.
	if !MustParseRange(">=2.0.0 <1.0.0").IsEmpty() {
		t.Error(">=2.0.0 <1.0.0 should be empty")
	}
	if MustParseRange("^1.0.0").IsEmpty() {
		t.Error("^1.0.0 should not be empty")
	}

	// Exact equality.
	if !Exact(MustParse("1.2.3")).Equal(MustParseRange("=1.2.3")) {
		t.Error("Exact(1.2.3) should equal =1.2.3")
	}
}

func TestMaxSatisfying(t *testing.T) {
	versions := []Version{
		MustParse("1.0.0"), MustParse("1.2.3"), MustParse("1.9.9"),
		MustParse("2.0.0"), MustParse("2.1.0"),
	}
	got, ok := MaxSatisfying(versions, MustParseRange("^1.0.0"))
	if !ok || got.String() != "1.9.9" {
		t.Errorf("MaxSatisfying = %v (ok=%v), want 1.9.9", got, ok)
	}
	if _, ok := MaxSatisfying([]Version{MustParse("3.0.0")}, MustParseRange("^1.0.0")); ok {
		t.Error("expected no match for ^1.0.0 over [3.0.0]")
	}
}
