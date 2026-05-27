package project

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
)

// PackageJSON is the subset of package.json that gnpm consumes, plus a
// Raw copy of the decoded document for commands that read or rewrite
// arbitrary fields (pkg get/set).
type PackageJSON struct {
	Name    string
	Version string

	Dependencies         map[string]string
	DevDependencies      map[string]string
	OptionalDependencies map[string]string
	PeerDependencies     map[string]string

	Scripts map[string]string
	Bin     map[string]string
	Engines map[string]string

	// Overrides applied wherever a package appears.
	Overrides map[string]string
	// NestedOverrides applied only under a given parent: parent → pkg → range.
	NestedOverrides map[string]map[string]string

	// Workspaces glob patterns from the array or {packages:[...]} form.
	Workspaces []string

	// PackageManager is the corepack `packageManager` field.
	PackageManager string
	// DevEnginesPackageManager / DevEnginesRuntime mirror devEngines.*.
	DevEnginesPackageManager *DevEnginesEntry
	DevEnginesRuntime        *DevEnginesEntry

	// OnlyBuiltDependencies is pnpm's build allowlist (unioned with
	// gnpm.allowBuilds for the build-script gate).
	OnlyBuiltDependencies []string
	// AllowBuilds is package.json#gnpm.allowBuilds.
	AllowBuilds []string
	// AuditIgnoreGhsas is package.json#gnpm.auditConfig.ignoreGhsas.
	AuditIgnoreGhsas []string
	// ConfigDependencies is package.json#gnpm.configDependencies (or
	// pnpm-workspace.yaml#configDependencies in pnpm mode).
	ConfigDependencies map[string]string

	// Raw is the decoded JSON document, preserved for pkg get/set.
	Raw map[string]any
}

// DevEnginesEntry is one slot of devEngines (runtime or packageManager).
type DevEnginesEntry struct {
	Name    string
	Version string
	OnFail  string
}

// ReadPackageJSON reads and parses the package.json at path.
func ReadPackageJSON(path string) (*PackageJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, core.Usage("no package.json at %s", path)
		}
		return nil, core.IOError("reading %s", path).Wrap(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, core.Usage("%s is not a valid JSON object: %v", path, err)
	}
	return FromMap(raw), nil
}

// FromMap builds a PackageJSON from a decoded JSON object.
func FromMap(m map[string]any) *PackageJSON {
	p := &PackageJSON{
		Name:                 stringField(m, "name", ""),
		Version:              stringField(m, "version", "0.0.0"),
		Dependencies:         strMap(m["dependencies"]),
		DevDependencies:      strMap(m["devDependencies"]),
		OptionalDependencies: strMap(m["optionalDependencies"]),
		PeerDependencies:     strMap(m["peerDependencies"]),
		Scripts:              strMap(m["scripts"]),
		Engines:              strMap(m["engines"]),
		ConfigDependencies:   map[string]string{},
		Raw:                  m,
	}

	// bin: a bare string maps the package's own name to that path.
	switch b := m["bin"].(type) {
	case string:
		p.Bin = map[string]string{p.Name: b}
	default:
		p.Bin = strMap(m["bin"])
	}

	p.Overrides, p.NestedOverrides = parseOverrides(m["overrides"])
	// pnpm keeps overrides under `pnpm.overrides`; merge them over the
	// npm-style top-level `overrides` (pnpm's location wins) so a pnpm
	// project's overrides are honored, not silently ignored.
	if pnpmSection, ok := m["pnpm"].(map[string]any); ok {
		pf, pn := parseOverrides(pnpmSection["overrides"])
		p.Overrides = MergeOverrides(p.Overrides, pf)
		p.NestedOverrides = MergeNestedOverrides(p.NestedOverrides, pn)
	}
	p.Workspaces = parseWorkspaces(m["workspaces"])
	p.OnlyBuiltDependencies = strList(m["onlyBuiltDependencies"])
	p.PackageManager = stringField(m, "packageManager", "")

	if de, ok := m["devEngines"].(map[string]any); ok {
		p.DevEnginesPackageManager = parseDevEngines(de["packageManager"])
		p.DevEnginesRuntime = parseDevEngines(de["runtime"])
	}

	if section, ok := m["gnpm"].(map[string]any); ok {
		p.AllowBuilds = strList(section["allowBuilds"])
		if ac, ok := section["auditConfig"].(map[string]any); ok {
			p.AuditIgnoreGhsas = strList(ac["ignoreGhsas"])
		}
		p.ConfigDependencies = parseConfigDeps(section["configDependencies"])
	}
	return p
}

func parseConfigDeps(raw any) map[string]string {
	out := map[string]string{}
	m, ok := raw.(map[string]any)
	if !ok {
		return out
	}
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if val != "" {
				out[k] = val
			}
		case map[string]any:
			if s, ok := val["version"].(string); ok && s != "" {
				out[k] = s
			}
		}
	}
	return out
}

func parseDevEngines(raw any) *DevEnginesEntry {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	name, nok := m["name"].(string)
	version, vok := m["version"].(string)
	if !nok || !vok {
		return nil
	}
	e := &DevEnginesEntry{Name: name, Version: version}
	if onFail, ok := m["onFail"].(string); ok {
		e.OnFail = onFail
	}
	return e
}

// parseOverrides splits the overrides field into flat (applies
// everywhere) and nested (parent → child → range) maps. Supports both
// the "foo>bar" key form and the {"foo": {"bar": ...}} object form, with
// "." denoting the parent itself.
func parseOverrides(raw any) (map[string]string, map[string]map[string]string) {
	flat := map[string]string{}
	nested := map[string]map[string]string{}
	m, ok := raw.(map[string]any)
	if !ok {
		return flat, nested
	}
	for key, value := range m {
		if strings.Contains(key, ">") {
			parts := strings.Split(key, ">")
			if len(parts) == 2 {
				if s, ok := value.(string); ok {
					p0 := strings.TrimSpace(parts[0])
					p1 := strings.TrimSpace(parts[1])
					ensure(nested, p0)[p1] = s
				}
			}
			continue
		}
		switch v := value.(type) {
		case string:
			flat[key] = v
		case map[string]any:
			for ik, iv := range v {
				if ik == "." {
					flat[key] = coerceString(iv)
				} else {
					ensure(nested, key)[ik] = coerceString(iv)
				}
			}
		}
	}
	return flat, nested
}

// MergeOverrides returns base with override's entries layered on top
// (override wins on conflicting keys). Either map may be nil.
func MergeOverrides(base, override map[string]string) map[string]string {
	if len(override) == 0 {
		return base
	}
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// MergeNestedOverrides layers override's parent→child→range entries over
// base (override wins on conflicting parent+child).
func MergeNestedOverrides(base, override map[string]map[string]string) map[string]map[string]string {
	if len(override) == 0 {
		return base
	}
	out := map[string]map[string]string{}
	for parent, m := range base {
		out[parent] = map[string]string{}
		for c, r := range m {
			out[parent][c] = r
		}
	}
	for parent, m := range override {
		if out[parent] == nil {
			out[parent] = map[string]string{}
		}
		for c, r := range m {
			out[parent][c] = r
		}
	}
	return out
}

func ensure(m map[string]map[string]string, k string) map[string]string {
	if m[k] == nil {
		m[k] = map[string]string{}
	}
	return m[k]
}

func parseWorkspaces(raw any) []string {
	switch w := raw.(type) {
	case []any:
		return toStringSlice(w)
	case map[string]any:
		if pkgs, ok := w["packages"].([]any); ok {
			return toStringSlice(pkgs)
		}
	}
	return nil
}

func toStringSlice(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringField(m map[string]any, key, fallback string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return fallback
}

func strList(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, coerceString(v))
	}
	return out
}

// strMap coerces a JSON object of string-ish values into map[string]string.
func strMap(raw any) map[string]string {
	m, ok := raw.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = coerceString(v)
	}
	return out
}

func coerceString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
