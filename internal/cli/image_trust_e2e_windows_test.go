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
	trustedRootPath := requiredE2EEnv(t, "CONTAINERBIN_IMAGE_TRUST_E2E_TRUSTED_ROOT")
	if !filepath.IsAbs(cosignPath) {
		t.Fatalf("CONTAINERBIN_IMAGE_TRUST_E2E_COSIGN must be absolute: %q", cosignPath)
	}
	if !filepath.IsAbs(trustedRootPath) {
		t.Fatalf("CONTAINERBIN_IMAGE_TRUST_E2E_TRUSTED_ROOT must be absolute: %q", trustedRootPath)
	}
	cosignBytes, err := os.ReadFile(cosignPath)
	if err != nil {
		t.Fatalf("read qualification cosign executable: %v", err)
	}
	cosignSum := sha256.Sum256(cosignBytes)
	cosignSHA256 := hex.EncodeToString(cosignSum[:])
	trustedRootBytes, err := os.ReadFile(trustedRootPath)
	if err != nil {
		t.Fatalf("read qualification Sigstore TrustedRoot: %v", err)
	}
	trustedRootSum := sha256.Sum256(trustedRootBytes)
	trustedRootSHA256 := hex.EncodeToString(trustedRootSum[:])

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
	dir := filepath.Join(t.TempDir(), "online")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
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

	offlineDir := filepath.Join(filepath.Dir(dir), "offline")
	if err := os.Mkdir(offlineDir, 0700); err != nil {
		t.Fatal(err)
	}
	offlinePolicyPath := filepath.Join(offlineDir, "policy.toml")
	offlineRule := strings.Join([]string{repository, "keyless", issuer, subject, "offline-bundle"}, "|")
	offlinePolicyBytes := []byte(fmt.Sprintf(
		"policy_version = 4\ncosign_path = %s\ncosign_sha256 = %q\ncosign_trusted_root_path = %s\ncosign_trusted_root_sha256 = %q\nimage_trust_rules = [%s]\n",
		strconv.Quote(cosignPath), cosignSHA256, strconv.Quote(trustedRootPath), trustedRootSHA256, strconv.Quote(offlineRule),
	))
	if err := os.WriteFile(offlinePolicyPath, offlinePolicyBytes, 0600); err != nil {
		t.Fatalf("write offline qualification policy: %v", err)
	}
	offlinePolicy, err := policy.LoadQualificationFile(offlinePolicyPath)
	if err != nil {
		t.Fatalf("load offline qualification policy: %v", err)
	}
	offlineConfigPath := filepath.Join(offlineDir, "container-bin.toml")
	if err := Lock(reg, offlineConfigPath, nil, offlinePolicy); err != nil {
		t.Fatalf("offline policy-covered Lock() on Windows Docker Desktop: %v", err)
	}
	assertQualifiedImageTrustLock(t, lockfile.PathFor(offlineConfigPath), image, offlinePolicy.Fingerprint)
	if err := Update(reg, offlineConfigPath, []string{"image-trust-e2e"}, offlinePolicy); err != nil {
		t.Fatalf("offline policy-covered Update() on Windows Docker Desktop: %v", err)
	}
	assertQualifiedImageTrustLock(t, lockfile.PathFor(offlineConfigPath), image, offlinePolicy.Fingerprint)
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
