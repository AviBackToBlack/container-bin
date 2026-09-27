//go:build windows

package imagetrust

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/policy"
)

const imageTrustLeakSentinel = "CB_IMAGE_TRUST_SHOULD_NOT_LEAK"

// A staged copy of this test binary acts as the external verifier. Checking
// the executable name lets init handle the cosign-shaped argv before Go's test
// flag parser sees it; the normal test process has a different executable name.
func init() {
	if !strings.EqualFold(filepath.Base(os.Args[0]), verifierName) {
		return
	}
	if err := runImageTrustVerifierHelper(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runImageTrustVerifierHelper() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "TEMP", "TMP"} {
		if !strings.EqualFold(filepath.Clean(os.Getenv(name)), filepath.Clean(dir)) {
			return fmt.Errorf("%s is not the private staging directory", name)
		}
	}
	if os.Getenv("SystemRoot") == "" || !strings.EqualFold(os.Getenv("SystemRoot"), os.Getenv("WINDIR")) {
		return errors.New("Windows system directory environment is missing or inconsistent")
	}
	if os.Getenv("COSIGN_YES") != "true" || os.Getenv("NO_COLOR") != "1" {
		return errors.New("non-interactive verifier environment is incomplete")
	}
	if os.Getenv(imageTrustLeakSentinel) != "" {
		return errors.New("ambient parent environment leaked into verifier")
	}
	switch {
	case len(os.Args) == 4 && os.Args[1] == "download" && os.Args[2] == "signature":
		if _, _, err := splitResolvedDigest(os.Args[3]); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
			"testID":    "native-windows-test",
		})
	case len(os.Args) == 8 && os.Args[1] == "verify-blob-attestation":
		bundlePath, ok := strings.CutPrefix(os.Args[2], "--bundle=")
		if !ok || filepath.Dir(bundlePath) != dir {
			return errors.New("bundle is not privately staged")
		}
		if _, err := os.ReadFile(bundlePath); err != nil {
			return fmt.Errorf("read staged bundle: %w", err)
		}
		if digest, ok := strings.CutPrefix(os.Args[3], "--digest="); !ok || len(digest) != 64 {
			return errors.New("missing exact sha256 digest")
		}
		if os.Args[4] != "--digestAlg=sha256" || os.Args[5] != "--type="+cosignPayloadType {
			return errors.New("missing exact digest algorithm or predicate type")
		}
		if subject, ok := strings.CutPrefix(os.Args[6], "--certificate-identity="); !ok || subject == "" {
			return errors.New("missing exact certificate identity")
		}
		if issuer, ok := strings.CutPrefix(os.Args[7], "--certificate-oidc-issuer="); !ok || issuer == "" {
			return errors.New("missing exact certificate issuer")
		}
		return nil
	default:
		return fmt.Errorf("unexpected verifier arguments: %q", os.Args[1:])
	}
}

func TestCommandRunnerExecutesProtectedSnapshotWithMinimalEnvironment(t *testing.T) {
	t.Setenv(imageTrustLeakSentinel, "must-not-leak")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	subject := "https://github.com/acme/tool/.github/workflows/release.yml@refs/tags/v1"
	issuer := "https://token.actions.githubusercontent.com"
	v := verifier{runner: commandRunner{}, now: time.Now, createStage: createPrivateStage}
	result, err := v.verifyAuthenticated(context.Background(), verificationRequest{
		resolved:   "ghcr.io/acme/tool@" + digest,
		repository: "ghcr.io/acme/tool",
		digest:     digest,
		rule: policy.ImageTrustRule{
			Repository: "ghcr.io/acme", Mechanism: policy.ImageTrustKeyless,
			Subject: subject, Issuer: issuer, NetworkMode: policy.ImageTrustOnline,
		},
		verifier:          testSnapshotBytes(contents),
		policyFingerprint: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Digest() != digest || result.Signer() != subject || result.Issuer() != issuer || result.SignatureCount() != 1 || len(result.BundleSHA256s()) != 1 {
		t.Fatalf("native Windows verification result = %+v", result)
	}
}
