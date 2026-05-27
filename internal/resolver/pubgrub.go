package resolver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/semver"
)

const rootPackage = "$root"

// Solver is a Pubgrub version solver with conflict-driven learning.
// Unit propagation derives the negation of an almost-satisfied
// incompatibility's remaining term; a fully-satisfied incompatibility is
// a conflict that rolls back the highest satisfying decision and learns a
// unary incompatibility forbidding that exact (package, version). The
// loop terminates: each conflict forbids at least one pair and the set is
// finite.
type Solver struct {
	req      Request
	Warnings []string

	solution      []*assignment
	byPkg         map[string][]*assignment
	incompat      []Incompatibility
	incompatByPkg map[string][]int // package → indices into incompat that mention it
	changed       map[string]bool
	decisionLevel int
	forbidden     map[string]map[string]bool

	versionsCache map[string][]semver.Version
	depsCache     map[string]map[string]PackageDependencies
}

// NewSolver builds a solver for the request.
func NewSolver(req Request) *Solver {
	return &Solver{
		req:           req,
		byPkg:         map[string][]*assignment{},
		incompatByPkg: map[string][]int{},
		changed:       map[string]bool{},
		forbidden:     map[string]map[string]bool{},
		versionsCache: map[string][]semver.Version{},
		depsCache:     map[string]map[string]PackageDependencies{},
	}
}

type assignmentKind int

const (
	decisionAssignment assignmentKind = iota
	derivationAssignment
)

type assignment struct {
	kind    assignmentKind
	pkg     string
	version semver.Version // decision only
	term    Term           // derivation only
	level   int
	cause   *Incompatibility
}

// Solve runs resolution and returns the flat package → version map.
func (s *Solver) Solve() (Result, error) {
	s.registerRoot()
	next := rootPackage
	iterations := 0
	for {
		iterations++
		if iterations > 50000 {
			return Result{}, core.ResolutionError("pubgrub exceeded %d iterations\n%s", iterations-1, s.explainRecent())
		}
		if err := s.unitPropagate(next); err != nil {
			return Result{}, err
		}
		n, more, err := s.makeDecision()
		if err != nil {
			return Result{}, err
		}
		if !more {
			break
		}
		next = n
	}
	out := map[string]semver.Version{}
	for _, a := range s.solution {
		if a.kind == decisionAssignment && a.pkg != rootPackage {
			out[a.pkg] = a.version
		}
	}
	return Result{Assignments: out}, nil
}

func (s *Solver) registerRoot() {
	s.decisionLevel = 0
	root := &assignment{kind: decisionAssignment, pkg: rootPackage, version: semver.Version{}, level: 0}
	s.solution = append(s.solution, root)
	s.byPkg[rootPackage] = []*assignment{root}
	s.changed[rootPackage] = true

	var all [][2]string
	for _, k := range sortedKeys(s.req.Dependencies) {
		all = append(all, [2]string{k, s.req.Dependencies[k]})
	}
	for _, k := range sortedKeys(s.req.OptionalDependencies) {
		all = append(all, [2]string{k, s.req.OptionalDependencies[k]})
	}
	for _, e := range all {
		eff := s.effective(e[0], e[1], rootPackage)
		s.addIncompat(Incompatibility{
			Terms: []Term{
				{Package: rootPackage, Range: semver.Any(), IsPositive: true},
				{Package: e[0], Range: parseRangeOrAny(eff), IsPositive: false},
			},
			Cause: fmt.Sprintf("root depends on %s@%s", e[0], eff),
		})
	}
}

func (s *Solver) effective(pkg, declared, parent string) string {
	if parent != "" {
		if scoped, ok := s.req.NestedOverrides[parent]; ok {
			if r, ok := scoped[pkg]; ok {
				return r
			}
		}
	}
	if r, ok := s.req.Overrides[pkg]; ok {
		return r
	}
	return declared
}

func parseRangeOrAny(raw string) semver.NpmRange {
	r, err := semver.ParseRange(raw)
	if err != nil {
		return semver.Any()
	}
	return r
}

// --- unit propagation -------------------------------------------------

func (s *Solver) unitPropagate(start string) error {
	s.changed[start] = true
	for len(s.changed) > 0 {
		pkg := anyKey(s.changed)
		delete(s.changed, pkg)

		// Only incompatibilities mentioning pkg can change status when
		// pkg's assignments change, so walk the per-package index rather
		// than every incompatibility. Visit newest-first (descending
		// insertion index), matching the order the full scan used: which
		// satisfied incompatibility surfaces first drives backtracking, so
		// preserving the order keeps resolution — and the lockfile —
		// identical. onConflict may append to this slice, but it returns
		// escalated immediately after, so iterating the snapshot is safe.
		idxs := s.incompatByPkg[pkg]
		for i := len(idxs) - 1; i >= 0; i-- {
			inc := s.incompat[idxs[i]]
			status, unsat := s.check(inc)
			switch status {
			case incSatisfied:
				escalated, err := s.onConflict(inc)
				if err != nil {
					return err
				}
				if escalated {
					return nil // backtracked; solve() re-enters
				}
			case incAlmostSatisfied:
				s.derive(unsat.Invert(), &inc)
				s.changed[unsat.Package] = true
			case incContradicted, incInconclusive:
				// nothing
			}
		}
	}
	return nil
}

func (s *Solver) onConflict(conflicted Incompatibility) (bool, error) {
	var culprit *assignment
	for _, t := range conflicted.Terms {
		for _, a := range s.byPkg[t.Package] {
			if a.kind != decisionAssignment {
				continue
			}
			if !s.termSatisfiedBy(t, a) {
				continue
			}
			if culprit == nil || a.level > culprit.level {
				culprit = a
			}
		}
	}
	if culprit == nil || culprit.level == 0 {
		return false, core.ResolutionError("version solving failed: %s\n%s", conflicted.Cause, s.explainRecent())
	}
	if s.forbidden[culprit.pkg] == nil {
		s.forbidden[culprit.pkg] = map[string]bool{}
	}
	s.forbidden[culprit.pkg][culprit.version.String()] = true
	s.addIncompat(Incompatibility{
		Terms: []Term{
			{Package: culprit.pkg, Range: semver.Exact(culprit.version), IsPositive: true},
		},
		Cause: fmt.Sprintf("%s@%s conflicts with %s", culprit.pkg, culprit.version, conflicted.Cause),
	})
	s.backtrackTo(culprit.level - 1)
	s.changed = map[string]bool{culprit.pkg: true}
	return true, nil
}

// --- decision ---------------------------------------------------------

type candidate struct {
	pkg    string
	rng    semver.NpmRange
	viable []semver.Version
}

func (s *Solver) makeDecision() (string, bool, error) {
	var candidates []candidate
	for _, pkg := range s.undecidedPackages() {
		rng, ok := s.currentPositiveRange(pkg)
		if !ok {
			continue
		}
		all, err := s.versions(pkg)
		if err != nil {
			return "", false, err
		}
		forbidden := s.forbidden[pkg]
		var viable []semver.Version
		for _, v := range all {
			if !rng.Satisfies(v) {
				continue
			}
			if forbidden[v.String()] {
				continue
			}
			viable = append(viable, v)
		}
		sort.Slice(viable, func(i, j int) bool { return viable[j].Less(viable[i]) }) // descending
		candidates = append(candidates, candidate{pkg: pkg, rng: rng, viable: viable})
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	// Prefer the package with the fewest viable versions (most
	// constrained); deterministic tiebreak by name.
	sort.SliceStable(candidates, func(i, j int) bool {
		if len(candidates[i].viable) != len(candidates[j].viable) {
			return len(candidates[i].viable) < len(candidates[j].viable)
		}
		return candidates[i].pkg < candidates[j].pkg
	})
	pick := candidates[0]

	if len(pick.viable) == 0 {
		s.addIncompat(Incompatibility{
			Terms: []Term{{Package: pick.pkg, Range: pick.rng, IsPositive: true}},
			Cause: fmt.Sprintf("no versions of %s satisfy %s", pick.pkg, pick.rng),
		})
		s.changed[pick.pkg] = true
		return pick.pkg, true, nil
	}

	version := pick.viable[0]
	// Prefer the `latest` dist-tag when it is viable (npm-pick-manifest:
	// don't auto-adopt a higher published-but-not-promoted release)...
	if latest, ok := s.req.Provider.Latest(pick.pkg); ok {
		for _, v := range pick.viable {
			if v.Equal(latest) {
				version = latest
				break
			}
		}
	}
	// ...but a lockfile-preferred version wins over both, for reproducibility.
	if pref, ok := s.req.Preferred[pick.pkg]; ok {
		for _, v := range pick.viable {
			if v.Equal(pref) {
				version = pref
				break
			}
		}
	}

	s.decisionLevel++
	dec := &assignment{kind: decisionAssignment, pkg: pick.pkg, version: version, level: s.decisionLevel}
	s.solution = append(s.solution, dec)
	s.byPkg[pick.pkg] = append(s.byPkg[pick.pkg], dec)
	if s.req.OnDecide != nil {
		s.req.OnDecide(pick.pkg, version)
	}

	deps, err := s.dependenciesOf(pick.pkg, version)
	if err != nil {
		return "", false, err
	}
	for _, dep := range sortedKeys(deps.Dependencies) {
		s.addDepIncompat(pick.pkg, version, dep, deps.Dependencies[dep])
	}
	// Transitive optionalDependencies (platform-specific native
	// siblings) get best-effort treatment: skip when nothing resolvable
	// satisfies them rather than failing the whole solve.
	for _, dep := range sortedKeys(deps.OptionalDependencies) {
		rng := deps.OptionalDependencies[dep]
		ok, err := s.hasSatisfyingVersion(dep, rng)
		if err != nil {
			return "", false, err
		}
		if !ok {
			continue
		}
		s.addDepIncompat(pick.pkg, version, dep, rng)
	}
	if s.req.AutoInstallPeers {
		for _, dep := range sortedKeys(deps.PeerDependencies) {
			if deps.OptionalPeers[dep] {
				continue
			}
			s.addDepIncompat(pick.pkg, version, dep, deps.PeerDependencies[dep])
		}
	} else {
		for _, dep := range sortedKeys(deps.PeerDependencies) {
			if deps.OptionalPeers[dep] {
				continue
			}
			if s.byPkg[dep] == nil {
				s.Warnings = append(s.Warnings, fmt.Sprintf("unmet peer dependency: %s@%s → %s@%s", pick.pkg, version, dep, deps.PeerDependencies[dep]))
			}
		}
	}

	s.changed[pick.pkg] = true
	return pick.pkg, true, nil
}

func (s *Solver) addDepIncompat(parent string, parentVersion semver.Version, dep, rng string) {
	eff := s.effective(dep, rng, parent)
	parsed, err := semver.ParseRange(eff)
	if err != nil {
		s.addIncompat(Incompatibility{
			Terms: []Term{{Package: parent, Range: semver.Exact(parentVersion), IsPositive: true}},
			Cause: fmt.Sprintf("%s@%s declares unparseable range %s@%s", parent, parentVersion, dep, rng),
		})
		return
	}
	s.addIncompat(Incompatibility{
		Terms: []Term{
			{Package: parent, Range: semver.Exact(parentVersion), IsPositive: true},
			{Package: dep, Range: parsed, IsPositive: false},
		},
		Cause: fmt.Sprintf("%s@%s depends on %s@%s", parent, parentVersion, dep, eff),
	})
}

// --- backtracking -----------------------------------------------------

func (s *Solver) backtrackTo(level int) {
	for len(s.solution) > 0 && s.solution[len(s.solution)-1].level > level {
		a := s.solution[len(s.solution)-1]
		s.solution = s.solution[:len(s.solution)-1]
		s.byPkg[a.pkg] = removeAssignment(s.byPkg[a.pkg], a)
		if len(s.byPkg[a.pkg]) == 0 {
			delete(s.byPkg, a.pkg)
		}
	}
	s.decisionLevel = level
}

// --- helpers ----------------------------------------------------------

func (s *Solver) addIncompat(inc Incompatibility) {
	if len(inc.Terms) == 0 {
		return
	}
	idx := len(s.incompat)
	s.incompat = append(s.incompat, inc)
	// Index the incompatibility under each distinct package it mentions so
	// unit propagation can fetch only the relevant incompatibilities instead
	// of scanning the whole list. Incompatibilities are append-only for the
	// life of a solve (backtracking removes assignments, never
	// incompatibilities — see backtrackTo), so an index entry never goes
	// stale. Terms are at most a handful, so the O(terms²) dedup is cheaper
	// than allocating a set per call.
	for i, t := range inc.Terms {
		dup := false
		for j := 0; j < i; j++ {
			if inc.Terms[j].Package == t.Package {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		s.incompatByPkg[t.Package] = append(s.incompatByPkg[t.Package], idx)
	}
}

func (s *Solver) derive(t Term, cause *Incompatibility) {
	a := &assignment{kind: derivationAssignment, pkg: t.Package, term: t, level: s.decisionLevel, cause: cause}
	s.solution = append(s.solution, a)
	s.byPkg[t.Package] = append(s.byPkg[t.Package], a)
}

func (s *Solver) undecidedPackages() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range s.solution {
		if a.kind != derivationAssignment || a.pkg == rootPackage {
			continue
		}
		if s.hasDecision(a.pkg) {
			continue
		}
		if a.term.IsPositive && !seen[a.pkg] {
			seen[a.pkg] = true
			out = append(out, a.pkg)
		}
	}
	return out
}

func (s *Solver) hasDecision(pkg string) bool {
	for _, a := range s.byPkg[pkg] {
		if a.kind == decisionAssignment {
			return true
		}
	}
	return false
}

func (s *Solver) currentPositiveRange(pkg string) (semver.NpmRange, bool) {
	list, ok := s.byPkg[pkg]
	if !ok {
		return semver.NpmRange{}, false
	}
	var positive *semver.NpmRange
	for _, a := range list {
		if a.kind != derivationAssignment || !a.term.IsPositive {
			continue
		}
		if positive == nil {
			r := a.term.Range
			positive = &r
		} else {
			r := positive.Intersect(a.term.Range)
			positive = &r
		}
	}
	if positive == nil {
		return semver.Any(), true
	}
	return *positive, true
}

func (s *Solver) versions(pkg string) ([]semver.Version, error) {
	if cached, ok := s.versionsCache[pkg]; ok {
		return cached, nil
	}
	v, err := s.req.Provider.Versions(pkg)
	if err != nil {
		return nil, err
	}
	sorted := append([]semver.Version(nil), v...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Less(sorted[j]) })
	s.versionsCache[pkg] = sorted
	return sorted, nil
}

func (s *Solver) dependenciesOf(pkg string, v semver.Version) (PackageDependencies, error) {
	if m, ok := s.depsCache[pkg]; ok {
		if d, ok := m[v.String()]; ok {
			return d, nil
		}
	}
	d, err := s.req.Provider.DependenciesOf(pkg, v)
	if err != nil {
		return PackageDependencies{}, err
	}
	if s.depsCache[pkg] == nil {
		s.depsCache[pkg] = map[string]PackageDependencies{}
	}
	s.depsCache[pkg][v.String()] = d
	return d, nil
}

func (s *Solver) hasSatisfyingVersion(pkg, rng string) (bool, error) {
	candidates, err := s.versions(pkg)
	if err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		return false, nil
	}
	parsed, err := semver.ParseRange(s.effective(pkg, rng, ""))
	if err != nil {
		return false, nil
	}
	for _, v := range candidates {
		if parsed.Satisfies(v) {
			return true, nil
		}
	}
	return false, nil
}

// --- incompatibility status ------------------------------------------

type incStatus int

const (
	incSatisfied incStatus = iota
	incAlmostSatisfied
	incContradicted
	incInconclusive
)

func (s *Solver) check(inc Incompatibility) (incStatus, Term) {
	var unsatisfied *Term
	countSatisfied := 0
	for i := range inc.Terms {
		t := inc.Terms[i]
		switch s.termStatus(t) {
		case termSatisfied:
			countSatisfied++
		case termContradicted:
			return incContradicted, Term{}
		case termInconclusive:
			if unsatisfied != nil {
				return incInconclusive, Term{}
			}
			tt := t
			unsatisfied = &tt
		}
	}
	if unsatisfied == nil {
		return incSatisfied, Term{}
	}
	if countSatisfied == len(inc.Terms)-1 {
		return incAlmostSatisfied, *unsatisfied
	}
	return incInconclusive, Term{}
}

type termStat int

const (
	termSatisfied termStat = iota
	termContradicted
	termInconclusive
)

func (s *Solver) termStatus(t Term) termStat {
	list := s.byPkg[t.Package]
	if len(list) == 0 {
		return termInconclusive
	}
	positive := semver.Any()
	var negatives []semver.NpmRange
	var decided *semver.Version
	for _, a := range list {
		if a.kind == decisionAssignment {
			v := a.version
			decided = &v
			continue
		}
		if a.term.IsPositive {
			positive = positive.Intersect(a.term.Range)
		} else {
			negatives = append(negatives, a.term.Range)
		}
	}

	if decided != nil {
		inRange := t.Range.Satisfies(*decided)
		ok := inRange
		if !t.IsPositive {
			ok = !inRange
		}
		if ok {
			return termSatisfied
		}
		return termContradicted
	}

	if t.IsPositive {
		if positive.Intersect(t.Range).IsEmpty() {
			return termContradicted
		}
		for _, neg := range negatives {
			if subsetOf(t.Range, neg) {
				return termContradicted
			}
		}
		if subsetOf(positive, t.Range) && everyEmptyIntersect(negatives, positive) {
			return termSatisfied
		}
		return termInconclusive
	}
	// t says NOT in t.Range.
	if subsetOf(positive, t.Range) && everyNotSubset(negatives, positive) {
		return termContradicted
	}
	if positive.Intersect(t.Range).IsEmpty() {
		return termSatisfied
	}
	for _, neg := range negatives {
		if subsetOf(t.Range, neg) {
			return termSatisfied
		}
	}
	return termInconclusive
}

func (s *Solver) termSatisfiedBy(t Term, a *assignment) bool {
	if a.kind == decisionAssignment {
		inRange := t.Range.Satisfies(a.version)
		if t.IsPositive {
			return inRange
		}
		return !inRange
	}
	derived := a.term
	if derived.Package != t.Package {
		return false
	}
	switch {
	case derived.IsPositive && t.IsPositive:
		return subsetOf(derived.Range, t.Range)
	case derived.IsPositive && !t.IsPositive:
		return derived.Range.Intersect(t.Range).IsEmpty()
	case !derived.IsPositive && t.IsPositive:
		return false
	default:
		return subsetOf(t.Range, derived.Range)
	}
}

func subsetOf(a, b semver.NpmRange) bool { return a.Subset(b) }

func everyEmptyIntersect(negatives []semver.NpmRange, positive semver.NpmRange) bool {
	for _, n := range negatives {
		if !n.Intersect(positive).IsEmpty() {
			return false
		}
	}
	return true
}

func everyNotSubset(negatives []semver.NpmRange, positive semver.NpmRange) bool {
	for _, n := range negatives {
		if subsetOf(positive, n) {
			return false
		}
	}
	return true
}

func (s *Solver) explainRecent() string {
	var b strings.Builder
	b.WriteString("Recent incompatibilities:\n")
	count := 0
	for i := len(s.incompat) - 1; i >= 0 && count < 20; i-- {
		b.WriteString("  - " + s.incompat[i].Cause + "\n")
		count++
	}
	return b.String()
}

// anyKey returns the lexicographically smallest key, making the
// propagation order deterministic (and thus the resolved lockfile
// reproducible) regardless of Go's randomized map iteration.
func anyKey(m map[string]bool) string {
	min := ""
	first := true
	for k := range m {
		if first || k < min {
			min, first = k, false
		}
	}
	return min
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func removeAssignment(list []*assignment, target *assignment) []*assignment {
	out := list[:0]
	for _, a := range list {
		if a != target {
			out = append(out, a)
		}
	}
	return out
}
