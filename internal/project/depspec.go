package project

import "strings"

// Protocol is the kind of a dependency specifier (doc/spec.md §3).
type Protocol int

const (
	ProtoSemver Protocol = iota
	ProtoWorkspace
	ProtoFile
	ProtoLink
	ProtoHTTPS
	ProtoGit
	ProtoCatalog
)

// Spec is a parsed dependency specifier. It separates the logical name
// (the package.json key, also the node_modules/<dir> name) from the real
// package name to resolve — they differ only for npm: aliases.
type Spec struct {
	LogicalName string
	PackageName string
	// Range is the version range, or for file/link the local path, or
	// for catalog the catalog name body.
	Range    string
	Protocol Protocol
	// URL holds the remote for https / git specifiers.
	URL string
}

// IsAlias reports whether the logical and real package names differ.
func (s Spec) IsAlias() bool { return s.LogicalName != s.PackageName }

// ParseSpec parses a package.json dependency value into a Spec.
func ParseSpec(logicalName, raw string) Spec {
	value := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(value, "npm:"):
		return parseAlias(logicalName, value[4:])
	case strings.HasPrefix(value, "https://"), strings.HasPrefix(value, "http://"):
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: "*", Protocol: ProtoHTTPS, URL: value}
	case strings.HasPrefix(value, "git+"), strings.HasPrefix(value, "git://"), strings.HasPrefix(value, "github:"):
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: "*", Protocol: ProtoGit, URL: value}
	case strings.HasPrefix(value, "file:"):
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: value[5:], Protocol: ProtoFile}
	case strings.HasPrefix(value, "link:"):
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: value[5:], Protocol: ProtoLink}
	case strings.HasPrefix(value, "workspace:"):
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: value[len("workspace:"):], Protocol: ProtoWorkspace}
	case strings.HasPrefix(value, "catalog:"):
		// Range keeps the raw body so a later catalog-resolution step can
		// swap it for the declared semver range.
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: value[len("catalog:"):], Protocol: ProtoCatalog}
	default:
		r := value
		if r == "" {
			r = "*"
		}
		return Spec{LogicalName: logicalName, PackageName: logicalName, Range: r}
	}
}

func parseAlias(logicalName, body string) Spec {
	// Scoped names start with @, so the separating @ comes after the
	// slash; for unscoped names the last @ separates name from range.
	var sep int
	if strings.HasPrefix(body, "@") {
		sep = strings.IndexByte(body[1:], '@')
		if sep >= 0 {
			sep++ // adjust for the [1:] slice offset
		}
	} else {
		sep = strings.LastIndexByte(body, '@')
	}
	if sep > 0 {
		return Spec{LogicalName: logicalName, PackageName: body[:sep], Range: body[sep+1:]}
	}
	return Spec{LogicalName: logicalName, PackageName: body, Range: "latest"}
}
