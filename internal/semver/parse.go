package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseRange parses input as an npm range. Empty, "*", "x", "X", and
// "latest" all mean "any version". Supported forms: exact (`1.2.3`), `=`,
// comparators (`>=`, `<=`, `>`, `<`), caret (`^`), tilde (`~`, `~>`),
// x-ranges (`1.x`, `1.2.*`, `1`), hyphen ranges (`1.2.3 - 2.3.4`), and
// `||` disjunction.
func ParseRange(input string) (NpmRange, error) {
	raw := normalizeOperatorSpaces(strings.TrimSpace(input))
	switch raw {
	case "", "*", "x", "X", "latest":
		return Any(), nil
	}
	parts := splitOr(raw)
	if len(parts) == 0 {
		return Any(), nil
	}
	clauses := make([]clause, 0, len(parts))
	for _, p := range parts {
		c, err := parseClause(p)
		if err != nil {
			return NpmRange{}, err
		}
		clauses = append(clauses, c)
	}
	return NpmRange{raw: raw, clauses: clauses}, nil
}

// MustParseRange is ParseRange that panics on error, for tests and
// constant ranges.
func MustParseRange(input string) NpmRange {
	r, err := ParseRange(input)
	if err != nil {
		panic(err)
	}
	return r
}

var opSpaceRe = regexp.MustCompile(`(>=|<=|~>|[><=~^])\s+`)

// normalizeOperatorSpaces collapses whitespace between an operator and
// its operand: ">=  1.0.0" → ">=1.0.0".
func normalizeOperatorSpaces(input string) string {
	return opSpaceRe.ReplaceAllString(input, "$1")
}

// splitOr splits on top-level "||", tracking parenthesis depth, and
// trims/drops empty parts.
func splitOr(s string) []string {
	var parts []string
	depth := 0
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && ch == '|' && i+1 < len(s) && s[i+1] == '|' {
			parts = append(parts, buf.String())
			buf.Reset()
			i++
			continue
		}
		buf.WriteByte(ch)
	}
	parts = append(parts, buf.String())
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseClause turns one AND-clause (a hyphen range or whitespace-joined
// comparators) into a single interval clause.
func parseClause(clauseStr string) (clause, error) {
	trimmed := strings.TrimSpace(clauseStr)
	if trimmed == "" {
		return anyClause(), nil
	}

	if lo, hi, ok := matchHyphen(trimmed); ok {
		return parseHyphen(lo, hi)
	}

	acc := anyClause()
	for _, tok := range strings.Fields(trimmed) {
		c, err := parseComparator(tok)
		if err != nil {
			return clause{}, err
		}
		acc = intersectClause(acc, c)
	}
	return acc, nil
}

func matchHyphen(input string) (lo, hi string, ok bool) {
	idx := strings.Index(input, " - ")
	if idx < 0 {
		return "", "", false
	}
	lo = strings.TrimSpace(input[:idx])
	hi = strings.TrimSpace(input[idx+3:])
	if lo == "" || hi == "" {
		return "", "", false
	}
	return lo, hi, true
}

func parseHyphen(loStr, hiStr string) (clause, error) {
	l, err := parsePartial(loStr)
	if err != nil {
		return clause{}, err
	}
	u, err := parsePartial(hiStr)
	if err != nil {
		return clause{}, err
	}
	c := clause{loSet: true, lo: l.lowerBound(), loIncl: true}
	// Upper bound: exact when the patch is present, else the next
	// minor/major; an x upper bound is unbounded.
	if u.major == nil {
		c.hiSet = false
	} else if u.minor == nil {
		c.hiSet, c.hi, c.hiIncl = true, Version{Major: *u.major + 1}, false
	} else if u.patch == nil {
		c.hiSet, c.hi, c.hiIncl = true, Version{Major: *u.major, Minor: *u.minor + 1}, false
	} else {
		c.hiSet, c.hi, c.hiIncl = true, u.lowerBound(), true
	}
	c.pre = hyphenPre(l, u)
	return c, nil
}

func hyphenPre(l, u partial) map[tuple]bool {
	var out map[tuple]bool
	add := func(p partial) {
		if !p.hasPre() {
			return
		}
		if out == nil {
			out = map[tuple]bool{}
		}
		out[p.tupleZero()] = true
	}
	add(l)
	add(u)
	return out
}

func parseComparator(tok string) (clause, error) {
	switch {
	case strings.HasPrefix(tok, ">="):
		return cmpGE(tok[2:], true)
	case strings.HasPrefix(tok, "<="):
		return cmpLE(tok[2:])
	case strings.HasPrefix(tok, ">"):
		return cmpGT(tok[1:])
	case strings.HasPrefix(tok, "<"):
		return cmpLT(tok[1:])
	case strings.HasPrefix(tok, "^"):
		return cmpCaret(tok[1:])
	case strings.HasPrefix(tok, "~>"):
		return cmpTilde(tok[2:])
	case strings.HasPrefix(tok, "~"):
		return cmpTilde(tok[1:])
	case strings.HasPrefix(tok, "="):
		return exactOrXRange(tok[1:])
	case tok == "*" || tok == "x" || tok == "X" || tok == "":
		return anyClause(), nil
	default:
		return exactOrXRange(tok)
	}
}

func cmpGE(body string, _ bool) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.isXRange() {
		lo, _, ok := v.xRangeBounds()
		if !ok {
			return anyClause(), nil // >=x → any
		}
		return geClause(lo, false), nil
	}
	return geClause(v.lowerBound(), v.hasPre()), nil
}

func cmpLE(body string) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.isXRange() {
		_, hi, ok := v.xRangeBounds()
		if !ok {
			return anyClause(), nil // <=x → any
		}
		return ltClause(hi, false), nil // <=1.2.x → <1.3.0
	}
	return leClause(v.lowerBound(), v.hasPre()), nil
}

func cmpGT(body string) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.isXRange() {
		if v.major == nil {
			return emptyClause(), nil // >x → never
		}
		_, hi, _ := v.xRangeBounds()
		return geClause(hi, false), nil // >1.x → >=2.0.0
	}
	return gtClause(v.lowerBound(), v.hasPre()), nil
}

func cmpLT(body string) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.isXRange() {
		if v.major == nil {
			return emptyClause(), nil // <x → never
		}
		lo, _, _ := v.xRangeBounds()
		return ltClause(lo, false), nil // <1.x → <1.0.0
	}
	return ltClause(v.lowerBound(), v.hasPre()), nil
}

func cmpCaret(body string) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.major == nil {
		return anyClause(), nil // ^x / ^* → any
	}
	lo, hi := v.caretBounds()
	return intervalClause(lo, hi, v), nil
}

func cmpTilde(body string) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.major == nil {
		return anyClause(), nil
	}
	lo, hi := v.tildeBounds()
	return intervalClause(lo, hi, v), nil
}

func exactOrXRange(body string) (clause, error) {
	v, err := parsePartial(body)
	if err != nil {
		return clause{}, err
	}
	if v.isXRange() {
		lo, hi, ok := v.xRangeBounds()
		if !ok {
			return anyClause(), nil
		}
		return intervalClause(lo, hi, v), nil
	}
	exact := v.lowerBound()
	c := clause{loSet: true, lo: exact, loIncl: true, hiSet: true, hi: exact, hiIncl: true}
	if v.hasPre() {
		c.pre = map[tuple]bool{v.tupleZero(): true}
	}
	return c, nil
}

// --- clause builders --------------------------------------------------

func preFor(v Version, hasPre bool) map[tuple]bool {
	if !hasPre {
		return nil
	}
	return map[tuple]bool{{v.Major, v.Minor, v.Patch}: true}
}

func geClause(v Version, hasPre bool) clause {
	return clause{loSet: true, lo: v, loIncl: true, pre: preFor(v, hasPre)}
}
func gtClause(v Version, hasPre bool) clause {
	return clause{loSet: true, lo: v, loIncl: false, pre: preFor(v, hasPre)}
}
func leClause(v Version, hasPre bool) clause {
	return clause{hiSet: true, hi: v, hiIncl: true, pre: preFor(v, hasPre)}
}
func ltClause(v Version, hasPre bool) clause {
	return clause{hiSet: true, hi: v, hiIncl: false, pre: preFor(v, hasPre)}
}

// intervalClause builds [lo, hi). The prerelease triple, when src names
// one, is the lower bound's — caret/tilde/x synthesize a non-prerelease
// upper bound.
func intervalClause(lo, hi Version, src partial) clause {
	c := clause{loSet: true, lo: lo, loIncl: true, hiSet: true, hi: hi, hiIncl: false}
	if src.hasPre() {
		c.pre = map[tuple]bool{src.tupleZero(): true}
	}
	return c
}

// --- partial version model -------------------------------------------

// partial is a possibly-incomplete version like "1", "1.2", "1.x", or
// "1.2.3-beta". A nil component marks an x-range wildcard.
type partial struct {
	major *int
	minor *int
	patch *int
	pre   string
}

func (p partial) hasPre() bool   { return p.pre != "" }
func (p partial) isXRange() bool { return p.major == nil || p.minor == nil || p.patch == nil }

func (p partial) tupleZero() tuple {
	return tuple{deref(p.major), deref(p.minor), deref(p.patch)}
}

func deref(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func splitPre(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

// lowerBound fills missing components with zero, keeping any prerelease.
func (p partial) lowerBound() Version {
	return Version{Major: deref(p.major), Minor: deref(p.minor), Patch: deref(p.patch), Pre: splitPre(p.pre)}
}

// caretBounds implements ^ semantics:
//
//	^1.2.3 → >=1.2.3 <2.0.0 ; ^0.2.3 → >=0.2.3 <0.3.0 ; ^0.0.3 → >=0.0.3 <0.0.4
//	^1.2.x → >=1.2.0 <2.0.0 ; ^0.x → >=0.0.0 <1.0.0
func (p partial) caretBounds() (lo, hi Version) {
	lo = p.lowerBound()
	maj := deref(p.major)
	switch {
	case p.major == nil || maj == 0 && p.minor == nil:
		return lo, Version{Major: maj + 1}
	case maj != 0:
		return lo, Version{Major: maj + 1}
	case deref(p.minor) != 0 || p.patch == nil:
		return lo, Version{Minor: deref(p.minor) + 1}
	default:
		return lo, Version{Minor: deref(p.minor), Patch: deref(p.patch) + 1}
	}
}

// tildeBounds implements ~ semantics:
//
//	~1.2.3 → >=1.2.3 <1.3.0 ; ~1.2 → >=1.2.0 <1.3.0 ; ~1 → >=1.0.0 <2.0.0
func (p partial) tildeBounds() (lo, hi Version) {
	lo = p.lowerBound()
	if p.minor != nil {
		return lo, Version{Major: deref(p.major), Minor: *p.minor + 1}
	}
	return lo, Version{Major: deref(p.major) + 1}
}

// xRangeBounds returns [lo, hi) for partial versions like 1, 1.2, 1.x,
// 1.2.x. ok is false (→ "any") when the major is a wildcard.
func (p partial) xRangeBounds() (lo, hi Version, ok bool) {
	if p.major == nil {
		return Version{}, Version{}, false
	}
	if p.minor == nil {
		return Version{Major: *p.major}, Version{Major: *p.major + 1}, true
	}
	if p.patch == nil {
		return Version{Major: *p.major, Minor: *p.minor}, Version{Major: *p.major, Minor: *p.minor + 1}, true
	}
	return Version{Major: *p.major, Minor: *p.minor, Patch: *p.patch},
		Version{Major: *p.major, Minor: *p.minor, Patch: *p.patch + 1}, true
}

func parsePartial(input string) (partial, error) {
	s := strings.TrimSpace(input)
	for len(s) > 0 && (s[0] == 'v' || s[0] == 'V' || s[0] == '=') {
		s = strings.TrimSpace(s[1:])
	}
	if s == "" {
		return partial{}, nil
	}
	if plus := strings.IndexByte(s, '+'); plus >= 0 {
		s = s[:plus]
	}
	var pre string
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		pre = s[dash+1:]
		s = s[:dash]
	}
	parts := strings.Split(s, ".")
	parseComp := func(i int) (*int, error) {
		if i >= len(parts) {
			return nil, nil
		}
		p := parts[i]
		if p == "" || p == "x" || p == "X" || p == "*" {
			return nil, nil
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("semver: invalid version component %q in %q", p, input)
		}
		return &n, nil
	}
	var out partial
	var err error
	if out.major, err = parseComp(0); err != nil {
		return partial{}, err
	}
	if out.minor, err = parseComp(1); err != nil {
		return partial{}, err
	}
	if out.patch, err = parseComp(2); err != nil {
		return partial{}, err
	}
	out.pre = pre
	return out, nil
}
