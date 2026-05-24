package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/koji-1009/gnpm/internal/core"
)

func testEnv(t *testing.T) (*Env, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	var out bytes.Buffer
	return &Env{
		Log:    core.NewLogger("test", core.LevelError),
		Stdout: &out,
		Stderr: &bytes.Buffer{},
		Cwd:    dir,
	}, &out
}

func writePkg(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPkgGetSetDelete(t *testing.T) {
	env, out := testEnv(t)
	writePkg(t, env.Cwd, `{"name":"demo","version":"1.0.0","scripts":{"build":"tsc"}}`)

	// get nested
	if err := cmdPkg(context.Background(), env, []string{"get", "scripts.build"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "\"tsc\"\n" {
		t.Errorf("pkg get = %q", got)
	}

	// set scalar + nested
	if err := cmdPkg(context.Background(), env, []string{"set", "version=2.0.0", "scripts.test=vitest"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(env.Cwd, "package.json"))
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["version"] != "2.0.0" {
		t.Errorf("version not set: %v", m["version"])
	}
	if scripts, _ := m["scripts"].(map[string]any); scripts["test"] != "vitest" {
		t.Errorf("nested set failed: %v", m["scripts"])
	}
	// top-level key order preserved (name first).
	if !bytes.HasPrefix(data, []byte("{\n  \"name\"")) {
		t.Errorf("top-level key order not preserved:\n%s", data)
	}

	// delete
	if err := cmdPkg(context.Background(), env, []string{"delete", "scripts.build"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(env.Cwd, "package.json"))
	json.Unmarshal(data, &m)
	if scripts, _ := m["scripts"].(map[string]any); scripts["build"] != nil {
		t.Errorf("delete failed: %v", m["scripts"])
	}
}

func TestConfigSetGetDelete(t *testing.T) {
	env, out := testEnv(t)
	if err := cmdConfig(context.Background(), env, []string{"set", "save-exact", "true"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cmdConfig(context.Background(), env, []string{"get", "save-exact"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "true\n" {
		t.Errorf("config get = %q", out.String())
	}
	if err := cmdConfig(context.Background(), env, []string{"delete", "save-exact"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	cmdConfig(context.Background(), env, []string{"get", "save-exact"})
	if out.String() != "\n" {
		t.Errorf("config get after delete = %q", out.String())
	}
}

func TestCleanRemovesNodeModules(t *testing.T) {
	env, _ := testEnv(t)
	nm := filepath.Join(env.Cwd, "node_modules", "x")
	os.MkdirAll(nm, 0o755)
	if err := cmdClean(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "node_modules")); !os.IsNotExist(err) {
		t.Error("node_modules should be removed")
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	env, _ := testEnv(t)
	writePkg(t, env.Cwd, `{"name":"demo","version":"1.0.0","scripts":{"ok":"exit 0","bad":"exit 7"}}`)
	// Disable verify so run doesn't try to install.
	os.WriteFile(filepath.Join(env.Cwd, ".npmrc"), []byte("verify-deps-before-run=off\n"), 0o644)

	if err := cmdRun(context.Background(), env, []string{"ok"}); err != nil {
		t.Errorf("run ok: %v", err)
	}
	err := cmdRun(context.Background(), env, []string{"bad"})
	if err == nil || core.ExitCodeFor(err) != 7 {
		t.Errorf("run bad should propagate exit 7, got %v (code %d)", err, core.ExitCodeFor(err))
	}
	// Unknown script → usage error (64).
	err = cmdRun(context.Background(), env, []string{"nope"})
	if core.ExitCodeFor(err) != core.ExitUsage {
		t.Errorf("unknown script should be usage error, got %d", core.ExitCodeFor(err))
	}
}

func TestExecMissingBin(t *testing.T) {
	env, _ := testEnv(t)
	writePkg(t, env.Cwd, `{"name":"demo","version":"1.0.0"}`)
	os.WriteFile(filepath.Join(env.Cwd, ".npmrc"), []byte("verify-deps-before-run=off\n"), 0o644)
	err := cmdExec(context.Background(), env, []string{"ghost-bin"})
	if core.ExitCodeFor(err) != core.ExitUsage {
		t.Errorf("missing bin should be usage error (64), got %d", core.ExitCodeFor(err))
	}
}
