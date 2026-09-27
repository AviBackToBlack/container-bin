package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
)

type fakeVerifiedImageTrust struct {
	mechanism         policy.ImageTrustMechanism
	networkMode       policy.ImageTrustNetworkMode
	repository        string
	digest            string
	signer            string
	issuer            string
	verifierSHA256    string
	policyFingerprint string
	verifiedAt        time.Time
	signatureCount    int
	bundles           []string
}

func (r fakeVerifiedImageTrust) Mechanism() policy.ImageTrustMechanism     { return r.mechanism }
func (r fakeVerifiedImageTrust) NetworkMode() policy.ImageTrustNetworkMode { return r.networkMode }
func (r fakeVerifiedImageTrust) Repository() string                        { return r.repository }
func (r fakeVerifiedImageTrust) Digest() string                            { return r.digest }
func (r fakeVerifiedImageTrust) Signer() string                            { return r.signer }
func (r fakeVerifiedImageTrust) Issuer() string                            { return r.issuer }
func (r fakeVerifiedImageTrust) VerifierSHA256() string                    { return r.verifierSHA256 }
func (r fakeVerifiedImageTrust) PolicyFingerprint() string                 { return r.policyFingerprint }
func (r fakeVerifiedImageTrust) VerifiedAt() time.Time                     { return r.verifiedAt }
func (r fakeVerifiedImageTrust) SignatureCount() int                       { return r.signatureCount }
func (r fakeVerifiedImageTrust) BundleSHA256s() []string                   { return append([]string(nil), r.bundles...) }

func validFakeTrustResult() fakeVerifiedImageTrust {
	return fakeVerifiedImageTrust{
		mechanism:         policy.ImageTrustKeyless,
		networkMode:       policy.ImageTrustOnline,
		repository:        "ghcr.io/acme/tool",
		digest:            "sha256:" + strings.Repeat("a", 64),
		signer:            "https://github.com/acme/tool/.github/workflows/release.yml@refs/tags/v1",
		issuer:            "https://token.actions.githubusercontent.com",
		verifierSHA256:    strings.Repeat("b", 64),
		policyFingerprint: strings.Repeat("c", 64),
		verifiedAt:        time.Date(2026, 9, 27, 12, 0, 0, 123, time.FixedZone("offset", 3600)),
		signatureCount:    1,
		bundles:           []string{strings.Repeat("d", 64)},
	}
}

func TestResolveRepositoryImageWithTrustProducesEvidence(t *testing.T) {
	configured := "ghcr.io/acme/tool:v1"
	result := validFakeTrustResult()
	resolved := result.repository + "@" + result.digest
	entry, err := resolveRepositoryImageWithTrustUsing(context.Background(), configured,
		func(ref string) (bool, error) { return ref == configured, nil },
		func(ref string) (lockfile.LockEntry, error) {
			return lockfile.LockEntry{Configured: ref, Resolved: resolved, Digest: result.digest}, nil
		},
		func(_ context.Context, ref, gotResolved string) (verifiedImageTrust, error) {
			if ref != configured || gotResolved != resolved {
				t.Fatalf("verify(%q, %q), want (%q, %q)", ref, gotResolved, configured, resolved)
			}
			return result, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Trust == nil || entry.Trust.Version != lockfile.ImageTrustEvidenceVersion || entry.Trust.Repository != result.repository || entry.Trust.Digest != result.digest || entry.Trust.BundleSHA256 != result.bundles[0] {
		t.Fatalf("unexpected evidence: %+v", entry.Trust)
	}
	if entry.Trust.VerifiedAt != "2026-09-27T11:00:00Z" {
		t.Fatalf("verified_at = %q", entry.Trust.VerifiedAt)
	}
	lf := &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{}}
	storeResolvedLockEntry(lf, configured, entry)
	if lf.Version != 2 || lf.Images[configured].Trust == nil {
		t.Fatalf("schema promotion failed: %+v", lf)
	}
	path := filepath.Join(t.TempDir(), "container-bin.lock")
	if err := lockfile.Write(path, lf); err != nil {
		t.Fatalf("write generated schema-2 evidence: %v", err)
	}
	loaded, err := lockfile.Load(path)
	if err != nil || loaded.Images[configured].Trust == nil {
		t.Fatalf("load generated schema-2 evidence = (%+v, %v)", loaded, err)
	}
}

func TestResolveRepositoryImageWithoutTrustKeepsDigestOnlyEntry(t *testing.T) {
	verified := false
	entry, err := resolveRepositoryImageWithTrustUsing(context.Background(), "docker.io/library/python:3.13",
		func(string) (bool, error) { return false, nil },
		func(ref string) (lockfile.LockEntry, error) {
			return lockfile.LockEntry{Configured: ref, Resolved: "python@sha256:" + strings.Repeat("a", 64), Digest: "sha256:" + strings.Repeat("a", 64)}, nil
		},
		func(context.Context, string, string) (verifiedImageTrust, error) { verified = true; return nil, nil },
	)
	if err != nil || entry.Trust != nil || verified {
		t.Fatalf("digest-only resolution = (%+v, %v), verified=%t", entry, err, verified)
	}
}

func TestResolveRepositoryImageWithTrustFailsClosed(t *testing.T) {
	configured := "ghcr.io/acme/tool:v1"
	result := validFakeTrustResult()
	resolved := result.repository + "@" + result.digest
	resolve := func(ref string) (lockfile.LockEntry, error) {
		return lockfile.LockEntry{Configured: ref, Resolved: resolved, Digest: result.digest}, nil
	}
	selectTrust := func(string) (bool, error) { return true, nil }
	for _, tc := range []struct {
		name    string
		result  fakeVerifiedImageTrust
		wantErr string
	}{
		{name: "no bundle", result: func() fakeVerifiedImageTrust { r := result; r.bundles = nil; return r }(), wantErr: "0 distinct transparency bundles"},
		{name: "multiple bundles", result: func() fakeVerifiedImageTrust {
			r := result
			r.bundles = []string{strings.Repeat("d", 64), strings.Repeat("e", 64)}
			return r
		}(), wantErr: "2 distinct transparency bundles"},
		{name: "no signature", result: func() fakeVerifiedImageTrust { r := result; r.signatureCount = 0; return r }(), wantErr: "no authenticated signatures"},
		{name: "offline", result: func() fakeVerifiedImageTrust { r := result; r.networkMode = policy.ImageTrustOfflineBundle; return r }(), wantErr: "unsupported network mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveRepositoryImageWithTrustUsing(context.Background(), configured, selectTrust, resolve,
				func(context.Context, string, string) (verifiedImageTrust, error) { return tc.result, nil })
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
	_, err := resolveRepositoryImageWithTrustUsing(context.Background(), configured, selectTrust, resolve,
		func(context.Context, string, string) (verifiedImageTrust, error) {
			return nil, errors.New("verification denied")
		})
	if err == nil || !strings.Contains(err.Error(), "verification denied") {
		t.Fatalf("verifier error = %v", err)
	}
}
