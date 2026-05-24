// Package scripts runs npm lifecycle scripts with a restricted
// environment and gates install-time build scripts behind an allowlist
// (doc/spec.md §6).
package scripts

import "strings"

// BuildDecision is one outcome of the build-script gate.
type BuildDecision int

const (
	// BuildAllow: opted in (allowBuilds or dangerouslyAllowAllBuilds).
	BuildAllow BuildDecision = iota
	// BuildNoTrigger: no install-time trigger; nothing to gate.
	BuildNoTrigger
	// BuildSkip: triggers present, not allowlisted, strictDepBuilds off.
	BuildSkip
	// BuildFail: triggers present, not allowlisted, strictDepBuilds on.
	BuildFail
)

// BuildTriggers are the signals that make a package build-script-bearing
// (doc/spec.md §6.1). prepare is intentionally not a trigger.
type BuildTriggers struct {
	HasPreinstall  bool
	HasInstall     bool
	HasPostinstall bool
	HasBindingGyp  bool
	HasHooksDir    bool
}

// Any reports whether any install-time trigger is present.
func (t BuildTriggers) Any() bool {
	return t.HasPreinstall || t.HasInstall || t.HasPostinstall || t.HasBindingGyp || t.HasHooksDir
}

// TriggersFromScripts derives the scripts-based triggers; callers layer
// the on-disk binding.gyp / .hooks checks separately.
func TriggersFromScripts(scripts map[string]string, hasBindingGyp, hasHooksDir bool) BuildTriggers {
	return BuildTriggers{
		HasPreinstall:  scripts["preinstall"] != "",
		HasInstall:     scripts["install"] != "",
		HasPostinstall: scripts["postinstall"] != "",
		HasBindingGyp:  hasBindingGyp,
		HasHooksDir:    hasHooksDir,
	}
}

// BuildPolicy evaluates the build-script gate (doc/spec.md §6.1).
type BuildPolicy struct {
	AllowBuilds               []string
	StrictDepBuilds           bool
	DangerouslyAllowAllBuilds bool
}

// Evaluate decides whether packageName's build scripts may run.
func (p BuildPolicy) Evaluate(packageName string, triggers BuildTriggers) BuildDecision {
	if p.DangerouslyAllowAllBuilds {
		return BuildAllow
	}
	if !triggers.Any() {
		return BuildNoTrigger
	}
	if MatchesAllowPattern(packageName, p.AllowBuilds) {
		return BuildAllow
	}
	if p.StrictDepBuilds {
		return BuildFail
	}
	return BuildSkip
}

// MatchesAllowPattern matches a package name against allowBuilds
// patterns: exact, a "@scope/*" scope wildcard, or a trailing "*" glob
// (doc/spec.md §6.2).
func MatchesAllowPattern(name string, patterns []string) bool {
	for _, pat := range patterns {
		if patternMatch(pat, name) {
			return true
		}
	}
	return false
}

func patternMatch(pattern, name string) bool {
	if pattern == name {
		return true
	}
	// "@scope/*" and trailing "*" both reduce to a prefix match on the
	// text before the final "*".
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return false
}
