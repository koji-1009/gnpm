package scripts

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
)

// LifecycleEvent is an install-time npm lifecycle event. Publish-bound
// events are not enumerated — gnpm has no publish flow.
type LifecycleEvent string

const (
	Preinstall  LifecycleEvent = "preinstall"
	Install     LifecycleEvent = "install"
	Postinstall LifecycleEvent = "postinstall"
	Prepare     LifecycleEvent = "prepare"
)

// InstallEvents is the per-dependency install-time event order.
var InstallEvents = []LifecycleEvent{Preinstall, Install, Postinstall, Prepare}

// Script is one lifecycle script invocation.
type Script struct {
	Event          LifecycleEvent
	PackageName    string
	PackageVersion string
	WorkingDir     string
	Command        string
}

// Result is the outcome of running a script.
type Result struct {
	ExitCode int
	Stdout   string
}

// envPassthrough lists the host env vars forwarded to lifecycle scripts
// (doc/spec.md §2.5). PATH is handled separately so node_modules/.bin can
// be prepended.
var envPassthrough = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "TERM", "PWD", "LANG",
	"LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TMPDIR",
	"USERPROFILE", "USERNAME", "COMPUTERNAME", "SYSTEMROOT", "WINDIR", "TEMP", "TMP",
	"NODE_OPTIONS", "CI",
}

// BuildEnv constructs the restricted environment for a lifecycle script:
// the passthrough allowlist, npm_* metadata, and PATH with binDir
// prepended. initCwd defaults to the current directory.
func BuildEnv(s Script, binDir, initCwd string) []string {
	env := map[string]string{}
	for _, name := range envPassthrough {
		if v, ok := os.LookupEnv(name); ok {
			env[name] = v
		}
	}
	pathKey := "PATH"
	if runtime.GOOS == "windows" {
		pathKey = "Path"
	}
	hostPath := os.Getenv(pathKey)
	if hostPath == "" {
		hostPath = os.Getenv("PATH")
	}
	if binDir != "" {
		sep := ":"
		if runtime.GOOS == "windows" {
			sep = ";"
		}
		if hostPath == "" {
			env[pathKey] = binDir
		} else {
			env[pathKey] = binDir + sep + hostPath
		}
	} else if hostPath != "" {
		env[pathKey] = hostPath
	}

	env["npm_lifecycle_event"] = string(s.Event)
	env["npm_package_name"] = s.PackageName
	env["npm_package_version"] = s.PackageVersion
	if initCwd == "" {
		initCwd, _ = os.Getwd()
	}
	env["INIT_CWD"] = initCwd

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// Runner executes lifecycle scripts via the system shell.
type Runner struct {
	// Timeout bounds each script (default 10 minutes). Exceeding it kills
	// the process.
	Timeout time.Duration
	// ShellPath overrides the POSIX shell (default /bin/sh).
	ShellPath string
}

// NewRunner returns a Runner with the default 10-minute timeout.
func NewRunner() *Runner { return &Runner{Timeout: 10 * time.Minute} }

// Run executes s with binDir prepended to PATH. It returns a ScriptError
// on non-zero exit or timeout.
func (r *Runner) Run(ctx context.Context, s Script, binDir string) (Result, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := r.commandSplit(s.Command)
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = s.WorkingDir
	cmd.Env = BuildEnv(s, binDir, "")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return Result{ExitCode: -1}, core.ScriptError("%s script for %s@%s timed out after %s",
			s.Event, s.PackageName, s.PackageVersion, timeout)
	}
	if err != nil {
		code := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return Result{ExitCode: code, Stdout: stdout.String()}, &core.Error{
			Kind:    core.KindScript,
			Message: scriptFailMsg(s, code),
		}
	}
	return Result{ExitCode: 0, Stdout: stdout.String()}, nil
}

func (r *Runner) commandSplit(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C", command}
	}
	shell := r.ShellPath
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell, []string{"-c", command}
}

func scriptFailMsg(s Script, code int) string {
	return string(s.Event) + " script for " + s.PackageName + "@" + s.PackageVersion +
		" exited with code " + itoa(code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
