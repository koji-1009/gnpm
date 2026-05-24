package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/semver"
)

func cmdList(ctx context.Context, env *Env, args []string) error {
	lock, err := loadLockfileOrUsage(env)
	if err != nil {
		return err
	}
	byName := indexByName(lock)
	root := lock.Importers["."]
	roots := mergeKeys(root.Dependencies, root.DevDependencies, root.OptionalDependencies)
	sort.Strings(roots)

	visited := map[string]bool{}
	var print func(name string, depth int)
	print = func(name string, depth int) {
		p, ok := byName[name]
		if !ok {
			return
		}
		fmt.Fprintf(env.Stdout, "%s%s@%s\n", strings.Repeat("  ", depth), p.Name, p.Version)
		if visited[name] {
			return
		}
		visited[name] = true
		children := sortedMapKeys(p.Dependencies)
		for _, c := range children {
			print(c, depth+1)
		}
	}
	for _, r := range roots {
		print(r, 0)
	}
	return nil
}

func cmdWhy(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("why", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return core.Usage("why requires a package name")
	}
	target := fs.Arg(0)
	lock, err := loadLockfileOrUsage(env)
	if err != nil {
		return err
	}
	byName := indexByName(lock)
	if _, ok := byName[target]; !ok {
		return core.Recoverable("%s is not present in the dependency graph", target)
	}

	// Reverse edges: child → parents.
	parents := map[string][]string{}
	for _, p := range lock.Packages {
		for dep := range p.Dependencies {
			parents[dep] = append(parents[dep], p.Name)
		}
	}
	root := lock.Importers["."]
	directRoots := mergeKeys(root.Dependencies, root.DevDependencies, root.OptionalDependencies)
	directSet := map[string]bool{}
	for _, d := range directRoots {
		directSet[d] = true
	}

	// Print each path from a direct dependency down to target.
	var path []string
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		path = append(path, name)
		if directSet[name] {
			rev := append([]string(nil), path...)
			for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
				rev[i], rev[j] = rev[j], rev[i]
			}
			fmt.Fprintln(env.Stdout, strings.Join(rev, " > "))
		}
		ps := parents[name]
		sort.Strings(ps)
		for _, p := range ps {
			walk(p)
		}
		path = path[:len(path)-1]
		seen[name] = false
	}
	walk(target)
	return nil
}

func cmdOutdated(ctx context.Context, env *Env, args []string) error {
	lock, err := loadLockfileOrUsage(env)
	if err != nil {
		return err
	}
	client, err := newRegistryClient(env)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, p := range lock.Packages {
		names[p.Name] = p.Version
	}
	sorted := sortedMapKeys(names)
	any := false
	for _, name := range sorted {
		pack, err := client.Packument(ctx, name, false)
		if err != nil {
			continue
		}
		latest := pack.Latest()
		if latest == "" || latest == names[name] {
			continue
		}
		cur, lok := semver.TryParse(names[name])
		lt, rok := semver.TryParse(latest)
		if lok && rok && !cur.Less(lt) {
			continue
		}
		any = true
		fmt.Fprintf(env.Stdout, "%s  %s  →  %s\n", name, names[name], latest)
	}
	if !any {
		env.Log.Info("all dependencies are up to date")
	}
	return nil
}

func cmdView(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return core.Usage("view requires a package name")
	}
	name, _ := splitNameSpec(fs.Arg(0))
	field := ""
	if fs.NArg() > 1 {
		field = fs.Arg(1)
	}
	client, err := newRegistryClient(env)
	if err != nil {
		return err
	}
	pack, err := client.Packument(ctx, name, false)
	if err != nil {
		return err
	}
	versions := sortedMapKeys(toAnyKeys(pack.Versions))
	if field == "" {
		out := map[string]any{"name": pack.Name, "dist-tags": pack.DistTags, "versions": versions}
		return printJSON(env, out, true)
	}
	switch field {
	case "name":
		return printJSON(env, pack.Name, true)
	case "versions":
		return printJSON(env, versions, true)
	case "dist-tags":
		return printJSON(env, pack.DistTags, true)
	case "latest":
		if l := pack.Latest(); l != "" {
			return printJSON(env, l, true)
		}
		return printJSON(env, nil, true)
	default:
		return printJSON(env, nil, true)
	}
}

// --- shared helpers ---------------------------------------------------

func loadLockfileOrUsage(env *Env) (*lockfile.Lockfile, error) {
	cfg, _ := npmrc.Loader{ProjectDir: env.Cwd}.Load()
	reg := npmrc.DefaultRegistry
	if cfg != nil {
		reg = cfg.Registry()
	}
	lock, err := lockfile.Read(env.Cwd, reg)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, core.Usage("no lockfile found; run `gnpm install` first")
	}
	return lock, nil
}

func newRegistryClient(env *Env) (*registry.Client, error) {
	cfg, err := npmrc.Loader{ProjectDir: env.Cwd}.Load()
	if err != nil {
		return nil, err
	}
	return registry.NewClient(registry.Options{Config: cfg, UserAgent: "gnpm/" + Version}), nil
}

func indexByName(lock *lockfile.Lockfile) map[string]lockfile.LockedPackage {
	out := map[string]lockfile.LockedPackage{}
	for _, p := range lock.Packages {
		out[p.Name] = p
	}
	return out
}

func mergeKeys(maps ...map[string]string) []string {
	set := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func toAnyKeys[V any](m map[string]V) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func printJSON(env *Env, v any, indent bool) error {
	var body []byte
	var err error
	if indent {
		body, err = json.MarshalIndent(v, "", "  ")
	} else {
		body, err = json.Marshal(v)
	}
	if err != nil {
		return core.IOError("encoding JSON: %v", err)
	}
	fmt.Fprintln(env.Stdout, string(body))
	return nil
}
