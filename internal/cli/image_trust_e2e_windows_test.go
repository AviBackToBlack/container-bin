//go:build windows && image_trust_e2e

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestImageTrustLockAndUpdateWindowsDockerDesktop(t *testing.T) {
	if os.Getenv("CONTAINERBIN_IMAGE_TRUST_E2E") != "1" {
		t.Skip("set CONTAINERBIN_IMAGE_TRUST_E2E=1 to run the real Windows Docker Desktop qualification")
	}

	cosignPath := requiredE2EEnv(t, "CONTAINERBIN_IMAGE_TRUST_E2E_COSIGN")
	image := requiredE2EEnv(t, "CONTAINERBIN_IMAGE_TRUST_E2E_IMAGE")
	issuer := requiredE2EEnv(t, "CONTAINERBIN_IMAGE_TRUST_E2E_ISSUER")
	subject := requiredE2EEnv(t, "CONTAINERBIN_IMAGE_TRUST_E2E_SUBJECT")
	if !filepath.IsAbs(cosignPath) {
		t.Fatalf("CONTAINERBIN_IMAGE_TRUST_E2E_COSIGN must be absolute: %q", cosignPath)
	}
	cosignBytes, err := os.ReadFile(cosignPath)
	if err != nil {
		t.Fatalf("read qualification cosign executable: %v", err)
	}
	cosignSum := sha256.Sum256(cosignBytes)
	cosignSHA256 := hex.EncodeToString(cosignSum[:])

	dockerOS := strings.TrimSpace(runE2ECommand(t, "docker", "info", "--format", "{{.OSType}}"))
	if dockerOS != "linux" {
		t.Fatalf("Docker engine OSType = %q, want linux", dockerOS)
	}
	dockerPlatform := strings.TrimSpace(runE2ECommand(t, "docker", "version", "--format", "{{.Server.Platform.Name}}"))
	if !strings.Contains(strings.ToLower(dockerPlatform), "docker desktop") {
		t.Fatalf("Docker server platform = %q, want Docker Desktop", dockerPlatform)
	}

	repository, err := policy.CanonicalRepository(image)
	if err != nil {
		t.Fatalf("qualification image: %v", err)
	}
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.toml")
	rule := strings.Join([]string{repository, "keyless", issuer, subject, "online"}, "|")
	policyBytes := []byte(fmt.Sprintf(
		"policy_version = 3\ncosign_path = %s\ncosign_sha256 = %q\nimage_trust_rules = [%s]\n",
		strconv.Quote(cosignPath), cosignSHA256, strconv.Quote(rule),
	))
	if err := os.WriteFile(policyPath, policyBytes, 0600); err != nil {
		t.Fatalf("write qualification policy: %v", err)
	}
	machinePolicy, err := policy.LoadQualificationFile(policyPath)
	if err != nil {
		t.Fatalf("load qualification policy: %v", err)
	}

	reg := registry.Registry{
		SchemaVersion: registry.MaxSchemaVersion,
		Tools: map[string]registry.Tool{
			"image-trust-e2e": {
				Name:     "image-trust-e2e",
				Image:    image,
				Provider: "stateless",
			},
		},
		Defaults: map[string]string{},
	}
	cfgPath := filepath.Join(dir, "container-bin.toml")
	if err := Lock(reg, cfgPath, nil, machinePolicy); err != nil {
		t.Fatalf("policy-covered Lock() on Windows Docker Desktop: %v", err)
	}
	assertQualifiedImageTrustLock(t, lockfile.PathFor(cfgPath), image, machinePolicy.Fingerprint)

	if err := Update(reg, cfgPath, []string{"image-trust-e2e"}, machinePolicy); err != nil {
		t.Fatalf("policy-covered Update() on Windows Docker Desktop: %v", err)
	}
	assertQualifiedImageTrustLock(t, lockfile.PathFor(cfgPath), image, machinePolicy.Fingerprint)
}

func requiredE2EEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when CONTAINERBIN_IMAGE_TRUST_E2E=1", name)
	}
	return value
}

func runE2ECommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func assertQualifiedImageTrustLock(t *testing.T, path, image, policyFingerprint string) {
	t.Helper()
	lf, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("load qualified lockfile: %v", err)
	}
	if lf == nil || lf.Version != 2 {
		t.Fatalf("qualified lockfile = %#v, want schema 2", lf)
	}
	entry, ok := lf.Images[image]
	if !ok || entry.Configured != image || entry.Trust == nil {
		t.Fatalf("qualified lock entry = %#v, want configured image with trust evidence", entry)
	}
	if entry.Trust.Digest == "" || !strings.HasSuffix(entry.Resolved, "@"+entry.Trust.Digest) {
		t.Fatalf("evidence digest %q is not bound to resolved image %q", entry.Trust.Digest, entry.Resolved)
	}
	if entry.Trust.PolicyFingerprint != policyFingerprint {
		t.Fatalf("evidence policy fingerprint = %q, want %q", entry.Trust.PolicyFingerprint, policyFingerprint)
	}
	if entry.Trust.VerifierSHA256 == "" || entry.Trust.BundleSHA256 == "" || entry.Trust.VerifiedAt == "" {
		t.Fatalf("qualified evidence is incomplete: %#v", entry.Trust)
	}
}
