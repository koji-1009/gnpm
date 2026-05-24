package installer

import (
	"strconv"
	"strings"
	"time"

	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/project"
)

// trustConfig reads the trustPolicy settings (doc/spec.md §2.4).
func (op *Operation) trustConfig(cfg *npmrc.Config) (noDowngrade bool, ignoreAfter time.Duration) {
	ws := project.ReadPnpmWorkspace(op.ProjectRoot)
	if op.setting(cfg, ws, "trust-policy") != "no-downgrade" {
		return false, 0
	}
	return true, parseDurationLoose(op.setting(cfg, ws, "trust-policy-ignore-after"))
}

// parseDurationLoose accepts Go durations plus a bare "<N>d" day form.
// Returns 0 when empty or unparseable (history kept forever).
func parseDurationLoose(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 0
}
