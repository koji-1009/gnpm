package installer

import (
	"context"

	"github.com/koji-1009/gnpm/internal/audit"
	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
)

// postInstallAudit runs an advisory check after install when
// --audit-level is set, failing the install on any finding at or above
// the level (doc/spec.md §7.2). Advisory fetch errors are surfaced as
// warnings, not failures — the install itself already succeeded.
func (op *Operation) postInstallAudit(ctx context.Context, cfg *npmrc.Config, lock *lockfile.Lockfile, warnings *[]string) error {
	if op.Options.AuditLevel == audit.SevUnknown {
		return nil
	}
	svc := &audit.Service{Config: cfg, UserAgent: "gnpm/" + op.Version}
	report := svc.Audit(ctx, lock)
	for _, e := range report.Errors {
		*warnings = append(*warnings, "audit: "+e)
	}
	blocking := 0
	for _, f := range report.Findings {
		if audit.ParseSeverity(f.Advisory.Severity).AtLeast(op.Options.AuditLevel) {
			blocking++
		}
	}
	if blocking > 0 {
		return core.IntegrityError("audit: %d advisor%s at or above --audit-level=%s",
			blocking, plural(blocking), op.Options.AuditLevel)
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
