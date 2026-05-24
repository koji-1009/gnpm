package registry

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
)

// Packument is the slim view of an npm packument: only the fields the
// resolver and linker consume. Storing the full document would waste
// memory and pin large JSON strings.
type Packument struct {
	Name     string
	Versions map[string]*PackumentVersion
	DistTags map[string]string
	// PublishTimes are per-version publish timestamps (UTC) from the
	// registry `time` map, used by the minimum-release-age filter.
	PublishTimes map[string]time.Time
}

// Latest returns the version tagged "latest", or "".
func (p *Packument) Latest() string { return p.DistTags["latest"] }

// DistSignature is one (keyid, sig) pair from a version's
// dist.signatures.
type DistSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// PackumentVersion is one version slice within a packument.
type PackumentVersion struct {
	Name                 string
	Version              string
	Tarball              string
	Integrity            string
	Dependencies         map[string]string
	OptionalDependencies map[string]string
	PeerDependencies     map[string]string
	// OptionalPeers are peer names marked optional in peerDependenciesMeta.
	OptionalPeers map[string]bool
	OS            []string
	CPU           []string
	Libc          []string
	Deprecated    string
	HasBin        bool
	Bin           map[string]string
	Scripts       map[string]string
	Engines       map[string]string
	BundledDeps   []string
	// HasInstallScript mirrors the slim packument flag: install-time
	// scripts exist even when their bodies are omitted.
	HasInstallScript bool
	Signatures       []DistSignature
}

// ParsePackument decodes a registry packument body (slim or full).
func ParsePackument(data []byte) (*Packument, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, core.NetworkError("packument is not valid JSON: %v", err)
	}
	return packumentFromMap(raw), nil
}

func packumentFromMap(raw map[string]any) *Packument {
	p := &Packument{
		Name:         asString(raw["name"]),
		Versions:     map[string]*PackumentVersion{},
		DistTags:     map[string]string{},
		PublishTimes: map[string]time.Time{},
	}
	if versions, ok := raw["versions"].(map[string]any); ok {
		for ver, v := range versions {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			p.Versions[ver] = versionFromMap(vm)
		}
	}
	if tags, ok := raw["dist-tags"].(map[string]any); ok {
		for k, v := range tags {
			p.DistTags[k] = asString(v)
		}
	}
	if times, ok := raw["time"].(map[string]any); ok {
		for k, v := range times {
			if k == "created" || k == "modified" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, asString(v)); err == nil {
				p.PublishTimes[k] = t.UTC()
			}
		}
	}
	return p
}

func versionFromMap(m map[string]any) *PackumentVersion {
	name := asString(m["name"])
	pv := &PackumentVersion{
		Name:                 name,
		Version:              asString(m["version"]),
		Dependencies:         stringMap(m["dependencies"]),
		OptionalDependencies: stringMap(m["optionalDependencies"]),
		PeerDependencies:     stringMap(m["peerDependencies"]),
		OptionalPeers:        optionalPeerNames(m["peerDependenciesMeta"]),
		OS:                   stringList(m["os"]),
		CPU:                  stringList(m["cpu"]),
		Libc:                 stringList(m["libc"]),
		Deprecated:           asString(m["deprecated"]),
		Scripts:              stringMap(m["scripts"]),
		Engines:              stringMap(m["engines"]),
		HasInstallScript:     m["hasInstallScript"] == true,
	}
	if dist, ok := m["dist"].(map[string]any); ok {
		pv.Tarball = asString(dist["tarball"])
		pv.Integrity = asString(dist["integrity"])
		pv.Signatures = parseSignatures(dist["signatures"])
	}
	if rawBin, present := m["bin"]; present {
		pv.HasBin = true
		pv.Bin = normalizeBin(rawBin, name)
	}
	pv.BundledDeps = parseBundled(m["bundledDependencies"], m["bundleDependencies"])
	return pv
}

func parseSignatures(raw any) []DistSignature {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []DistSignature
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		keyid, kok := m["keyid"].(string)
		sig, sok := m["sig"].(string)
		if kok && sok {
			out = append(out, DistSignature{KeyID: keyid, Sig: sig})
		}
	}
	return out
}

func normalizeBin(raw any, pkgName string) map[string]string {
	switch b := raw.(type) {
	case string:
		name := pkgName
		if slash := strings.LastIndexByte(pkgName, '/'); slash >= 0 {
			name = pkgName[slash+1:]
		}
		return map[string]string{name: b}
	case map[string]any:
		out := make(map[string]string, len(b))
		for k, v := range b {
			out[k] = asString(v)
		}
		return out
	default:
		return map[string]string{}
	}
}

func parseBundled(a, b any) []string {
	raw := a
	if raw == nil {
		raw = b
	}
	switch v := raw.(type) {
	case []any:
		return stringList(v)
	case map[string]any:
		out := make([]string, 0, len(v))
		for k := range v {
			out = append(out, k)
		}
		return out
	default:
		return nil
	}
}

func optionalPeerNames(raw any) map[string]bool {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out map[string]bool
	for name, v := range m {
		if meta, ok := v.(map[string]any); ok && meta["optional"] == true {
			if out == nil {
				out = map[string]bool{}
			}
			out[name] = true
		}
	}
	return out
}

// MarshalSlim serializes the packument in the slim on-disk cache format,
// mirroring the fields ParsePackument reads.
func (p *Packument) MarshalSlim() ([]byte, error) {
	versions := make(map[string]any, len(p.Versions))
	for k, v := range p.Versions {
		versions[k] = v.slimMap()
	}
	out := map[string]any{
		"name":      p.Name,
		"dist-tags": p.DistTags,
		"versions":  versions,
	}
	if len(p.PublishTimes) > 0 {
		times := make(map[string]any, len(p.PublishTimes))
		for k, t := range p.PublishTimes {
			times[k] = t.UTC().Format(time.RFC3339)
		}
		out["time"] = times
	}
	return json.Marshal(out)
}

func (pv *PackumentVersion) slimMap() map[string]any {
	m := map[string]any{
		"name":    pv.Name,
		"version": pv.Version,
	}
	if pv.Tarball != "" || pv.Integrity != "" || len(pv.Signatures) > 0 {
		dist := map[string]any{}
		if pv.Tarball != "" {
			dist["tarball"] = pv.Tarball
		}
		if pv.Integrity != "" {
			dist["integrity"] = pv.Integrity
		}
		if len(pv.Signatures) > 0 {
			dist["signatures"] = pv.Signatures
		}
		m["dist"] = dist
	}
	putMap(m, "dependencies", pv.Dependencies)
	putMap(m, "optionalDependencies", pv.OptionalDependencies)
	putMap(m, "peerDependencies", pv.PeerDependencies)
	if len(pv.OptionalPeers) > 0 {
		meta := map[string]any{}
		for name := range pv.OptionalPeers {
			meta[name] = map[string]bool{"optional": true}
		}
		m["peerDependenciesMeta"] = meta
	}
	putList(m, "os", pv.OS)
	putList(m, "cpu", pv.CPU)
	putList(m, "libc", pv.Libc)
	if pv.Deprecated != "" {
		m["deprecated"] = pv.Deprecated
	}
	putMap(m, "bin", pv.Bin)
	putMap(m, "scripts", pv.Scripts)
	putMap(m, "engines", pv.Engines)
	putList(m, "bundledDependencies", pv.BundledDeps)
	if pv.HasInstallScript {
		m["hasInstallScript"] = true
	}
	return m
}

func putMap(dst map[string]any, key string, m map[string]string) {
	if len(m) > 0 {
		dst[key] = m
	}
}

func putList(dst map[string]any, key string, l []string) {
	if len(l) > 0 {
		dst[key] = l
	}
}

// --- coercion helpers -------------------------------------------------

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func stringMap(raw any) map[string]string {
	m, ok := raw.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = asString(v)
	}
	return out
}

func stringList(raw any) []string {
	l, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l))
	for _, v := range l {
		out = append(out, asString(v))
	}
	return out
}
