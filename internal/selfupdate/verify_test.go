package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
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
	verified, err := testVerifier(runner).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err != nil {
		t.Fatal(err)
	}
	resolvedBinary := mustResolveTestPath(t, fixture.binary)
	resolvedGH := mustResolveTestPath(t, fixture.gh)
	if verified.BinaryPath() != resolvedBinary || verified.Target() != fixture.plan.Target || verified.SHA256() != fixture.digest || verified.Size() != int64(len(fixture.binaryBytes)) {
		t.Fatalf("unexpected verified result: %+v", verified)
	}
	wantArgs := []string{
		"attestation", "verify", resolvedBinary,
		"--hostname", "github.com",
		"--repo", expectedReleaseRepo,
		"--cert-identity", "https://github.com/" + expectedReleaseRepo + "/" + expectedReleaseWorkflow + "@refs/tags/v1.2.0",
		"--source-ref", "refs/tags/v1.2.0",
		"--cert-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--digest-alg", "sha256",
		"--predicate-type", provenancePredicate,
		"--format", "json",
	}
	if gotExecutable != resolvedGH || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("attestation invocation = %q %q, want %q %q", gotExecutable, gotArgs, resolvedGH, wantArgs)
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
			_, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				called = true
				return nil, nil, nil
			})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
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
		{name: "wrong architecture", mutate: func(f *verificationFixture) { f.plan.Arch = "386" }, want: "no qualified artifact"},
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
			_, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				called = true
				return nil, nil, nil
			})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
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
		_, err = testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
			return nil, nil, errors.New("unexpected")
		})).Verify(context.Background(), fixture.plan, fixture.binary, other, fixture.gh)
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
			_, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				return stdout, tc.stderr, tc.err
			})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Verify = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyDetectsExecutableMutationDuringAttestation(t *testing.T) {
	fixture := newVerificationFixture(t)
	_, err := testVerifier(attestationRunnerFunc(func(_ context.Context, _ string, _ []string) ([]byte, []byte, error) {
		mutated := append([]byte(nil), fixture.binaryBytes...)
		mutated[0] ^= 0xff
		if writeErr := os.WriteFile(fixture.binary, mutated, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return attestationJSON(fixture.digest), nil, nil
	})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err == nil || !strings.Contains(err.Error(), "changed during verification") {
		t.Fatalf("mutation Verify = %v", err)
	}
}

func TestVerifyARM64AuthenticatesArchiveBeforeExactExtraction(t *testing.T) {
	fixture := newARM64VerificationFixture(t, nil)
	called := false
	verified, err := testVerifier(attestationRunnerFunc(func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		called = true
		if args[2] != mustResolveTestPath(t, fixture.binary) {
			t.Fatalf("attested path = %q", args[2])
		}
		return attestationJSON(fixture.digest), nil, nil
	})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err != nil {
		t.Fatal(err)
	}
	if !called || filepath.Base(verified.BinaryPath()) != "cb.exe" {
		t.Fatalf("unexpected ARM64 verification result: called=%t verified=%+v", called, verified)
	}
	data, err := os.ReadFile(verified.BinaryPath())
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("verified ARM64 ContainerBin executable")
	sum := sha256.Sum256(want)
	if !bytes.Equal(data, want) || verified.SHA256() != hex.EncodeToString(sum[:]) || verified.Size() != int64(len(want)) {
		t.Fatalf("unexpected extracted ARM64 executable: bytes=%q verified=%+v", data, verified)
	}
	verifiedAgain, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
		return attestationJSON(fixture.digest), nil, nil
	})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err != nil {
		t.Fatal(err)
	}
	if verifiedAgain.BinaryPath() != verified.BinaryPath() || verifiedAgain.SHA256() != verified.SHA256() || verifiedAgain.Size() != verified.Size() {
		t.Fatalf("repeat verification changed result: first=%+v second=%+v", verified, verifiedAgain)
	}
}

func TestVerifyARM64RejectsMismatchedExistingExtractionWithoutOverwrite(t *testing.T) {
	fixture := newARM64VerificationFixture(t, nil)
	destination := filepath.Join(filepath.Dir(fixture.binary), "cb.exe")
	want := []byte("untrusted pre-existing bytes")
	if err := os.WriteFile(destination, want, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
		return attestationJSON(fixture.digest), nil, nil
	})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err == nil || !strings.Contains(err.Error(), "does not match the authenticated archive") {
		t.Fatalf("mismatched existing extraction Verify = %v", err)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(data, want) {
		t.Fatalf("mismatched existing extraction was changed: bytes=%q err=%v", data, readErr)
	}
}

func TestVerifyAMD64AcceptsCanonicalDualArchitectureManifest(t *testing.T) {
	fixture := newVerificationFixture(t)
	manifest, err := os.ReadFile(fixture.checksums)
	if err != nil {
		t.Fatal(err)
	}
	manifest = append(manifest, []byte(strings.Repeat("b", 64)+"  container-bin-v1.2.0-windows-arm64.zip\n")...)
	if err := os.WriteFile(fixture.checksums, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.plan.Checksums.Size = int64(len(manifest))
	fixture.plan.checksumLayout = checksumLayoutDualArch
	if _, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
		return attestationJSON(fixture.digest), nil, nil
	})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyARM64RejectsUnexpectedArchiveEntryAfterAuthentication(t *testing.T) {
	fixture := newARM64VerificationFixture(t, map[string][]byte{"unexpected.bin": []byte("unsafe")})
	called := false
	_, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
		called = true
		return attestationJSON(fixture.digest), nil, nil
	})).Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err == nil || !strings.Contains(err.Error(), "unexpected entry") || !called {
		t.Fatalf("ARM64 unsafe archive Verify = %v, called=%t", err, called)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(fixture.binary), "cb.exe")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsafe archive left extracted executable: %v", statErr)
	}
}

func TestVerifyRejectsGitHubCLIAuthenticationFailureOrMutation(t *testing.T) {
	t.Run("authentication failure", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		called := false
		v := verifier{
			runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				called = true
				return nil, nil, nil
			}),
			authenticate: func(context.Context, string) (string, error) {
				return "", errors.New("untrusted publisher")
			},
		}
		_, err := v.Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
		if err == nil || !strings.Contains(err.Error(), "untrusted publisher") || called {
			t.Fatalf("Verify = %v, called=%v", err, called)
		}
	})

	t.Run("changed during verification", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		authentications := 0
		v := verifier{
			runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
				return attestationJSON(fixture.digest), nil, nil
			}),
			authenticate: func(context.Context, string) (string, error) {
				authentications++
				return fmt.Sprintf("digest-%d", authentications), nil
			},
		}
		_, err := v.Verify(context.Background(), fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
		if err == nil || !strings.Contains(err.Error(), "GitHub CLI executable changed") || authentications != 2 {
			t.Fatalf("Verify = %v, authentications=%d", err, authentications)
		}
	})
}

func TestHashVerificationFileRejectsSizeChangeAfterInspection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cb.exe")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("-after"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = hashVerificationFile(path, info)
	if err == nil || !strings.Contains(err.Error(), "changed size while hashing") {
		t.Fatalf("hashVerificationFile error = %v", err)
	}
}

func TestVerifyPropagatesCancellationToGitHubCLIAuthentication(t *testing.T) {
	fixture := newVerificationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	authenticated := false
	runnerCalled := false
	v := verifier{
		runner: attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
			runnerCalled = true
			return nil, nil, nil
		}),
		authenticate: func(ctx context.Context, _ string) (string, error) {
			authenticated = true
			return "", ctx.Err()
		},
	}
	_, err := v.Verify(ctx, fixture.plan, fixture.binary, fixture.checksums, fixture.gh)
	if err == nil || !strings.Contains(err.Error(), "context canceled") || !authenticated || runnerCalled {
		t.Fatalf("Verify() error = %v, authenticated=%t, runnerCalled=%t", err, authenticated, runnerCalled)
	}
}

func TestAttestationEnvironmentIsExplicitlyAllowlisted(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_TOKEN", "ignored-fallback")
	t.Setenv("GH_HOST", "attacker.example")
	t.Setenv("GH_CONFIG_DIR", filepath.Join(t.TempDir(), "hostile-config"))
	t.Setenv("HTTPS_PROXY", "https://attacker.example")
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "attacker.pem"))
	t.Setenv("SystemRoot", `X:\attacker`)
	t.Setenv("WINDIR", `X:\attacker`)
	hostileLocalAppData := filepath.Join(t.TempDir(), "attacker-local-app-data")
	t.Setenv("LOCALAPPDATA", hostileLocalAppData)
	env, err := attestationEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("invalid environment entry %q", entry)
		}
		got[strings.ToUpper(name)] = value
	}
	if got["GH_TOKEN"] != "test-token" || got["GH_PROMPT_DISABLED"] != "1" || got["NO_COLOR"] != "1" {
		t.Fatalf("required verifier environment missing: %v", got)
	}
	for _, forbidden := range []string{"GITHUB_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "HTTPS_PROXY", "SSL_CERT_FILE", "PATH", "HOME"} {
		if _, ok := got[forbidden]; ok {
			t.Errorf("inherited verifier environment contains %s", forbidden)
		}
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"SYSTEMROOT", "WINDIR"} {
			if strings.EqualFold(got[name], `X:\attacker`) {
				t.Errorf("verifier environment trusted inherited %s", name)
			}
		}
		if strings.EqualFold(got["LOCALAPPDATA"], hostileLocalAppData) {
			t.Error("verifier environment trusted inherited LOCALAPPDATA")
		}
	}
}

func TestAttestationEnvironmentRequiresExplicitToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	_, err := attestationEnvironment()
	if err == nil || !strings.Contains(err.Error(), "explicit GH_TOKEN or GITHUB_TOKEN") {
		t.Fatalf("attestationEnvironment error = %v", err)
	}
}

func TestVerifyRejectsSymlinkInputs(t *testing.T) {
	fixture := newVerificationFixture(t)
	link := filepath.Join(t.TempDir(), "cb.exe")
	if err := os.Symlink(fixture.binary, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := testVerifier(attestationRunnerFunc(func(context.Context, string, []string) ([]byte, []byte, error) {
		return nil, nil, errors.New("unexpected")
	})).Verify(context.Background(), fixture.plan, link, fixture.checksums, fixture.gh)
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
			Current:        "v1.1.0",
			Target:         "v1.2.0",
			OS:             "windows",
			Arch:           "amd64",
			ReleaseURL:     releaseWebRoot + "/tag/v1.2.0",
			Binary:         Asset{Name: "cb.exe", URL: releaseWebRoot + "/download/v1.2.0/cb.exe", Size: int64(len(binaryBytes))},
			Archive:        Asset{Name: archiveName, URL: releaseWebRoot + "/download/v1.2.0/" + archiveName, Size: 1},
			Checksums:      Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.2.0/SHA256SUMS", Size: int64(len(manifest))},
			ExpectedRepo:   expectedReleaseRepo,
			ExpectedRef:    "refs/tags/v1.2.0",
			Workflow:       expectedReleaseWorkflow,
			checksumLayout: checksumLayoutLegacyAMD64,
		},
		binary:      binary,
		checksums:   checksums,
		gh:          gh,
		binaryBytes: binaryBytes,
		digest:      digest,
	}
}

func newARM64VerificationFixture(t *testing.T, extras map[string][]byte) verificationFixture {
	t.Helper()
	dir := t.TempDir()
	archiveName := "container-bin-v1.2.0-windows-arm64.zip"
	archiveBytes := arm64TestArchive(t, extras)
	sum := sha256.Sum256(archiveBytes)
	digest := hex.EncodeToString(sum[:])
	manifest := strings.Repeat("a", 64) + "  cb.exe\n" +
		strings.Repeat("b", 64) + "  container-bin-v1.2.0-windows-amd64.zip\n" +
		digest + "  " + archiveName + "\n"
	archive := filepath.Join(dir, archiveName)
	checksums := filepath.Join(dir, "SHA256SUMS")
	gh := filepath.Join(t.TempDir(), "gh.exe")
	for path, data := range map[string][]byte{
		archive:   archiveBytes,
		checksums: []byte(manifest),
		gh:        []byte("trusted GitHub CLI test executable"),
	} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	asset := Asset{Name: archiveName, URL: releaseWebRoot + "/download/v1.2.0/" + archiveName, Size: int64(len(archiveBytes))}
	return verificationFixture{
		plan: Plan{
			Current:        "v1.1.0",
			Target:         "v1.2.0",
			OS:             "windows",
			Arch:           "arm64",
			ReleaseURL:     releaseWebRoot + "/tag/v1.2.0",
			Binary:         asset,
			Archive:        asset,
			Checksums:      Asset{Name: "SHA256SUMS", URL: releaseWebRoot + "/download/v1.2.0/SHA256SUMS", Size: int64(len(manifest))},
			ExpectedRepo:   expectedReleaseRepo,
			ExpectedRef:    "refs/tags/v1.2.0",
			Workflow:       expectedReleaseWorkflow,
			checksumLayout: checksumLayoutDualArch,
		},
		binary:      archive,
		checksums:   checksums,
		gh:          gh,
		binaryBytes: archiveBytes,
		digest:      digest,
	}
}

func arm64TestArchive(t *testing.T, extras map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entries := map[string][]byte{
		"cb.exe":    []byte("verified ARM64 ContainerBin executable"),
		"LICENSE":   []byte("license"),
		"README.md": []byte("readme"),
	}
	for name, data := range extras {
		entries[name] = data
	}
	names := []string{"cb.exe", "LICENSE", "README.md"}
	var extraNames []string
	for name := range extras {
		if name != "cb.exe" && name != "LICENSE" && name != "README.md" {
			extraNames = append(extraNames, name)
		}
	}
	sort.Strings(extraNames)
	names = append(names, extraNames...)
	for _, name := range names {
		data, ok := entries[name]
		if !ok {
			continue
		}
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func attestationJSON(digest string) []byte {
	return []byte(fmt.Sprintf(`[{"attestation":{"bundle":"ignored"},"verificationResult":{"statement":{"predicateType":"%s","subject":[{"name":"cb.exe","digest":{"sha256":"%s"}}]}}}]`, provenancePredicate, digest))
}

func mustResolveTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

type attestationRunnerFunc func(context.Context, string, []string) ([]byte, []byte, error)

func (f attestationRunnerFunc) Run(ctx context.Context, executable string, args []string) ([]byte, []byte, error) {
	return f(ctx, executable, args)
}

func testVerifier(runner attestationRunner) verifier {
	return verifier{
		runner: runner,
		authenticate: func(context.Context, string) (string, error) {
			return "authenticated-test-gh", nil
		},
	}
}
