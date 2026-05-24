// Package workspacestate computes the install fingerprint and reads /
// writes node_modules/.gnpm/workspace-state.json. The fingerprint backs
// optimisticRepeatInstall and verifyDepsBeforeRun (doc/spec.md §4.3).
package workspacestate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/koji-1009/gnpm/internal/core"
)

// SchemaVersion is the workspace-state.json schema version.
const SchemaVersion = 1

// State is the persisted install fingerprint record.
type State struct {
	SchemaVersion int    `json:"schemaVersion"`
	Hash          string `json:"hash"`
	EngineKey     string `json:"engineKey"`
	InstalledAt   string `json:"installedAt"`
	GnpmVersion   string `json:"gnpmVersion"`
}

// HashInput is the dependency view fed to ComputeHash.
type HashInput struct {
	Dependencies         map[string]string
	DevDependencies      map[string]string
	OptionalDependencies map[string]string
	PeerDependencies     map[string]string
	// LockfileFingerprint is "absent" or "sha256:<hex>" (see
	// LockfileFingerprint / LockfileFingerprintBytes).
	LockfileFingerprint string
	EngineKey           string
}

// ComputeHash implements the canonicalization in doc/spec.md §4.3.1:
// a fixed-key-order object whose four dependency maps are emitted in
// sorted-key order, encoded as canonical JSON (no insignificant
// whitespace, HTML escaping disabled so < > & stay literal, no trailing
// newline), then SHA-256'd to 64 lowercase hex characters.
func ComputeHash(in HashInput) string {
	var b bytes.Buffer
	b.WriteString(`{"dependencies":`)
	b.WriteString(encodeStringMap(in.Dependencies))
	b.WriteString(`,"devDependencies":`)
	b.WriteString(encodeStringMap(in.DevDependencies))
	b.WriteString(`,"optionalDependencies":`)
	b.WriteString(encodeStringMap(in.OptionalDependencies))
	b.WriteString(`,"peerDependencies":`)
	b.WriteString(encodeStringMap(in.PeerDependencies))
	b.WriteString(`,"lockfile":`)
	b.WriteString(encodeString(in.LockfileFingerprint))
	b.WriteString(`,"engineKey":`)
	b.WriteString(encodeString(in.EngineKey))
	b.WriteByte('}')
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

// encodeStringMap emits {} for an absent/empty map, else a JSON object
// with keys in Go's default (sorted byte order) and HTML escaping off.
func encodeStringMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m) // map keys are sorted by encoding/json
	return trimNewline(buf.String())
}

func encodeString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return trimNewline(buf.String())
}

func trimNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

// LockfileFingerprintAbsent is the fingerprint when no lockfile exists.
const LockfileFingerprintAbsent = "absent"

// LockfileFingerprintBytes returns "sha256:<hex>" of the raw lockfile
// bytes (no normalization), or "absent" for nil.
func LockfileFingerprintBytes(data []byte) string {
	if data == nil {
		return LockfileFingerprintAbsent
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EngineKey builds "<platform>;<arch>;node<major>" using Go's GOOS/GOARCH
// vocabulary. nodeMajor is the devEngines.runtime major when known, the
// GNPM_HOST_NODE_MAJOR env value otherwise, and "?" when neither is set
// (doc/spec.md §4.3).
func EngineKey(nodeMajor string) string {
	if nodeMajor == "" {
		nodeMajor = os.Getenv("GNPM_HOST_NODE_MAJOR")
	}
	if nodeMajor == "" {
		nodeMajor = "?"
	}
	return runtime.GOOS + ";" + runtime.GOARCH + ";node" + nodeMajor
}

// MajorString extracts the leading integer of a version range (e.g.
// "^22", ">=22 <23", "22.11.0" → "22"), or "" when none is present.
func MajorString(versionRange string) string {
	start := -1
	for i := 0; i < len(versionRange); i++ {
		if versionRange[i] >= '0' && versionRange[i] <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := start
	for end < len(versionRange) && versionRange[end] >= '0' && versionRange[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(versionRange[start:end])
	if err != nil {
		return ""
	}
	return strconv.Itoa(n)
}

func statePath(projectRoot string) string {
	return filepath.Join(projectRoot, "node_modules", ".gnpm", "workspace-state.json")
}

// Read loads the workspace state, returning (nil, nil) when absent or
// unreadable.
func Read(projectRoot string) (*State, error) {
	data, err := os.ReadFile(statePath(projectRoot))
	if err != nil {
		return nil, nil
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil
	}
	return &s, nil
}

// Write persists the workspace state, creating node_modules/.gnpm.
func Write(projectRoot string, s State) error {
	s.SchemaVersion = SchemaVersion
	path := statePath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.IOError("creating node_modules/.gnpm").Wrap(err)
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return core.IOError("encoding workspace state").Wrap(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return core.IOError("writing workspace state").Wrap(err)
	}
	return nil
}
