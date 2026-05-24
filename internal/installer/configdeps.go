package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/koji-1009/gnpm/internal/archive"
	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/registry"
)

// materializeConfigDeps fetches and unpacks configDependencies into
// node_modules/.gnpm-config/<name>/ (doc/spec.md §2.4). Each is looked up
// by the exact version string given (a range will not resolve), is not
// registered in node_modules, and never runs lifecycle scripts.
func (op *Operation) materializeConfigDeps(ctx context.Context, client *registry.Client, pkg *project.PackageJSON) error {
	merged := map[string]string{}
	for name, version := range pkg.ConfigDependencies {
		merged[name] = version
	}
	if project.DetectMode(op.ProjectRoot) == project.ModePnpm {
		for name, version := range project.ReadPnpmWorkspace(op.ProjectRoot).ConfigDependencies {
			merged[name] = version
		}
	}
	if len(merged) == 0 {
		return nil
	}
	root := filepath.Join(op.ProjectRoot, "node_modules", ".gnpm-config")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return core.IOError("creating node_modules/.gnpm-config").Wrap(err)
	}

	names := make([]string, 0, len(merged))
	for n := range merged {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		version := merged[name]
		dest := filepath.Join(root, filepath.FromSlash(name))
		if _, err := os.Stat(dest); err == nil {
			continue // already materialized
		}
		pack, err := client.Packument(ctx, name, false)
		if err != nil {
			return err
		}
		slice := pack.Versions[version]
		if slice == nil {
			return core.Usage("configDependencies: %s@%s not found on registry", name, version)
		}
		if slice.Tarball == "" || slice.Integrity == "" {
			return core.Usage("configDependencies: %s@%s has no tarball/integrity", name, version)
		}
		data, err := client.Tarball(ctx, slice.Tarball, slice.Integrity)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return core.IOError("creating config dep dir %s", name).Wrap(err)
		}
		if _, err := archive.Extract(bytes.NewReader(data), dest); err != nil {
			return err
		}
	}
	return nil
}
