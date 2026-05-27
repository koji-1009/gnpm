package cli

import (
	"context"
	"flag"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/audit"
	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/installer"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/pkgedit"
	"github.com/koji-1009/gnpm/internal/signature"
)

// installFlags binds the shared install option flags onto a FlagSet.
type installFlags struct {
	frozen           bool
	ignoreScripts    bool
	allowScripts     string
	minReleaseAge    int
	offline          bool
	preferOffline    bool
	production       bool
	engineStrict     bool
	enforceSignature string
	auditLevel       string
}

func bindInstallFlags(fs *flag.FlagSet) *installFlags {
	f := &installFlags{}
	fs.BoolVar(&f.frozen, "frozen-lockfile", false, "fail if the lockfile would change")
	fs.BoolVar(&f.ignoreScripts, "ignore-scripts", false, "do not run lifecycle scripts")
	fs.StringVar(&f.allowScripts, "allow-scripts", "allowlist", "none|allowlist|all")
	fs.IntVar(&f.minReleaseAge, "min-release-age", -1, "minimum release age in minutes (default: 1440 in pnpm mode, 0 in npm mode)")
	fs.BoolVar(&f.offline, "offline", false, "forbid network access")
	fs.BoolVar(&f.preferOffline, "prefer-offline", false, "prefer cached data")
	fs.BoolVar(&f.production, "production", false, "skip devDependencies")
	fs.BoolVar(&f.engineStrict, "engine-strict", false, "fail on engine mismatch")
	fs.StringVar(&f.enforceSignature, "enforce-signatures", "none", "none|weak|strict")
	fs.StringVar(&f.auditLevel, "audit-level", "", "run a post-install audit failing at low|moderate|high|critical")
	return f
}

func (f *installFlags) options() (installer.Options, error) {
	opts := installer.DefaultOptions()
	switch f.allowScripts {
	case "none":
		opts.ScriptPolicy = installer.ScriptNone
	case "allowlist":
		opts.ScriptPolicy = installer.ScriptAllowlist
	case "all":
		opts.ScriptPolicy = installer.ScriptAll
		opts.DangerouslyAllowAllBuilds = true
	default:
		return opts, core.Usage("--allow-scripts must be none|allowlist|all, got %q", f.allowScripts)
	}
	// -1 is the sentinel for "unset" (use the mode default); anything below
	// that is a user error.
	if f.minReleaseAge < -1 {
		return opts, core.Usage("--min-release-age must be non-negative")
	}
	sigPolicy, ok := signature.ParsePolicy(f.enforceSignature)
	if !ok {
		return opts, core.Usage("--enforce-signatures must be none|weak|strict, got %q", f.enforceSignature)
	}
	opts.SignaturePolicy = sigPolicy
	if f.auditLevel != "" {
		level := audit.ParseSeverity(f.auditLevel)
		if level == audit.SevUnknown {
			return opts, core.Usage("--audit-level must be low|moderate|high|critical, got %q", f.auditLevel)
		}
		opts.AuditLevel = level
	}
	opts.FrozenLockfile = f.frozen
	opts.IgnoreScripts = f.ignoreScripts
	opts.Offline = f.offline
	opts.PreferOffline = f.preferOffline
	opts.Production = f.production
	opts.EngineStrict = f.engineStrict
	opts.MinReleaseAge = minutes(f.minReleaseAge)
	return opts, nil
}

func runInstall(ctx context.Context, env *Env, opts installer.Options) error {
	op := &installer.Operation{ProjectRoot: env.Cwd, Options: opts, Log: env.Log, Version: Version}
	report, err := op.Run(ctx)
	if err != nil {
		return err
	}
	env.Log.Info("done — %d package(s) linked", report.Added)
	return nil
}

func cmdInstall(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	f := bindInstallFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	opts, err := f.options()
	if err != nil {
		return err
	}
	return runInstall(ctx, env, opts)
}

func cmdCi(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("ci", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	f := bindInstallFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	opts, err := f.options()
	if err != nil {
		return err
	}
	opts.FrozenLockfile = true // ci is a locked install
	return runInstall(ctx, env, opts)
}

func cmdAdd(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	saveDev := fs.Bool("save-dev", false, "add to devDependencies")
	fs.BoolVar(saveDev, "D", false, "add to devDependencies (shorthand)")
	saveOptional := fs.Bool("save-optional", false, "add to optionalDependencies")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		return core.Usage("add requires at least one package")
	}
	field := "dependencies"
	if *saveDev {
		field = "devDependencies"
	} else if *saveOptional {
		field = "optionalDependencies"
	}

	doc, err := pkgedit.Load(packageJSONPath(env))
	if err != nil {
		return err
	}
	bareNames := []string{}
	deps := doc.DepField(field, true)
	for _, arg := range names {
		name, spec := splitNameSpec(arg)
		if name == "" {
			return core.Usage("invalid package %q", arg)
		}
		if spec == "" {
			deps[name] = "latest"
			bareNames = append(bareNames, name)
		} else {
			deps[name] = spec
		}
	}
	if err := doc.Save(); err != nil {
		return err
	}

	if err := runInstall(ctx, env, installer.DefaultOptions()); err != nil {
		return err
	}
	// Rewrite bare-name adds to a caret range over the resolved version.
	return pinResolvedVersions(env, field, bareNames)
}

func cmdRemove(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		return core.Usage("remove requires at least one package")
	}
	doc, err := pkgedit.Load(packageJSONPath(env))
	if err != nil {
		return err
	}
	removed := false
	for _, field := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		if deps := doc.DepField(field, false); deps != nil {
			for _, name := range names {
				if _, ok := deps[name]; ok {
					delete(deps, name)
					removed = true
				}
			}
		}
	}
	if !removed {
		env.Log.Warn("none of the named packages were in package.json")
	}
	if err := doc.Save(); err != nil {
		return err
	}
	return runInstall(ctx, env, installer.DefaultOptions())
}

func cmdUpdate(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	opts := installer.DefaultOptions()
	opts.Update = true
	return runInstall(ctx, env, opts)
}

// pinResolvedVersions rewrites bare-name dependencies to "^<version>"
// using the freshly written lockfile, mirroring `npm add`.
func pinResolvedVersions(env *Env, field string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	lock, err := lockfile.Read(env.Cwd, "")
	if err != nil || lock == nil {
		return nil
	}
	versionByName := map[string]string{}
	for _, p := range lock.Packages {
		versionByName[p.Name] = p.Version
	}
	doc, err := pkgedit.Load(packageJSONPath(env))
	if err != nil {
		return nil
	}
	deps := doc.DepField(field, false)
	if deps == nil {
		return nil
	}
	changed := false
	for _, name := range names {
		if v, ok := versionByName[name]; ok {
			deps[name] = "^" + v
			changed = true
		}
	}
	if changed {
		return doc.Save()
	}
	return nil
}

func packageJSONPath(env *Env) string {
	return env.Cwd + "/package.json"
}

// splitNameSpec splits "name@spec" into (name, spec), handling scoped
// names (@scope/name@spec).
func splitNameSpec(arg string) (string, string) {
	if strings.HasPrefix(arg, "@") {
		if at := strings.IndexByte(arg[1:], '@'); at >= 0 {
			return arg[:at+1], arg[at+2:]
		}
		return arg, ""
	}
	if at := strings.IndexByte(arg, '@'); at > 0 {
		return arg[:at], arg[at+1:]
	}
	return arg, ""
}

// parseFlags parses args, converting flag errors to UsageError (exit 64).
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return core.Usage("%v", err)
	}
	return nil
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }
