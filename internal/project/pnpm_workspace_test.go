package project

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadPnpmWorkspace(t *testing.T) {
	d := t.TempDir()
	body := `packages:
  - "packages/*"
  - "apps/*"
allowBuilds:
  - esbuild
  - "@swc/*"
onlyBuiltDependencies:
  - sharp
blockExoticSubdeps: true
minimumReleaseAge: 1440
catalogMode: strict
catalog:
  react: ^18.3.0
  react-dom: ^18.3.0
catalogs:
  testing:
    vitest: ^3.0.0
namedRegistries:
  corp: https://corp.example/
configDependencies:
  shared-config: 1.0.0
  obj-config:
    version: 2.0.0
settings:
  verifyDepsBeforeRun: warn
`
	if err := os.WriteFile(filepath.Join(d, "pnpm-workspace.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	w := ReadPnpmWorkspace(d)

	if !reflect.DeepEqual(w.Packages, []string{"packages/*", "apps/*"}) {
		t.Errorf("packages = %v", w.Packages)
	}
	if !reflect.DeepEqual(w.AllowBuilds, []string{"esbuild", "@swc/*"}) {
		t.Errorf("allowBuilds = %v", w.AllowBuilds)
	}
	if !reflect.DeepEqual(w.OnlyBuiltDependencies, []string{"sharp"}) {
		t.Errorf("onlyBuiltDependencies = %v", w.OnlyBuiltDependencies)
	}
	if w.Catalog["react"] != "^18.3.0" || w.Catalogs["default"]["react-dom"] != "^18.3.0" {
		t.Errorf("default catalog = %v / %v", w.Catalog, w.Catalogs["default"])
	}
	if w.Catalogs["testing"]["vitest"] != "^3.0.0" {
		t.Errorf("testing catalog = %v", w.Catalogs["testing"])
	}
	if w.NamedRegistries["corp"] != "https://corp.example/" {
		t.Errorf("namedRegistries = %v", w.NamedRegistries)
	}
	if w.ConfigDependencies["shared-config"] != "1.0.0" || w.ConfigDependencies["obj-config"] != "2.0.0" {
		t.Errorf("configDependencies = %v", w.ConfigDependencies)
	}
	// Policy scalars become kebab-case settings.
	if w.Settings["block-exotic-subdeps"] != "true" {
		t.Errorf("block-exotic-subdeps = %q", w.Settings["block-exotic-subdeps"])
	}
	if w.Settings["minimum-release-age"] != "1440" {
		t.Errorf("minimum-release-age = %q", w.Settings["minimum-release-age"])
	}
	if w.Settings["catalog-mode"] != "strict" {
		t.Errorf("catalog-mode = %q", w.Settings["catalog-mode"])
	}
	if w.Settings["verify-deps-before-run"] != "warn" {
		t.Errorf("verify-deps-before-run (nested settings) = %q", w.Settings["verify-deps-before-run"])
	}
}

func TestReadPnpmWorkspaceMissing(t *testing.T) {
	w := ReadPnpmWorkspace(t.TempDir())
	if !w.IsEmpty() {
		t.Errorf("missing file should be empty, got %+v", w)
	}
}

func TestCamelToKebab(t *testing.T) {
	cases := map[string]string{
		"blockExoticSubdeps":  "block-exotic-subdeps",
		"trustPolicy":         "trust-policy",
		"pmOnFail":            "pm-on-fail",
		"verifyDepsBeforeRun": "verify-deps-before-run",
		"already-kebab":       "already-kebab",
	}
	for in, want := range cases {
		if got := camelToKebab(in); got != want {
			t.Errorf("camelToKebab(%q) = %q, want %q", in, got, want)
		}
	}
}
