package installer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/koji-1009/gnpm/internal/npmrc"
)

// TestReleaseAgeModeDefault pins the mode-dependent minimum-release-age default:
// pnpm mode inherits pnpm's one-day supply-chain gate, npm mode applies none,
// and an explicit flag/.npmrc value always wins (including an explicit 0).
func TestReleaseAgeModeDefault(t *testing.T) {
	npmDir := t.TempDir() // bare dir → npm mode
	pnpmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pnpmDir, "pnpm-workspace.yaml"), []byte("packages: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		root string
		opt  time.Duration
		cfg  map[string]string
		want time.Duration
	}{
		{"npm mode, unset → no gate", npmDir, UnsetMinReleaseAge, nil, 0},
		{"pnpm mode, unset → pnpm's one-day gate", pnpmDir, UnsetMinReleaseAge, nil, PnpmDefaultMinReleaseAge},
		{"pnpm mode, explicit 0 → no gate", pnpmDir, 0, nil, 0},
		{"pnpm mode, explicit flag wins", pnpmDir, 90 * time.Minute, nil, 90 * time.Minute},
		{"pnpm mode, .npmrc wins over mode default", pnpmDir, UnsetMinReleaseAge, map[string]string{"minimum-release-age": "30"}, 30 * time.Minute},
		{"npm mode, .npmrc opts in", npmDir, UnsetMinReleaseAge, map[string]string{"minimum-release-age": "30"}, 30 * time.Minute},
		{"npm mode, .npmrc explicit 0 stays off", npmDir, UnsetMinReleaseAge, map[string]string{"minimum-release-age": "0"}, 0},
	}
	for _, tc := range cases {
		entries := tc.cfg
		if entries == nil {
			entries = map[string]string{}
		}
		op := &Operation{ProjectRoot: tc.root, Options: Options{MinReleaseAge: tc.opt}}
		if got := op.releaseAge(npmrc.New(entries)).Minimum; got != tc.want {
			t.Errorf("%s: Minimum = %v, want %v", tc.name, got, tc.want)
		}
	}
}
