package lockfile

import (
	"strings"
	"testing"
)

// TestPnpmPassthroughRoundTrip guards that a pnpm→internal→pnpm conversion (the
// path every install takes) preserves pnpm-lock.yaml content gnpm does not
// model — overrides, patchedDependencies, onlyBuiltDependencies, time, the
// settings block, and per-package fields like libc — instead of dropping it.
func TestPnpmPassthroughRoundTrip(t *testing.T) {
	const src = `lockfileVersion: '9.0'
settings:
  autoInstallPeers: false
  excludeLinksFromLockfile: false
overrides:
  lodash: ^4.17.21
patchedDependencies:
  react@18.2.0:
    hash: abcdef
    path: patches/react@18.2.0.patch
onlyBuiltDependencies:
  - esbuild
time:
  lodash@4.17.21: '2021-02-20T00:00:00.000Z'
importers:
  .:
    dependencies:
      lodash:
        specifier: ^4.17.0
        version: 4.17.21
    dependenciesMeta:
      lodash:
        injected: true
packages:
  lodash@4.17.21:
    resolution: {integrity: sha512-deadbeef}
    libc:
      - glibc
snapshots:
  lodash@4.17.21: {}
`

	parsed, err := ParsePnpm([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through the internal model, as install does.
	internal := PnpmToLockfile(parsed, "https://registry.npmjs.org/")
	out, err := WritePnpmString(LockfileToPnpm(internal))
	if err != nil {
		t.Fatal(err)
	}

	// Re-parse the regenerated lockfile and assert the unmodeled content survived.
	got, err := ParsePnpm([]byte(out))
	if err != nil {
		t.Fatalf("regenerated lockfile does not parse: %v\n%s", err, out)
	}
	for _, section := range []string{"overrides", "patchedDependencies", "onlyBuiltDependencies", "time"} {
		if _, ok := got.PreservedTopLevel[section]; !ok {
			t.Errorf("top-level %q dropped on round trip\n--- regenerated ---\n%s", section, out)
		}
	}
	if got.LockfileVersion != "9.0" {
		t.Errorf("lockfileVersion = %q, want 9.0", got.LockfileVersion)
	}
	if got.Settings["autoInstallPeers"] != false {
		t.Errorf("settings.autoInstallPeers = %v, want false (original preserved, not clobbered)", got.Settings["autoInstallPeers"])
	}
	if pkg, ok := got.Packages["lodash@4.17.21"]; !ok {
		t.Error("lodash@4.17.21 missing from regenerated packages")
	} else if _, ok := pkg.Preserved["libc"]; !ok {
		t.Error("package field libc dropped on round trip")
	}
	// patch path should appear verbatim somewhere in the output.
	if !strings.Contains(out, "patches/react@18.2.0.patch") {
		t.Error("patchedDependencies content not re-emitted")
	}
	if imp, ok := got.Importers["."]; !ok {
		t.Error("root importer missing from regenerated lockfile")
	} else if _, ok := imp.Preserved["dependenciesMeta"]; !ok {
		t.Error("importer dependenciesMeta (injected) dropped on round trip")
	}
}
