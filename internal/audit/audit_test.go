package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
)

func TestAuditMatchesAndIgnores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo an advisory for "lodash" affecting <4.17.21.
		resp := map[string][]map[string]any{
			"lodash": {{
				"github_advisory_id":  "GHSA-xxxx-yyyy-zzzz",
				"title":               "Prototype pollution",
				"severity":            "high",
				"url":                 "https://example/advisory",
				"vulnerable_versions": "<4.17.21",
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	lock := &lockfile.Lockfile{Packages: map[string]lockfile.LockedPackage{
		"lodash@4.17.20": {Name: "lodash", Version: "4.17.20"},
		"left-pad@1.0.0": {Name: "left-pad", Version: "1.0.0"},
	}}
	cfg := npmrc.New(map[string]string{"registry": srv.URL + "/"})

	svc := &Service{Config: cfg, UserAgent: "gnpm/test"}
	report := svc.Audit(context.Background(), lock)
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(report.Findings))
	}
	if report.Findings[0].Package != "lodash" || report.Findings[0].Advisory.ID != "GHSA-xxxx-yyyy-zzzz" {
		t.Errorf("finding = %+v", report.Findings[0])
	}
	if report.MaxSeverity() != SevHigh {
		t.Errorf("max severity = %v", report.MaxSeverity())
	}

	// With the GHSA ignored, no findings.
	svc.Ignore = map[string]bool{"ghsa-xxxx-yyyy-zzzz": true}
	if got := svc.Audit(context.Background(), lock); len(got.Findings) != 0 {
		t.Errorf("ignored advisory still reported: %d findings", len(got.Findings))
	}
}

func TestSeverityOrdering(t *testing.T) {
	if !SevCritical.AtLeast(SevHigh) || SevLow.AtLeast(SevHigh) {
		t.Error("severity ordering wrong")
	}
	if ParseSeverity("moderate") != SevModerate {
		t.Error("ParseSeverity moderate")
	}
}
