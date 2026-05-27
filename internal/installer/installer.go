package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/platform"
	"github.com/koji-1009/gnpm/internal/policy"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/regprovider"
	"github.com/koji-1009/gnpm/internal/resolver"
	"github.com/koji-1009/gnpm/internal/scripts"
	"github.com/koji-1009/gnpm/internal/semver"
	"github.com/koji-1009/gnpm/internal/signature"
	"github.com/koji-1009/gnpm/internal/store"
	"github.com/koji-1009/gnpm/internal/treeresolver"
	"github.com/koji-1009/gnpm/internal/workspacestate"
)

// Operation runs one install against a project root.
type Operation struct {
	ProjectRoot string
	Options     Options
	Log         *core.Logger
	Version     string // gnpm version, recorded in workspace state

	keyStore *signature.KeyStore // set when SignaturePolicy != none
	nodeVer  func() (semver.Version, bool)
	prof     *phaseTimer
}

// phaseTimer prints per-phase wall time to stderr when GNPM_PROFILE=1, so
// install hotspots are visible. Inert (near-zero cost) otherwise.
type phaseTimer struct {
	enabled bool
	last    time.Time
}

func newPhaseTimer() *phaseTimer {
	return &phaseTimer{enabled: os.Getenv("GNPM_PROFILE") == "1", last: time.Now()}
}

func (p *phaseTimer) mark(label string) {
	if p == nil || !p.enabled {
		return
	}
	now := time.Now()
	fmt.Fprintf(os.Stderr, "  PHASE %-24s %7.2f ms\n", label, float64(now.Sub(p.last))/1e6)
	p.last = now
}

// Run executes the install pipeline and returns a report.
func (op *Operation) Run(ctx context.Context) (Report, error) {
	op.prof = newPhaseTimer()
	pkg, err := project.ReadPackageJSON(filepath.Join(op.ProjectRoot, "package.json"))
	if err != nil {
		return Report{}, err
	}
	op.prof.mark("read package.json")

	mode := project.DetectMode(op.ProjectRoot)
	lockPath := filepath.Join(op.ProjectRoot, lockfile.ProjectLockfileName(mode))
	lockBytes, _ := os.ReadFile(lockPath) // nil on miss
	engineKey := op.engineKey(pkg)
	freshHash := workspacestate.ComputeHash(workspacestate.HashInput{
		Dependencies:         pkg.Dependencies,
		DevDependencies:      pkg.DevDependencies,
		OptionalDependencies: pkg.OptionalDependencies,
		PeerDependencies:     pkg.PeerDependencies,
		LockfileFingerprint:  fingerprint(lockBytes),
		EngineKey:            engineKey,
	})

	// Optimistic repeat install: skip everything when the fingerprint
	// matches the recorded state (suppressed under --frozen-lockfile).
	if op.Options.OptimisticRepeatInstall && !op.Options.FrozenLockfile {
		if rec, _ := workspacestate.Read(op.ProjectRoot); workspacestate.Matches(rec, freshHash, engineKey) {
			op.Log.Info("up to date (workspace state matches) — nothing to do")
			return Report{}, nil
		}
	}
	op.prof.mark("read lock + hash + state")

	// Past the no-op short-circuit: kick off node-version detection. The
	// result is cached by node-binary identity, so only the first install
	// on a host pays the ~40 ms fork+exec; later ones read the cache.
	op.nodeVer = startNodeDetect(ctx, filepath.Join(op.cacheRoot(), "node-version.json"))
	// Join that goroutine before returning on every path. It writes its
	// disk cache from the background, so letting it outlive Run risks a
	// write landing after the caller considers the install done (e.g. a
	// partial cache file, or — in tests — a write racing t.TempDir cleanup
	// and failing it with ENOTEMPTY). By the time Run returns, detection has
	// almost always finished, so this join is effectively free.
	defer func() {
		if op.nodeVer != nil {
			op.nodeVer()
		}
	}()

	cfg, err := npmrc.Loader{ProjectDir: op.ProjectRoot}.Load()
	if err != nil {
		return Report{}, err
	}
	if err := op.checkPackageManager(pkg, cfg); err != nil {
		return Report{}, err
	}
	st := store.New(op.storeRoot())
	if err := st.Initialize(); err != nil {
		return Report{}, err
	}
	cache := registry.NewCache(op.cacheRoot())
	if err := cache.Initialize(); err != nil {
		return Report{}, err
	}
	client := registry.NewClient(registry.Options{
		Config: cfg, Cache: cache, UserAgent: "gnpm/" + op.Version,
		Offline: op.Options.Offline, PreferOffline: op.Options.PreferOffline,
	})
	op.prof.mark("npmrc + store + cache init")
	if op.Options.SignaturePolicy != signature.PolicyNone {
		reg := cfg.Registry()
		token := ""
		if u, err := url.Parse(reg); err == nil {
			token = cfg.AuthTokenFor(u)
		}
		op.keyStore = signature.NewKeyStore(reg, nil, token)
	}

	var existing *lockfile.Lockfile
	if lockBytes != nil {
		existing, err = lockfile.Parse(lockBytes, mode, cfg.Registry())
		if err != nil {
			return Report{}, err
		}
	}
	// A locked install (ci / --frozen-lockfile) requires a lockfile.
	if op.Options.FrozenLockfile && existing == nil {
		return Report{}, core.Usage("--frozen-lockfile requires an existing %s", lockfile.ProjectLockfileName(mode))
	}

	// Root preinstall runs before any resolution.
	if op.scriptsEnabled() {
		if err := op.runRootScript(ctx, pkg, scripts.Preinstall); err != nil {
			return Report{}, err
		}
	}

	op.prof.mark("parse lockfile")

	// Locked fast path: lockfile fully consistent with package.json.
	if existing != nil && op.lockMatchesPackageJSON(pkg, existing, op.linkerKind(cfg) == linker.Hoisted) {
		report, err := op.runLocked(ctx, pkg, existing, st, client, cfg, mode)
		if err != nil {
			return Report{}, err
		}
		if err := op.finish(pkg, existing, engineKey, mode); err != nil {
			return Report{}, err
		}
		op.prof.mark("finish (lockfile + state)")
		return report, nil
	}

	// Full resolve.
	provider := regprovider.New(ctx, client, op.releaseAge(cfg), time.Time{})
	var trust *policy.TrustHistory
	if noDowngrade, ignoreAfter := op.trustConfig(cfg); noDowngrade {
		trust = policy.ReadTrustHistory(op.ProjectRoot)
		provider.SetFloors(trust.Floors(time.Now().UTC(), ignoreAfter))
	}
	members := project.ResolveWorkspaces(op.ProjectRoot, project.WorkspacePatterns(op.ProjectRoot, pkg, mode))
	declared, aliasByPackage, directExotic, err := op.declaredRegistryDeps(ctx, pkg, cfg, provider, members)
	if err != nil {
		return Report{}, err
	}
	// Overrides apply from package.json (npm `overrides` + pnpm `pnpm.overrides`)
	// and pnpm-workspace.yaml `overrides`; the workspace (monorepo-wide) wins.
	overrides, nestedOverrides := op.effectiveOverrides(pkg)

	var (
		linkSpecs      []linker.LinkSpec
		lockPackages   map[string]lockfile.LockedPackage
		warnings       []string
		fetchWarnings  []string
		isolatedExotic []directExoticInstall // direct exotic, materialized post-link (isolated only)
	)
	if op.linkerKind(cfg) == linker.Hoisted {
		// npm-style tree resolution: allows multiple versions of a package
		// (hoist the first, nest conflicts), so version-conflicting graphs
		// install rather than failing. Exotic edges (direct and transitive)
		// resolve through the injected ResolveExotic capability — git/https
		// specifiers join the resolver as edges alongside the registry deps.
		resolveExotic, exoticByTarball := op.exoticResolver(ctx, st, existing)
		resolverDeps := declared
		if len(directExotic) > 0 {
			resolverDeps = make(map[string]string, len(declared)+len(directExotic))
			for k, v := range declared {
				resolverDeps[k] = v
			}
			for k, v := range directExotic {
				resolverDeps[k] = v
			}
		}
		blockExotic := op.settingValue(cfg, "block-exotic-subdeps") == "true"

		// Pipeline cold installs (doc/pipelined-install.md): prefetch
		// packuments in the background and stream each finalized registry
		// package straight into a tarball-fetch pool via OnResolved, so
		// downloads + ingest overlap the metadata fetches instead of waiting
		// for them. The greedy tree resolver never revises a placement, so a
		// streamed version is final.
		warmupDone := make(chan struct{})
		go func() { provider.Warmup(declared); close(warmupDone) }()
		fetcher := op.newTarballFetcher(ctx, provider, st, client)

		placements, treeWarnings, rerr := treeresolver.Resolve(treeresolver.Request{
			Dependencies:       resolverDeps,
			Provider:           provider,
			Overrides:          overrides,
			NestedOverrides:    nestedOverrides,
			AutoInstallPeers:   true,
			BlockExoticSubdeps: blockExotic,
			TrustedExotic:      policy.IsTrustedExoticRepo,
			ResolveExotic:      resolveExotic,
			OnResolved:         fetcher.submit,
		})
		fetcher.closeAndWait() // drain downloads (also unblocks workers if Resolve errored)
		<-warmupDone           // join the background prefetch — no leaked goroutine
		// A fetch failure cancels the run and surfaces as the root cause;
		// prefer it over the resolver's resulting context error.
		if fetcher.err != nil {
			return Report{}, fetcher.err
		}
		if rerr != nil {
			return Report{}, rerr
		}
		warnings = append(warnings, treeWarnings...)
		warnings = append(warnings, fetcher.warnings...)
		op.prof.mark("resolve + fetch (pipelined)")
		op.updateTrust(trust, placementVersions(placements))
		if op.Options.FrozenLockfile && existing != nil {
			if err := verifyFrozenNames(placementNVSet(placements), existing, lockfile.ProjectLockfileName(mode)); err != nil {
				return Report{}, err
			}
		}
		// Assemble link specs + lockfile entries deterministically from the
		// fetched metadata (order-independent of download completion).
		regPlacements, exoticPlacements := splitExotic(placements)
		linkSpecs, lockPackages = assembleHoisted(regPlacements, fetcher.infos, aliasByPackage)
		// Exotic instances are materialized by the same hoisted linker: from
		// the store for https, by copying the clone dir for git.
		exLinks, exLocks, exErr := exoticLinkSpecs(exoticPlacements, exoticByTarball)
		if exErr != nil {
			return Report{}, exErr
		}
		linkSpecs = append(linkSpecs, exLinks...)
		for k, v := range exLocks {
			lockPackages[k] = v
		}
	} else {
		// Isolated (pnpm-style) layout uses the single-version pubgrub
		// solver and symlink farm. The solver has no exotic notion, so only
		// direct git/https deps are supported here: fetch them, fold their
		// own deps into the solve, and materialize them at top level after
		// linking. Transitive exotic deps are not resolved in this mode.
		for _, logical := range sortedKeys(directExotic) {
			res, detail, ferr := op.fetchExotic(ctx, st, directExotic[logical], existing)
			if ferr != nil {
				return Report{}, ferr
			}
			for k, v := range res.Deps {
				if _, ok := declared[k]; !ok {
					declared[k] = v
				}
			}
			isolatedExotic = append(isolatedExotic, directExoticInstall{logical: logical, detail: detail, tarball: res.Tarball})
		}
		provider.Warmup(declared)
		solver := resolver.NewSolver(resolver.Request{
			Dependencies:     declared,
			Provider:         provider,
			Overrides:        overrides,
			NestedOverrides:  nestedOverrides,
			Preferred:        op.preferred(existing),
			AutoInstallPeers: true,
		})
		solution, serr := solver.Solve()
		if serr != nil {
			return Report{}, serr
		}
		warnings = append(warnings, solver.Warnings...)
		op.prof.mark("resolve (pubgrub)")
		op.updateTrust(trust, solutionVersions(solution))
		if op.Options.FrozenLockfile && existing != nil {
			if err := verifyFrozen(solution, existing, lockfile.ProjectLockfileName(mode)); err != nil {
				return Report{}, err
			}
		}
		linkSpecs, lockPackages, fetchWarnings, err = op.fetchAndIngest(ctx, solution, provider, declared, aliasByPackage, st, client)
		if err != nil {
			return Report{}, err
		}
		op.prof.mark("fetch tarballs + ingest")
	}
	warnings = append(warnings, fetchWarnings...)

	linkWarnings, err := op.link(cfg, st, linkSpecs)
	if err != nil {
		return Report{}, err
	}
	warnings = append(warnings, linkWarnings...)
	op.prof.mark("link (materialize)")

	if err := op.applyLocalLinks(pkg); err != nil {
		return Report{}, err
	}
	// Isolated mode: materialize direct git/https packages as real
	// top-level directories now that their deps are linked.
	exoticLocked, err := op.materializeDirectExotic(st, isolatedExotic)
	if err != nil {
		return Report{}, err
	}
	for _, lp := range exoticLocked {
		lockPackages[lp.Path] = lp
	}
	if err := op.linkWorkspaces(members, linkSpecs); err != nil {
		return Report{}, err
	}
	if err := op.materializeConfigDeps(ctx, client, pkg); err != nil {
		return Report{}, err
	}
	if err := op.prune(linkSpecs, pkg); err != nil {
		return Report{}, err
	}

	if err := op.checkEngines(ctx, linkSpecs, &warnings); err != nil {
		return Report{}, err
	}

	if op.scriptsEnabled() {
		sw, err := op.runLifecycleScripts(ctx, pkg, cfg, linkSpecs, op.linkerKind(cfg))
		warnings = append(warnings, sw...)
		if err != nil {
			return Report{}, err
		}
	}

	lock := &lockfile.Lockfile{
		Version:   lockfile.SchemaVersion,
		Importers: map[string]lockfile.Importer{".": importerOf(pkg)},
		Packages:  lockPackages,
	}
	if err := op.finish(pkg, lock, engineKey, mode); err != nil {
		return Report{}, err
	}
	if err := op.postInstallAudit(ctx, cfg, lock, &warnings); err != nil {
		return Report{}, err
	}
	for _, w := range warnings {
		op.Log.Warn("%s", w)
	}
	return Report{Warnings: warnings, Added: len(linkSpecs)}, nil
}

// finish writes the lockfile, then records the workspace-state hash
// computed from the lockfile it just wrote. Hashing the final lockfile
// (rather than the pre-install one) lets the very next install
// short-circuit when nothing changed.
func (op *Operation) finish(pkg *project.PackageJSON, lock *lockfile.Lockfile, engineKey string, mode project.Mode) error {
	_, written, err := lockfile.Write(op.ProjectRoot, lock, pkg.Name, pkg.Version, mode)
	if err != nil {
		return err
	}
	hash := workspacestate.ComputeHash(workspacestate.HashInput{
		Dependencies:         pkg.Dependencies,
		DevDependencies:      pkg.DevDependencies,
		OptionalDependencies: pkg.OptionalDependencies,
		PeerDependencies:     pkg.PeerDependencies,
		LockfileFingerprint:  fingerprint(written),
		EngineKey:            engineKey,
	})
	if err := workspacestate.Write(op.ProjectRoot, workspacestate.State{
		Hash: hash, EngineKey: engineKey, InstalledAt: time.Now().UTC().Format(time.RFC3339), GnpmVersion: op.Version,
	}); err != nil {
		op.Log.Warn("could not write workspace state: %v", err)
	}
	return nil
}

// declaredRegistryDeps collects the root's registry dependencies as
// (packageName → range), resolving npm: aliases, dist-tags, and catalog
// references, and applying catalogMode prefer/strict. It also returns the
// direct git/https specifiers (the third result) for the tree resolver's
// exotic path. file/link specifiers are handled by applyLocalLinks;
// workspace-internal deps are linked, not resolved here.
func (op *Operation) declaredRegistryDeps(ctx context.Context, pkg *project.PackageJSON, cfg *npmrc.Config, provider *regprovider.Provider, members []project.Workspace) (map[string]string, map[string]string, map[string]string, error) {
	declared := map[string]string{}
	aliasByPackage := map[string]string{}
	directExotic := map[string]string{}
	merged := map[string]string{}
	addAll := func(m map[string]string) {
		for k, v := range m {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
	}
	addAll(pkg.Dependencies)
	if !op.Options.Production {
		addAll(pkg.DevDependencies)
	}
	addAll(pkg.OptionalDependencies)
	// Union every workspace member's declared dependencies so the root
	// install resolves the whole monorepo's needs into one hoisted tree.
	workspaceNames := map[string]bool{}
	for _, m := range members {
		workspaceNames[m.Name] = true
	}
	for _, m := range members {
		addAll(m.PackageJSON.Dependencies)
		if !op.Options.Production {
			addAll(m.PackageJSON.DevDependencies)
		}
		addAll(m.PackageJSON.OptionalDependencies)
	}
	ws := project.ReadPnpmWorkspace(op.ProjectRoot)
	catalogMode := policy.ParseCatalogMode(op.setting(cfg, ws, "catalog-mode"))
	for logical, raw := range merged {
		spec := project.ParseSpec(logical, raw)
		// Workspace-internal deps are linked, not resolved from the registry.
		if spec.Protocol == project.ProtoWorkspace || workspaceNames[spec.PackageName] {
			continue
		}
		if spec.Protocol == project.ProtoCatalog {
			resolved, ok := policy.ResolveCatalog(ws, spec.PackageName, spec.Range)
			if !ok {
				op.Log.Warn("skipping %s: no catalog entry for %s", logical, spec.Range)
				continue
			}
			declared[spec.PackageName] = resolved
			continue
		}
		switch spec.Protocol {
		case project.ProtoFile, project.ProtoLink:
			continue // symlinked by applyLocalLinks
		case project.ProtoHTTPS, project.ProtoGit:
			// Direct git/https deps resolve through the tree resolver's
			// injected exotic capability (with their own transitive deps).
			directExotic[logical] = raw
			continue
		}
		if spec.Protocol != project.ProtoSemver {
			op.Log.Warn("skipping %s: %s specifiers are not yet supported", logical, protocolName(spec.Protocol))
			continue
		}
		// catalogMode prefer/strict applies the default catalog's range to
		// a non-catalog declaration.
		if catalogMode != policy.CatalogManual {
			if catRange, ok := policy.ResolveCatalog(ws, spec.PackageName, ""); ok {
				switch catalogMode {
				case policy.CatalogPrefer:
					spec.Range = catRange
				case policy.CatalogStrict:
					if spec.Range != catRange {
						return nil, nil, nil, core.Usage("catalogMode=strict: %s declares %q but the catalog pins %q", logical, spec.Range, catRange)
					}
				}
			}
		}
		rng := op.resolveDistTag(provider, spec.PackageName, spec.Range)
		declared[spec.PackageName] = rng
		if spec.IsAlias() {
			aliasByPackage[spec.PackageName] = spec.LogicalName
		}
	}
	return declared, aliasByPackage, directExotic, nil
}

// setting resolves a kebab-case policy setting from .npmrc, falling back
// to pnpm-workspace.yaml settings in pnpm mode.
func (op *Operation) setting(cfg *npmrc.Config, ws *project.PnpmWorkspace, key string) string {
	if v, ok := cfg.Get(key); ok {
		return v
	}
	if v, ok := ws.Settings[key]; ok {
		return v
	}
	return ""
}

// settingValue is setting without a pre-read workspace; it reads
// pnpm-workspace.yaml itself when the key is absent from .npmrc.
func (op *Operation) settingValue(cfg *npmrc.Config, key string) string {
	if v, ok := cfg.Get(key); ok {
		return v
	}
	if v, ok := project.ReadPnpmWorkspace(op.ProjectRoot).Settings[key]; ok {
		return v
	}
	return ""
}

func (op *Operation) resolveDistTag(provider *regprovider.Provider, pkg, rng string) string {
	trimmed := strings.TrimSpace(rng)
	if trimmed == "" {
		return "*"
	}
	if !isDistTag(trimmed) {
		return trimmed
	}
	if resolved, err := provider.ResolveDistTag(pkg, trimmed); err == nil && resolved != "" {
		return resolved
	}
	return trimmed
}

// fetchAndIngest downloads, verifies, and ingests every resolved package
// (platform-incompatible variants skipped), returning link specs and
// lockfile entries.
func (op *Operation) fetchAndIngest(
	ctx context.Context,
	solution resolver.Result,
	provider *regprovider.Provider,
	declared, aliasByPackage map[string]string,
	st *store.Store,
	client *registry.Client,
) ([]linker.LinkSpec, map[string]lockfile.LockedPackage, []string, error) {
	type entry struct {
		name    string
		version semver.Version
	}
	var entries []entry
	for name, v := range solution.Assignments {
		entries = append(entries, entry{name, v})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var mu sync.Mutex
	var linkSpecs []linker.LinkSpec
	lockPackages := map[string]lockfile.LockedPackage{}
	var warnings []string

	err := core.ForEachLimited(entries, core.HTTPConcurrency, func(e entry) error {
		slice, err := provider.SliceOf(e.name, e.version)
		if err != nil {
			return err
		}
		if slice == nil || slice.Tarball == "" || slice.Integrity == "" {
			return core.NetworkError("no tarball/integrity for %s@%s", e.name, e.version)
		}
		// Record the lockfile entry for every resolved package (keeps the
		// lockfile cross-platform); download + link only this platform's.
		locked := lockfile.LockedPackage{
			Name:                 e.name,
			Version:              e.version.String(),
			Tarball:              slice.Tarball,
			Integrity:            slice.Integrity,
			Dependencies:         slice.Dependencies,
			OptionalDependencies: slice.OptionalDependencies,
			PeerDependencies:     slice.PeerDependencies,
			OS:                   slice.OS,
			CPU:                  slice.CPU,
			HasBin:               slice.HasBin,
			HasInstallScript:     slice.HasInstallScript,
			Bin:                  slice.Bin,
			Scripts:              slice.Scripts,
			Engines:              slice.Engines,
			Signatures:           lockSignatures(slice.Signatures),
		}
		mu.Lock()
		lockPackages[e.name+"@"+e.version.String()] = locked
		mu.Unlock()
		if !platformMatches(slice) {
			return nil // recorded above; not downloaded or linked on this platform
		}
		bytes, err := client.Tarball(ctx, slice.Tarball, slice.Integrity)
		if err != nil {
			return err
		}
		if w, err := op.verifySignature(ctx, e.name, e.version.String(), slice.Integrity, toSigs(slice.Signatures)); err != nil {
			return err
		} else if w != "" {
			mu.Lock()
			warnings = append(warnings, w)
			mu.Unlock()
		}
		if _, err := st.IngestTarball(bytes, slice.Integrity); err != nil {
			return err
		}
		// Resolve this package's dependency edges to concrete versions.
		bundled := map[string]bool{}
		for _, b := range slice.BundledDeps {
			bundled[b] = true
		}
		depVersions := map[string]string{}
		for dep := range slice.Dependencies {
			if bundled[dep] {
				continue
			}
			if v, ok := solution.Assignments[dep]; ok {
				depVersions[dep] = v.String()
			}
		}
		spec := linker.LinkSpec{
			Name:         e.name,
			Version:      e.version.String(),
			Integrity:    slice.Integrity,
			Dependencies: depVersions,
			Bin:          slice.Bin,
			IsDirect:     declared[e.name] != "",
			LinkAlias:    aliasByPackage[e.name],
			Scripts:      slice.Scripts,
			Engines:      slice.Engines,
		}
		mu.Lock()
		linkSpecs = append(linkSpecs, spec)
		mu.Unlock()
		return nil
	})
	return linkSpecs, lockPackages, warnings, err
}

func (op *Operation) link(cfg *npmrc.Config, st *store.Store, specs []linker.LinkSpec) ([]string, error) {
	if op.linkerKind(cfg) == linker.Isolated {
		return nil, linker.IsolatedLinker{Store: st}.Link(op.ProjectRoot, specs)
	}
	return linker.HoistedLinker{Store: st}.Link(op.ProjectRoot, specs)
}

func (op *Operation) linkerKind(cfg *npmrc.Config) linker.Kind {
	return linker.ParseKind(cfg.GetOr("node-linker", "hoisted"))
}

// --- helpers ----------------------------------------------------------

func (op *Operation) scriptsEnabled() bool {
	return op.EffectiveScriptPolicyOK()
}

func (op *Operation) EffectiveScriptPolicyOK() bool {
	return op.Options.EffectiveScriptPolicy() != ScriptNone
}

// effectiveOverrides merges dependency overrides from package.json (npm
// `overrides` and pnpm `pnpm.overrides`, already folded into pkg) with
// pnpm-workspace.yaml `overrides`. The workspace is monorepo-wide policy, so
// it wins on conflicts.
func (op *Operation) effectiveOverrides(pkg *project.PackageJSON) (map[string]string, map[string]map[string]string) {
	ws := project.ReadPnpmWorkspace(op.ProjectRoot)
	return project.MergeOverrides(pkg.Overrides, ws.Overrides),
		project.MergeNestedOverrides(pkg.NestedOverrides, ws.NestedOverrides)
}

func (op *Operation) releaseAge(cfg *npmrc.Config) regprovider.ReleaseAge {
	min := op.Options.MinReleaseAge
	if min < 0 { // unset by flag → .npmrc, else the project mode's default
		switch m := cfg.Int("minimum-release-age", -1); {
		case m >= 0:
			min = time.Duration(m) * time.Minute
		case project.DetectMode(op.ProjectRoot) == project.ModePnpm:
			// pnpm enables a one-day minimum-release-age gate by default;
			// match it in pnpm mode so gnpm is no less safe out of the box.
			// npm applies no such gate, so npm mode stays 0.
			min = PnpmDefaultMinReleaseAge
		default:
			min = 0
		}
	}
	exclude := splitCSV(cfg.GetOr("minimum-release-age-exclude", ""))
	return regprovider.ReleaseAge{
		Minimum:           min,
		Strict:            cfg.Bool("minimum-release-age-strict", false),
		IgnoreMissingTime: cfg.Bool("minimum-release-age-ignore-missing-time", true),
		Exclude:           exclude,
	}
}

func (op *Operation) engineKey(pkg *project.PackageJSON) string {
	major := ""
	if rt := pkg.DevEnginesRuntime; rt != nil && rt.Name == "node" {
		major = workspacestate.MajorString(rt.Version)
	}
	return workspacestate.EngineKey(major)
}

func (op *Operation) storeRoot() string {
	if op.Options.StoreRoot != "" {
		return op.Options.StoreRoot
	}
	return filepath.Join(homeDir(), ".gnpm", "store")
}

func (op *Operation) cacheRoot() string {
	if op.Options.CacheRoot != "" {
		return op.Options.CacheRoot
	}
	return filepath.Join(homeDir(), ".gnpm", "cache")
}

func (op *Operation) checkEngines(ctx context.Context, specs []linker.LinkSpec, warnings *[]string) error {
	// Skip the (already-running) node probe entirely when nothing pins
	// engines.node — most warm relinks of small trees hit this.
	hasReq := false
	for _, spec := range specs {
		if spec.Engines["node"] != "" {
			hasReq = true
			break
		}
	}
	if !hasReq {
		return nil
	}
	nodeVer, ok := op.nodeVer()
	if !ok {
		return nil
	}
	for _, spec := range specs {
		req := spec.Engines["node"]
		if req == "" {
			continue
		}
		rng, err := semver.ParseRange(req)
		if err != nil || rng.Satisfies(nodeVer) {
			continue
		}
		msg := spec.ID() + " requires node " + req + ", running node " + nodeVer.String()
		if op.Options.EngineStrict {
			return core.Usage("%s", msg)
		}
		*warnings = append(*warnings, msg)
	}
	return nil
}

func detectNodeVersion(ctx context.Context) (semver.Version, bool) {
	out, err := exec.CommandContext(ctx, "node", "--version").Output()
	if err != nil {
		return semver.Version{}, false
	}
	s := strings.TrimSpace(string(out))
	s = strings.TrimPrefix(s, "v")
	v, ok := semver.TryParse(s)
	return v, ok
}

// startNodeDetect resolves the node version on a background goroutine and
// returns a memoized getter that blocks only on first use. The value is
// cached on disk by node-binary identity (path + mtime + size), so the
// ~40 ms fork+exec is paid once per host rather than on every install.
func startNodeDetect(ctx context.Context, cacheFile string) func() (semver.Version, bool) {
	type result struct {
		v  semver.Version
		ok bool
	}
	ch := make(chan result, 1)
	go func() {
		v, ok := cachedNodeVersion(ctx, cacheFile)
		ch <- result{v, ok}
	}()
	var once sync.Once
	var r result
	return func() (semver.Version, bool) {
		once.Do(func() { r = <-ch })
		return r.v, r.ok
	}
}

type nodeVerCache struct {
	Path    string `json:"path"`
	ModNano int64  `json:"modNano"`
	Size    int64  `json:"size"`
	Version string `json:"version"`
}

// cachedNodeVersion returns the host node version, reading a disk cache
// keyed by the node binary's path/mtime/size and only execing `node
// --version` on a cache miss.
func cachedNodeVersion(ctx context.Context, cacheFile string) (semver.Version, bool) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return semver.Version{}, false
	}
	info, err := os.Stat(nodePath)
	if err != nil {
		return semver.Version{}, false
	}
	if data, err := os.ReadFile(cacheFile); err == nil {
		var c nodeVerCache
		if json.Unmarshal(data, &c) == nil &&
			c.Path == nodePath && c.ModNano == info.ModTime().UnixNano() && c.Size == info.Size() {
			if v, ok := semver.TryParse(c.Version); ok {
				return v, true
			}
		}
	}
	v, ok := detectNodeVersion(ctx)
	if !ok {
		return v, false
	}
	if data, err := json.Marshal(nodeVerCache{Path: nodePath, ModNano: info.ModTime().UnixNano(), Size: info.Size(), Version: v.String()}); err == nil {
		_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
		_ = os.WriteFile(cacheFile, data, 0o644)
	}
	return v, true
}

func importerOf(pkg *project.PackageJSON) lockfile.Importer {
	return lockfile.Importer{
		Dependencies:         pkg.Dependencies,
		DevDependencies:      pkg.DevDependencies,
		OptionalDependencies: pkg.OptionalDependencies,
		PeerDependencies:     pkg.PeerDependencies,
	}
}

// preferred seeds the resolver with the lockfile's versions unless this
// is an update (which bumps to the highest in-range version).
func (op *Operation) preferred(lock *lockfile.Lockfile) map[string]semver.Version {
	if op.Options.Update {
		return map[string]semver.Version{}
	}
	return preferredFromLock(lock)
}

func preferredFromLock(lock *lockfile.Lockfile) map[string]semver.Version {
	out := map[string]semver.Version{}
	if lock == nil {
		return out
	}
	for _, p := range lock.Packages {
		if v, ok := semver.TryParse(p.Version); ok {
			out[p.Name] = v
		}
	}
	return out
}

func verifyFrozen(sol resolver.Result, lock *lockfile.Lockfile, name string) error {
	set := map[string]bool{}
	for n, v := range sol.Assignments {
		set[n+"@"+v.String()] = true
	}
	return verifyFrozenNames(set, lock, name)
}

// verifyFrozenNames fails when resolution introduces a name@version not
// present in the lockfile.
func verifyFrozenNames(resolved map[string]bool, lock *lockfile.Lockfile, name string) error {
	have := map[string]bool{}
	for _, p := range lock.Packages {
		have[p.Name+"@"+p.Version] = true
	}
	var missing []string
	for nv := range resolved {
		if !have[nv] {
			missing = append(missing, nv)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return core.Usage("--frozen-lockfile: resolution diverges from %s (%d new entries: %s)", name, len(missing), strings.Join(missing, ", "))
	}
	return nil
}

// updateTrust raises and persists the trust history with resolved
// versions when trustPolicy is active.
func (op *Operation) updateTrust(trust *policy.TrustHistory, resolved map[string]string) {
	if trust == nil {
		return
	}
	trust.Update(resolved, time.Now().UTC())
	if err := trust.Write(op.ProjectRoot); err != nil {
		op.Log.Warn("could not write trust history: %v", err)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// placementVersion is the version string a placement records in the
// lockfile: the raw label for exotic instances (which may not be semver),
// the parsed semver otherwise.
func placementVersion(p treeresolver.Placement) string {
	if p.Exotic {
		return p.VersionLabel
	}
	return p.Version.String()
}

// placementVersions collects registry version floors for trustPolicy.
// Exotic instances are excluded: they are pinned to a commit/URL, not a
// registry version, so they carry no meaningful downgrade floor.
func placementVersions(ps []treeresolver.Placement) map[string]string {
	out := map[string]string{}
	for _, p := range ps {
		if p.Exotic {
			continue
		}
		if cur, ok := out[p.Name]; !ok || cur < p.Version.String() {
			out[p.Name] = p.Version.String()
		}
	}
	return out
}

func placementNVSet(ps []treeresolver.Placement) map[string]bool {
	out := map[string]bool{}
	for _, p := range ps {
		out[p.Name+"@"+placementVersion(p)] = true
	}
	return out
}

func solutionVersions(sol resolver.Result) map[string]string {
	out := map[string]string{}
	for n, v := range sol.Assignments {
		out[n] = v.String()
	}
	return out
}

func platformMatches(slice *registry.PackumentVersion) bool {
	return platform.MatchList(slice.OS, platform.OS()) &&
		platform.MatchList(slice.CPU, platform.CPU()) &&
		(len(slice.Libc) == 0 || platform.MatchList(slice.Libc, platform.Libc()))
}

// lockEntryMatchesPlatform reports whether a lockfile package targets this
// host (os/cpu). A cross-platform lockfile records foreign-platform
// optional deps; a locked install must skip materializing the ones that
// are not for this platform.
func lockEntryMatchesPlatform(p lockfile.LockedPackage) bool {
	return platform.MatchList(p.OS, platform.OS()) && platform.MatchList(p.CPU, platform.CPU())
}

func lockSignatures(sigs []registry.DistSignature) []lockfile.LockedSignature {
	if len(sigs) == 0 {
		return nil
	}
	out := make([]lockfile.LockedSignature, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, lockfile.LockedSignature{KeyID: s.KeyID, Sig: s.Sig})
	}
	return out
}

func isDistTag(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isAlpha(c) || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func protocolName(p project.Protocol) string {
	switch p {
	case project.ProtoWorkspace:
		return "workspace:"
	case project.ProtoFile:
		return "file:"
	case project.ProtoLink:
		return "link:"
	case project.ProtoHTTPS:
		return "https"
	case project.ProtoGit:
		return "git"
	case project.ProtoCatalog:
		return "catalog:"
	default:
		return "semver"
	}
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func fingerprint(b []byte) string {
	if b == nil {
		return workspacestate.LockfileFingerprintAbsent
	}
	return workspacestate.LockfileFingerprintBytes(b)
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "."
}
