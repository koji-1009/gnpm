// Package sbom emits a CycloneDX 1.7 or SPDX 2.3 software bill of
// materials from a resolved lockfile (doc/spec.md §5.2 sbom).
package sbom

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
)

// Build renders the lockfile as an SBOM document in the given format
// ("cyclonedx" or "spdx"). specVersion overrides the default spec version
// string when non-empty.
func Build(lock *lockfile.Lockfile, format, specVersion, projectName string) ([]byte, error) {
	switch format {
	case "cyclonedx":
		return buildCycloneDX(lock, orDefault(specVersion, "1.7")), nil
	case "spdx":
		return buildSPDX(lock, orDefault(specVersion, "SPDX-2.3"), projectName), nil
	default:
		return nil, core.Usage("--format must be cyclonedx or spdx, got %q", format)
	}
}

// serialNumber is stable across re-runs over an unchanged lockfile: the
// SHA-256 of the sorted set of name@version|integrity tuples.
func serialNumber(lock *lockfile.Lockfile) string {
	tuples := make([]string, 0, len(lock.Packages))
	for _, p := range lock.Packages {
		tuples = append(tuples, p.Name+"@"+p.Version+"|"+p.Integrity)
	}
	sort.Strings(tuples)
	sum := sha256.Sum256([]byte(strings.Join(tuples, "\n")))
	return hex.EncodeToString(sum[:])
}

func sortedPackages(lock *lockfile.Lockfile) []lockfile.LockedPackage {
	out := make([]lockfile.LockedPackage, 0, len(lock.Packages))
	for _, p := range lock.Packages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func buildCycloneDX(lock *lockfile.Lockfile, specVersion string) []byte {
	var components []any
	for _, p := range sortedPackages(lock) {
		comp := map[string]any{
			"type":    "library",
			"name":    p.Name,
			"version": p.Version,
			"purl":    purl(p.Name, p.Version),
		}
		if h := hexDigest(p.Integrity); h != "" {
			comp["hashes"] = []any{map[string]any{"alg": cycloneAlg(p.Integrity), "content": h}}
		}
		components = append(components, comp)
	}
	doc := map[string]any{
		"bomFormat":    "CycloneDX",
		"specVersion":  specVersion,
		"serialNumber": "urn:gnpm:sbom:" + serialNumber(lock),
		"version":      1,
		"metadata":     map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339), "tools": []any{map[string]any{"name": "gnpm"}}},
		"components":   components,
	}
	body, _ := json.MarshalIndent(doc, "", "  ")
	return body
}

func buildSPDX(lock *lockfile.Lockfile, specVersion, projectName string) []byte {
	var packages []any
	for _, p := range sortedPackages(lock) {
		entry := map[string]any{
			"SPDXID":           "SPDXRef-Package-" + spdxID(p.Name, p.Version),
			"name":             p.Name,
			"versionInfo":      p.Version,
			"downloadLocation": orDefault(p.Tarball, "NOASSERTION"),
			"externalRefs": []any{map[string]any{
				"referenceCategory": "PACKAGE-MANAGER",
				"referenceType":     "purl",
				"referenceLocator":  purl(p.Name, p.Version),
			}},
		}
		if h := hexDigest(p.Integrity); h != "" {
			entry["checksums"] = []any{map[string]any{"algorithm": spdxAlg(p.Integrity), "checksumValue": h}}
		}
		packages = append(packages, entry)
	}
	if projectName == "" {
		projectName = "project"
	}
	doc := map[string]any{
		"spdxVersion":       specVersion,
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              projectName,
		"documentNamespace": "urn:gnpm:sbom:" + serialNumber(lock),
		"creationInfo":      map[string]any{"creators": []any{"Tool: gnpm"}, "created": time.Now().UTC().Format(time.RFC3339)},
		"packages":          packages,
	}
	body, _ := json.MarshalIndent(doc, "", "  ")
	return body
}

// purl builds an npm package URL, percent-encoding the scope slash.
func purl(name, version string) string {
	if strings.HasPrefix(name, "@") {
		name = "%40" + strings.TrimPrefix(name, "@")
	}
	return "pkg:npm/" + name + "@" + version
}

func spdxID(name, version string) string {
	r := strings.NewReplacer("@", "-", "/", "-", ".", "-")
	return r.Replace(name) + "-" + r.Replace(version)
}

// hexDigest converts an SRI integrity ("sha512-<base64>") to a hex digest.
func hexDigest(integrity string) string {
	dash := strings.IndexByte(integrity, '-')
	if dash < 0 {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(integrity[dash+1:])
	if err != nil {
		return ""
	}
	return hex.EncodeToString(raw)
}

func cycloneAlg(integrity string) string {
	switch {
	case strings.HasPrefix(integrity, "sha512"):
		return "SHA-512"
	case strings.HasPrefix(integrity, "sha256"):
		return "SHA-256"
	case strings.HasPrefix(integrity, "sha1"):
		return "SHA-1"
	default:
		return "SHA-512"
	}
}

func spdxAlg(integrity string) string {
	switch {
	case strings.HasPrefix(integrity, "sha512"):
		return "SHA512"
	case strings.HasPrefix(integrity, "sha256"):
		return "SHA256"
	case strings.HasPrefix(integrity, "sha1"):
		return "SHA1"
	default:
		return "SHA512"
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
