package project

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDetectMode(t *testing.T) {
	t.Run("pnpm via lock", func(t *testing.T) {
		d := t.TempDir()
		touch(t, d, "pnpm-lock.yaml")
		touch(t, d, "package-lock.json") // dual presence stays pnpm
		if got := DetectMode(d); got != ModePnpm {
			t.Errorf("got %v, want pnpm", got)
		}
	})
	t.Run("pnpm via workspace", func(t *testing.T) {
		d := t.TempDir()
		touch(t, d, "pnpm-workspace.yaml")
		if got := DetectMode(d); got != ModePnpm {
			t.Errorf("got %v, want pnpm", got)
		}
	})
	t.Run("npm via package-lock", func(t *testing.T) {
		d := t.TempDir()
		touch(t, d, "package-lock.json")
		if got := DetectMode(d); got != ModeNpm {
			t.Errorf("got %v, want npm", got)
		}
	})
	t.Run("npm via non-auth npmrc", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, ".npmrc", "save-exact=true\n")
		if got := DetectMode(d); got != ModeNpm {
			t.Errorf("got %v, want npm", got)
		}
	})
	t.Run("gnpm when npmrc is auth-only", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, ".npmrc", "//registry.npmjs.org/:_authToken=secret\n_auth=abc\nemail=a@b.c\n")
		if got := DetectMode(d); got != ModeGnpm {
			t.Errorf("got %v, want gnpm (auth-only npmrc)", got)
		}
	})
	t.Run("gnpm fresh", func(t *testing.T) {
		d := t.TempDir()
		if got := DetectMode(d); got != ModeGnpm {
			t.Errorf("got %v, want gnpm", got)
		}
	})
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		name, raw string
		want      Spec
	}{
		{"react", "^18.2.0", Spec{LogicalName: "react", PackageName: "react", Range: "^18.2.0", Protocol: ProtoSemver}},
		{"r", "npm:react@^18.0.0", Spec{LogicalName: "r", PackageName: "react", Range: "^18.0.0", Protocol: ProtoSemver}},
		{"t", "npm:@types/node@^20", Spec{LogicalName: "t", PackageName: "@types/node", Range: "^20", Protocol: ProtoSemver}},
		{"x", "npm:lodash", Spec{LogicalName: "x", PackageName: "lodash", Range: "latest", Protocol: ProtoSemver}},
		{"w", "workspace:^", Spec{LogicalName: "w", PackageName: "w", Range: "^", Protocol: ProtoWorkspace}},
		{"f", "file:../shared", Spec{LogicalName: "f", PackageName: "f", Range: "../shared", Protocol: ProtoFile}},
		{"l", "link:../shared", Spec{LogicalName: "l", PackageName: "l", Range: "../shared", Protocol: ProtoLink}},
		{"h", "https://ex.com/p.tgz", Spec{LogicalName: "h", PackageName: "h", Range: "*", Protocol: ProtoHTTPS, URL: "https://ex.com/p.tgz"}},
		{"g", "github:owner/repo#main", Spec{LogicalName: "g", PackageName: "g", Range: "*", Protocol: ProtoGit, URL: "github:owner/repo#main"}},
		{"c", "catalog:testing", Spec{LogicalName: "c", PackageName: "c", Range: "testing", Protocol: ProtoCatalog}},
		{"e", "", Spec{LogicalName: "e", PackageName: "e", Range: "*", Protocol: ProtoSemver}},
	}
	for _, c := range cases {
		got := ParseSpec(c.name, c.raw)
		if got != c.want {
			t.Errorf("ParseSpec(%q,%q) = %+v, want %+v", c.name, c.raw, got, c.want)
		}
	}
	if !ParseSpec("r", "npm:react@^18").IsAlias() {
		t.Error("npm: alias should report IsAlias")
	}
	if ParseSpec("react", "^18").IsAlias() {
		t.Error("plain spec should not report IsAlias")
	}
}

func TestReadPackageJSON(t *testing.T) {
	d := t.TempDir()
	body := `{
  "name": "demo",
  "version": "1.2.3",
  "dependencies": { "react": "^18.2.0" },
  "devDependencies": { "vitest": "^3.0.0" },
  "scripts": { "build": "tsc" },
  "bin": "./cli.js",
  "workspaces": ["packages/*"],
  "engines": { "node": ">=18" },
  "packageManager": "gnpm@0.0.1",
  "overrides": { "lodash": "4.17.21", "foo>bar": "1.0.0", "baz": { "qux": "2.0.0", ".": "3.0.0" } },
  "onlyBuiltDependencies": ["esbuild"],
  "devEngines": { "runtime": { "name": "node", "version": "22" } },
  "gnpm": {
    "allowBuilds": ["@swc/*"],
    "auditConfig": { "ignoreGhsas": ["GHSA-xxxx"] },
    "configDependencies": { "shared-config": "1.0.0", "obj-config": { "version": "2.0.0" } }
  }
}`
	write(t, d, "package.json", body)
	pkg, err := ReadPackageJSON(filepath.Join(d, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name != "demo" || pkg.Version != "1.2.3" {
		t.Errorf("name/version = %q/%q", pkg.Name, pkg.Version)
	}
	if pkg.Dependencies["react"] != "^18.2.0" || pkg.DevDependencies["vitest"] != "^3.0.0" {
		t.Errorf("deps wrong: %v / %v", pkg.Dependencies, pkg.DevDependencies)
	}
	if pkg.Bin["demo"] != "./cli.js" {
		t.Errorf("string bin should map to package name: %v", pkg.Bin)
	}
	if !reflect.DeepEqual(pkg.Workspaces, []string{"packages/*"}) {
		t.Errorf("workspaces = %v", pkg.Workspaces)
	}
	if pkg.Overrides["lodash"] != "4.17.21" || pkg.Overrides["baz"] != "3.0.0" {
		t.Errorf("flat overrides = %v", pkg.Overrides)
	}
	if pkg.NestedOverrides["foo"]["bar"] != "1.0.0" || pkg.NestedOverrides["baz"]["qux"] != "2.0.0" {
		t.Errorf("nested overrides = %v", pkg.NestedOverrides)
	}
	if len(pkg.OnlyBuiltDependencies) != 1 || pkg.OnlyBuiltDependencies[0] != "esbuild" {
		t.Errorf("onlyBuiltDependencies = %v", pkg.OnlyBuiltDependencies)
	}
	if pkg.DevEnginesRuntime == nil || pkg.DevEnginesRuntime.Version != "22" {
		t.Errorf("devEngines.runtime = %+v", pkg.DevEnginesRuntime)
	}
	if len(pkg.AllowBuilds) != 1 || pkg.AllowBuilds[0] != "@swc/*" {
		t.Errorf("allowBuilds = %v", pkg.AllowBuilds)
	}
	if len(pkg.AuditIgnoreGhsas) != 1 || pkg.AuditIgnoreGhsas[0] != "GHSA-xxxx" {
		t.Errorf("auditIgnoreGhsas = %v", pkg.AuditIgnoreGhsas)
	}
	if pkg.ConfigDependencies["shared-config"] != "1.0.0" || pkg.ConfigDependencies["obj-config"] != "2.0.0" {
		t.Errorf("configDependencies = %v", pkg.ConfigDependencies)
	}
}

func touch(t *testing.T, dir, name string) {
	t.Helper()
	write(t, dir, name, "")
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
