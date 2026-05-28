package lockfile

import (
	"sort"
	"strings"
)

// PnpmToLockfile converts a parsed pnpm-lock.yaml into the internal
// model. Registry packages store only integrity in pnpm; their tarball
// URL is reconstructed from registry + name + version. gnpm's hoisted
// linker dedupes to one version per name, so the peer context that
// distinguishes pnpm snapshots collapses and the conversion is lossless
// for install purposes.
func PnpmToLockfile(p *PnpmLockfile, registry string) *Lockfile {
	// Every importer (the root "." and each workspace member, keyed by its
	// path) is carried through so a monorepo's shared lockfile round-trips.
	importers := map[string]Importer{}
	linkVersions := map[string]map[string]string{}
	for path, imp := range p.Importers {
		importers[path] = Importer{
			Dependencies:         specifierMap(imp.Dependencies),
			DevDependencies:      specifierMap(imp.DevDependencies),
			OptionalDependencies: specifierMap(imp.OptionalDependencies),
			PeerDependencies:     specifierMap(imp.PeerDependencies),
		}
		capture := func(deps map[string]PnpmDirectDep) {
			for name, d := range deps {
				if strings.HasPrefix(d.Version, "link:") || strings.HasPrefix(d.Version, "file:") {
					if linkVersions[path] == nil {
						linkVersions[path] = map[string]string{}
					}
					linkVersions[path][name] = d.Version
				}
			}
		}
		capture(imp.Dependencies)
		capture(imp.DevDependencies)
		capture(imp.OptionalDependencies)
	}
	if len(importers) == 0 {
		importers["."] = Importer{}
	}

	// Index snapshots by base name@version, dropping the peer suffix; the
	// bare key sorts before any "(peer)" variant and wins.
	snapshotByBase := map[string]PnpmSnapshotEntry{}
	keys := make([]string, 0, len(p.Snapshots))
	for k := range p.Snapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		base := stripPeerSuffix(k)
		if _, ok := snapshotByBase[base]; !ok {
			snapshotByBase[base] = p.Snapshots[k]
		}
	}

	packages := map[string]LockedPackage{}
	for key, entry := range p.Packages {
		name, version, ok := parsePnpmID(key)
		if !ok {
			continue
		}
		snap := snapshotByBase[stripPeerSuffix(key)]
		integrity := str(entry.Resolution["integrity"])
		tarball := str(entry.Resolution["tarball"])
		if tarball == "" {
			tarball = registryTarballURL(registry, name, version)
		}
		meta := map[string]PeerDependencyMeta{}
		for k, m := range entry.PeerDependenciesMeta {
			meta[k] = PeerDependencyMeta{Optional: m["optional"] == true}
		}
		packages[name+"@"+version] = LockedPackage{
			Name:                 name,
			Version:              version,
			Tarball:              tarball,
			Integrity:            integrity,
			Dependencies:         snap.Dependencies,
			OptionalDependencies: snap.OptionalDependencies,
			PeerDependencies:     entry.PeerDependencies,
			PeerDependenciesMeta: meta,
			OS:                   entry.OS,
			CPU:                  entry.CPU,
			HasBin:               entry.HasBin,
			Engines:              entry.Engines,
			Signatures:           signaturesFromPnpm(entry.Signatures),
		}
	}
	return &Lockfile{
		Version:      SchemaVersion,
		Importers:    importers,
		Packages:     packages,
		Pnpm:         pnpmPassthrough(p),
		LinkVersions: linkVersions,
	}
}

// pnpmPassthrough captures the pnpm-lock.yaml content the internal model does
// not represent so LockfileToPnpm can re-emit it. Snapshot-preserved fields are
// keyed by base "name@version" to match the flat keys the writer regenerates.
func pnpmPassthrough(p *PnpmLockfile) *PnpmPassthrough {
	pt := &PnpmPassthrough{
		LockfileVersion:   p.LockfileVersion,
		Settings:          p.Settings,
		TopLevel:          p.PreservedTopLevel,
		PackagePreserved:  map[string]map[string]any{},
		SnapshotPreserved: map[string]map[string]any{},
		ImporterPreserved: map[string]map[string]any{},
	}
	for key, e := range p.Packages {
		if len(e.Preserved) > 0 {
			pt.PackagePreserved[key] = e.Preserved
		}
	}
	for key, e := range p.Snapshots {
		if len(e.Preserved) > 0 {
			pt.SnapshotPreserved[stripPeerSuffix(key)] = e.Preserved
		}
	}
	for path, i := range p.Importers {
		if len(i.Preserved) > 0 {
			pt.ImporterPreserved[path] = i.Preserved
		}
	}
	return pt
}

// LockfileToPnpm converts the internal model into a pnpm-lock.yaml v9
// model. The internal model stores ranges; pnpm snapshots store resolved
// versions, looked up via the lockfile-wide name→version map. Edges to
// names absent from the lockfile (platform-skipped optionals, unresolved
// peers) are dropped, matching pnpm's "materialized edges only" rule.
func LockfileToPnpm(lock *Lockfile) *PnpmLockfile {
	versionByName := map[string]string{}
	byName := map[string]LockedPackage{}
	for _, p := range lock.Packages {
		versionByName[p.Name] = p.Version
		byName[p.Name] = p
	}
	suffix := peerSuffixes(byName)

	importers := map[string]PnpmImporter{}
	for path, imp := range lock.Importers {
		links := lock.LinkVersions[path]
		importers[path] = PnpmImporter{
			Dependencies:         directDeps(imp.Dependencies, versionByName, suffix, links),
			DevDependencies:      directDeps(imp.DevDependencies, versionByName, suffix, links),
			OptionalDependencies: directDeps(imp.OptionalDependencies, versionByName, suffix, links),
			PeerDependencies:     directDeps(imp.PeerDependencies, versionByName, suffix, links),
		}
	}
	if len(importers) == 0 {
		importers["."] = PnpmImporter{}
	}

	packages := map[string]PnpmPackageEntry{}
	snapshots := map[string]PnpmSnapshotEntry{}
	for _, p := range lock.Packages {
		key := p.Name + "@" + p.Version
		resolution := map[string]any{}
		if p.Integrity != "" {
			resolution["integrity"] = p.Integrity
		}
		meta := map[string]map[string]any{}
		for k, m := range p.PeerDependenciesMeta {
			meta[k] = map[string]any{"optional": m.Optional}
		}
		var sigs []map[string]any
		for _, s := range p.Signatures {
			sigs = append(sigs, map[string]any{"keyid": s.KeyID, "sig": s.Sig})
		}
		packages[key] = PnpmPackageEntry{
			Resolution:           resolution,
			Engines:              p.Engines,
			OS:                   p.OS,
			CPU:                  p.CPU,
			PeerDependencies:     p.PeerDependencies,
			PeerDependenciesMeta: meta,
			HasBin:               p.HasBin,
			Signatures:           sigs,
		}
		// packages: stays keyed by base name@version; snapshots: are keyed by
		// the peer-context form pnpm uses, with edges referencing contextual keys.
		snapshots[key+suffix[p.Name]] = PnpmSnapshotEntry{
			Dependencies:         resolveEdgesCtx(p.Dependencies, versionByName, suffix),
			OptionalDependencies: resolveEdgesCtx(p.OptionalDependencies, versionByName, suffix),
		}
	}

	out := &PnpmLockfile{
		LockfileVersion: "9.0",
		Settings:        map[string]any{"autoInstallPeers": true, "excludeLinksFromLockfile": false},
		Importers:       importers,
		Packages:        packages,
		Snapshots:       snapshots,
		Catalogs:        map[string]map[string]string{},
	}
	// Re-emit the pnpm-only content captured on read (overrides,
	// patchedDependencies, time, the original settings block, per-package
	// fields like libc, …) so a round trip does not silently drop it. Only
	// the original settings/version override the defaults above.
	if pt := lock.Pnpm; pt != nil {
		if pt.LockfileVersion != "" {
			out.LockfileVersion = pt.LockfileVersion
		}
		if len(pt.Settings) > 0 {
			out.Settings = pt.Settings
		}
		out.PreservedTopLevel = pt.TopLevel
		for key, pres := range pt.PackagePreserved {
			if e, ok := out.Packages[key]; ok {
				e.Preserved = pres
				out.Packages[key] = e
			}
		}
		for snapKey, e := range out.Snapshots {
			if pres, ok := pt.SnapshotPreserved[stripPeerSuffix(snapKey)]; ok && len(pres) > 0 {
				e.Preserved = pres
				out.Snapshots[snapKey] = e
			}
		}
		for path, pres := range pt.ImporterPreserved {
			if e, ok := out.Importers[path]; ok {
				e.Preserved = pres
				out.Importers[path] = e
			}
		}
	}
	return out
}

func parsePnpmID(key string) (name, version string, ok bool) {
	base := stripPeerSuffix(key)
	at := strings.LastIndexByte(base, '@')
	if at <= 0 {
		return "", "", false
	}
	return base[:at], base[at+1:], true
}

func stripPeerSuffix(key string) string {
	if i := strings.IndexByte(key, '('); i >= 0 {
		return key[:i]
	}
	return key
}

func registryTarballURL(registry, name, version string) string {
	unscoped := name
	if strings.HasPrefix(name, "@") {
		if slash := strings.IndexByte(name, '/'); slash >= 0 {
			unscoped = name[slash+1:]
		}
	}
	base := strings.TrimRight(registry, "/")
	return base + "/" + name + "/-/" + unscoped + "-" + version + ".tgz"
}

func specifierMap(deps map[string]PnpmDirectDep) map[string]string {
	out := map[string]string{}
	for k, d := range deps {
		out[k] = d.Specifier
	}
	return out
}

func directDeps(declared map[string]string, versionByName, suffix, links map[string]string) map[string]PnpmDirectDep {
	out := map[string]PnpmDirectDep{}
	for name, spec := range declared {
		// A workspace/link/file dep resolves to a link:/file: target, not a
		// registry version (pnpm records these in the importer too).
		if lv, ok := links[name]; ok {
			out[name] = PnpmDirectDep{Specifier: spec, Version: lv}
			continue
		}
		version, ok := versionByName[name]
		if !ok {
			continue // git: and other non-link sources are tracked elsewhere
		}
		out[name] = PnpmDirectDep{Specifier: spec, Version: version + suffix[name]}
	}
	return out
}

func resolveEdges(deps map[string]string, versionByName map[string]string) map[string]string {
	out := map[string]string{}
	for name := range deps {
		if version, ok := versionByName[name]; ok {
			out[name] = version
		}
	}
	return out
}

// resolveEdgesCtx is resolveEdges with pnpm peer-context suffixes appended to
// each edge value (`version(peer@v)…`), matching the snapshot keys pnpm emits.
func resolveEdgesCtx(deps map[string]string, versionByName, suffix map[string]string) map[string]string {
	out := map[string]string{}
	for name := range deps {
		if version, ok := versionByName[name]; ok {
			out[name] = version + suffix[name]
		}
	}
	return out
}

// peerSuffixes computes each package's pnpm peer-context suffix: the sorted
// concatenation of "(peerKey)" over the package's peer context, where peerKey
// is itself contextual (recursively). pnpm keys snapshots
// name@version(peerA@vA)(peerB@vB(...)). gnpm resolves one version per package,
// so each has a single context, derived from the resolved graph.
//
// A package's context is its own resolved peers plus peers that bubble up from
// its dependency subtree — a dependency's peer propagates to the parent unless
// the parent provides it as a regular dependency (pnpm's rule). Peers absent
// from the graph (unsatisfied / absent optional) are omitted; pnpm records
// those as transitivePeerDependencies, not in the key.
func peerSuffixes(byName map[string]LockedPackage) map[string]string {
	// peerSet(name): the names forming name's peer context.
	setMemo := map[string]map[string]bool{}
	var peerSet func(name string, stack map[string]bool) map[string]bool
	peerSet = func(name string, stack map[string]bool) map[string]bool {
		if s, ok := setMemo[name]; ok {
			return s
		}
		p, ok := byName[name]
		if !ok || stack[name] {
			return nil // missing, or peer/dependency cycle — stop
		}
		stack[name] = true
		set := map[string]bool{}
		for peer := range p.PeerDependencies {
			if _, ok := byName[peer]; ok {
				set[peer] = true
			}
		}
		bubble := func(deps map[string]string) {
			for dep := range deps {
				for q := range peerSet(dep, stack) {
					if q == name {
						continue // the package satisfies its own peer by being it
					}
					if _, provides := p.Dependencies[q]; !provides {
						set[q] = true
					}
				}
			}
		}
		bubble(p.Dependencies)
		bubble(p.OptionalDependencies)
		delete(stack, name)
		setMemo[name] = set
		return set
	}
	// suffix(name): "(Q@vQ+suffix(Q))" over peerSet(name), sorted.
	suffixMemo := map[string]string{}
	var suffix func(name string, stack map[string]bool) string
	suffix = func(name string, stack map[string]bool) string {
		if s, ok := suffixMemo[name]; ok {
			return s
		}
		if stack[name] {
			return ""
		}
		stack[name] = true
		var pieces []string
		for q := range peerSet(name, map[string]bool{}) {
			pieces = append(pieces, "("+q+"@"+byName[q].Version+suffix(q, stack)+")")
		}
		delete(stack, name)
		sort.Strings(pieces)
		s := strings.Join(pieces, "")
		suffixMemo[name] = s
		return s
	}
	out := make(map[string]string, len(byName))
	for name := range byName {
		out[name] = suffix(name, map[string]bool{})
	}
	return out
}

func signaturesFromPnpm(raw []map[string]any) []LockedSignature {
	var out []LockedSignature
	for _, m := range raw {
		keyid, kok := m["keyid"].(string)
		sig, sok := m["sig"].(string)
		if kok && sok {
			out = append(out, LockedSignature{KeyID: keyid, Sig: sig})
		}
	}
	return out
}
