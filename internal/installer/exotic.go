package installer

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/linker"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/platform"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/store"
	"github.com/koji-1009/gnpm/internal/treeresolver"
)

// exoticResolved is the materialization detail the installer keeps for one
// resolved git/https package, keyed by its pinned source. The tree
// resolver only learns the package's identity and dependency edges
// (treeresolver.ExoticResolution); the store key / clone directory stay
// here, on the I/O side of the boundary.
type exoticResolved struct {
	integrity string            // https: store key (empty for git)
	copyFrom  string            // git: clone dir to copy from (empty for https)
	version   string            // raw, possibly non-semver
	deps      map[string]string // declared runtime deps (for the lockfile)
	optDeps   map[string]string // declared optionalDependencies
	bin       map[string]string
}

// exoticResolver returns the capability injected into the tree resolver to
// resolve git/https specifiers (direct or transitive), and the map it
// populates with materialization detail keyed by pinned source. The
// resolver calls the closure for each exotic edge and recurses into the
// returned dependencies, so transitive exotic deps resolve through the
// same boundary as registry deps.
func (op *Operation) exoticResolver(ctx context.Context, st *store.Store, existing *lockfile.Lockfile) (func(string) (treeresolver.ExoticResolution, error), map[string]exoticResolved) {
	byTarball := map[string]exoticResolved{}
	resolve := func(spec string) (treeresolver.ExoticResolution, error) {
		res, detail, err := op.fetchExotic(ctx, st, spec, existing)
		if err != nil {
			return treeresolver.ExoticResolution{}, err
		}
		byTarball[res.Tarball] = detail
		return res, nil
	}
	return resolve, byTarball
}

// fetchExotic fetches one git/https specifier, reads its package.json, and
// returns both the resolver-facing identity and the installer-side
// materialization detail.
func (op *Operation) fetchExotic(ctx context.Context, st *store.Store, spec string, existing *lockfile.Lockfile) (treeresolver.ExoticResolution, exoticResolved, error) {
	p := project.ParseSpec("", spec)
	switch p.Protocol {
	case project.ProtoHTTPS:
		data, err := httpGet(ctx, p.URL)
		if err != nil {
			return treeresolver.ExoticResolution{}, exoticResolved{}, err
		}
		sum := sha512.Sum512(data)
		integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
		if want, ok := findHTTPSLock(existing, p.URL); ok && want != integrity {
			return treeresolver.ExoticResolution{}, exoticResolved{}, core.IntegrityError("https dependency %s changed: lockfile pins %s but the download is %s", p.URL, want, integrity)
		}
		if _, err := st.IngestTarball(data, integrity); err != nil {
			return treeresolver.ExoticResolution{}, exoticResolved{}, core.IOError("ingesting %s", p.URL).Wrap(err)
		}
		version, deps, optDeps, bin := storePackageMeta(st, integrity)
		return treeresolver.ExoticResolution{Version: version, Deps: deps, OptionalDeps: optDeps, Tarball: p.URL},
			exoticResolved{integrity: integrity, version: version, deps: deps, optDeps: optDeps, bin: bin}, nil
	case project.ProtoGit:
		cloneURL, ref := parseGitURL(p.URL)
		checkout := ref
		if commit, ok := findGitLock(existing, cloneURL); ok {
			checkout = commit
		}
		key := sha256Hex(cloneURL + "#" + checkout)[:16]
		dir := filepath.Join(homeDir(), ".gnpm", "git", key)
		if _, err := os.Stat(dir); err != nil {
			if err := gitClone(ctx, cloneURL, checkout, dir); err != nil {
				return treeresolver.ExoticResolution{}, exoticResolved{}, err
			}
		}
		commit, err := gitHead(ctx, dir)
		if err != nil {
			return treeresolver.ExoticResolution{}, exoticResolved{}, err
		}
		version, deps, optDeps, bin := dirPackageMeta(dir)
		tarball := "git+" + cloneURL + "#" + commit
		return treeresolver.ExoticResolution{Version: version, Deps: deps, OptionalDeps: optDeps, Tarball: tarball},
			exoticResolved{copyFrom: dir, version: version, deps: deps, optDeps: optDeps, bin: bin}, nil
	default:
		return treeresolver.ExoticResolution{}, exoticResolved{}, core.Usage("unsupported exotic specifier %q", spec)
	}
}

// splitExotic partitions placements into registry-sourced and
// exotic-sourced sets.
func splitExotic(placements []treeresolver.Placement) (reg, exotic []treeresolver.Placement) {
	for _, p := range placements {
		if p.Exotic {
			exotic = append(exotic, p)
		} else {
			reg = append(reg, p)
		}
	}
	return reg, exotic
}

// exoticLinkSpecs turns exotic placements into link specs (materialized by
// the hoisted linker: from the store for https, by copying the clone dir
// for git) and lockfile entries, looking up each placement's
// materialization detail by its pinned source. Every exotic placement was
// produced by a ResolveExotic call that recorded its detail in byTarball;
// a missing entry is an internal inconsistency, so we fail loudly rather
// than materialize an empty directory.
func exoticLinkSpecs(placements []treeresolver.Placement, byTarball map[string]exoticResolved, versionAtPath map[string]string) ([]linker.LinkSpec, map[string]lockfile.LockedPackage, error) {
	var specs []linker.LinkSpec
	locks := map[string]lockfile.LockedPackage{}
	for _, p := range placements {
		r, ok := byTarball[p.Tarball]
		if !ok || (r.integrity == "" && r.copyFrom == "") {
			return nil, nil, core.IOError("no materialization source for exotic dependency %s (%s)", p.Name, p.Tarball)
		}
		// Resolve the exotic package's own dependency edges to concrete
		// versions so the isolated linker can wire them into its private
		// node_modules (the hoisted linker ignores these and uses the tree).
		edges := map[string]string{}
		for d := range r.deps {
			if v, ok := resolveEdgeVersion(p.Path, d, versionAtPath); ok {
				edges[d] = v
			}
		}
		for d := range r.optDeps {
			if v, ok := resolveEdgeVersion(p.Path, d, versionAtPath); ok {
				edges[d] = v
			}
		}
		specs = append(specs, linker.LinkSpec{
			Name: p.Name, Version: p.VersionLabel, Path: p.Path, IsDirect: p.IsDirect,
			Integrity: r.integrity, CopyFrom: r.copyFrom, Bin: r.bin, Dependencies: edges,
		})
		locks[p.Path] = lockfile.LockedPackage{
			Name: p.Name, Version: p.VersionLabel, Path: p.Path,
			Tarball: p.Tarball, Integrity: r.integrity,
			Dependencies: r.deps, OptionalDependencies: r.optDeps,
			Bin: r.bin, HasBin: len(r.bin) > 0,
		}
	}
	return specs, locks, nil
}

// directExoticInstall is a direct git/https package the isolated path
// materializes at top level after linking (the pubgrub solver has no
// exotic notion, so these are handled outside it).
type directExoticInstall struct {
	logical string
	detail  exoticResolved
	tarball string
}

// materializeDirectExotic installs each direct exotic package as a real
// directory under node_modules/<logical> and returns its lockfile entry.
func (op *Operation) materializeDirectExotic(st *store.Store, pkgs []directExoticInstall) (map[string]lockfile.LockedPackage, error) {
	locked := map[string]lockfile.LockedPackage{}
	nm := filepath.Join(op.ProjectRoot, "node_modules")
	for _, ex := range pkgs {
		dest := filepath.Join(nm, filepath.FromSlash(ex.logical))
		_ = os.RemoveAll(dest)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, core.IOError("mkdir for %s", ex.logical).Wrap(err)
		}
		if ex.detail.integrity != "" {
			if err := st.Materialize(ex.detail.integrity, dest); err != nil {
				return nil, err
			}
		} else if err := platform.CopyTreeExcluding(ex.detail.copyFrom, dest, ".git"); err != nil {
			return nil, core.IOError("copying git dependency %s", ex.logical).Wrap(err)
		}
		// Write the package's bin shims into the top-level .bin (the
		// isolated linker only shims the packages it placed itself).
		if err := linker.WriteBins(linker.TopLevelBinDir(op.ProjectRoot), dest, ex.detail.bin); err != nil {
			return nil, err
		}
		locked[ex.logical] = lockfile.LockedPackage{
			Name: ex.logical, Version: ex.detail.version, Path: ex.logical,
			Tarball: ex.tarball, Integrity: ex.detail.integrity,
			Dependencies: ex.detail.deps, OptionalDependencies: ex.detail.optDeps,
			Bin: ex.detail.bin, HasBin: len(ex.detail.bin) > 0,
		}
	}
	return locked, nil
}

// storePackageMeta reads version + dependencies + optionalDependencies +
// bin from an ingested tarball's package.json in the store.
func storePackageMeta(st *store.Store, integrity string) (version string, deps, optDeps, bin map[string]string) {
	m, err := st.ReadManifest(integrity)
	if err != nil || m == nil {
		return "0.0.0", nil, nil, nil
	}
	for _, f := range m.Files {
		if f.RelPath == "package.json" {
			data, err := os.ReadFile(st.Layout.FilePath(f.SHA512Hex))
			if err != nil {
				return "0.0.0", nil, nil, nil
			}
			return parsePkgMeta(data)
		}
	}
	return "0.0.0", nil, nil, nil
}

func dirPackageMeta(dir string) (version string, deps, optDeps, bin map[string]string) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "0.0.0", nil, nil, nil
	}
	return parsePkgMeta(data)
}

// parsePkgMeta extracts version, runtime dependencies, optionalDependencies
// (kept separate so the resolver can skip an unresolvable optional), and
// bin from a package.json body. The package's own name field is
// intentionally ignored: a git/https dependency installs under the key the
// requirer used, not its manifest name.
func parsePkgMeta(data []byte) (version string, deps, optDeps, bin map[string]string) {
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return "0.0.0", nil, nil, nil
	}
	p := project.FromMap(raw)
	version = p.Version
	if version == "" {
		version = "0.0.0"
	}
	return version, p.Dependencies, p.OptionalDependencies, p.Bin
}
