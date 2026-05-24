// Package installer is the end-to-end install orchestration: resolve →
// fetch → ingest → link → run scripts → write lockfile + workspace state
// (doc/spec.md §5 install). It ties together the resolver, registry,
// store, linker, scripts, lockfile, and workspace-state packages.
package installer

import (
	"time"

	"github.com/koji-1009/gnpm/internal/audit"
	"github.com/koji-1009/gnpm/internal/signature"
)

// ScriptPolicy selects which install-time lifecycle scripts run.
type ScriptPolicy int

const (
	// ScriptNone skips every install-time script (--ignore-scripts).
	ScriptNone ScriptPolicy = iota
	// ScriptAllowlist runs scripts only for allowlisted packages.
	ScriptAllowlist
	// ScriptAll runs every package's scripts (legacy npm behavior).
	ScriptAll
)

// Options controls install behavior (doc/spec.md §5).
type Options struct {
	FrozenLockfile bool
	Production     bool
	IgnoreScripts  bool
	ScriptPolicy   ScriptPolicy
	Offline        bool
	PreferOffline  bool
	EngineStrict   bool

	MinReleaseAge time.Duration

	// SignaturePolicy controls ECDSA tarball signature enforcement.
	SignaturePolicy signature.Policy

	// AuditLevel, when set above SevUnknown, runs an audit after install
	// and fails when any advisory meets the level.
	AuditLevel audit.Severity

	StrictDepBuilds           bool
	DangerouslyAllowAllBuilds bool
	OptimisticRepeatInstall   bool

	// Update bumps dependencies to the highest in-range version by not
	// seeding the resolver with the lockfile's existing versions.
	Update bool

	// StoreRoot / CacheRoot override the default ~/.gnpm locations.
	StoreRoot string
	CacheRoot string
}

// DefaultOptions returns the install defaults (doc/spec.md §2.4).
func DefaultOptions() Options {
	return Options{
		ScriptPolicy:            ScriptAllowlist,
		OptimisticRepeatInstall: true,
	}
}

// EffectiveScriptPolicy folds IgnoreScripts into the policy.
func (o Options) EffectiveScriptPolicy() ScriptPolicy {
	if o.IgnoreScripts {
		return ScriptNone
	}
	return o.ScriptPolicy
}

// Report is the outcome of an install.
type Report struct {
	Warnings []string
	Added    int
}
