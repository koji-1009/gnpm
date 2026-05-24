package sbom

import (
	"encoding/json"
	"testing"

	"github.com/koji-1009/gnpm/internal/lockfile"
)

func sampleLock() *lockfile.Lockfile {
	return &lockfile.Lockfile{
		Packages: map[string]lockfile.LockedPackage{
			"react@18.2.0":     {Name: "react", Version: "18.2.0", Integrity: "sha512-" + b64of64(), Tarball: "https://r/react.tgz"},
			"@scope/pkg@1.0.0": {Name: "@scope/pkg", Version: "1.0.0", Integrity: "sha512-" + b64of64()},
		},
	}
}

func b64of64() string {
	// 64 zero bytes base64-encoded (valid sha512 digest length).
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
}

func TestCycloneDX(t *testing.T) {
	doc, err := Build(sampleLock(), "cyclonedx", "", "demo")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	if m["bomFormat"] != "CycloneDX" || m["specVersion"] != "1.7" {
		t.Errorf("header wrong: %v / %v", m["bomFormat"], m["specVersion"])
	}
	sn, _ := m["serialNumber"].(string)
	if len(sn) < len("urn:gnpm:sbom:") {
		t.Errorf("serialNumber = %q", sn)
	}
	comps, _ := m["components"].([]any)
	if len(comps) != 2 {
		t.Fatalf("components = %d, want 2", len(comps))
	}
	// scoped purl is percent-encoded.
	first := comps[0].(map[string]any)
	if first["purl"] != "pkg:npm/%40scope/pkg@1.0.0" {
		t.Errorf("scoped purl = %v", first["purl"])
	}
}

func TestSPDX(t *testing.T) {
	doc, err := Build(sampleLock(), "spdx", "", "demo")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	if m["spdxVersion"] != "SPDX-2.3" || m["SPDXID"] != "SPDXRef-DOCUMENT" {
		t.Errorf("spdx header wrong: %v", m)
	}
	pkgs, _ := m["packages"].([]any)
	if len(pkgs) != 2 {
		t.Fatalf("packages = %d, want 2", len(pkgs))
	}
}

func TestSerialNumberStable(t *testing.T) {
	a, _ := Build(sampleLock(), "cyclonedx", "", "demo")
	b, _ := Build(sampleLock(), "cyclonedx", "", "demo")
	var ma, mb map[string]any
	json.Unmarshal(a, &ma)
	json.Unmarshal(b, &mb)
	if ma["serialNumber"] != mb["serialNumber"] {
		t.Error("serialNumber should be stable across runs over the same lockfile")
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, err := Build(sampleLock(), "spxx", "", "demo"); err == nil {
		t.Error("unknown format should error")
	}
}
