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
	importer := Importer{}
	if root, ok := p.Importers["."]; ok {
		importer = Importer{
			Dependencies:         specifierMap(root.Dependencies),
			DevDependencies:      specifierMap(root.DevDependencies),
			OptionalDependencies: specifierMap(root.OptionalDependencies),
			PeerDependencies:     specifierMap(root.PeerDependencies),
		}
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
	return &Lockfile{Version: SchemaVersion, Importers: map[string]Importer{".": importer}, Packages: packages}
}

// LockfileToPnpm converts the internal model into a pnpm-lock.yaml v9
// model. The internal model stores ranges; pnpm snapshots store resolved
// versions, looked up via the lockfile-wide name→version map. Edges to
// names absent from the lockfile (platform-skipped optionals, unresolved
// peers) are dropped, matching pnpm's "materialized edges only" rule.
func LockfileToPnpm(lock *Lockfile) *PnpmLockfile {
	versionByName := map[string]string{}
	for _, p := range lock.Packages {
		versionByName[p.Name] = p.Version
	}

	root := lock.Importers["."]
	importer := PnpmImporter{
		Dependencies:         directDeps(root.Dependencies, versionByName),
		DevDependencies:      directDeps(root.DevDependencies, versionByName),
		OptionalDependencies: directDeps(root.OptionalDependencies, versionByName),
		PeerDependencies:     directDeps(root.PeerDependencies, versionByName),
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
		snapshots[key] = PnpmSnapshotEntry{
			Dependencies:         resolveEdges(p.Dependencies, versionByName),
			OptionalDependencies: resolveEdges(p.OptionalDependencies, versionByName),
		}
	}

	return &PnpmLockfile{
		LockfileVersion: "9.0",
		Settings:        map[string]any{"autoInstallPeers": true, "excludeLinksFromLockfile": false},
		Importers:       map[string]PnpmImporter{".": importer},
		Packages:        packages,
		Snapshots:       snapshots,
		Catalogs:        map[string]map[string]string{},
	}
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

func directDeps(declared map[string]string, versionByName map[string]string) map[string]PnpmDirectDep {
	out := map[string]PnpmDirectDep{}
	for name, spec := range declared {
		version, ok := versionByName[name]
		if !ok {
			continue // workspace link / file: / git: tracked elsewhere
		}
		out[name] = PnpmDirectDep{Specifier: spec, Version: version}
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
