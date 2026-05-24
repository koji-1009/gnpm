package policy

import "github.com/koji-1009/gnpm/internal/project"

// CatalogMode governs how catalogs interact with non-catalog workspace
// ranges (doc/spec.md §9.2).
type CatalogMode int

const (
	// CatalogManual resolves catalog: references only.
	CatalogManual CatalogMode = iota
	// CatalogPrefer uses the catalog range when one exists, even if the
	// workspace declared a different range.
	CatalogPrefer
	// CatalogStrict fails when a declared range diverges from the catalog.
	CatalogStrict
)

// ParseCatalogMode maps a setting value to a CatalogMode (default manual).
func ParseCatalogMode(s string) CatalogMode {
	switch s {
	case "prefer":
		return CatalogPrefer
	case "strict":
		return CatalogStrict
	default:
		return CatalogManual
	}
}

// ResolveCatalog resolves a catalog reference to a concrete range using
// the workspace's catalog tables (doc/spec.md §9). catalogName is the
// body after "catalog:" ("" means the default catalog). ok is false when
// the entry is undefined.
func ResolveCatalog(ws *project.PnpmWorkspace, packageName, catalogName string) (string, bool) {
	name := catalogName
	if name == "" {
		name = "default"
	}
	table, ok := ws.Catalogs[name]
	if !ok {
		// The default catalog may have come in via the singular `catalog:`.
		if name == "default" {
			if r, ok := ws.Catalog[packageName]; ok {
				return r, true
			}
		}
		return "", false
	}
	r, ok := table[packageName]
	return r, ok
}
