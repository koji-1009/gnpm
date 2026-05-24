package lockfile

import (
	"strings"
	"testing"

	"github.com/koji-1009/gnpm/internal/project"
)

func sampleLockfile() *Lockfile {
	return &Lockfile{
		Version: SchemaVersion,
		Importers: map[string]Importer{".": {
			Dependencies:    map[string]string{"react": "^18.2.0"},
			DevDependencies: map[string]string{"vitest": "^3.0.0"},
		}},
		Packages: map[string]LockedPackage{
			"react@18.2.0": {
				Name:             "react",
				Version:          "18.2.0",
				Tarball:          "https://registry.npmjs.org/react/-/react-18.2.0.tgz",
				Integrity:        "sha512-abc",
				Dependencies:     map[string]string{"loose-envify": "^1.1.0"},
				Bin:              map[string]string{},
				Scripts:          map[string]string{"postinstall": "node setup.js"},
				Signatures:       []LockedSignature{{KeyID: "SHA256:k", Sig: "s"}},
				HasInstallScript: true,
			},
			"loose-envify@1.4.0": {
				Name:      "loose-envify",
				Version:   "1.4.0",
				Tarball:   "https://registry.npmjs.org/loose-envify/-/loose-envify-1.4.0.tgz",
				Integrity: "sha512-def",
				Bin:       map[string]string{"loose-envify": "cli.js"},
			},
		},
	}
}

func TestNpmRoundTrip(t *testing.T) {
	lock := sampleLockfile()
	body, err := WriteNpmString(lock, "demo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	// npm-tolerated extensions present.
	if !strings.Contains(body, "_signatures") || !strings.Contains(body, "_scripts") {
		t.Error("expected _signatures and _scripts extensions in output")
	}
	if !strings.Contains(body, `"lockfileVersion": 3`) {
		t.Error("expected lockfileVersion 3")
	}

	back, err := ImportNpm([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if back.Importers["."].Dependencies["react"] != "^18.2.0" {
		t.Errorf("importer deps lost: %v", back.Importers["."])
	}
	// The internal map is keyed by node_modules path (here == name).
	r := back.Packages["react"]
	if r.Integrity != "sha512-abc" || r.Dependencies["loose-envify"] != "^1.1.0" {
		t.Errorf("react entry lost data: %+v", r)
	}
	if !r.HasInstallScript || r.Scripts["postinstall"] != "node setup.js" {
		t.Errorf("scripts extension lost: %+v", r)
	}
	if len(r.Signatures) != 1 || r.Signatures[0].KeyID != "SHA256:k" {
		t.Errorf("signatures lost: %v", r.Signatures)
	}
	le := back.Packages["loose-envify"]
	if !le.HasBin || le.Bin["loose-envify"] != "cli.js" {
		t.Errorf("bin lost: %+v", le)
	}
}

func TestPnpmParseAndConvert(t *testing.T) {
	doc := `lockfileVersion: '9.0'
settings:
  autoInstallPeers: true
importers:
  .:
    dependencies:
      react:
        specifier: ^18.2.0
        version: 18.2.0
packages:
  react@18.2.0:
    resolution: {integrity: sha512-abc}
    engines: {node: '>=0.10.0'}
  loose-envify@1.4.0:
    resolution: {integrity: sha512-def}
    hasBin: true
snapshots:
  react@18.2.0:
    dependencies:
      loose-envify: 1.4.0
  loose-envify@1.4.0: {}
`
	p, err := ParsePnpm([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if p.LockfileVersion != "9.0" {
		t.Errorf("lockfileVersion = %q", p.LockfileVersion)
	}
	if p.Importers["."].Dependencies["react"].Version != "18.2.0" {
		t.Errorf("importer = %+v", p.Importers["."])
	}

	lock := PnpmToLockfile(p, "https://registry.npmjs.org/")
	r := lock.Packages["react@18.2.0"]
	if r.Integrity != "sha512-abc" {
		t.Errorf("integrity = %q", r.Integrity)
	}
	if r.Tarball != "https://registry.npmjs.org/react/-/react-18.2.0.tgz" {
		t.Errorf("reconstructed tarball = %q", r.Tarball)
	}
	if r.Dependencies["loose-envify"] != "1.4.0" {
		t.Errorf("snapshot deps lost: %v", r.Dependencies)
	}
	if !lock.Packages["loose-envify@1.4.0"].HasBin {
		t.Error("hasBin lost")
	}
}

func TestPnpmRoundTripThroughInternal(t *testing.T) {
	lock := sampleLockfile()
	pnpm := LockfileToPnpm(lock)
	body, err := WritePnpmString(pnpm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "react@18.2.0") || !strings.Contains(body, "snapshots:") {
		t.Errorf("pnpm output missing structure:\n%s", body)
	}
	reparsed, err := ParsePnpm([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	back := PnpmToLockfile(reparsed, "https://registry.npmjs.org/")
	if back.Packages["react@18.2.0"].Integrity != "sha512-abc" {
		t.Errorf("integrity lost in pnpm round trip")
	}
	// react's snapshot edge to loose-envify resolves to the locked version.
	if back.Packages["react@18.2.0"].Dependencies["loose-envify"] != "1.4.0" {
		t.Errorf("snapshot edge lost: %v", back.Packages["react@18.2.0"].Dependencies)
	}
}

func TestParseDispatchByMode(t *testing.T) {
	lock := sampleLockfile()
	npmBody, _ := WriteNpmString(lock, "demo", "1.0.0")
	got, err := Parse([]byte(npmBody), project.ModeNpm, "https://registry.npmjs.org/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages["react"].Integrity != "sha512-abc" {
		t.Error("npm-mode parse failed")
	}
	if ProjectLockfileName(project.ModePnpm) != "pnpm-lock.yaml" {
		t.Error("pnpm mode lockfile name wrong")
	}
	if ProjectLockfileName(project.ModeNpm) != "package-lock.json" {
		t.Error("npm mode lockfile name wrong")
	}
}
