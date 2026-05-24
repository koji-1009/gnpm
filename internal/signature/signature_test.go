package signature

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifyAndEnforce(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	const keyID = "SHA256:test"
	ks := WithKeys(map[string]*ecdsa.PublicKey{keyID: &priv.PublicKey})

	name, version, integrity := "react", "18.2.0", "sha512-abc"
	msg := []byte(name + "@" + version + ":" + integrity)
	digest := sha256.Sum256(msg)
	der, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	goodSig := Signature{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(der)}

	// Valid signature.
	if r, _ := ks.Verify(context.Background(), name, version, integrity, []Signature{goodSig}); r != ResultValid {
		t.Errorf("expected ResultValid, got %v", r)
	}
	// No signatures → Absent.
	if r, _ := ks.Verify(context.Background(), name, version, integrity, nil); r != ResultAbsent {
		t.Errorf("expected ResultAbsent, got %v", r)
	}
	// Tampered integrity → the signature no longer matches → Invalid.
	if r, _ := ks.Verify(context.Background(), name, version, "sha512-TAMPERED", []Signature{goodSig}); r != ResultInvalid {
		t.Errorf("expected ResultInvalid on tamper, got %v", r)
	}
	// Unknown keyid → no key matches → Invalid.
	if r, _ := ks.Verify(context.Background(), name, version, integrity, []Signature{{KeyID: "other", Sig: goodSig.Sig}}); r != ResultInvalid {
		t.Errorf("expected ResultInvalid for unknown keyid, got %v", r)
	}
}

func TestEnforce(t *testing.T) {
	// weak: absence allowed, invalid fatal.
	if _, err := Enforce(PolicyWeak, "x", "1", ResultAbsent); err != nil {
		t.Error("weak should allow absent")
	}
	if _, err := Enforce(PolicyWeak, "x", "1", ResultInvalid); err == nil {
		t.Error("weak should fail on invalid")
	}
	// strict: absence fatal, valid ok.
	if _, err := Enforce(PolicyStrict, "x", "1", ResultAbsent); err == nil {
		t.Error("strict should fail on absent")
	}
	if _, err := Enforce(PolicyStrict, "x", "1", ResultValid); err != nil {
		t.Error("strict should allow valid")
	}
	// none: never fails.
	if _, err := Enforce(PolicyNone, "x", "1", ResultInvalid); err != nil {
		t.Error("none should never fail")
	}
}

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]Policy{"none": PolicyNone, "weak": PolicyWeak, "strict": PolicyStrict, "": PolicyNone} {
		if got, ok := ParsePolicy(in); !ok || got != want {
			t.Errorf("ParsePolicy(%q) = %v (%v)", in, got, ok)
		}
	}
	if _, ok := ParsePolicy("bogus"); ok {
		t.Error("bogus policy should not parse")
	}
}
