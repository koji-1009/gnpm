package registry

import (
	"crypto/sha512"
	"encoding/base64"
	"testing"
)

func TestIntegrityParseVerify(t *testing.T) {
	data := []byte("hello tarball bytes")
	sum := sha512.Sum512(data)
	lit := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])

	in, err := ParseIntegrity(lit)
	if err != nil {
		t.Fatal(err)
	}
	if in.Algorithm != "sha512" {
		t.Errorf("algorithm = %q", in.Algorithm)
	}
	if in.Encode() != lit {
		t.Errorf("Encode() = %q, want %q", in.Encode(), lit)
	}
	if err := in.Verify(data); err != nil {
		t.Errorf("Verify should pass: %v", err)
	}
	if err := in.Verify([]byte("tampered")); err == nil {
		t.Error("Verify should fail on tampered bytes")
	}
}

func TestIntegrityParseRejects(t *testing.T) {
	for _, s := range []string{"", "sha512", "sha512-", "-abc", "noseparator"} {
		if _, err := ParseIntegrity(s); err == nil {
			t.Errorf("ParseIntegrity(%q) should fail", s)
		}
	}
	// Unsupported algorithm surfaces at Hasher time, not parse time.
	in, err := ParseIntegrity("md5-abcd")
	if err != nil {
		t.Fatalf("parse should accept unknown algo: %v", err)
	}
	if _, err := in.Hasher(); err == nil {
		t.Error("Hasher should reject md5")
	}
}

func TestPackumentParseRoundTrip(t *testing.T) {
	body := `{
      "name": "demo",
      "dist-tags": { "latest": "1.2.0", "next": "2.0.0-beta.1" },
      "time": {
        "created": "2020-01-01T00:00:00.000Z",
        "1.2.0": "2021-06-01T12:00:00.000Z"
      },
      "versions": {
        "1.2.0": {
          "name": "demo",
          "version": "1.2.0",
          "dist": {
            "tarball": "https://r/demo/-/demo-1.2.0.tgz",
            "integrity": "sha512-abc",
            "signatures": [{"keyid": "SHA256:k", "sig": "s"}]
          },
          "dependencies": { "left-pad": "^1.0.0" },
          "peerDependencies": { "react": "^18", "less": "*" },
          "peerDependenciesMeta": { "less": { "optional": true } },
          "bin": "./cli.js",
          "os": ["darwin", "linux"],
          "cpu": ["arm64"],
          "hasInstallScript": true
        }
      }
    }`
	p, err := ParsePackument([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "demo" || p.Latest() != "1.2.0" {
		t.Errorf("name/latest = %q/%q", p.Name, p.Latest())
	}
	if _, ok := p.PublishTimes["created"]; ok {
		t.Error("created should be excluded from publish times")
	}
	if pt, ok := p.PublishTimes["1.2.0"]; !ok || pt.Year() != 2021 {
		t.Errorf("publish time 1.2.0 = %v (ok=%v)", pt, ok)
	}
	v := p.Versions["1.2.0"]
	if v == nil {
		t.Fatal("missing version 1.2.0")
	}
	if v.Tarball == "" || v.Integrity != "sha512-abc" {
		t.Errorf("dist parse wrong: %+v", v)
	}
	if len(v.Signatures) != 1 || v.Signatures[0].KeyID != "SHA256:k" {
		t.Errorf("signatures = %v", v.Signatures)
	}
	if !v.HasBin || v.Bin["demo"] != "./cli.js" {
		t.Errorf("string bin should map to package name: %v", v.Bin)
	}
	if !v.OptionalPeers["less"] || v.OptionalPeers["react"] {
		t.Errorf("optional peers = %v", v.OptionalPeers)
	}
	if !v.HasInstallScript {
		t.Error("hasInstallScript should be true")
	}

	// Round-trip through the slim cache format and re-parse.
	blob, err := p.MarshalSlim()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ParsePackument(blob)
	if err != nil {
		t.Fatal(err)
	}
	v2 := p2.Versions["1.2.0"]
	if v2 == nil || v2.Integrity != "sha512-abc" || !v2.OptionalPeers["less"] || !v2.HasInstallScript {
		t.Errorf("slim round-trip lost data: %+v", v2)
	}
	if _, ok := p2.PublishTimes["1.2.0"]; !ok {
		t.Error("slim round-trip dropped publish times")
	}
}
