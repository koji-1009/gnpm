package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/semver"
)

// TrustHistory records the highest version ever installed per package,
// persisted alongside the lockfile to defend against republish-based
// downgrade attacks (doc/spec.md §2.4 trustPolicy).
type TrustHistory struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Records       map[string]TrustRecord `json:"records"`
}

// TrustRecord is one package's highest-seen version and when it was set.
type TrustRecord struct {
	Version    string `json:"version"`
	RecordedAt string `json:"recordedAt"`
}

func trustPath(projectRoot string) string {
	return filepath.Join(projectRoot, "node_modules", ".gnpm", "trust-history.json")
}

// ReadTrustHistory loads the history, returning an empty one when absent.
func ReadTrustHistory(projectRoot string) *TrustHistory {
	h := &TrustHistory{SchemaVersion: 1, Records: map[string]TrustRecord{}}
	data, err := os.ReadFile(trustPath(projectRoot))
	if err != nil {
		return h
	}
	var parsed TrustHistory
	if json.Unmarshal(data, &parsed) == nil && parsed.Records != nil {
		h.Records = parsed.Records
	}
	return h
}

// Floors returns the per-package minimum versions implied by the history.
// Records older than ignoreAfter (when > 0) are dropped, matching
// trustPolicyIgnoreAfter.
func (h *TrustHistory) Floors(now time.Time, ignoreAfter time.Duration) map[string]semver.Version {
	out := map[string]semver.Version{}
	for name, rec := range h.Records {
		if ignoreAfter > 0 {
			if at, err := time.Parse(time.RFC3339, rec.RecordedAt); err == nil {
				if now.Sub(at) > ignoreAfter {
					continue
				}
			}
		}
		if v, ok := semver.TryParse(rec.Version); ok {
			out[name] = v
		}
	}
	return out
}

// Update raises each package's recorded version to the resolved version
// when higher, stamping the time of any change.
func (h *TrustHistory) Update(resolved map[string]string, now time.Time) {
	stamp := now.UTC().Format(time.RFC3339)
	for name, version := range resolved {
		v, ok := semver.TryParse(version)
		if !ok {
			continue
		}
		if rec, exists := h.Records[name]; exists {
			if cur, ok := semver.TryParse(rec.Version); ok && !v.Less(cur) {
				if v.Equal(cur) {
					continue // unchanged
				}
			} else if ok && v.Less(cur) {
				continue // never lower the recorded high-water mark
			}
		}
		h.Records[name] = TrustRecord{Version: version, RecordedAt: stamp}
	}
}

// Write persists the history under node_modules/.gnpm.
func (h *TrustHistory) Write(projectRoot string) error {
	h.SchemaVersion = 1
	path := trustPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.IOError("creating node_modules/.gnpm").Wrap(err)
	}
	body, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return core.IOError("encoding trust history").Wrap(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return core.IOError("writing trust history").Wrap(err)
	}
	return nil
}
