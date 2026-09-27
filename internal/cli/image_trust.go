package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/imagetrust"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
)

type verifiedImageTrust interface {
	Mechanism() policy.ImageTrustMechanism
	NetworkMode() policy.ImageTrustNetworkMode
	Repository() string
	Digest() string
	Signer() string
	Issuer() string
	VerifierSHA256() string
	PolicyFingerprint() string
	VerifiedAt() time.Time
	SignatureCount() int
	BundleSHA256s() []string
}

type repositoryImageResolver func(string) (lockfile.LockEntry, error)
type imageTrustSelector func(string) (bool, error)
type imageTrustVerifier func(context.Context, string, string) (verifiedImageTrust, error)

func resolveRepositoryImageWithTrust(configured string, machinePolicy policy.Policy) (lockfile.LockEntry, error) {
	return resolveRepositoryImageWithTrustUsing(
		context.Background(),
		configured,
		func(ref string) (bool, error) {
			_, required, err := machinePolicy.ImageTrustFor(ref)
			return required, err
		},
		func(ref string) (lockfile.LockEntry, error) {
			return lockfile.ResolveRepositoryImage(ref, machinePolicy)
		},
		func(ctx context.Context, ref, resolved string) (verifiedImageTrust, error) {
			return imagetrust.Verify(ctx, machinePolicy, ref, resolved)
		},
	)
}

func resolveRepositoryImageWithTrustUsing(ctx context.Context, configured string, selectTrust imageTrustSelector, resolve repositoryImageResolver, verify imageTrustVerifier) (lockfile.LockEntry, error) {
	if ctx == nil || selectTrust == nil || resolve == nil || verify == nil {
		return lockfile.LockEntry{}, errors.New("image trust lock producer is incomplete")
	}
	required, err := selectTrust(configured)
	if err != nil {
		return lockfile.LockEntry{}, fmt.Errorf("select image trust policy for %q: %w", configured, err)
	}
	entry, err := resolve(configured)
	if err != nil {
		return lockfile.LockEntry{}, err
	}
	if !required {
		return entry, nil
	}
	result, err := verify(ctx, configured, entry.Resolved)
	if err != nil {
		return lockfile.LockEntry{}, fmt.Errorf("verify image trust for %q: %w", configured, err)
	}
	evidence, err := lockEvidenceFromVerification(result)
	if err != nil {
		return lockfile.LockEntry{}, fmt.Errorf("record image trust for %q: %w", configured, err)
	}
	entry.Trust = evidence
	return entry, nil
}

func lockEvidenceFromVerification(result verifiedImageTrust) (*lockfile.ImageTrustEvidence, error) {
	if result == nil {
		return nil, errors.New("verifier returned no result")
	}
	if result.NetworkMode() != policy.ImageTrustOnline {
		return nil, fmt.Errorf("verification used unsupported network mode %q", result.NetworkMode())
	}
	if result.SignatureCount() < 1 {
		return nil, errors.New("verifier returned no authenticated signatures")
	}
	bundles := result.BundleSHA256s()
	if len(bundles) != 1 {
		return nil, fmt.Errorf("verification returned %d distinct transparency bundles; evidence schema requires exactly one", len(bundles))
	}
	return &lockfile.ImageTrustEvidence{
		Version:           lockfile.ImageTrustEvidenceVersion,
		Mechanism:         result.Mechanism(),
		Repository:        result.Repository(),
		Digest:            result.Digest(),
		Signer:            result.Signer(),
		Issuer:            result.Issuer(),
		BundleSHA256:      bundles[0],
		VerifiedAt:        result.VerifiedAt().UTC().Truncate(time.Second).Format(time.RFC3339),
		Verifier:          lockfile.ImageTrustVerifierCosign,
		VerifierSHA256:    result.VerifierSHA256(),
		PolicyFingerprint: result.PolicyFingerprint(),
	}, nil
}

func storeResolvedLockEntry(lock *lockfile.LockFile, configured string, entry lockfile.LockEntry) {
	if entry.Trust != nil && lock.Version < 2 {
		lock.Version = 2
	}
	lock.Images[configured] = entry
}
