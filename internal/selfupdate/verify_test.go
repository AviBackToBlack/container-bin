package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVerifyRequiresChecksumAndExactAttestationPolicy(t *testing.T) {
	fixture := newVerificationFixture(t)
	var gotExecutable string
	var gotArgs []string
	runner := attestationRunnerFunc(func(_ context.Context, executable string, args []string) ([]byte, []byte, error) {
		gotExecutable = executable
		gotArgs = append([]string(nil), args...)
		return attestationJSON(fixture.digest), nil, nil
	})
	verified, err := (verifier{runner: runner}).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err != nil {
		t.Fatal(err)
	}
	if verified.BinaryPath() != fixture.binary || verified.Target() != fixture.plan.Target || verified.SHA256() != fixture.digest || verified.Size() != int64(len(fixture.binaryBytes)) {
		t.Fatalf("unexpected verified result: %+v", verified)
	}
	wantArgs := []string{
		"attestation", "verify", fixture.binary,
		"--hostname", "github.com",
		"--repo", expectedReleaseRepo,
		"--signer-workflow", expectedReleaseRepo + "/" + expectedReleaseWorkflow,
		"--source-ref", "refs/tags/v1.2.0",
		"--cert-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--digest-alg", "sha256",
		"--predicate-type", provenancePredicate,
		"--format", "json",
	}
	if gotExecutable != fixture.gh || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("attestation invocation = %q %q, want %q %q", gotExecutable, gotArgs, fixture.gh, wantArgs)
	}
}

func TestVerifyRejectsChecksumFailuresBeforeAttestation(t *testing.T) {
	cases := []struct {
		name     string
		manifest func(verificationFixture) string
		want     string
	}{
		{name: "mismatch", manifest: func(f verificationFixture) string {
			return strings.Repeat("0", 64) + "  cb.exe\n" + strings.Repeat("a", 64) + "  " + f.plan.Archive.Name + "\n"
		}, want: "does not match"},
		{name: "duplicate", manifest: func(f verificationFixture) string {
			return f.digest + "  cb.exe\n" + f.digest + "  cb.exe\n"
		}, want: "duplicate"},
		{name: "missing archive", manifest: func(f verificationFixture) string {
			return f.digest + "  cb.exe\n"
		}, want: "missing asset"},
		{name: "unexpected", manifest: func(f verificationFixture) string {
			return f.digest + "  cb.exe\n" + strings.Repeat("a", 64) + "  other.zip\n"
		}, want: "unexpected asset"},
		{name: "binary marker", manifest: func(f verificationFixture) string {
			return f.digest + " *cb.exe\n" + strings.Repeat("a", 64) + "  " + f.plan.Archive.Name + "\n"
		}, want: "non-canonical"},
		{name: "uppercase digest", manifest: func(f verificationFixture) string {
			return strings.ToUpper(f.digest) + "  cb.exe\n" + strings.Repeat("a", 64) + "  " + f.plan.Archive.Name + "\n"
		}, want: "invalid SHA-256"},
		{name: "CRLF", manifest: func(f verificationFixture) string {
			return f.digest + "  cb.exe\r\n" + strings.Repeat("a", 64) + "  " + f.plan.Archive.Name + "\r\n"
		}, want: "canonical LF"},
		{name: "no final newline", manifest: func(f verificationFixture) string {
			return f.digest + "  cb.exe\n" + strings.Repeat("a", 64) + "  " + f.plan.Archive.Name
		}, want: "canonical LF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			manifest := tc.manifest(fixture)
			if err := os.WriteFile(fixture.checksums, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.plan.Checksums.Size = int64(len(manifest))
			called := false
			_, err := (verifier{runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				called = true
				return nil, nil, nil
			})}).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
			if err == nil || !strings.Contains(err.Error(), tc.want) || called {
				t.Fatalf("Verify = %v, called=%v, want %q before attestation", err, called, tc.want)
			}
		})
	}
}

func TestVerifyRejectsInvalidPolicyLayoutAndPathsBeforeAttestation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*verificationFixture)
		want   string
	}{
		{name: "wrong repository", mutate: func(f *verificationFixture) { f.plan.ExpectedRepo = "other/repo" }, want: "unexpected provenance policy"},
		{name: "wrong workflow", mutate: func(f *verificationFixture) { f.plan.Workflow = ".github/workflows/other.yml" }, want: "unexpected provenance policy"},
		{name: "wrong ref", mutate: func(f *verificationFixture) { f.plan.ExpectedRef = "refs/heads/main" }, want: "unexpected provenance policy"},
		{name: "wrong architecture", mutate: func(f *verificationFixture) { f.plan.Arch = "arm64" }, want: "no qualified artifact"},
		{name: "wrong binary name", mutate: func(f *verificationFixture) { f.plan.Binary.Name = "other.exe" }, want: "unexpected asset layout"},
		{name: "wrong archive name", mutate: func(f *verificationFixture) { f.plan.Archive.Name = "other.zip" }, want: "unexpected asset layout"},
		{name: "wrong release URL", mutate: func(f *verificationFixture) { f.plan.ReleaseURL = "https://evil.example/release" }, want: "non-canonical release URL"},
		{name: "wrong binary URL", mutate: func(f *verificationFixture) { f.plan.Binary.URL = "https://evil.example/cb.exe" }, want: "non-canonical release URL"},
		{name: "relative binary", mutate: func(f *verificationFixture) { f.binary = "cb.exe" }, want: "must be absolute"},
		{name: "relative gh", mutate: func(f *verificationFixture) { f.gh = "gh.exe" }, want: "must be absolute"},
		{name: "binary size changed", mutate: func(f *verificationFixture) { f.plan.Binary.Size++ }, want: "size is"},
		{name: "manifest size changed", mutate: func(f *verificationFixture) { f.plan.Checksums.Size++ }, want: "manifest size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			tc.mutate(&fixture)
			called := false
			_, err := (verifier{runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				called = true
				return nil, nil, nil
			})}).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
			if err == nil || !strings.Contains(err.Error(), tc.want) || called {
				t.Fatalf("Verify = %v, called=%v, want %q before attestation", err, called, tc.want)
			}
		})
	}

	t.Run("separate directories", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		other := filepath.Join(t.TempDir(), "SHA256SUMS")
		data, err := os.ReadFile(fixture.checksums)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(other, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = (verifier{runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
			return nil, nil, errors.New("unexpected")
		})}).Verify(context.Background(), fixture.plan, fixture.binary, other, fixture.gh)
		if err == nil || !strings.Contains(err.Error(), "exact staged layout") {
			t.Fatalf("separate-directory Verify = %v", err)
		}
	})
}

func TestVerifyRejectsAttestationFailureOrInvalidResult(t *testing.T) {
	cases := []struct {
		name   string
		stdout func(verificationFixture) []byte
		stderr []byte
		err    error
		want   string
	}{
		{name: "command failure", stderr: []byte("identity mismatch"), err: errors.New("exit 1"), want: "identity mismatch"},
		{name: "empty", stdout: func(verificationFixture) []byte { return nil }, want: "invalid output"},
		{name: "malformed", stdout: func(verificationFixture) []byte { return []byte("{") }, want: "parse"},
		{name: "none", stdout: func(verificationFixture) []byte { return []byte("[]") }, want: "no verified attestations"},
		{name: "wrong predicate", stdout: func(f verificationFixture) []byte {
			return []byte(fmt.Sprintf(`[{"verificationResult":{"statement":{"predicateType":"other","subject":[{"digest":{"sha256":"%s"}}]}}}]`, f.digest))
		}, want: "unexpected predicate"},
		{name: "wrong digest", stdout: func(verificationFixture) []byte { return attestationJSON(strings.Repeat("f", 64)) }, want: "does not cover"},
		{name: "trailing JSON", stdout: func(f verificationFixture) []byte { return append(attestationJSON(f.digest), []byte("{}")...) }, want: "trailing JSON"},
		{name: "oversized", stdout: func(verificationFixture) []byte { return make([]byte, maxVerifierOutput+1) }, want: "safety limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			stdout := []byte(nil)
			if tc.stdout != nil {
				stdout = tc.stdout(fixture)
			}
			_, err := (verifier{runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				return stdout, tc.stderr, tc.err
			})}).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Verify = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyDetectsExecutableMutationDuringAttestation(t *testing.T) {
	fixture := newVerificationFixture(t)
	_, err := (verifier{runner: attestationRunnerFunc(func(_ context.Context, _ string, _ []string) ([]byte, []byte, error) {
		mutated := append([]byte(nil), fixture.binaryBytes...)
		mutated[0] ^= 0xff
		if writeErr := os.WriteFile(fixture.binary, mutated, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return attestationJSON(fixture.digest), nil, nil
	})}).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err == nil || !strings.Contains(err.Error(), "changed during verification") {
		t.Fatalf("mutation Verify = %v", err)
	}
}

func TestVerifyRejectsSymlinkInputs(t *testing.T) {
	fixture := newVerificationFixture(t)
	link := filepath.Join(t.TempDir(), "cb.exe")
	if err := os.Symlink(fixture.binary, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := (verifier{runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
		return nil, nil, errors.New("unexpected")
	})}).Verify(context.Background(), fixture.plan, link, fixture.checksums, fixture.gh)
	if err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlink Verify = %v", err)
	}
}

type verificationFixture struct {
	plan        Plan
	binary      string
	checksums   string
	gh          string
	binaryBytes []byte
	digest      string
}

func newVerificationFixture(t *testing.T) verificationFixture {
	t.Helper()
	dir := t.TempDir()
	binaryBytes := []byte("verified ContainerBin release binary")
	sum := sha256.Sum256(binaryBytes)
	digest := hex.EncodeToString(sum[:])
	archiveName := "container-bin-v1.2.0-windows-amd64.zip"
	manifest := digest + "  cb.exe\n" + strings.Repeat("a", 64) + "  " + archiveName + "\n"
	binary := filepath.Join(dir, "cb.exe")
	checksums := filepath.Join(dir, "SHA256SUMS")
	gh := filepath.Join(t.TempDir(), "gh.exe")
	for path, data := range map[string][]byte{
		binary:    binaryBytes,
		checksums: []byte(manifest),
		gh:        []byte("trusted GitHub CLI test executable"),
	} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return verificationFixture{
		plan: Plan{
			Current:      "v1.1.0",
			Target:       "v1.2.0",
			OS:           "windows",
			Arch:         "amd64",
			ReleaseURL:   releaseWebRoot + "/tag/v1.2.0",
			Binary:       Asset{Name: "cb.exe", URL: releaseWebRoot + "/download/v1.2.0/cb.exe", Size: int64(len(binaryBytes))},
			Archive:      Asset{Name: archiveName, URL: releaseWebRoot + "/download/v1.2.0/" + archiveName, Size: 1},
			Checksums:    Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.2.0/SHA256SUMS", Size: int64(len(manifest))},
			ExpectedRepo: expectedReleaseRepo,
			ExpectedRef:  "refs/tags/v1.2.0",
			Workflow:     expectedReleaseWorkflow,
		},
		binary:      binary,
		checksums:   checksums,
		gh:          gh,
		binaryBytes: binaryBytes,
		digest:      digest,
	}
}

func attestationJSON(digest string) []byte {
	return []byte(fmt.Sprintf(`[{"attestation":{"bundle":"ignored"},"verificationResult":{"statement":{"predicateType":"%s","subject":[{"name":"cb.exe","digest":{"sha256":"%s"}}]}}}]`, provenancePredicate, digest))
}

type attestationRunnerFunc func(context.Context, string, []string) ([]byte, []byte, error)

func (f attestationRunnerFunc) Run(ctx context.Context, executable string, args []string) ([]byte, []byte, error) {
	return f(ctx, executable, args)
}
