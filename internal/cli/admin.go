package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/pkgedit"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/semver"
)

// --- pkg --------------------------------------------------------------

func cmdPkg(ctx context.Context, env *Env, args []string) error {
	if len(args) == 0 {
		return core.Usage("pkg requires a subcommand: get, set, or delete")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "get":
		return pkgGet(env, rest)
	case "set":
		return pkgSet(env, rest)
	case "delete":
		return pkgDelete(env, rest)
	default:
		return core.Usage("unknown pkg subcommand %q", sub)
	}
}

func pkgGet(env *Env, paths []string) error {
	if len(paths) == 0 {
		return core.Usage("pkg get requires a field path")
	}
	doc, err := pkgedit.Load(packageJSONPath(env))
	if err != nil {
		return err
	}
	if len(paths) == 1 {
		v, _ := getPath(doc.Values, paths[0])
		return printJSON(env, v, false)
	}
	out := map[string]any{}
	for _, p := range paths {
		v, _ := getPath(doc.Values, p)
		out[p] = v
	}
	return printJSON(env, out, true)
}

func pkgSet(env *Env, pairs []string) error {
	if len(pairs) == 0 {
		return core.Usage("pkg set requires path=value")
	}
	doc, err := pkgedit.Load(packageJSONPath(env))
	if err != nil {
		return err
	}
	for _, kv := range pairs {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return core.Usage("pkg set argument %q is not in path=value form", kv)
		}
		path, raw := kv[:eq], kv[eq+1:]
		var value any
		if json.Unmarshal([]byte(raw), &value) != nil {
			value = raw // not valid JSON → treat as a string
		}
		setPath(doc.Values, path, value)
	}
	return doc.Save()
}

func pkgDelete(env *Env, paths []string) error {
	if len(paths) == 0 {
		return core.Usage("pkg delete requires a field path")
	}
	doc, err := pkgedit.Load(packageJSONPath(env))
	if err != nil {
		return err
	}
	for _, p := range paths {
		deletePath(doc.Values, p)
	}
	return doc.Save()
}

func getPath(m map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

func deletePath(m map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

// --- config -----------------------------------------------------------

func cmdConfig(ctx context.Context, env *Env, args []string) error {
	if len(args) == 0 {
		return core.Usage("config requires a subcommand: get, set, or delete")
	}
	sub, rest := args[0], args[1:]
	npmrcPath := filepath.Join(env.Cwd, ".npmrc")
	switch sub {
	case "get":
		if len(rest) == 0 {
			return core.Usage("config get requires a key")
		}
		cfg, err := npmrc.Loader{ProjectDir: env.Cwd}.Load()
		if err != nil {
			return err
		}
		v, _ := cfg.Get(rest[0])
		fmt.Fprintln(env.Stdout, v)
		return nil
	case "set":
		if len(rest) < 2 {
			return core.Usage("config set requires a key and value")
		}
		return npmrcSet(npmrcPath, strings.ToLower(rest[0]), rest[1])
	case "delete":
		if len(rest) == 0 {
			return core.Usage("config delete requires a key")
		}
		return npmrcDelete(npmrcPath, strings.ToLower(rest[0]))
	default:
		return core.Usage("unknown config subcommand %q", sub)
	}
}

func npmrcSet(path, key, value string) error {
	lines := readLines(path)
	found := false
	for i, line := range lines {
		if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(strings.ToLower(k)) == key {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	return writeLines(path, lines)
}

func npmrcDelete(path, key string) error {
	lines := readLines(path)
	out := lines[:0]
	for _, line := range lines {
		if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(strings.ToLower(k)) == key {
			continue
		}
		out = append(out, line)
	}
	return writeLines(path, out)
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func writeLines(path string, lines []string) error {
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return core.IOError("writing %s", path).Wrap(err)
	}
	return nil
}

// --- clean ------------------------------------------------------------

func cmdClean(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	deleteLock := fs.Bool("delete-lockfile", false, "also remove the lockfile")
	dryRun := fs.Bool("dry-run", false, "print what would be removed")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	targets := []string{filepath.Join(env.Cwd, "node_modules")}
	if *deleteLock {
		mode := project.DetectMode(env.Cwd)
		targets = append(targets, filepath.Join(env.Cwd, lockfile.ProjectLockfileName(mode)))
	}
	for _, t := range targets {
		if _, err := os.Stat(t); err != nil {
			continue
		}
		if *dryRun {
			fmt.Fprintf(env.Stdout, "would remove %s\n", t)
			continue
		}
		if err := os.RemoveAll(t); err != nil {
			return core.Recoverable("failed to remove %s: %v", t, err)
		}
		env.Log.Info("removed %s", t)
	}
	return nil
}

// --- peers ------------------------------------------------------------

func cmdPeers(ctx context.Context, env *Env, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return core.Usage("peers requires the `check` subcommand")
	}
	cfg, _ := npmrc.Loader{ProjectDir: env.Cwd}.Load()
	reg := npmrc.DefaultRegistry
	if cfg != nil {
		reg = cfg.Registry()
	}
	lock, err := lockfile.Read(env.Cwd, reg)
	if err != nil {
		return err
	}
	if lock == nil {
		return core.Recoverable("no lockfile found; run `gnpm install` first")
	}
	installed := map[string]string{}
	for _, p := range lock.Packages {
		installed[p.Name] = p.Version
	}

	var unmet []string
	for _, p := range lock.Packages {
		for peer, rng := range p.PeerDependencies {
			if p.PeerDependenciesMeta[peer].Optional {
				continue
			}
			have, ok := installed[peer]
			if !ok {
				unmet = append(unmet, fmt.Sprintf("%s requires peer %s@%s (missing)", p.Name, peer, rng))
				continue
			}
			r, perr := semver.ParseRange(rng)
			v, verr := semver.Parse(have)
			if perr == nil && verr == nil && !r.Satisfies(v) {
				unmet = append(unmet, fmt.Sprintf("%s requires peer %s@%s (have %s)", p.Name, peer, rng, have))
			}
		}
	}
	if len(unmet) == 0 {
		env.Log.Info("all peer dependencies satisfied")
		return nil
	}
	sort.Strings(unmet)
	for _, u := range unmet {
		fmt.Fprintln(env.Stdout, u)
	}
	return core.Recoverable("%d unmet peer dependenc%s", len(unmet), plural(len(unmet)))
}

// --- doctor -----------------------------------------------------------

func cmdDoctor(ctx context.Context, env *Env, args []string) error {
	cfg, err := npmrc.Loader{ProjectDir: env.Cwd}.Load()
	if err != nil {
		return err
	}
	mode := project.DetectMode(env.Cwd)
	fmt.Fprintf(env.Stdout, "project mode:   %s\n", mode)
	fmt.Fprintf(env.Stdout, "registry:       %s\n", cfg.Registry())
	named := cfg.NamedRegistries()
	for _, alias := range sortedMapKeys(named) {
		fmt.Fprintf(env.Stdout, "named registry: %s → %s\n", alias, named[alias])
	}

	failed := false
	if _, err := exec.LookPath("node"); err != nil {
		fmt.Fprintln(env.Stdout, "node:           NOT FOUND on PATH")
		failed = true
	} else {
		fmt.Fprintln(env.Stdout, "node:           found on PATH")
	}

	if reachable(ctx, cfg.Registry()) {
		fmt.Fprintln(env.Stdout, "reachability:   registry reachable")
	} else {
		fmt.Fprintln(env.Stdout, "reachability:   registry UNREACHABLE")
		failed = true
	}

	if failed {
		return core.Recoverable("doctor found at least one problem")
	}
	return nil
}

func reachable(ctx context.Context, registryURL string) bool {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, registryURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
