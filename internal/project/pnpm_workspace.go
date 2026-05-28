package project

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PnpmWorkspace is the install-relevant view of pnpm-workspace.yaml
// (doc/spec.md §2, §8, §9). Policy scalars are surfaced via Settings,
// keyed in the kebab-case .npmrc form so they can merge into the config
// layer (spec §2.1 point 6) in pnpm mode.
type PnpmWorkspace struct {
	Packages              []string
	AllowBuilds           []string
	OnlyBuiltDependencies []string
	ConfigDependencies    map[string]string
	// Catalog is the default catalog (top-level `catalog:`), also folded
	// into Catalogs["default"].
	Catalog map[string]string
	// Catalogs maps a catalog name to its (package → range) table.
	Catalogs        map[string]map[string]string
	NamedRegistries map[string]string
	// Overrides / NestedOverrides come from pnpm-workspace.yaml `overrides`
	// (pnpm's monorepo-wide dependency overrides), parsed like package.json's.
	Overrides       map[string]string
	NestedOverrides map[string]map[string]string
	// Settings holds scalar policy keys converted to kebab-case (e.g.
	// blockExoticSubdeps → block-exotic-subdeps), from both top-level
	// scalars and a nested `settings:` block.
	Settings map[string]string
}

// IsEmpty reports whether nothing install-relevant was found.
func (w *PnpmWorkspace) IsEmpty() bool {
	return len(w.Packages) == 0 && len(w.AllowBuilds) == 0 &&
		len(w.OnlyBuiltDependencies) == 0 && len(w.ConfigDependencies) == 0 &&
		len(w.Catalog) == 0 && len(w.Catalogs) == 0 &&
		len(w.NamedRegistries) == 0 && len(w.Settings) == 0 &&
		len(w.Overrides) == 0 && len(w.NestedOverrides) == 0
}

// structuredKeys are handled as typed fields and are not folded into
// Settings.
var structuredKeys = map[string]bool{
	"packages": true, "allowbuilds": true, "onlybuiltdependencies": true,
	"configdependencies": true, "catalog": true, "catalogs": true,
	"namedregistries": true, "settings": true, "overrides": true,
	// pnpm-lock.yaml carries these too; never treat them as settings.
	"importers": true, "lockfileversion": true, "snapshots": true,
}

// ReadPnpmWorkspace reads <root>/pnpm-workspace.yaml. A missing or
// unparseable file yields an empty config (never an error) so callers can
// treat absence and emptiness uniformly.
func ReadPnpmWorkspace(root string) *PnpmWorkspace {
	w := &PnpmWorkspace{
		ConfigDependencies: map[string]string{},
		Catalog:            map[string]string{},
		Catalogs:           map[string]map[string]string{},
		NamedRegistries:    map[string]string{},
		Settings:           map[string]string{},
	}
	data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml"))
	if err != nil {
		return w
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return w
	}
	parsePnpmDoc(doc, w)
	return w
}

// parsePnpmDoc populates w from a decoded YAML mapping. Factored out so
// the same key handling can be reused for the pnpm-lock.yaml `settings:`
// block.
func parsePnpmDoc(doc map[string]any, w *PnpmWorkspace) {
	w.Packages = yamlStringList(doc["packages"])
	w.AllowBuilds = yamlStringList(doc["allowBuilds"])
	w.OnlyBuiltDependencies = yamlStringList(doc["onlyBuiltDependencies"])
	w.ConfigDependencies = yamlConfigDeps(doc["configDependencies"])

	if cat := yamlStringMap(doc["catalog"]); len(cat) > 0 {
		w.Catalog = cat
		w.Catalogs["default"] = cat
	}
	if cats, ok := doc["catalogs"].(map[string]any); ok {
		for name, table := range cats {
			if m := yamlStringMap(table); len(m) > 0 {
				w.Catalogs[name] = m
				if name == "default" {
					w.Catalog = m
				}
			}
		}
	}
	if nr := yamlStringMap(doc["namedRegistries"]); len(nr) > 0 {
		w.NamedRegistries = nr
	}
	w.Overrides, w.NestedOverrides = parseOverrides(doc["overrides"])

	for k, v := range doc {
		if structuredKeys[strings.ToLower(k)] {
			continue
		}
		if s, ok := scalarString(v); ok {
			w.Settings[camelToKebab(k)] = s
		}
	}
	if settings, ok := doc["settings"].(map[string]any); ok {
		for k, v := range settings {
			if s, ok := scalarString(v); ok {
				w.Settings[camelToKebab(k)] = s
			}
		}
	}
}

func yamlStringList(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func yamlStringMap(raw any) map[string]string {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := scalarString(v); ok {
			out[k] = s
		}
	}
	return out
}

func yamlConfigDeps(raw any) map[string]string {
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

// scalarString renders a YAML scalar (string, bool, int, float) as a
// string. Returns ok=false for collections.
func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case int:
		return itoa(x), true
	case int64:
		return itoa(int(x)), true
	case float64:
		// pnpm policy values are integers/booleans/strings; render a
		// whole float without a trailing ".0".
		if x == float64(int64(x)) {
			return itoa(int(x)), true
		}
		return "", false
	default:
		return "", false
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// camelToKebab converts a camelCase pnpm key to the kebab-case .npmrc
// form: blockExoticSubdeps → block-exotic-subdeps. Already-kebab keys
// pass through lowercased.
func camelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
