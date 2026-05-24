// Package linker builds a project's node_modules from the content store,
// in either the npm-style flat (hoisted) layout or the pnpm-style
// isolated symlink farm (doc/spec.md §8, node-linker setting).
package linker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/platform"
)

// Kind selects the node_modules layout.
type Kind int

const (
	// Hoisted is npm's flat layout: node_modules/<name>.
	Hoisted Kind = iota
	// Isolated is pnpm's strict layout: node_modules/.gnpm/<id>/node_modules/<name>
	// with symlinks wiring each package's own dependencies.
	Isolated
)

// ParseKind maps a node-linker setting to a Kind (default hoisted).
func ParseKind(s string) Kind {
	if s == "isolated" {
		return Isolated
	}
	return Hoisted
}

// LinkSpec is a resolved package ready to be linked.
type LinkSpec struct {
	Name      string
	Version   string
	Integrity string // SRI; the store key
	// Dependencies maps dependency name → resolved version, used to wire
	// each dependency into the package's private node_modules in the
	// isolated layout.
	Dependencies map[string]string
	Bin          map[string]string
	IsDirect     bool
	// LinkAlias, when set, is the top-level node_modules name to use
	// instead of Name (npm: aliases).
	LinkAlias string
	Scripts   map[string]string
	Engines   map[string]string
	// Path is the node_modules-relative install location: "react" when
	// hoisted to the top level, "a/node_modules/lodash" when nested for a
	// version conflict. Empty means top-level under TopLevelName().
	Path string
	// CopyFrom, when set, is a source directory copied into place (minus
	// .git) instead of materializing from the store — used for git
	// dependencies, which are not store-ingested. Integrity is empty then.
	CopyFrom string
}

// InstallPath returns the node_modules-relative location of the package.
func (s LinkSpec) InstallPath() string {
	if s.Path != "" {
		return s.Path
	}
	return s.TopLevelName()
}

// ID is "<name>@<version>".
func (s LinkSpec) ID() string { return s.Name + "@" + s.Version }

// TopLevelName is the directory name under top-level node_modules.
func (s LinkSpec) TopLevelName() string {
	if s.LinkAlias != "" {
		return s.LinkAlias
	}
	return s.Name
}

// TopLevelBinDir returns node_modules/.bin for a project root.
func TopLevelBinDir(projectRoot string) string {
	return filepath.Join(projectRoot, "node_modules", ".bin")
}

// WriteBins writes launcher shims into binDir for each name→relative-path
// bin entry, resolving each path against the package directory pkgDir.
func WriteBins(binDir, pkgDir string, bin map[string]string) error {
	for name, rel := range bin {
		source := filepath.Join(pkgDir, filepath.FromSlash(rel))
		if err := writeBinShim(source, filepath.Join(binDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func safeID(id string) string { return strings.ReplaceAll(id, "/", "+") }

// writeBinShim writes a launcher at linkPath that runs `node source`.
func writeBinShim(source, linkPath string) error {
	if _, err := os.Lstat(linkPath); err == nil {
		return nil // an entry already exists
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return core.IOError("mkdir .bin").Wrap(err)
	}
	if runtime.GOOS == "windows" {
		cmd := "@ECHO OFF\r\nnode \"" + source + "\" %*\r\n"
		if err := os.WriteFile(linkPath+".cmd", []byte(cmd), 0o644); err != nil {
			return core.IOError("writing .cmd shim").Wrap(err)
		}
		ps := "$ErrorActionPreference = \"Stop\"\n& node \"" + source + "\" $args\nexit $LASTEXITCODE\n"
		return os.WriteFile(linkPath+".ps1", []byte(ps), 0o644)
	}
	shim := "#!/bin/sh\nexec node \"" + source + "\" \"$@\"\n"
	if err := os.WriteFile(linkPath, []byte(shim), 0o755); err != nil {
		return core.IOError("writing bin shim").Wrap(err)
	}
	return platform.ChmodExecutable(linkPath)
}

func versionGreater(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, nb := atoiSafe(pa[i]), atoiSafe(pb[i])
		if na != nb {
			return na > nb
		}
	}
	return len(pa) > len(pb)
}

func atoiSafe(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return n
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}
