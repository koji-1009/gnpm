// Package lockfile reads and writes the two lockfile formats gnpm
// interoperates with — package-lock.json (npm v3) and pnpm-lock.yaml —
// against a single internal model. The install pipeline only sees the
// internal Lockfile; per-format read/write are pure conversions on
// either side (doc/spec.md §4).
package lockfile

// SchemaVersion is the internal lockfile model version.
const SchemaVersion = 1

// Lockfile is the internal representation, shaped after npm v3.
type Lockfile struct {
	Version   int
	Importers map[string]Importer
	// Packages keyed by "<name>@<version>".
	Packages map[string]LockedPackage
}

// Importer is the direct dependency selectors of a workspace member.
type Importer struct {
	Dependencies         map[string]string
	DevDependencies      map[string]string
	OptionalDependencies map[string]string
	PeerDependencies     map[string]string
}

// LockedPackage is one resolved package entry.
type LockedPackage struct {
	Name    string
	Version string
	// Path is the node_modules-relative install location (npm v3 keys the
	// lockfile by it). Empty means top-level (== Name); nested copies for
	// version conflicts use "<parent>/node_modules/<name>".
	Path                 string
	Tarball              string
	Integrity            string
	Dependencies         map[string]string
	OptionalDependencies map[string]string
	PeerDependencies     map[string]string
	PeerDependenciesMeta map[string]PeerDependencyMeta
	OS                   []string
	CPU                  []string
	HasBin               bool
	HasInstallScript     bool
	Bin                  map[string]string
	Scripts              map[string]string
	Engines              map[string]string
	// Signatures are registry ECDSA signatures persisted as the
	// `_signatures` extension so warm installs can re-verify.
	Signatures []LockedSignature
}

// LockedSignature is one (keyid, sig) pair; sig is base64(DER).
type LockedSignature struct {
	KeyID string
	Sig   string
}

// PeerDependencyMeta carries the optional flag for a peer.
type PeerDependencyMeta struct {
	Optional bool
}
