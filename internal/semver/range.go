package semver

import (
	"sort"
	"strings"
)

// tuple is a (major, minor, patch) key used to record which version
// triples a clause permits prereleases at.
type tuple [3]int

// clause is one AND-group of comparators reduced to a single numeric
// interval, plus the set of (major,minor,patch) tuples at which a bound
// comparator carried an explicit prerelease. node-semver only lets a
// prerelease version satisfy a comparator set when some comparator in
// that set names a prerelease at the same triple; pre records exactly
// those triples so satisfies can enforce the rule.
type clause struct {
	loSet  bool
	lo     Version
	loIncl bool
	hiSet  bool
	hi     Version
	hiIncl bool
	pre    map[tuple]bool
}

func anyClause() clause { return clause{} }

// emptyClause matches no version: a degenerate point excluded on both
// ends. Used for comparators like ">x" / "<x" that node-semver treats as
// unsatisfiable.
func emptyClause() clause {
	return clause{loSet: true, hiSet: true} // lo == hi == 0.0.0, both exclusive
}

func (c clause) numericContains(v Version) bool {
	if c.loSet {
		switch cmp := v.Compare(c.lo); {
		case cmp < 0:
			return false
		case cmp == 0 && !c.loIncl:
			return false
		}
	}
	if c.hiSet {
		switch cmp := v.Compare(c.hi); {
		case cmp > 0:
			return false
		case cmp == 0 && !c.hiIncl:
			return false
		}
	}
	return true
}

func (c clause) satisfies(v Version) bool {
	if !c.numericContains(v) {
		return false
	}
	if !v.IsPrerelease() {
		return true
	}
	// node-semver: a prerelease version is admitted only when this
	// comparator set named a prerelease at the same triple.
	return c.pre[tuple{v.Major, v.Minor, v.Patch}]
}

func (c clause) isEmpty() bool {
	if !c.loSet || !c.hiSet {
		return false // at least half-unbounded → always inhabited
	}
	switch c.lo.Compare(c.hi) {
	case 1:
		return true
	case 0:
		return !(c.loIncl && c.hiIncl)
	default:
		return false
	}
}

func mergePre(a, b map[tuple]bool) map[tuple]bool {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[tuple]bool, len(a)+len(b))
	for t := range a {
		out[t] = true
	}
	for t := range b {
		out[t] = true
	}
	return out
}

// intersectClause computes the numeric intersection of a and b and
// unions their prerelease triples (the AND of two comparator sets is one
// set, so a prerelease allowed by either bound is allowed).
func intersectClause(a, b clause) clause {
	r := clause{pre: mergePre(a.pre, b.pre)}
	// Lower bound: the higher of the two starts.
	switch {
	case !a.loSet:
		r.loSet, r.lo, r.loIncl = b.loSet, b.lo, b.loIncl
	case !b.loSet:
		r.loSet, r.lo, r.loIncl = a.loSet, a.lo, a.loIncl
	default:
		switch a.lo.Compare(b.lo) {
		case 1:
			r.loSet, r.lo, r.loIncl = true, a.lo, a.loIncl
		case -1:
			r.loSet, r.lo, r.loIncl = true, b.lo, b.loIncl
		default:
			r.loSet, r.lo, r.loIncl = true, a.lo, a.loIncl && b.loIncl
		}
	}
	// Upper bound: the lower of the two ends.
	switch {
	case !a.hiSet:
		r.hiSet, r.hi, r.hiIncl = b.hiSet, b.hi, b.hiIncl
	case !b.hiSet:
		r.hiSet, r.hi, r.hiIncl = a.hiSet, a.hi, a.hiIncl
	default:
		switch a.hi.Compare(b.hi) {
		case -1:
			r.hiSet, r.hi, r.hiIncl = true, a.hi, a.hiIncl
		case 1:
			r.hiSet, r.hi, r.hiIncl = true, b.hi, b.hiIncl
		default:
			r.hiSet, r.hi, r.hiIncl = true, a.hi, a.hiIncl && b.hiIncl
		}
	}
	return r
}

// NpmRange is an npm-flavored version range: a disjunction (OR) of
// clauses. It supports node-semver satisfaction plus the interval
// algebra (Intersect / IsEmpty / Subset) the resolver needs.
type NpmRange struct {
	raw     string
	clauses []clause
}

// Any matches every release version (the `*` range).
func Any() NpmRange { return NpmRange{raw: "*", clauses: []clause{anyClause()}} }

// Exact builds the range that matches only v.
func Exact(v Version) NpmRange {
	c := clause{loSet: true, lo: v, loIncl: true, hiSet: true, hi: v, hiIncl: true}
	if v.IsPrerelease() {
		c.pre = map[tuple]bool{{v.Major, v.Minor, v.Patch}: true}
	}
	return NpmRange{raw: "=" + v.String(), clauses: []clause{c}}
}

// Raw returns the original text the range was parsed from.
func (r NpmRange) Raw() string { return r.raw }

func (r NpmRange) String() string {
	if r.raw != "" {
		return r.raw
	}
	return r.Canonical()
}

// Satisfies reports whether v falls in the range under node-semver rules.
func (r NpmRange) Satisfies(v Version) bool {
	for _, c := range r.clauses {
		if c.satisfies(v) {
			return true
		}
	}
	return false
}

// IsEmpty reports whether the range matches no version.
func (r NpmRange) IsEmpty() bool {
	for _, c := range r.clauses {
		if !c.isEmpty() {
			return false
		}
	}
	return true
}

// Intersect returns the range allowed by both r and o.
func (r NpmRange) Intersect(o NpmRange) NpmRange {
	var cs []clause
	for _, a := range r.clauses {
		for _, b := range o.clauses {
			c := intersectClause(a, b)
			if !c.isEmpty() {
				cs = append(cs, c)
			}
		}
	}
	return NpmRange{raw: r.raw + " " + o.raw, clauses: cs}
}

// Union returns the range allowed by either r or o.
func (r NpmRange) Union(o NpmRange) NpmRange {
	cs := make([]clause, 0, len(r.clauses)+len(o.clauses))
	cs = append(cs, r.clauses...)
	cs = append(cs, o.clauses...)
	return NpmRange{raw: r.raw + " || " + o.raw, clauses: cs}
}

// Subset reports whether every version r allows is also allowed by o.
// Implemented as r ∩ o == r over the canonical (normalized, merged)
// interval form — the test the pubgrub solver relies on.
func (r NpmRange) Subset(o NpmRange) bool {
	return r.Intersect(o).Canonical() == r.Canonical()
}

// Equal reports whether r and o allow exactly the same versions.
func (r NpmRange) Equal(o NpmRange) bool {
	return r.Canonical() == o.Canonical()
}

// Canonical renders a normalized, deterministic string of the range's
// numeric intervals (prerelease triples excluded). Two ranges with the
// same canonical string allow the same release versions; the resolver
// uses string equality of this form for subset and equality checks.
func (r NpmRange) Canonical() string {
	merged := normalize(r.clauses)
	if len(merged) == 0 {
		return "∅"
	}
	parts := make([]string, len(merged))
	for i, c := range merged {
		parts[i] = canonicalClause(c)
	}
	return strings.Join(parts, "|")
}

func canonicalClause(c clause) string {
	var b strings.Builder
	if !c.loSet {
		b.WriteString("(-inf")
	} else if c.loIncl {
		b.WriteString("[" + c.lo.String())
	} else {
		b.WriteString("(" + c.lo.String())
	}
	b.WriteByte(',')
	if !c.hiSet {
		b.WriteString("+inf)")
	} else if c.hiIncl {
		b.WriteString(c.hi.String() + "]")
	} else {
		b.WriteString(c.hi.String() + ")")
	}
	return b.String()
}

// normalize drops empty clauses, sorts by lower bound, and merges
// overlapping or adjacent clauses so the canonical form is independent
// of how the range was built.
func normalize(clauses []clause) []clause {
	var cs []clause
	for _, c := range clauses {
		if !c.isEmpty() {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool { return cmpLo(cs[i], cs[j]) < 0 })
	merged := []clause{cs[0]}
	for _, c := range cs[1:] {
		last := &merged[len(merged)-1]
		if overlapsOrAdjacent(*last, c) {
			*last = unionAdjacent(*last, c)
		} else {
			merged = append(merged, c)
		}
	}
	return merged
}

// cmpLo orders clauses by their lower start: unbounded first, then by
// version, with an inclusive start sorting before an exclusive one.
func cmpLo(a, b clause) int {
	switch {
	case !a.loSet && !b.loSet:
		return 0
	case !a.loSet:
		return -1
	case !b.loSet:
		return 1
	}
	if c := a.lo.Compare(b.lo); c != 0 {
		return c
	}
	switch {
	case a.loIncl && !b.loIncl:
		return -1
	case !a.loIncl && b.loIncl:
		return 1
	default:
		return 0
	}
}

// overlapsOrAdjacent reports whether b (sorted at or after a by lower
// bound) touches or overlaps a, so the two can be merged.
func overlapsOrAdjacent(a, b clause) bool {
	if !a.hiSet { // a runs to +inf
		return true
	}
	if !b.loSet { // b runs from -inf; since a starts no later, they meet
		return true
	}
	switch a.hi.Compare(b.lo) {
	case 1:
		return true // a's top is past b's start
	case 0:
		return a.hiIncl || b.loIncl // touching endpoints merge if either includes it
	default:
		return false
	}
}

// unionAdjacent merges b into a, which must overlap or be adjacent. The
// result keeps the lower start (a's, by sort order) and the higher end.
func unionAdjacent(a, b clause) clause {
	r := a
	r.pre = mergePre(a.pre, b.pre)
	// Upper bound: the higher of the two ends.
	switch {
	case !a.hiSet || !b.hiSet:
		r.hiSet = false
		r.hi = Version{}
		r.hiIncl = false
	default:
		switch a.hi.Compare(b.hi) {
		case 1:
			r.hiSet, r.hi, r.hiIncl = true, a.hi, a.hiIncl
		case -1:
			r.hiSet, r.hi, r.hiIncl = true, b.hi, b.hiIncl
		default:
			r.hiSet, r.hi, r.hiIncl = true, a.hi, a.hiIncl || b.hiIncl
		}
	}
	return r
}

// MaxSatisfying returns the highest version in versions that satisfies r,
// and ok=false when none does.
func MaxSatisfying(versions []Version, r NpmRange) (Version, bool) {
	var best Version
	found := false
	for _, v := range versions {
		if r.Satisfies(v) && (!found || best.Less(v)) {
			best, found = v, true
		}
	}
	return best, found
}
