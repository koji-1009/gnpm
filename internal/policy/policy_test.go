package policy

import (
	"testing"

	"github.com/koji-1009/gnpm/internal/project"
)

func TestEvaluatePmOnFail(t *testing.T) {
	foreign := &project.PackageJSON{PackageManager: "pnpm@9.1.0"}
	ours := &project.PackageJSON{PackageManager: "gnpm@^0.0.1"}
	mismatch := &project.PackageJSON{PackageManager: "gnpm@^9.0.0"}

	if r := EvaluatePmOnFail(ours, "0.0.1", PmWarn); r.Action != PmProceed {
		t.Errorf("satisfying gnpm pin should proceed, got %v", r)
	}
	if r := EvaluatePmOnFail(foreign, "0.0.1", PmWarn); r.Action != PmActWarn {
		t.Errorf("foreign manager should warn, got %v", r)
	}
	if r := EvaluatePmOnFail(foreign, "0.0.1", PmError); r.Action != PmActFail {
		t.Errorf("foreign manager + error policy should fail, got %v", r)
	}
	if r := EvaluatePmOnFail(foreign, "0.0.1", PmIgnore); r.Action != PmProceed {
		t.Errorf("ignore policy should proceed, got %v", r)
	}
	if r := EvaluatePmOnFail(mismatch, "0.0.1", PmWarn); r.Action != PmActWarn {
		t.Errorf("version mismatch should warn, got %v", r)
	}

	// devEngines onFail overrides the global policy.
	de := &project.PackageJSON{DevEnginesPackageManager: &project.DevEnginesEntry{Name: "pnpm", Version: "9", OnFail: "ignore"}}
	if r := EvaluatePmOnFail(de, "0.0.1", PmError); r.Action != PmProceed {
		t.Errorf("devEngines onFail=ignore should override error policy, got %v", r)
	}
}

func TestResolveCatalog(t *testing.T) {
	ws := &project.PnpmWorkspace{
		Catalog: map[string]string{"react": "^18.3.0"},
		Catalogs: map[string]map[string]string{
			"default": {"react": "^18.3.0"},
			"testing": {"vitest": "^3.0.0"},
		},
	}
	if r, ok := ResolveCatalog(ws, "react", ""); !ok || r != "^18.3.0" {
		t.Errorf("default catalog react = %q (%v)", r, ok)
	}
	if r, ok := ResolveCatalog(ws, "vitest", "testing"); !ok || r != "^3.0.0" {
		t.Errorf("testing catalog vitest = %q (%v)", r, ok)
	}
	if _, ok := ResolveCatalog(ws, "missing", ""); ok {
		t.Error("missing entry should not resolve")
	}
}
