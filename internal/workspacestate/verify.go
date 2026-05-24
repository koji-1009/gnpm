package workspacestate

// VerifyPolicy is the verifyDepsBeforeRun setting (doc/spec.md §2.4).
type VerifyPolicy int

const (
	VerifyOff VerifyPolicy = iota
	VerifyWarn
	VerifyError
	VerifyInstall
	VerifyPrompt
)

// ParseVerifyPolicy maps a setting value to a VerifyPolicy. ok is false
// for an unrecognized value; the default is VerifyInstall.
func ParseVerifyPolicy(s string) (VerifyPolicy, bool) {
	switch s {
	case "off":
		return VerifyOff, true
	case "warn":
		return VerifyWarn, true
	case "error":
		return VerifyError, true
	case "install":
		return VerifyInstall, true
	case "prompt":
		return VerifyPrompt, true
	default:
		return VerifyInstall, false
	}
}

// Matches reports whether the recorded state matches the freshly computed
// hash and engine key — i.e. node_modules is up to date.
func Matches(recorded *State, freshHash, engineKey string) bool {
	return recorded != nil && recorded.Hash == freshHash && recorded.EngineKey == engineKey
}
