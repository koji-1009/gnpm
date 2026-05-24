// Package policy holds install-time policy evaluations that are not tied
// to resolution: the packageManager mismatch gate (pmOnFail), catalog
// reference resolution, the trusted-exotic-repo allowlist, and the
// install trust history / no-downgrade defense (doc/spec.md §2.4, §9).
package policy

import (
	"strings"

	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/semver"
)

// PmOnFail is the pmOnFail setting.
type PmOnFail int

const (
	PmIgnore PmOnFail = iota
	PmWarn
	PmError
)

// ParsePmOnFail maps a setting value to a PmOnFail (default warn).
func ParsePmOnFail(s string) PmOnFail {
	switch s {
	case "ignore":
		return PmIgnore
	case "error":
		return PmError
	default:
		return PmWarn
	}
}

// PmAction is the resolved action.
type PmAction int

const (
	PmProceed PmAction = iota
	PmActWarn
	PmActFail
)

// PmResult carries the action and a human-readable reason.
type PmResult struct {
	Action  PmAction
	Message string
}

// EvaluatePmOnFail checks package.json#packageManager and
// devEngines.packageManager against this gnpm build. A foreign manager or
// an unsatisfied version range triggers the policy; devEngines' onFail
// overrides the global policy for the project where it appears.
func EvaluatePmOnFail(pkg *project.PackageJSON, gnpmVersion string, policy PmOnFail) PmResult {
	// devEngines.packageManager takes precedence and may override policy.
	if de := pkg.DevEnginesPackageManager; de != nil {
		effective := policy
		if de.OnFail != "" {
			effective = ParsePmOnFail(de.OnFail)
		}
		if ok, msg := managerSatisfies(de.Name, de.Version, gnpmVersion); !ok {
			return actFor(effective, msg)
		}
		return PmResult{Action: PmProceed}
	}
	if pkg.PackageManager == "" {
		return PmResult{Action: PmProceed}
	}
	name, version := splitPackageManager(pkg.PackageManager)
	if ok, msg := managerSatisfies(name, version, gnpmVersion); !ok {
		return actFor(policy, msg)
	}
	return PmResult{Action: PmProceed}
}

func managerSatisfies(name, versionRange, gnpmVersion string) (bool, string) {
	if name != "" && name != "gnpm" {
		return false, "package.json pins packageManager to \"" + name + "@" + versionRange +
			"\" but this is gnpm; mismatched package managers can produce different lockfiles"
	}
	if versionRange == "" {
		return true, ""
	}
	rng, err := semver.ParseRange(versionRange)
	if err != nil {
		return true, "" // unparseable pin → don't block
	}
	v, err := semver.Parse(gnpmVersion)
	if err != nil {
		return true, "" // dev build version not semver → don't block
	}
	if rng.Satisfies(v) {
		return true, ""
	}
	return false, "gnpm " + gnpmVersion + " does not satisfy required range \"" + versionRange + "\""
}

func actFor(policy PmOnFail, msg string) PmResult {
	switch policy {
	case PmIgnore:
		return PmResult{Action: PmProceed}
	case PmError:
		return PmResult{Action: PmActFail, Message: msg}
	default:
		return PmResult{Action: PmActWarn, Message: msg}
	}
}

// splitPackageManager splits "name@version" (corepack form); the version
// may carry a "+hash" suffix which is dropped.
func splitPackageManager(s string) (string, string) {
	at := strings.IndexByte(s, '@')
	if at < 0 {
		return s, ""
	}
	name := s[:at]
	version := s[at+1:]
	if plus := strings.IndexByte(version, '+'); plus >= 0 {
		version = version[:plus]
	}
	return name, version
}
