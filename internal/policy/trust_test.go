package policy

import (
	"testing"
	"time"
)

func TestTrustHistoryUpdateAndFloors(t *testing.T) {
	now := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)
	h := &TrustHistory{Records: map[string]TrustRecord{}}

	h.Update(map[string]string{"react": "18.2.0", "lodash": "4.17.21"}, now)
	floors := h.Floors(now, 0)
	if floors["react"].String() != "18.2.0" {
		t.Errorf("react floor = %v", floors["react"])
	}

	// A lower version must not lower the recorded high-water mark.
	h.Update(map[string]string{"react": "17.0.0"}, now)
	if h.Records["react"].Version != "18.2.0" {
		t.Errorf("downgrade lowered the record: %v", h.Records["react"])
	}

	// A higher version raises it.
	h.Update(map[string]string{"react": "18.3.0"}, now)
	if h.Records["react"].Version != "18.3.0" {
		t.Errorf("upgrade did not raise the record: %v", h.Records["react"])
	}
}

func TestTrustHistoryIgnoreAfter(t *testing.T) {
	now := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	h := &TrustHistory{Records: map[string]TrustRecord{
		"stale": {Version: "1.0.0", RecordedAt: old},
		"fresh": {Version: "2.0.0", RecordedAt: recent},
	}}
	floors := h.Floors(now, 30*24*time.Hour) // 30-day window
	if _, ok := floors["stale"]; ok {
		t.Error("stale record beyond ignoreAfter should be dropped")
	}
	if floors["fresh"].String() != "2.0.0" {
		t.Errorf("fresh record should impose a floor, got %v", floors["fresh"])
	}
	// Without ignoreAfter, both apply.
	if len(h.Floors(now, 0)) != 2 {
		t.Error("ignoreAfter=0 should keep all records")
	}
}
