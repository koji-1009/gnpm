package cli

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/installer"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/workspacestate"
)

func cmdRun(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return core.Usage("run requires a script name")
	}
	scriptName := rest[0]
	scriptArgs := rest[1:]
	// Strip a leading "--" separator if present.
	if len(scriptArgs) > 0 && scriptArgs[0] == "--" {
		scriptArgs = scriptArgs[1:]
	}

	pkg, err := project.ReadPackageJSON(packageJSONPath(env))
	if err != nil {
		return err
	}
	command, ok := pkg.Scripts[scriptName]
	if !ok {
		return core.Usage("no script named %q in package.json", scriptName)
	}

	if err := verifyDepsBeforeRun(ctx, env); err != nil {
		return err
	}

	full := command
	if len(scriptArgs) > 0 {
		full += " " + strings.Join(scriptArgs, " ")
	}
	return spawnShell(ctx, env, full)
}

func cmdExec(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return core.Usage("exec requires a binary name")
	}
	bin := rest[0]
	binPath := filepath.Join(env.Cwd, "node_modules", ".bin", bin)
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(binPath + ".cmd"); err == nil {
			binPath += ".cmd"
		}
	}
	if _, err := os.Stat(binPath); err != nil {
		return core.Usage("no binary named %q in node_modules/.bin", bin)
	}

	if err := verifyDepsBeforeRun(ctx, env); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, binPath, rest[1:]...)
	cmd.Dir = env.Cwd
	cmd.Env = runEnv(env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, env.Stdout, env.Stderr
	return propagate(cmd.Run())
}

// spawnShell runs command through the system shell with node_modules/.bin
// on PATH, propagating the child's exit code.
func spawnShell(ctx context.Context, env *Env, command string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = env.Cwd
	cmd.Env = runEnv(env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, env.Stdout, env.Stderr
	return propagate(cmd.Run())
}

// runEnv inherits the host environment and prepends node_modules/.bin to
// PATH (run/exec, unlike install scripts, run with the full host env).
func runEnv(env *Env) []string {
	binDir := filepath.Join(env.Cwd, "node_modules", ".bin")
	out := os.Environ()
	pathKey := "PATH"
	sep := ":"
	if runtime.GOOS == "windows" {
		pathKey, sep = "Path", ";"
	}
	for i, kv := range out {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, pathKey) {
			out[i] = k + "=" + binDir + sep + v
			return out
		}
	}
	return append(out, pathKey+"="+binDir)
}

// propagate maps a child process's exit error to an ExitError carrying
// the same code so the parent exits with it verbatim (doc/spec.md §5.1).
func propagate(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if asExit(err, &exitErr) {
		return &core.ExitError{Code: exitErr.ExitCode()}
	}
	return core.IOError("running command: %v", err)
}

// verifyDepsBeforeRun applies the verify-deps-before-run policy before a
// run/exec (doc/spec.md §2.4).
func verifyDepsBeforeRun(ctx context.Context, env *Env) error {
	policy := resolveVerifyPolicy(env)
	if policy == workspacestate.VerifyOff {
		return nil
	}
	upToDate, err := installer.WorkspaceUpToDate(env.Cwd)
	if err != nil || upToDate {
		return err
	}
	switch policy {
	case workspacestate.VerifyWarn:
		env.Log.Warn("dependencies are out of date (verify-deps-before-run=warn)")
		return nil
	case workspacestate.VerifyInstall:
		env.Log.Info("dependencies are out of date — installing first")
		_, err := (&installer.Operation{ProjectRoot: env.Cwd, Options: installer.DefaultOptions(), Log: env.Log, Version: Version}).Run(ctx)
		return err
	default: // error, or prompt in a non-TTY
		return core.Usage("dependencies are out of date; run `gnpm install` first")
	}
}

func resolveVerifyPolicy(env *Env) workspacestate.VerifyPolicy {
	value := lookupSetting(env.Cwd, "verify-deps-before-run")
	if value == "" {
		return workspacestate.VerifyInstall
	}
	p, _ := workspacestate.ParseVerifyPolicy(value)
	return p
}

// lookupSetting resolves a kebab-case setting from .npmrc, falling back to
// pnpm-workspace.yaml settings in pnpm mode.
func lookupSetting(root, key string) string {
	cfg, err := npmrc.Loader{ProjectDir: root}.Load()
	if err == nil {
		if v, ok := cfg.Get(key); ok {
			return v
		}
	}
	if project.DetectMode(root) == project.ModePnpm {
		if v, ok := project.ReadPnpmWorkspace(root).Settings[key]; ok {
			return v
		}
	}
	return ""
}

func asExit(err error, target **exec.ExitError) bool {
	for err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
		} else {
			return false
		}
	}
	return false
}
