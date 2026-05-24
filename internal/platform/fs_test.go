package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTreeExcludingPreservesSymlinksAndSkipsGit(t *testing.T) {
	src := t.TempDir()
	mustWrite := func(rel, body string) {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("index.js", "x")
	mustWrite("lib/a.js", "y")
	mustWrite(".git/config", "z") // must be excluded
	if err := os.Symlink("index.js", filepath.Join(src, "link.js")); err != nil {
		t.Skip("symlinks not supported on this platform")
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := CopyTreeExcluding(src, dst, ".git"); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(filepath.Join(dst, "lib", "a.js")); string(b) != "y" {
		t.Errorf("nested regular file not copied: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error(".git should be excluded from the copy")
	}
	fi, err := os.Lstat(filepath.Join(dst, "link.js"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink not preserved: mode=%v err=%v", fi.Mode(), err)
	}
	if target, _ := os.Readlink(filepath.Join(dst, "link.js")); target != "index.js" {
		t.Errorf("symlink target not preserved: %q", target)
	}
}
