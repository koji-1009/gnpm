package lockfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
)

// WriteNpmString serializes the lockfile into npm's package-lock.json
// (v3) shape. Output is two-space-indented with HTML escaping disabled so
// tarball URLs containing & / < / > stay literal, matching npm.
func WriteNpmString(lock *Lockfile, projectName, projectVersion string) (string, error) {
	root := map[string]any{
		"name":            projectName,
		"lockfileVersion": 3,
		"requires":        true,
	}
	if projectVersion != "" {
		root["version"] = projectVersion
	}

	packages := map[string]any{}
	rootImporter := lock.Importers["."]
	rootEntry := map[string]any{"name": projectName}
	if projectVersion != "" {
		rootEntry["version"] = projectVersion
	}
	putSorted(rootEntry, "dependencies", rootImporter.Dependencies)
	putSorted(rootEntry, "devDependencies", rootImporter.DevDependencies)
	putSorted(rootEntry, "optionalDependencies", rootImporter.OptionalDependencies)
	putSorted(rootEntry, "peerDependencies", rootImporter.PeerDependencies)
	packages[""] = rootEntry

	for _, id := range sortedKeys(lock.Packages) {
		pkg := lock.Packages[id]
		path := pkg.Path
		if path == "" {
			path = pkg.Name
		}
		packages["node_modules/"+path] = serializeNpmPackage(pkg)
	}
	root["packages"] = packages

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return "", core.LockfileError("encoding package-lock.json: %v", err)
	}
	return buf.String(), nil
}

func serializeNpmPackage(pkg LockedPackage) map[string]any {
	out := map[string]any{"version": pkg.Version}
	if pkg.Tarball != "" {
		out["resolved"] = pkg.Tarball
	}
	if pkg.Integrity != "" {
		out["integrity"] = pkg.Integrity
	}
	putSorted(out, "dependencies", pkg.Dependencies)
	putSorted(out, "optionalDependencies", pkg.OptionalDependencies)
	putSorted(out, "peerDependencies", pkg.PeerDependencies)
	if len(pkg.PeerDependenciesMeta) > 0 {
		meta := map[string]any{}
		for k, v := range pkg.PeerDependenciesMeta {
			meta[k] = map[string]any{"optional": v.Optional}
		}
		out["peerDependenciesMeta"] = meta
	}
	if len(pkg.OS) > 0 {
		out["os"] = pkg.OS
	}
	if len(pkg.CPU) > 0 {
		out["cpu"] = pkg.CPU
	}
	putSorted(out, "engines", pkg.Engines)
	putSorted(out, "bin", pkg.Bin)
	if pkg.HasInstallScript {
		out["hasInstallScript"] = true
	}
	// gnpm extensions (leading underscore = npm-tolerated internal key).
	if len(pkg.Signatures) > 0 {
		sigs := make([]any, 0, len(pkg.Signatures))
		for _, s := range pkg.Signatures {
			sigs = append(sigs, map[string]any{"keyid": s.KeyID, "sig": s.Sig})
		}
		out["_signatures"] = sigs
	}
	putSorted(out, "_scripts", pkg.Scripts)
	return out
}

// WriteNpmFile atomically writes the lockfile to path.
func WriteNpmFile(lock *Lockfile, path, projectName, projectVersion string) error {
	body, err := WriteNpmString(lock, projectName, projectVersion)
	if err != nil {
		return err
	}
	return atomicWrite(path, []byte(body))
}

// ImportNpm parses a package-lock.json (v3, with a v1 fallback) into the
// internal model.
func ImportNpm(data []byte) (*Lockfile, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, core.LockfileError("package-lock.json is not a JSON object: %v", err)
	}

	importer := Importer{}
	nodes, _ := root["packages"].(map[string]any)
	if r, ok := nodes[""].(map[string]any); ok {
		importer = importerFrom(r)
	} else {
		importer = importerFrom(root)
	}

	packages := map[string]LockedPackage{}
	for key, v := range nodes {
		if key == "" {
			continue
		}
		body, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name := str(body["name"])
		if name == "" {
			name = packageNameFromPath(key)
		}
		version := str(body["version"])
		if name == "" || version == "" {
			continue
		}
		_, hasBin := body["bin"]
		path := strings.TrimPrefix(key, "node_modules/")
		packages[path] = LockedPackage{
			Name:                 name,
			Version:              version,
			Path:                 path,
			Tarball:              str(body["resolved"]),
			Integrity:            str(body["integrity"]),
			Dependencies:         strMap(body["dependencies"]),
			OptionalDependencies: strMap(body["optionalDependencies"]),
			PeerDependencies:     strMap(body["peerDependencies"]),
			OS:                   strList(body["os"]),
			CPU:                  strList(body["cpu"]),
			HasBin:               hasBin,
			HasInstallScript:     body["hasInstallScript"] == true,
			Bin:                  strMap(body["bin"]),
			Scripts:              strMap(body["_scripts"]),
			Engines:              strMap(body["engines"]),
			Signatures:           signaturesFrom(body["_signatures"]),
		}
	}
	return &Lockfile{Version: SchemaVersion, Importers: map[string]Importer{".": importer}, Packages: packages}, nil
}

func importerFrom(m map[string]any) Importer {
	return Importer{
		Dependencies:         strMap(m["dependencies"]),
		DevDependencies:      strMap(m["devDependencies"]),
		OptionalDependencies: strMap(m["optionalDependencies"]),
		PeerDependencies:     strMap(m["peerDependencies"]),
	}
}

func signaturesFrom(raw any) []LockedSignature {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []LockedSignature
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		keyid, kok := m["keyid"].(string)
		sig, sok := m["sig"].(string)
		if kok && sok {
			out = append(out, LockedSignature{KeyID: keyid, Sig: sig})
		}
	}
	return out
}

func packageNameFromPath(key string) string {
	const prefix = "node_modules/"
	rest, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, "/node_modules/"); i >= 0 {
		return rest[i+len("/node_modules/"):]
	}
	return rest
}

// --- shared helpers ---------------------------------------------------

func putSorted(dst map[string]any, key string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	dst[key] = sortedStringMap(m)
}

// sortedStringMap returns a json.Marshaler-friendly representation that
// json encodes in sorted key order (Go already sorts map keys, so a
// plain map suffices; kept explicit for clarity).
func sortedStringMap(m map[string]string) map[string]string { return m }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func strMap(raw any) map[string]string {
	m, ok := raw.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = str(v)
	}
	return out
}

func strList(raw any) []string {
	l, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l))
	for _, v := range l {
		out = append(out, str(v))
	}
	return out
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.IOError("creating lockfile dir").Wrap(err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return core.IOError("writing lockfile tmp").Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return core.IOError("committing lockfile").Wrap(err)
	}
	return nil
}
