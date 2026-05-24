// Package cli implements gnpm's command dispatch: global flag parsing,
// the command table, and the exit-code contract from doc/spec.md §5.
//
// Dispatch is built on the standard library only — there is no
// third-party CLI framework — which keeps the binary small and gives
// precise control over the 0 / 1 / 64 / 70 exit codes the spec mandates.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/koji-1009/gnpm/internal/core"
)

// Version is reported by `gnpm --version` and sent as the registry
// User-Agent.
const Version = "0.0.1-dev"

// Env is the per-invocation context handed to every command: resolved
// global options plus the standard streams and working directory.
type Env struct {
	Log    *core.Logger
	JSON   bool
	Color  bool
	Stdout io.Writer
	Stderr io.Writer
	Cwd    string
}

// Command is one entry in the dispatch table.
type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, env *Env, args []string) error
}

// Main is the process entry point. It returns the exit code; cmd/gnpm
// passes it to os.Exit. A panic escaping a command is mapped to exit 70
// with the stack written to stderr, matching the spec's "unhandled
// exception" row.
func Main(args []string) (code int) {
	stderr := os.Stderr
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "gnpm: panic: %v\n", r)
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, false)
			stderr.Write(buf[:n])
			code = core.ExitSoftware
		}
	}()

	err := run(context.Background(), args, os.Stdout, stderr)
	if err != nil {
		// UsageError prints with the usage hint; everything else
		// prints its message. Diagnostics always go to stderr.
		fmt.Fprintf(stderr, "gnpm: %s\n", err.Error())
		return core.ExitCodeFor(err)
	}
	return core.ExitOK
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	g, err := parseGlobal(args)
	if err != nil {
		return err
	}

	if g.showVersion {
		fmt.Fprintf(stdout, "gnpm %s\n", Version)
		fmt.Fprintf(stdout, "%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	}

	if g.command == "" || g.showHelp {
		printUsage(stdout)
		if g.command == "" && !g.showHelp {
			// No command and not an explicit --help is a usage error.
			return core.Usage("no command given")
		}
		return nil
	}

	cmd := lookup(g.command)
	if cmd == nil {
		return core.Usage("unknown command %q (run `gnpm --help`)", g.command)
	}

	cwd, _ := os.Getwd()
	env := &Env{
		Log:    core.NewLogger("gnpm", g.level),
		JSON:   g.json,
		Color:  g.color,
		Stdout: stdout,
		Stderr: stderr,
		Cwd:    cwd,
	}
	return cmd.Run(ctx, env, g.commandArgs)
}

// globalOpts holds the parsed top-level flags and the command split.
type globalOpts struct {
	level       core.LogLevel
	json        bool
	color       bool
	showVersion bool
	showHelp    bool
	command     string
	commandArgs []string
}

// parseGlobal consumes leading global flags and splits out the command
// and its arguments. The first non-flag token is the command; every
// token after it is passed through verbatim so the command parses its
// own flags. Unknown flags appearing before any command are a usage
// error; unknown flags after the command belong to the command.
func parseGlobal(args []string) (globalOpts, error) {
	g := globalOpts{level: core.LevelInfo, color: true}
	var (
		silent    bool
		verbose   bool
		levelText string
		levelSet  bool
	)

	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") {
			g.command = a
			g.commandArgs = args[i+1:]
			break
		}
		switch {
		case a == "--version":
			g.showVersion = true
		case a == "--help" || a == "-h":
			g.showHelp = true
		case a == "--silent":
			silent = true
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--json":
			g.json = true
		case a == "--color":
			g.color = true
		case a == "--no-color":
			g.color = false
		case a == "--loglevel":
			if i+1 >= len(args) {
				return g, core.Usage("--loglevel requires a value")
			}
			i++
			levelText, levelSet = args[i], true
		case strings.HasPrefix(a, "--loglevel="):
			levelText, levelSet = strings.TrimPrefix(a, "--loglevel="), true
		default:
			return g, core.Usage("unknown flag %q", a)
		}
		i++
	}

	// If the loop broke on "--" the next token (if any) is the command.
	if g.command == "" && i < len(args) && args[i] != "" {
		g.command = args[i]
		g.commandArgs = args[i+1:]
	}

	// Resolve the effective level: --loglevel wins, then --silent /
	// --verbose, else info.
	switch {
	case levelSet:
		lvl, ok := core.ParseLogLevel(levelText)
		if !ok {
			return g, core.Usage("invalid --loglevel %q", levelText)
		}
		g.level = lvl
	case silent:
		g.level = core.LevelSilent
	case verbose:
		g.level = core.LevelDebug
	}
	return g, nil
}

func lookup(name string) *Command {
	for i := range commands {
		if commands[i].Name == name {
			return &commands[i]
		}
	}
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "gnpm %s — npm/pnpm-compatible package manager\n\n", Version)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  gnpm [global flags] <command> [args]")
	fmt.Fprintln(w, "\nGlobal flags:")
	fmt.Fprintln(w, "  --silent                 suppress progress output")
	fmt.Fprintln(w, "  -v, --verbose            print per-package events")
	fmt.Fprintln(w, "  --loglevel <level>       silent|error|warn|info|debug|trace")
	fmt.Fprintln(w, "  --color / --no-color     colorize output")
	fmt.Fprintln(w, "  --json                   machine-readable output (where supported)")
	fmt.Fprintln(w, "  --version                print version and exit")
	fmt.Fprintln(w, "\nCommands:")

	names := make([]string, len(commands))
	for i := range commands {
		names[i] = commands[i].Name
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-10s %s\n", n, lookup(n).Summary)
	}
}
