package installer

import (
	"context"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/lockfile"
	"github.com/koji-1009/gnpm/internal/npmrc"
	"github.com/koji-1009/gnpm/internal/policy"
	"github.com/koji-1009/gnpm/internal/project"
	"github.com/koji-1009/gnpm/internal/registry"
	"github.com/koji-1009/gnpm/internal/signature"
)

// verifySignature applies the configured signature policy to a tarball's
// signatures. Returns a warning (non-fatal) or a fatal IntegrityError.
func (op *Operation) verifySignature(ctx context.Context, name, version, integrity string, sigs []signature.Signature) (string, error) {
	if op.keyStore == nil { // policy == none
		return "", nil
	}
	result, err := op.keyStore.Verify(ctx, name, version, integrity, sigs)
	if err != nil {
		// A keys-endpoint failure should not silently pass strict mode.
		if op.Options.SignaturePolicy == signature.PolicyStrict {
			return "", err
		}
		return "", nil
	}
	return signature.Enforce(op.Options.SignaturePolicy, name, version, result)
}

func toSigs(sigs []registry.DistSignature) []signature.Signature {
	out := make([]signature.Signature, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, signature.Signature{KeyID: s.KeyID, Sig: s.Sig})
	}
	return out
}

func lockedToSigs(sigs []lockfile.LockedSignature) []signature.Signature {
	out := make([]signature.Signature, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, signature.Signature{KeyID: s.KeyID, Sig: s.Sig})
	}
	return out
}

// checkPackageManager enforces the pmOnFail policy against
// package.json#packageManager / devEngines.packageManager (doc/spec.md
// §2.4 pmOnFail).
func (op *Operation) checkPackageManager(pkg *project.PackageJSON, cfg *npmrc.Config) error {
	setting := cfg.GetOr("pm-on-fail", "")
	if setting == "" && project.DetectMode(op.ProjectRoot) == project.ModePnpm {
		setting = project.ReadPnpmWorkspace(op.ProjectRoot).Settings["pm-on-fail"]
	}
	result := policy.EvaluatePmOnFail(pkg, op.Version, policy.ParsePmOnFail(setting))
	switch result.Action {
	case policy.PmActWarn:
		op.Log.Warn("%s", result.Message)
	case policy.PmActFail:
		return core.Usage("%s (pmOnFail=error)", result.Message)
	}
	return nil
}
