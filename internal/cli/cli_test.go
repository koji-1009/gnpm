package cli

import (
	"reflect"
	"testing"

	"github.com/koji-1009/gnpm/internal/core"
)

func TestParseGlobal(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCmd  string
		wantArgs []string
		wantLvl  core.LogLevel
		wantJSON bool
		wantErr  bool
	}{
		{name: "bare command", args: []string{"install"}, wantCmd: "install", wantArgs: []string{}, wantLvl: core.LevelInfo},
		{name: "command with args", args: []string{"add", "react", "react-dom"}, wantCmd: "add", wantArgs: []string{"react", "react-dom"}, wantLvl: core.LevelInfo},
		{name: "global before command", args: []string{"--silent", "install"}, wantCmd: "install", wantArgs: []string{}, wantLvl: core.LevelSilent},
		{name: "verbose maps to debug", args: []string{"-v", "ci"}, wantCmd: "ci", wantArgs: []string{}, wantLvl: core.LevelDebug},
		{name: "loglevel wins over silent", args: []string{"--silent", "--loglevel", "trace", "install"}, wantCmd: "install", wantArgs: []string{}, wantLvl: core.LevelTrace},
		{name: "loglevel equals form", args: []string{"--loglevel=warn", "audit"}, wantCmd: "audit", wantArgs: []string{}, wantLvl: core.LevelWarn},
		{name: "json flag", args: []string{"--json", "audit"}, wantCmd: "audit", wantArgs: []string{}, wantLvl: core.LevelInfo, wantJSON: true},
		{name: "command flags pass through", args: []string{"install", "--frozen-lockfile"}, wantCmd: "install", wantArgs: []string{"--frozen-lockfile"}, wantLvl: core.LevelInfo},
		{name: "unknown global flag", args: []string{"--nope", "install"}, wantErr: true},
		{name: "loglevel missing value", args: []string{"--loglevel"}, wantErr: true},
		{name: "invalid loglevel", args: []string{"--loglevel", "bogus", "x"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, err := parseGlobal(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if core.ExitCodeFor(err) != core.ExitUsage {
					t.Fatalf("expected usage exit code, got %d", core.ExitCodeFor(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if g.command != c.wantCmd {
				t.Errorf("command = %q, want %q", g.command, c.wantCmd)
			}
			if !reflect.DeepEqual(g.commandArgs, c.wantArgs) {
				t.Errorf("commandArgs = %#v, want %#v", g.commandArgs, c.wantArgs)
			}
			if g.level != c.wantLvl {
				t.Errorf("level = %v, want %v", g.level, c.wantLvl)
			}
			if g.json != c.wantJSON {
				t.Errorf("json = %v, want %v", g.json, c.wantJSON)
			}
		})
	}
}

func TestEveryCommandHasRun(t *testing.T) {
	if len(commands) != 19 {
		t.Fatalf("expected 19 spec commands, got %d", len(commands))
	}
	seen := map[string]bool{}
	for _, c := range commands {
		if c.Run == nil {
			t.Errorf("command %q has nil Run", c.Name)
		}
		if c.Summary == "" {
			t.Errorf("command %q has empty Summary", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true
	}
}
