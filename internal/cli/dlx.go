package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/installer"
)

// stringList is a repeatable flag value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cmdDlx(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("dlx", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var pkgs stringList
	fs.Var(&pkgs, "p", "package to install (repeatable)")
	call := fs.String("c", "", "binary to call")
	fs.StringVar(call, "call", "", "binary to call")
	offline := fs.Bool("offline", false, "forbid network access")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()

	// Determine the installed package set, the bin to run, and its args.
	var installSpecs []string
	var binName string
	var binArgs []string
	if len(pkgs) > 0 {
		installSpecs = pkgs
		if *call != "" {
			binName = *call
			binArgs = rest
		} else {
			if len(rest) == 0 {
				return core.Usage("dlx with -p requires a binary name (or use -c)")
			}
			binName, binArgs = rest[0], rest[1:]
		}
	} else {
		if len(rest) == 0 {
			return core.Usage("dlx requires a package")
		}
		installSpecs = []string{rest[0]}
		binArgs = rest[1:]
		if *call != "" {
			binName = *call
		} else {
			name, _ := splitNameSpec(rest[0])
			if name == "" {
				return core.Usage("malformed package spec %q", rest[0])
			}
			binName = lastPathSeg(name)
		}
	}

	// Build the name→version map and the cache key.
	deps := map[string]string{}
	var rendered []string
	for _, spec := range installSpecs {
		name, ver := splitNameSpec(spec)
		if name == "" {
			return core.Usage("malformed package spec %q", spec)
		}
		if ver == "" {
			ver = "latest"
		}
		deps[name] = ver
		rendered = append(rendered, name+"@"+ver)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return core.Usage("dlx could not locate the user's home directory")
	}
	cacheDir := filepath.Join(home, ".gnpm", "dlx", dlxKey(rendered))
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return core.IOError("creating dlx cache").Wrap(err)
	}

	// Materialize a temp project and install (scripts always skipped,
	// optimistic repeat off so the cache dir is always re-linked).
	pkgBody, _ := json.MarshalIndent(map[string]any{"name": "gnpm-dlx", "version": "0.0.0", "dependencies": deps}, "", "  ")
	if err := os.WriteFile(filepath.Join(cacheDir, "package.json"), pkgBody, 0o644); err != nil {
		return core.IOError("writing dlx package.json").Wrap(err)
	}
	opts := installer.DefaultOptions()
	opts.ScriptPolicy = installer.ScriptNone
	opts.IgnoreScripts = true
	opts.OptimisticRepeatInstall = false
	opts.Offline = *offline
	if _, err := (&installer.Operation{ProjectRoot: cacheDir, Options: opts, Log: env.Log, Version: Version}).Run(ctx); err != nil {
		return err
	}

	binPath := filepath.Join(cacheDir, "node_modules", ".bin", binName)
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(binPath + ".cmd"); err == nil {
			binPath += ".cmd"
		}
	}
	if _, err := os.Stat(binPath); err != nil {
		return core.Usage("dlx: binary %q not found after install; pass --call to disambiguate", binName)
	}
	cmd := exec.CommandContext(ctx, binPath, binArgs...)
	cmd.Dir = env.Cwd
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(cacheDir, "node_modules", ".bin")+pathSep()+os.Getenv("PATH"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, env.Stdout, env.Stderr
	return propagate(cmd.Run())
}

// dlxKey derives the cache key: sorted name@version strings joined by
// newline, SHA-256'd, first 16 hex chars (doc/spec.md §10.1).
func dlxKey(rendered []string) string {
	sort.Strings(rendered)
	sum := sha256.Sum256([]byte(strings.Join(rendered, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

func lastPathSeg(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func pathSep() string {
	if runtime.GOOS == "windows" {
		return ";"
	}
	return ":"
}
