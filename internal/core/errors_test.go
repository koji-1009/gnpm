package core

import (
	"fmt"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"usage", Usage("bad"), ExitUsage},
		{"recoverable", Recoverable("findings"), ExitRecoverable},
		{"network", NetworkError("dns"), ExitSoftware},
		{"integrity", IntegrityError("mismatch"), ExitSoftware},
		{"explicit child code", &ExitError{Code: 137}, 137},
		{"plain error", fmt.Errorf("boom"), ExitSoftware},
		{"wrapped usage", fmt.Errorf("ctx: %w", Usage("bad")), ExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitCodeFor(c.err); got != c.want {
				t.Fatalf("ExitCodeFor(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

func TestHasKind(t *testing.T) {
	err := NetworkError("timeout to %s", "registry").Wrap(fmt.Errorf("i/o timeout"))
	if !HasKind(err, KindNetwork) {
		t.Fatal("expected KindNetwork")
	}
	if HasKind(err, KindIntegrity) {
		t.Fatal("did not expect KindIntegrity")
	}
	if HasKind(Usage("x"), KindNetwork) {
		t.Fatal("UsageError is not a typed Error")
	}
}
