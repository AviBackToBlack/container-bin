package imagetrust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/policy"
)

type runnerFunc func(context.Context, string, []string, string) ([]byte, []byte, error)

func (f runnerFunc) Run(ctx context.Context, executable string, args []string, dir string) ([]byte, []byte, error) {
	return f(ctx, executable, args, dir)
}

func TestVerifyAuthenticatedKeylessStagesExactSnapshotAndValidatesOutput(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	issuer := "https://token.actions.githubusercontent.com"
	subject := "https://github.com/acme/tool/.github/workflows/release.yml@refs/tags/v1"
	bundle := testSignatureBundle(t, "keyless")
	downloadOutput := append(append([]byte(nil), bundle...), '\n')
	parent := t.TempDir()
	var stagedDir string
	calls := 0
	runner := runnerFunc(func(_ context.Context, executable string, args []string, dir string) ([]byte, []byte, error) {
		calls++
		stagedDir = dir
		if filepath.Dir(executable) != dir || filepath.Base(executable) != verifierName {
			t.Fatalf("staged executable = %q in %q", executable, dir)
		}
		contents, err := os.ReadFile(executable)
		if err != nil || string(contents) != "authenticated cosign" {
			t.Fatalf("staged verifier = %q, %v", contents, err)
		}
		if calls == 1 {
			want := []string{"download", "signature", "ghcr.io/acme/tool@" + digest}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("download args = %#v, want %#v", args, want)
			}
			return downloadOutput, nil, nil
		}
		if calls != 2 {
			t.Fatalf("unexpected verifier call %d: %#v", calls, args)
		}
		wantPrefix := []string{
			"verify-blob-attestation",
			"--bundle=" + filepath.Join(dir, "bundle-001.sigstore.json"),
			"--digest=" + strings.TrimPrefix(digest, "sha256:"),
			"--digestAlg=sha256",
			"--type=" + cosignPayloadType,
			"--certificate-identity=" + subject,
			"--certificate-oidc-issuer=" + issuer,
		}
		if !reflect.DeepEqual(args, wantPrefix) {
			t.Fatalf("bundle verification args = %#v, want %#v", args, wantPrefix)
		}
		gotBundle, err := os.ReadFile(filepath.Join(dir, "bundle-001.sigstore.json"))
		if err != nil || !bytes.Equal(gotBundle, bundle) {
			t.Fatalf("staged bundle = %q, %v", gotBundle, err)
		}
		return nil, nil, nil
	})
	now := time.Date(2026, 9, 26, 18, 2, 3, 456, time.FixedZone("offset", 3600))
	v := verifier{
		runner: runner,
		now:    func() time.Time { return now },
		createStage: func() (string, error) {
			return os.MkdirTemp(parent, stagingPrefix)
		},
	}
	request := verificationRequest{
		resolved:          "ghcr.io/acme/tool@" + digest,
		repository:        "ghcr.io/acme/tool",
		digest:            digest,
		rule:              policy.ImageTrustRule{Repository: "ghcr.io/acme", Mechanism: policy.ImageTrustKeyless, Issuer: issuer, Subject: subject, NetworkMode: policy.ImageTrustOnline},
		verifier:          testSnapshot("authenticated cosign"),
		policyFingerprint: strings.Repeat("b", 64),
	}
	result, err := v.verifyAuthenticated(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mechanism() != policy.ImageTrustKeyless || result.NetworkMode() != policy.ImageTrustOnline || result.Repository() != request.repository || result.Digest() != digest || result.Signer() != subject || result.Issuer() != issuer || result.SignatureCount() != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.VerifiedAt() != time.Date(2026, 9, 26, 17, 2, 3, 0, time.UTC) {
		t.Fatalf("VerifiedAt() = %s", result.VerifiedAt())
	}
	wantBundle := sha256.Sum256(bundle)
	if got := result.BundleSHA256s(); !reflect.DeepEqual(got, []string{hex.EncodeToString(wantBundle[:])}) {
		t.Fatalf("BundleSHA256s() = %v", got)
	}
	stdoutSum := sha256.Sum256(downloadOutput)
	if result.OutputSHA256() != hex.EncodeToString(stdoutSum[:]) || result.VerifierSHA256() != request.verifier.SHA256() || result.PolicyFingerprint() != request.policyFingerprint {
		t.Fatalf("result bindings are incomplete: %+v", result)
	}
	if _, err := os.Stat(stagedDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private stage remains after success: %v", err)
	}
	copy := result.BundleSHA256s()
	copy[0] = strings.Repeat("f", 64)
	if result.BundleSHA256s()[0] == copy[0] {
		t.Fatal("BundleSHA256s exposed mutable internal state")
	}
}

func TestVerifyAuthenticatedKeyStagesPinnedKeyAndScrubsSourcePaths(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	key := testSnapshot("public key bytes")
	bundle := testSignatureBundle(t, "key")
	parent := t.TempDir()
	calls := 0
	v := verifier{
		now: time.Now,
		createStage: func() (string, error) {
			return os.MkdirTemp(parent, stagingPrefix)
		},
		runner: runnerFunc(func(_ context.Context, executable string, args []string, dir string) ([]byte, []byte, error) {
			calls++
			if filepath.Dir(executable) != dir {
				t.Fatalf("verifier escaped stage: %q", executable)
			}
			if calls == 1 {
				return append(append([]byte(nil), bundle...), '\n'), nil, nil
			}
			wantKey := "--key=" + filepath.Join(dir, publicKeyName)
			if calls != 2 || len(args) != 6 || args[0] != "verify-blob-attestation" || args[5] != wantKey {
				t.Fatalf("key bundle verification args = %#v", args)
			}
			contents, err := os.ReadFile(strings.TrimPrefix(args[5], "--key="))
			if err != nil || !bytes.Equal(contents, key.Bytes()) {
				t.Fatalf("staged key = %q, %v", contents, err)
			}
			return nil, nil, nil
		}),
	}
	request := verificationRequest{
		resolved:          "registry.example.com/team/tool@" + digest,
		repository:        "registry.example.com/team/tool",
		digest:            digest,
		rule:              policy.ImageTrustRule{Repository: "registry.example.com/team", Mechanism: policy.ImageTrustKey, NetworkMode: policy.ImageTrustOnline},
		verifier:          testSnapshot("cosign bytes"),
		key:               key,
		policyFingerprint: strings.Repeat("d", 64),
	}
	result, err := v.verifyAuthenticated(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Signer() != key.SHA256() || result.Issuer() != "" || result.SignatureCount() != 1 || len(result.BundleSHA256s()) != 1 {
		t.Fatalf("unexpected key result: %+v", result)
	}
}

func TestVerifyAuthenticatedRejectsOfflineBeforeStaging(t *testing.T) {
	called := false
	v := verifier{
		runner: runnerFunc(func(context.Context, string, []string, string) ([]byte, []byte, error) {
			called = true
			return nil, nil, nil
		}),
		now: time.Now,
		createStage: func() (string, error) {
			called = true
			return "", errors.New("must not stage")
		},
	}
	_, err := v.verifyAuthenticated(context.Background(), verificationRequest{
		rule: policy.ImageTrustRule{Repository: "ghcr.io/acme", Mechanism: policy.ImageTrustKeyless, NetworkMode: policy.ImageTrustOfflineBundle},
	})
	if err == nil || !strings.Contains(err.Error(), "complete offline trusted-root material") || called {
		t.Fatalf("offline verification = %v, called=%t", err, called)
	}
}

func TestVerifyAuthenticatedRejectsStagedMutationAndCleansUp(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	parent := t.TempDir()
	var stagedDir string
	v := verifier{
		now: time.Now,
		createStage: func() (string, error) {
			var err error
			stagedDir, err = os.MkdirTemp(parent, stagingPrefix)
			return stagedDir, err
		},
		runner: runnerFunc(func(_ context.Context, executable string, _ []string, _ string) ([]byte, []byte, error) {
			if err := os.WriteFile(executable, []byte("mutated staged verifier"), 0700); err != nil {
				t.Fatal(err)
			}
			return append(testSignatureBundle(t, "mutated"), '\n'), nil, nil
		}),
	}
	_, err := v.verifyAuthenticated(context.Background(), verificationRequest{
		resolved:   "ghcr.io/acme/tool@" + digest,
		repository: "ghcr.io/acme/tool",
		digest:     digest,
		rule: policy.ImageTrustRule{
			Repository: "ghcr.io/acme", Mechanism: policy.ImageTrustKeyless,
			Subject: "subject", Issuer: "https://issuer.example", NetworkMode: policy.ImageTrustOnline,
		},
		verifier:          testSnapshot("original staged verifier"),
		policyFingerprint: strings.Repeat("f", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "changed during verification") {
		t.Fatalf("staged mutation error = %v", err)
	}
	if _, statErr := os.Stat(stagedDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("private stage remains after failure: %v", statErr)
	}
}

func TestVerifyAuthenticatedBoundsErrorsAndCleansUp(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	parent := t.TempDir()
	v := verifier{
		now: time.Now,
		createStage: func() (string, error) {
			return os.MkdirTemp(parent, stagingPrefix)
		},
		runner: runnerFunc(func(context.Context, string, []string, string) ([]byte, []byte, error) {
			return nil, []byte("bad\nmessage\x1b[31m"), errors.New("exit 1")
		}),
	}
	_, err := v.verifyAuthenticated(context.Background(), verificationRequest{
		resolved:   "ghcr.io/acme/tool@" + digest,
		repository: "ghcr.io/acme/tool",
		digest:     digest,
		rule: policy.ImageTrustRule{
			Repository: "ghcr.io/acme", Mechanism: policy.ImageTrustKeyless,
			Subject: "subject", Issuer: "https://issuer.example", NetworkMode: policy.ImageTrustOnline,
		},
		verifier:          testSnapshot("cosign"),
		policyFingerprint: strings.Repeat("b", 64),
	})
	if err == nil || !strings.Contains(err.Error(), `"bad\nmessage\x1b[31m"`) {
		t.Fatalf("runner error was not safely quoted: %v", err)
	}
}

func TestParseDownloadedBundlesIsStrict(t *testing.T) {
	valid := testSignatureBundle(t, "one")
	second := testSignatureBundle(t, "two")
	got, err := parseDownloadedBundles(append(append(append([]byte(nil), valid...), '\n'), second...))
	if err != nil || len(got) != 2 || !bytes.Equal(got[0], valid) || !bytes.Equal(got[1], second) {
		t.Fatalf("parseDownloadedBundles() = (%q, %v)", got, err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"malformed", []byte("{")},
		{"array", []byte("[]")},
		{"scalar", []byte(`"bundle"`)},
		{"empty object", []byte("{}")},
		{"trailing garbage", append(append([]byte(nil), valid...), []byte(" garbage")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseDownloadedBundles(tc.data); err == nil {
				t.Fatal("invalid cosign bundle output accepted")
			}
		})
	}
}

func TestSplitResolvedDigestIsStrict(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	repository, gotDigest, err := splitResolvedDigest("registry-1.docker.io/library/python@" + digest)
	if err != nil || repository != "docker.io/library/python" || gotDigest != digest {
		t.Fatalf("splitResolvedDigest() = (%q, %q, %v)", repository, gotDigest, err)
	}
	for _, value := range []string{
		"python:3.13",
		"ghcr.io/acme/tool@sha256:short",
		"ghcr.io/acme/tool:tag@" + digest,
		"ghcr.io/acme/tool@@" + digest,
		"sha256:" + strings.Repeat("a", 64),
	} {
		if _, _, err := splitResolvedDigest(value); err == nil {
			t.Errorf("accepted non-immutable resolved image %q", value)
		}
	}
}

func TestCanonicalVerificationTargetNormalizesDockerHubAlias(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	repository, gotDigest, target, err := canonicalVerificationTarget(
		"python:3.13",
		"registry-1.docker.io/library/python@"+digest,
	)
	if err != nil || repository != "docker.io/library/python" || gotDigest != digest || target != "docker.io/library/python@"+digest {
		t.Fatalf("canonicalVerificationTarget() = (%q, %q, %q, %v)", repository, gotDigest, target, err)
	}
	if _, _, _, err := canonicalVerificationTarget("ghcr.io/acme/tool:v1", "ghcr.io/other/tool@"+digest); err == nil {
		t.Fatal("mismatched resolved repository was accepted")
	}
}

func TestBoundedBufferReportsOverflowWithoutShortWrite(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	n, err := buffer.Write([]byte("abcdef"))
	if err != nil || n != 6 || string(buffer.data) != "abcd" || !buffer.overflow {
		t.Fatalf("Write() = (%d, %v), data=%q overflow=%t", n, err, buffer.data, buffer.overflow)
	}
}

func testSnapshot(contents string) authenticatedSnapshot {
	return testSnapshotBytes([]byte(contents))
}

func testSnapshotBytes(contents []byte) authenticatedSnapshot {
	sum := sha256.Sum256(contents)
	return authenticatedSnapshot{contents: append([]byte(nil), contents...), digest: hex.EncodeToString(sum[:])}
}

func testSignatureBundle(t *testing.T, id string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		"testID":    id,
	})
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
