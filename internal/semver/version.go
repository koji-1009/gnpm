// Package semver implements the npm dialect of semantic versioning:
// version parsing and precedence (SemVer 2.0) plus npm-flavored range
// parsing and matching (the node-semver rules, including its
// prerelease-exclusion behavior). Go's golang.org/x/mod/semver is not
// npm-compatible, so this is implemented from scratch and validated
// against the node-semver fixture corpus in version_test.go.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version. Build metadata is intentionally
// dropped at parse time: SemVer 2.0 §10 says it does not affect
// precedence, and carrying it would break equality against a tarball
// version that lacks the +build suffix.
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre holds the dot-separated prerelease identifiers, nil when the
	// version is a normal release.
	Pre []string
}

// IsPrerelease reports whether the version carries a prerelease tag.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// String renders the version in canonical form (build metadata omitted).
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Parse parses a strict semver string. A single leading "v"/"V" is not
// accepted (registry versions never carry one); ranges strip it, not
// versions. Build metadata after "+" is discarded.
func Parse(input string) (Version, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return Version{}, fmt.Errorf("semver: empty version")
	}
	// Drop build metadata.
	if plus := strings.IndexByte(s, '+'); plus >= 0 {
		s = s[:plus]
	}
	var pre []string
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		preStr := s[dash+1:]
		s = s[:dash]
		if preStr == "" {
			return Version{}, fmt.Errorf("semver: empty prerelease in %q", input)
		}
		pre = strings.Split(preStr, ".")
		for _, id := range pre {
			if id == "" {
				return Version{}, fmt.Errorf("semver: empty prerelease identifier in %q", input)
			}
			if err := validatePrereleaseID(id); err != nil {
				return Version{}, fmt.Errorf("semver: %v in %q", err, input)
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("semver: %q is not major.minor.patch", input)
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := parseNumericComponent(p)
		if err != nil {
			return Version{}, fmt.Errorf("semver: %v in %q", err, input)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, nil
}

// MustParse is Parse that panics on error, for tests and constant
// versions.
func MustParse(input string) Version {
	v, err := Parse(input)
	if err != nil {
		panic(err)
	}
	return v
}

// TryParse returns the parsed version and ok=false on failure.
func TryParse(input string) (Version, bool) {
	v, err := Parse(input)
	return v, err == nil
}

func parseNumericComponent(p string) (int, error) {
	if p == "" {
		return 0, fmt.Errorf("empty version component")
	}
	// No leading zeros (SemVer 2.0 §2), except the literal "0".
	if len(p) > 1 && p[0] == '0' {
		return 0, fmt.Errorf("version component %q has a leading zero", p)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid version component %q", p)
	}
	return n, nil
}

func validatePrereleaseID(id string) error {
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
			return fmt.Errorf("invalid prerelease identifier %q", id)
		}
	}
	// A numeric identifier must not carry a leading zero.
	if isNumericID(id) && len(id) > 1 && id[0] == '0' {
		return fmt.Errorf("numeric prerelease identifier %q has a leading zero", id)
	}
	return nil
}

func isNumericID(id string) bool {
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return len(id) > 0
}

// Compare returns -1, 0, or +1 as v sorts before, equal to, or after o,
// following SemVer 2.0 precedence (build metadata ignored, a release
// outranks its prereleases).
func (v Version) Compare(o Version) int {
	if c := cmpInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, o.Patch); c != 0 {
		return c
	}
	return comparePre(v.Pre, o.Pre)
}

// Less reports whether v < o.
func (v Version) Less(o Version) bool { return v.Compare(o) < 0 }

// Equal reports whether v and o have identical precedence.
func (v Version) Equal(o Version) bool { return v.Compare(o) == 0 }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePre compares two prerelease identifier lists. An empty list
// (a normal release) sorts higher than any non-empty list.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // release > prerelease
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := comparePreID(a[i], b[i]); c != 0 {
			return c
		}
	}
	// All shared identifiers equal: the shorter set has lower precedence.
	return cmpInt(len(a), len(b))
}

func comparePreID(a, b string) int {
	an, bn := isNumericID(a), isNumericID(b)
	switch {
	case an && bn:
		// Numeric identifiers compared numerically. Lengths are bounded
		// by version strings, so Atoi is safe.
		ai, _ := strconv.Atoi(a)
		bi, _ := strconv.Atoi(b)
		return cmpInt(ai, bi)
	case an:
		return -1 // numeric < alphanumeric
	case bn:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
