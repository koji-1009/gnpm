package lockfile

import (
	"fmt"

	"github.com/koji-1009/gnpm/internal/core"
	"gopkg.in/yaml.v3"
)

// PnpmLockfile is a pnpm-lock.yaml v9 view that retains every top-level
// field so a round trip through the writer is lossless (doc/spec.md §4.2;
// semantic-equivalent, not byte-equivalent).
type PnpmLockfile struct {
	LockfileVersion   string
	Settings          map[string]any
	Importers         map[string]PnpmImporter
	Packages          map[string]PnpmPackageEntry
	Snapshots         map[string]PnpmSnapshotEntry
	Catalogs          map[string]map[string]string
	PreservedTopLevel map[string]any
}

type PnpmImporter struct {
	Dependencies         map[string]PnpmDirectDep
	DevDependencies      map[string]PnpmDirectDep
	OptionalDependencies map[string]PnpmDirectDep
	PeerDependencies     map[string]PnpmDirectDep
	// Preserved holds importer keys gnpm does not model (e.g.
	// dependenciesMeta for injected deps), kept for lossless round trips.
	Preserved map[string]any
}

type PnpmDirectDep struct {
	Specifier string
	Version   string
}

type PnpmPackageEntry struct {
	Resolution           map[string]any
	Engines              map[string]string
	OS                   []string
	CPU                  []string
	PeerDependencies     map[string]string
	PeerDependenciesMeta map[string]map[string]any
	HasBin               bool
	Deprecated           string
	BundledDependencies  []string
	Signatures           []map[string]any
	Preserved            map[string]any
}

type PnpmSnapshotEntry struct {
	Dependencies               map[string]string
	OptionalDependencies       map[string]string
	TransitivePeerDependencies []string
	Preserved                  map[string]any
}

var pnpmTopLevelKnown = map[string]bool{
	"lockfileVersion": true, "settings": true, "importers": true,
	"packages": true, "snapshots": true, "catalog": true, "catalogs": true,
}
var pnpmImporterKnown = map[string]bool{
	"dependencies": true, "devDependencies": true,
	"optionalDependencies": true, "peerDependencies": true,
}
var pnpmPackageKnown = map[string]bool{
	"resolution": true, "engines": true, "os": true, "cpu": true,
	"peerDependencies": true, "peerDependenciesMeta": true, "hasBin": true,
	"deprecated": true, "bundledDependencies": true, "signatures": true,
}
var pnpmSnapshotKnown = map[string]bool{
	"dependencies": true, "optionalDependencies": true, "transitivePeerDependencies": true,
}

// ParsePnpm parses a pnpm-lock.yaml document.
func ParsePnpm(data []byte) (*PnpmLockfile, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, core.LockfileError("pnpm-lock.yaml parse: %v", err)
	}
	if doc == nil {
		return nil, core.LockfileError("pnpm-lock.yaml root must be a map")
	}

	out := &PnpmLockfile{
		LockfileVersion:   yamlScalarString(doc["lockfileVersion"]),
		Settings:          asMap(doc["settings"]),
		Importers:         map[string]PnpmImporter{},
		Packages:          map[string]PnpmPackageEntry{},
		Snapshots:         map[string]PnpmSnapshotEntry{},
		Catalogs:          map[string]map[string]string{},
		PreservedTopLevel: preservedExcept(doc, pnpmTopLevelKnown),
	}

	for path, v := range asMap(doc["importers"]) {
		body := asMap(v)
		out.Importers[path] = PnpmImporter{
			Dependencies:         readDirectDeps(asMap(body["dependencies"])),
			DevDependencies:      readDirectDeps(asMap(body["devDependencies"])),
			OptionalDependencies: readDirectDeps(asMap(body["optionalDependencies"])),
			PeerDependencies:     readDirectDeps(asMap(body["peerDependencies"])),
			Preserved:            preservedExcept(body, pnpmImporterKnown),
		}
	}

	for key, v := range asMap(doc["packages"]) {
		body := asMap(v)
		out.Packages[key] = PnpmPackageEntry{
			Resolution:           asMap(body["resolution"]),
			Engines:              yamlStringMap(body["engines"]),
			OS:                   yamlStringList(body["os"]),
			CPU:                  yamlStringList(body["cpu"]),
			PeerDependencies:     yamlStringMap(body["peerDependencies"]),
			PeerDependenciesMeta: yamlMetaMap(body["peerDependenciesMeta"]),
			HasBin:               body["hasBin"] == true,
			Deprecated:           yamlScalarString(body["deprecated"]),
			BundledDependencies:  yamlStringList(body["bundledDependencies"]),
			Signatures:           yamlMapList(body["signatures"]),
			Preserved:            preservedExcept(body, pnpmPackageKnown),
		}
	}

	for key, v := range asMap(doc["snapshots"]) {
		body := asMap(v)
		out.Snapshots[key] = PnpmSnapshotEntry{
			Dependencies:               yamlStringMap(body["dependencies"]),
			OptionalDependencies:       yamlStringMap(body["optionalDependencies"]),
			TransitivePeerDependencies: yamlStringList(body["transitivePeerDependencies"]),
			Preserved:                  preservedExcept(body, pnpmSnapshotKnown),
		}
	}

	if short := yamlStringMap(doc["catalog"]); len(short) > 0 {
		out.Catalogs["default"] = short
	}
	for name, v := range asMap(doc["catalogs"]) {
		out.Catalogs[name] = yamlStringMap(v)
	}
	return out, nil
}

// WritePnpmString serializes a PnpmLockfile to block-style YAML.
func WritePnpmString(p *PnpmLockfile) (string, error) {
	out := map[string]any{}
	if p.LockfileVersion != "" {
		out["lockfileVersion"] = p.LockfileVersion
	}
	if len(p.Settings) > 0 {
		out["settings"] = p.Settings
	}
	if len(p.Importers) > 0 {
		imp := map[string]any{}
		for path, i := range p.Importers {
			body := map[string]any{}
			putDirectDeps(body, "dependencies", i.Dependencies)
			putDirectDeps(body, "devDependencies", i.DevDependencies)
			putDirectDeps(body, "optionalDependencies", i.OptionalDependencies)
			putDirectDeps(body, "peerDependencies", i.PeerDependencies)
			for k, v := range i.Preserved {
				body[k] = v
			}
			imp[path] = body
		}
		out["importers"] = imp
	}
	if len(p.Packages) > 0 {
		pkgs := map[string]any{}
		for key, e := range p.Packages {
			body := map[string]any{}
			if len(e.Resolution) > 0 {
				body["resolution"] = e.Resolution
			}
			putStrMap(body, "engines", e.Engines)
			putStrList(body, "os", e.OS)
			putStrList(body, "cpu", e.CPU)
			putStrMap(body, "peerDependencies", e.PeerDependencies)
			if len(e.PeerDependenciesMeta) > 0 {
				body["peerDependenciesMeta"] = e.PeerDependenciesMeta
			}
			if e.HasBin {
				body["hasBin"] = true
			}
			if e.Deprecated != "" {
				body["deprecated"] = e.Deprecated
			}
			putStrList(body, "bundledDependencies", e.BundledDependencies)
			if len(e.Signatures) > 0 {
				body["signatures"] = e.Signatures
			}
			for k, v := range e.Preserved {
				body[k] = v
			}
			pkgs[key] = body
		}
		out["packages"] = pkgs
	}
	if len(p.Snapshots) > 0 {
		snaps := map[string]any{}
		for key, e := range p.Snapshots {
			body := map[string]any{}
			putStrMap(body, "dependencies", e.Dependencies)
			putStrMap(body, "optionalDependencies", e.OptionalDependencies)
			putStrList(body, "transitivePeerDependencies", e.TransitivePeerDependencies)
			for k, v := range e.Preserved {
				body[k] = v
			}
			snaps[key] = body
		}
		out["snapshots"] = snaps
	}
	if def, ok := p.Catalogs["default"]; ok && len(def) > 0 {
		out["catalog"] = def
	}
	named := map[string]any{}
	for name, table := range p.Catalogs {
		if name == "default" {
			continue
		}
		named[name] = table
	}
	if len(named) > 0 {
		out["catalogs"] = named
	}
	for k, v := range p.PreservedTopLevel {
		out[k] = v
	}

	body, err := yaml.Marshal(out)
	if err != nil {
		return "", core.LockfileError("encoding pnpm-lock.yaml: %v", err)
	}
	return string(body), nil
}

// WritePnpmFile atomically writes the pnpm lockfile to path.
func WritePnpmFile(p *PnpmLockfile, path string) error {
	body, err := WritePnpmString(p)
	if err != nil {
		return err
	}
	return atomicWrite(path, []byte(body))
}

// --- yaml coercion helpers --------------------------------------------

func asMap(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func readDirectDeps(raw map[string]any) map[string]PnpmDirectDep {
	out := map[string]PnpmDirectDep{}
	for name, v := range raw {
		body := asMap(v)
		out[name] = PnpmDirectDep{
			Specifier: yamlScalarString(body["specifier"]),
			Version:   yamlScalarString(body["version"]),
		}
	}
	return out
}

func yamlMetaMap(raw any) map[string]map[string]any {
	m := asMap(raw)
	if len(m) == 0 {
		return nil
	}
	out := map[string]map[string]any{}
	for k, v := range m {
		out[k] = asMap(v)
	}
	return out
}

func yamlMapList(raw any) []map[string]any {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, asMap(e))
	}
	return out
}

func yamlStringMap(raw any) map[string]string {
	m := asMap(raw)
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = yamlScalarString(v)
	}
	return out
}

func yamlStringList(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, yamlScalarString(v))
	}
	return out
}

func yamlScalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func preservedExcept(src map[string]any, known map[string]bool) map[string]any {
	var out map[string]any
	for k, v := range src {
		if known[k] {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	return out
}

func putDirectDeps(dst map[string]any, key string, deps map[string]PnpmDirectDep) {
	if len(deps) == 0 {
		return
	}
	m := map[string]any{}
	for name, d := range deps {
		m[name] = map[string]any{"specifier": d.Specifier, "version": d.Version}
	}
	dst[key] = m
}

func putStrMap(dst map[string]any, key string, m map[string]string) {
	if len(m) > 0 {
		dst[key] = m
	}
}

func putStrList(dst map[string]any, key string, l []string) {
	if len(l) > 0 {
		dst[key] = l
	}
}
