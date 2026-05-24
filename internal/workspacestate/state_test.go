package workspacestate

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestComputeHashReferenceExample(t *testing.T) {
	// doc/spec.md §4.3.1 reference: empty project, no lockfile.
	const canonical = `{"dependencies":{},"devDependencies":{},"optionalDependencies":{},"peerDependencies":{},"lockfile":"absent","engineKey":"linux;amd64;node?"}`
	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])

	got := ComputeHash(HashInput{
		LockfileFingerprint: "absent",
		EngineKey:           "linux;amd64;node?",
	})
	if got != want {
		t.Errorf("hash = %s, want %s (canonicalization diverged)", got, want)
	}
}

func TestComputeHashSortsAndDoesNotHTMLEscape(t *testing.T) {
	in := HashInput{
		Dependencies:        map[string]string{"b": ">=1.0.0 <2.0.0", "a": "^1.0.0"},
		LockfileFingerprint: "absent",
		EngineKey:           "linux;amd64;node?",
	}
	// Build the expected canonical string by hand: keys sorted (a,b),
	// and ">"/"<" left literal (not > / <).
	const canonical = `{"dependencies":{"a":"^1.0.0","b":">=1.0.0 <2.0.0"},` +
		`"devDependencies":{},"optionalDependencies":{},"peerDependencies":{},` +
		`"lockfile":"absent","engineKey":"linux;amd64;node?"}`
	sum := sha256.Sum256([]byte(canonical))
	if got := ComputeHash(in); got != hex.EncodeToString(sum[:]) {
		t.Errorf("hash mismatch: HTML escaping or key order diverged")
	}
}

func TestComputeHashDeterministic(t *testing.T) {
	in := HashInput{
		Dependencies:        map[string]string{"react": "^18", "react-dom": "^18"},
		DevDependencies:     map[string]string{"vitest": "^3"},
		LockfileFingerprint: LockfileFingerprintBytes([]byte("lockfile bytes")),
		EngineKey:           EngineKey("22"),
	}
	if ComputeHash(in) != ComputeHash(in) {
		t.Error("hash is not deterministic")
	}
}

func TestLockfileFingerprint(t *testing.T) {
	if LockfileFingerprintBytes(nil) != "absent" {
		t.Error("nil bytes should be absent")
	}
	fp := LockfileFingerprintBytes([]byte("x"))
	if len(fp) != len("sha256:")+64 || fp[:7] != "sha256:" {
		t.Errorf("fingerprint format = %q", fp)
	}
}

func TestMajorString(t *testing.T) {
	cases := map[string]string{"^22": "22", ">=22 <23": "22", "22.11.0": "22", "node": "", "": ""}
	for in, want := range cases {
		if got := MajorString(in); got != want {
			t.Errorf("MajorString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateReadWrite(t *testing.T) {
	root := t.TempDir()
	if s, _ := Read(root); s != nil {
		t.Error("expected no state initially")
	}
	want := State{Hash: "abc", EngineKey: "linux;amd64;node22", InstalledAt: "2026-05-24T00:00:00Z", GnpmVersion: "0.0.1-dev"}
	if err := Write(root, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root)
	if err != nil || got == nil {
		t.Fatalf("read after write: %v / %v", got, err)
	}
	if got.Hash != "abc" || got.SchemaVersion != SchemaVersion {
		t.Errorf("state round trip = %+v", got)
	}
}

func TestVerifyMatches(t *testing.T) {
	s := &State{Hash: "h", EngineKey: "k"}
	if !Matches(s, "h", "k") {
		t.Error("should match")
	}
	if Matches(s, "h2", "k") || Matches(nil, "h", "k") {
		t.Error("should not match on different hash or nil state")
	}
}
