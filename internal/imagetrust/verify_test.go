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
	bundle := map[string]any{"SignedEntryTimestamp": "set", "Payload": map[string]any{"logID": "rekor"}}
	stdout := testCosignOutput(t, digest, subject, issuer, bundle)
	parent := t.TempDir()
	var stagedDir string
	runner := runnerFunc(func(_ context.Context, executable string, args []string, dir string) ([]byte, []byte, error) {
		stagedDir = dir
		if filepath.Dir(executable) != dir || filepath.Base(executable) != verifierName {
			t.Fatalf("staged executable = %q in %q", executable, dir)
		}
		contents, err := os.ReadFile(executable)
		if err != nil || string(contents) != "authenticated cosign" {
			t.Fatalf("staged verifier = %q, %v", contents, err)
		}
		want := []string{
			"verify", "--output=json", "--max-workers=1",
			"--certificate-identity=" + subject,
			"--certificate-oidc-issuer=" + issuer,
			"ghcr.io/acme/tool@" + digest,
		}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
		return stdout, nil, nil
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
	wantBundle := sha256.Sum256(mustJSON(t, bundle))
	if got := result.BundleSHA256s(); !reflect.DeepEqual(got, []string{hex.EncodeToString(wantBundle[:])}) {
		t.Fatalf("BundleSHA256s() = %v", got)
	}
	stdoutSum := sha256.Sum256(stdout)
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
	stdout := testCosignOutput(t, digest, "", "", nil)
	parent := t.TempDir()
	v := verifier{
		now: time.Now,
		createStage: func() (string, error) {
			return os.MkdirTemp(parent, stagingPrefix)
		},
		runner: runnerFunc(func(_ context.Context, executable string, args []string, dir string) ([]byte, []byte, error) {
			if filepath.Dir(executable) != dir {
				t.Fatalf("verifier escaped stage: %q", executable)
			}
			wantPrefix := "--key=" + filepath.Join(dir, publicKeyName)
			if len(args) != 5 || args[3] != wantPrefix || args[4] != "registry.example.com/team/tool@"+digest {
				t.Fatalf("key verification args = %#v", args)
			}
			contents, err := os.ReadFile(strings.TrimPrefix(args[3], "--key="))
			if err != nil || !bytes.Equal(contents, key.Bytes()) {
				t.Fatalf("staged key = %q, %v", contents, err)
			}
			return stdout, nil, nil
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
	if result.Signer() != key.SHA256() || result.Issuer() != "" || result.SignatureCount() != 1 || len(result.BundleSHA256s()) != 0 {
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
			return testCosignOutput(t, digest, "subject", "https://issuer.example", nil), nil, nil
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

func TestValidateOutputRejectsMalformedOrMismatchedResults(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	rule := policy.ImageTrustRule{Mechanism: policy.ImageTrustKeyless, Subject: "subject", Issuer: "https://issuer.example"}
	valid := testCosignOutput(t, digest, rule.Subject, rule.Issuer, map[string]any{"Payload": map[string]any{"logID": "rekor"}})
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"malformed", []byte("{")},
		{"object not array", []byte(`{"Critical":{}}`)},
		{"empty array", []byte("[]")},
		{"trailing", append(append([]byte(nil), valid...), []byte(" {}")...)},
		{"wrong digest", testCosignOutput(t, "sha256:"+strings.Repeat("b", 64), rule.Subject, rule.Issuer, nil)},
		{"wrong type", bytes.Replace(valid, []byte(cosignPayloadType), []byte("in-toto attestation"), 1)},
		{"wrong subject", testCosignOutput(t, digest, "other", rule.Issuer, nil)},
		{"wrong issuer", testCosignOutput(t, digest, rule.Subject, "https://other.example", nil)},
		{"null bundle", testCosignOutputRaw(t, digest, rule.Subject, rule.Issuer, json.RawMessage("null"))},
		{"scalar bundle", testCosignOutputRaw(t, digest, rule.Subject, rule.Issuer, json.RawMessage(`"bundle"`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := validateOutput(tc.data, digest, rule); err == nil {
				t.Fatal("invalid cosign output accepted")
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

func TestBoundedBufferReportsOverflowWithoutShortWrite(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	n, err := buffer.Write([]byte("abcdef"))
	if err != nil || n != 6 || string(buffer.data) != "abcd" || !buffer.overflow {
		t.Fatalf("Write() = (%d, %v), data=%q overflow=%t", n, err, buffer.data, buffer.overflow)
	}
}

func testSnapshot(contents string) authenticatedSnapshot {
	sum := sha256.Sum256([]byte(contents))
	return authenticatedSnapshot{contents: []byte(contents), digest: hex.EncodeToString(sum[:])}
}

func testCosignOutput(t *testing.T, digest, subject, issuer string, bundle any) []byte {
	t.Helper()
	var raw json.RawMessage
	if bundle != nil {
		raw = mustJSON(t, bundle)
	}
	return testCosignOutputRaw(t, digest, subject, issuer, raw)
}

func testCosignOutputRaw(t *testing.T, digest, subject, issuer string, bundle json.RawMessage) []byte {
	t.Helper()
	optional := map[string]any{}
	if subject != "" {
		optional["Subject"] = subject
	}
	if issuer != "" {
		optional["Issuer"] = issuer
	}
	if bundle != nil {
		optional["Bundle"] = bundle
	}
	payload := []any{map[string]any{
		"Critical": map[string]any{
			"Identity": map[string]any{"docker-reference": ""},
			"Image":    map[string]any{"Docker-manifest-digest": digest},
			"Type":     cosignPayloadType,
		},
		"Optional": optional,
	}}
	return mustJSON(t, payload)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
