package policy

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAtMissingIsUnmanaged(t *testing.T) {
	p, err := loadAt(filepath.Join(t.TempDir(), "missing.toml"), func(string) error { return nil }, time.Now())
	if err != nil || p.Managed() {
		t.Fatalf("loadAt missing = (%+v, %v), want unmanaged nil", p, err)
	}
}

func TestLoadAtStrictAndCanonical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	contents := "policy_version = 1\nrequire_lock = true\nallow_local_images = true\nallowed_repositories = [\"registry-1.docker.io/library\", \"GHCR.IO/Astral-SH\", \"ghcr.io/astral-sh\"]\nexpires_at = \"2030-01-02T03:04:05Z\"\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := loadAt(path, func(got string) error {
		if got != path {
			t.Fatalf("ownership path = %q, want %q", got, path)
		}
		return nil
	}, time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Managed() || !p.RequireLock || !p.AllowLocalImages || p.Fingerprint == "" {
		t.Fatalf("unexpected parsed policy: %+v", p)
	}
	want := []string{"docker.io/library", "ghcr.io/astral-sh"}
	if strings.Join(p.AllowedRepositories, ",") != strings.Join(want, ",") {
		t.Fatalf("allowed repositories = %v, want %v", p.AllowedRepositories, want)
	}
	if !strings.Contains(p.Summary(), "fingerprint=sha256:") || !strings.Contains(p.Summary(), path) {
		t.Fatalf("summary missing provenance: %q", p.Summary())
	}
}

func TestLoadAtAcceptsMultilineAllowedRepositories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	contents := `policy_version = 1
allowed_repositories = [
  "docker.io/library", # official images
  "astral-sh/uv",
  "ghcr.io/acme/developer-tools",
]
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := loadAt(path, func(string) error { return nil }, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker.io/astral-sh/uv", "docker.io/library", "ghcr.io/acme/developer-tools"}
	if strings.Join(p.AllowedRepositories, ",") != strings.Join(want, ",") {
		t.Fatalf("allowed repositories = %v, want %v", p.AllowedRepositories, want)
	}
}

func TestRegistrySignaturePolicyVerifiesExactBytesAndRotation(t *testing.T) {
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	keyA := testSigningKey(1)
	keyB := testSigningKey(2)
	p := parseSigningPolicy(t, now, []string{
		testSigningKeySpec("ops-2028", keyA.Public().(ed25519.PublicKey), "2028-01-01T00:00:00Z", "2030-01-01T00:00:00Z"),
		testSigningKeySpec("ops-2029", keyB.Public().(ed25519.PublicKey), "2028-06-01T00:00:00Z", "2031-01-01T00:00:00Z"),
	}, nil)
	registryBytes := []byte("schema_version = 2\n[tools.demo]\nimage = \"demo:1\"\nprovider = \"stateless\"\n")
	for _, signer := range []struct {
		id  string
		key ed25519.PrivateKey
	}{{"ops-2028", keyA}, {"ops-2029", keyB}} {
		envelope := testSignatureEnvelope(signer.id, ed25519.Sign(signer.key, registryBytes))
		if err := p.authenticateRegistryEnvelope("container-bin.toml", registryBytes, envelope, now); err != nil {
			t.Fatalf("rotation signer %s rejected: %v", signer.id, err)
		}
	}

	mutated := append([]byte(nil), registryBytes...)
	mutated[len(mutated)-2] = '2'
	err := p.authenticateRegistryEnvelope("container-bin.toml", mutated, testSignatureEnvelope("ops-2028", ed25519.Sign(keyA, registryBytes)), now)
	assertPolicyCode(t, err, "registry_signature_invalid")
	if summary := p.Summary(); !strings.Contains(summary, "require_registry_signature=true") || !strings.Contains(summary, "registry_trusted_keys=2") {
		t.Fatalf("summary does not report registry signature policy: %q", summary)
	}
}

func TestRegistrySignaturePolicyRevocationAndValidity(t *testing.T) {
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	revokedKey := testSigningKey(3)
	activeKey := testSigningKey(4)
	p := parseSigningPolicy(t, now, []string{
		testSigningKeySpec("revoked-key", revokedKey.Public().(ed25519.PublicKey), "2028-01-01T00:00:00Z", "2030-01-01T00:00:00Z"),
		testSigningKeySpec("active-key", activeKey.Public().(ed25519.PublicKey), "2028-01-01T00:00:00Z", "2030-01-01T00:00:00Z"),
	}, []string{"revoked-key"})
	registryBytes := []byte("exact registry bytes")
	assertPolicyCode(t, p.authenticateRegistryEnvelope("container-bin.toml", registryBytes, testSignatureEnvelope("revoked-key", ed25519.Sign(revokedKey, registryBytes)), now), "registry_signer_unauthorized")
	assertPolicyCode(t, p.authenticateRegistryEnvelope("container-bin.toml", registryBytes, testSignatureEnvelope("active-key", ed25519.Sign(activeKey, registryBytes)), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)), "registry_signer_inactive")
	assertPolicyCode(t, p.AuthorizeRegistryMutation("cb add"), "registry_signed_readonly")
}

func TestAuthenticateRegistryReadsDetachedRegularFile(t *testing.T) {
	now := time.Now().UTC()
	key := testSigningKey(5)
	p := parseSigningPolicy(t, now, []string{
		testSigningKeySpec("filesystem-key", key.Public().(ed25519.PublicKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)),
	}, nil)
	registryBytes := []byte("signed registry")
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	if err := p.AuthenticateRegistry(path, nil); err == nil {
		t.Fatal("missing signed registry accepted")
	} else {
		assertPolicyCode(t, err, "registry_signature_missing")
	}
	assertPolicyCode(t, p.AuthenticateRegistry(path, registryBytes), "registry_signature_missing")
	if err := os.WriteFile(path+".sig", testSignatureEnvelope("filesystem-key", ed25519.Sign(key, registryBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.AuthenticateRegistry(path, registryBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", bytes.Repeat([]byte{'x'}, maxRegistrySignatureFileSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Policy{}).LoadRegistrySignature(path, registryBytes); err == nil {
		t.Fatal("oversized optional detached signature accepted for backup")
	} else {
		assertPolicyCode(t, err, "registry_signature_invalid")
	}
}

func TestParseRejectsInvalidRegistrySignaturePolicies(t *testing.T) {
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	key := testSigningKey(6).Public().(ed25519.PublicKey)
	valid := testSigningKeySpec("valid-key", key, "2028-01-01T00:00:00Z", "2030-01-01T00:00:00Z")
	cases := []struct {
		name, body, code string
	}{
		{"schema one", fmt.Sprintf("policy_version = 1\nrequire_registry_signature = true\nregistry_signing_keys = [%q]\n", valid), "version"},
		{"missing keys", "policy_version = 2\nrequire_registry_signature = true\n", "syntax"},
		{"invalid key id", fmt.Sprintf("policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [%q]\n", strings.Replace(valid, "valid-key", "INVALID", 1)), "syntax"},
		{"duplicate key id", fmt.Sprintf("policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [%q, %q]\n", valid, valid), "syntax"},
		{"duplicate public key", fmt.Sprintf("policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [%q, %q]\n", valid, strings.Replace(valid, "valid-key", "second-key", 1)), "syntax"},
		{"invalid public key", "policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [\"valid-key|not-base64|2028-01-01T00:00:00Z|2030-01-01T00:00:00Z\"]\n", "syntax"},
		{"noncanonical time", fmt.Sprintf("policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [%q]\n", strings.Replace(valid, "2028-01-01T00:00:00Z", "2028-01-01T01:00:00+01:00", 1)), "syntax"},
		{"no active key", fmt.Sprintf("policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [%q]\n", strings.Replace(valid, "2030-01-01T00:00:00Z", "2028-12-31T00:00:00Z", 1)), "syntax"},
		{"duplicate revoked id", fmt.Sprintf("policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [%q]\nrevoked_registry_key_ids = [\"old-key\", \"old-key\"]\n", valid), "syntax"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse("policy.toml", []byte(tc.body), now)
			assertPolicyCode(t, err, tc.code)
		})
	}
}

func TestImageTrustPolicyCanonicalRulesAndSelection(t *testing.T) {
	verifierPath := filepath.Join(t.TempDir(), "cosign")
	keyPath := filepath.Join(t.TempDir(), "keys", "release.pub")
	verifierHash := strings.Repeat("a", 64)
	keyHash := strings.Repeat("b", 64)
	keylessRule := "GHCR.IO/Acme|keyless|https://token.actions.githubusercontent.com|https://github.com/acme/tools/.github/workflows/release.yml@refs/tags/v1.2.3|online"
	keyRule := strings.Join([]string{"ghcr.io/acme/release", "key", keyPath, keyHash, "offline-bundle"}, "|")
	body := fmt.Sprintf("policy_version = 3\ncosign_path = %q\ncosign_sha256 = %q\nimage_trust_rules = [%q, %q]\n", verifierPath, verifierHash, keylessRule, keyRule)
	p, err := parse("policy.toml", []byte(body), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	verifier, ok := p.CosignVerifier()
	if !ok || verifier.Path != verifierPath || verifier.SHA256 != verifierHash {
		t.Fatalf("CosignVerifier() = (%+v, %t)", verifier, ok)
	}
	rule, ok, err := p.ImageTrustFor("ghcr.io/acme/release/tool:v1")
	if err != nil || !ok || rule.Repository != "ghcr.io/acme/release" || rule.Mechanism != ImageTrustKey || rule.PublicKey.Path != keyPath || rule.PublicKey.SHA256 != keyHash || rule.NetworkMode != ImageTrustOfflineBundle {
		t.Fatalf("nested ImageTrustFor() = (%+v, %t, %v)", rule, ok, err)
	}
	rule, ok, err = p.ImageTrustFor("ghcr.io/acme/other:v1")
	if err != nil || !ok || rule.Repository != "ghcr.io/acme" || rule.Mechanism != ImageTrustKeyless || rule.Issuer != "https://token.actions.githubusercontent.com" || rule.Subject == "" || rule.NetworkMode != ImageTrustOnline {
		t.Fatalf("parent ImageTrustFor() = (%+v, %t, %v)", rule, ok, err)
	}
	if _, ok, err := p.ImageTrustFor("python:3.13"); err != nil || ok {
		t.Fatalf("unconfigured ImageTrustFor() = (_, %t, %v), want digest-only absence", ok, err)
	}
	if _, _, err := p.ImageTrustFor("https://ghcr.io/acme/tool"); err == nil {
		t.Fatal("invalid image reference was treated as an absent trust rule")
	}
	summary := p.Summary()
	if !strings.Contains(summary, "image_trust_rules=2") || !strings.Contains(summary, "cosign_pinned=true") {
		t.Fatalf("summary does not report image trust: %q", summary)
	}
	for _, secret := range []string{verifierPath, verifierHash, keyPath, keyHash, rule.Subject} {
		if strings.Contains(summary, secret) {
			t.Fatalf("summary disclosed image trust material %q: %q", secret, summary)
		}
	}
}

func TestParseRejectsInvalidImageTrustPolicies(t *testing.T) {
	verifierPath := filepath.Join(t.TempDir(), "cosign")
	keyPath := filepath.Join(t.TempDir(), "release.pub")
	validHash := strings.Repeat("a", 64)
	validRule := "ghcr.io/acme|keyless|https://token.actions.githubusercontent.com|https://github.com/acme/tools/.github/workflows/release.yml@refs/tags/v1|online"
	validVerifier := fmt.Sprintf("cosign_path = %q\ncosign_sha256 = %q\n", verifierPath, validHash)
	policy := func(version int, fields string, rules ...string) string {
		quoted := make([]string, len(rules))
		for i, rule := range rules {
			quoted[i] = fmt.Sprintf("%q", rule)
		}
		return fmt.Sprintf("policy_version = %d\nrequire_lock = true\n%simage_trust_rules = [%s]\n", version, fields, strings.Join(quoted, ", "))
	}
	cases := []struct {
		name, body, code string
	}{
		{"schema two", policy(2, validVerifier, validRule), "version"},
		{"missing verifier", policy(3, "", validRule), "syntax"},
		{"verifier without rules", policy(3, validVerifier), "syntax"},
		{"relative verifier", policy(3, "cosign_path = \"cosign\"\ncosign_sha256 = \""+validHash+"\"\n", validRule), "syntax"},
		{"uppercase verifier hash", policy(3, fmt.Sprintf("cosign_path = %q\ncosign_sha256 = %q\n", verifierPath, strings.ToUpper(validHash)), validRule), "syntax"},
		{"short rule", policy(3, validVerifier, "ghcr.io/acme|keyless"), "syntax"},
		{"unsupported mechanism", policy(3, validVerifier, "ghcr.io/acme|notation|issuer|subject|online"), "syntax"},
		{"insecure issuer", policy(3, validVerifier, "ghcr.io/acme|keyless|http://issuer.example|subject|online"), "syntax"},
		{"empty subject", policy(3, validVerifier, "ghcr.io/acme|keyless|https://issuer.example||online"), "syntax"},
		{"relative key", policy(3, validVerifier, "ghcr.io/acme|key|release.pub|"+validHash+"|online"), "syntax"},
		{"bad key hash", policy(3, validVerifier, strings.Join([]string{"ghcr.io/acme", "key", keyPath, "bad", "online"}, "|")), "syntax"},
		{"bad network mode", policy(3, validVerifier, "ghcr.io/acme|keyless|https://issuer.example|subject|best-effort"), "syntax"},
		{"duplicate canonical repository", policy(3, validVerifier, validRule, "GHCR.IO/ACME|keyless|https://issuer.example|subject|online"), "syntax"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse("policy.toml", []byte(tc.body), time.Now())
			assertPolicyCode(t, err, tc.code)
		})
	}
}

func TestImageTrustPolicyFailsClosedUntilEvidenceIsSupported(t *testing.T) {
	verifierPath := filepath.Join(t.TempDir(), "cosign")
	verifierHash := strings.Repeat("a", 64)
	rule := "ghcr.io/acme|keyless|https://token.actions.githubusercontent.com|https://github.com/acme/tools/.github/workflows/release.yml@refs/tags/v1|online"
	body := fmt.Sprintf("policy_version = 3\ncosign_path = %q\ncosign_sha256 = %q\nimage_trust_rules = [%q]\n", verifierPath, verifierHash, rule)
	p, err := parse("policy.toml", []byte(body), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertPolicyCode(t, p.AuthorizeImage("ghcr.io/acme/tool:v1", true, false), "image_trust_unverified")
	assertPolicyCode(t, p.AuthorizeLockTarget("ghcr.io/acme/tool:v1", false), "image_trust_unverified")
	p.AllowLocalImages = true
	assertPolicyCode(t, p.AuthorizeImage("ghcr.io/acme/tool:v1", true, true), "image_trust_unverified")
	if err := p.AuthorizeImage("docker.io/library/python:3.13", true, false); err != nil {
		t.Fatalf("unconfigured digest-only repository rejected: %v", err)
	}
	p.AllowedRepositories = []string{"docker.io/library"}
	assertPolicyCode(t, p.AuthorizeImage("ghcr.io/acme/tool:v1", true, false), "repository_denied")
}

func TestImageTrustDoesNotChangeEarlierSchemaReferenceAuthorization(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, version := range []int{1, 2, 3} {
		p := Policy{SchemaVersion: version}
		if err := p.AuthorizeImage(imageID, true, false); err != nil {
			t.Errorf("schema %d image-ID authorization changed without image trust rules: %v", version, err)
		}
	}
}

func TestParseRegistrySignatureEnvelopeIsStrict(t *testing.T) {
	signature := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, ed25519.SignatureSize))
	cases := []string{
		fmt.Sprintf("signature_version = 2\nalgorithm = \"ed25519\"\nkey_id = \"key\"\nsignature = %q\n", signature),
		fmt.Sprintf("signature_version = 1\nalgorithm = \"rsa\"\nkey_id = \"key\"\nsignature = %q\n", signature),
		fmt.Sprintf("signature_version = 1\nalgorithm = \"ed25519\"\nkey_id = \"INVALID\"\nsignature = %q\n", signature),
		"signature_version = 1\nalgorithm = \"ed25519\"\nkey_id = \"key\"\nsignature = \"bad\"\n",
		fmt.Sprintf("signature_version = 1\nalgorithm = \"ed25519\"\nkey_id = \"key\"\nsignature = %q\nextra = true\n", signature),
	}
	for i, raw := range cases {
		if _, err := parseRegistrySignatureEnvelope([]byte(raw)); err == nil {
			t.Errorf("case %d accepted malformed envelope", i)
		}
	}
}

func TestLoadAtOwnershipFailureIsCoded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte("policy_version = 1\nrequire_lock = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadAt(path, func(string) error { return errors.New("writable") }, time.Now())
	assertPolicyCode(t, err, "ownership")
}

func TestLoadAtRejectsNonRegularPolicyBeforeOwnershipOrRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ownershipCalled := false
	_, err := loadAt(path, func(string) error {
		ownershipCalled = true
		return nil
	}, time.Now())
	assertPolicyCode(t, err, "ownership")
	if ownershipCalled {
		t.Fatal("ownership verifier called for non-regular policy object")
	}
}

func TestParseRejectsInvalidPolicies(t *testing.T) {
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, contents, code string
	}{
		{"missing version", "require_lock = true\n", "version"},
		{"unknown version", "policy_version = 4\nrequire_lock = true\n", "version"},
		{"duplicate", "policy_version = 1\nrequire_lock = true\nrequire_lock = false\n", "syntax"},
		{"unknown key", "policy_version = 1\nrequire_lock = true\nsurprise = true\n", "syntax"},
		{"section", "policy_version = 1\nrequire_lock = true\n[extra]\n", "syntax"},
		{"no controls", "policy_version = 1\nallow_local_images = true\n", "syntax"},
		{"expired", "policy_version = 1\nrequire_lock = true\nexpires_at = \"2028-01-01T00:00:00Z\"\n", "expired"},
		{"bad rule", "policy_version = 1\nallowed_repositories = [\"ghcr.io//team\"]\n", "syntax"},
		{"unterminated array", "policy_version = 1\nallowed_repositories = [\n  \"ghcr.io/team\",\n", "syntax"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse("policy.toml", []byte(tc.contents), now)
			assertPolicyCode(t, err, tc.code)
		})
	}
}

func TestCanonicalRepository(t *testing.T) {
	cases := map[string]string{
		"python:3.13":                                       "docker.io/library/python",
		"docker.io/python@sha256:aaaa":                      "docker.io/library/python",
		"index.docker.io/library/python:latest":             "docker.io/library/python",
		"registry-1.docker.io/team/tool@sha256:bbbb":        "docker.io/team/tool",
		"astral-sh/uv:0.8":                                  "docker.io/astral-sh/uv",
		"GHCR.IO/AviBackToBlack/Container-Bin:v1":           "ghcr.io/avibacktoblack/container-bin",
		"localhost:5000/team/tool:dev":                      "localhost:5000/team/tool",
		"example.com:5443/nested/team/tool@sha256:deadbeef": "example.com:5443/nested/team/tool",
	}
	for input, want := range cases {
		got, err := CanonicalRepository(input)
		if err != nil || got != want {
			t.Errorf("CanonicalRepository(%q) = (%q, %v), want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "https://ghcr.io/team/tool", `ghcr.io\team\tool`, "ghcr.io//team", "../tool", "ghcr.io/team/../tool", "sha256:" + strings.Repeat("a", 64), "python@", "ghcr.io/team/tool:bad:tag", "localhost:abc/team/tool"} {
		if got, err := CanonicalRepository(bad); err == nil {
			t.Errorf("CanonicalRepository(%q) = %q, want error", bad, got)
		}
	}
}

func TestAuthorizeImage(t *testing.T) {
	p := Policy{SchemaVersion: 1, RequireLock: true, AllowedRepositories: []string{"docker.io/library", "ghcr.io/team"}}
	if err := p.AuthorizeImage("python:3.13", true, false); err != nil {
		t.Fatal(err)
	}
	if err := p.AuthorizeImage("ghcr.io/team/sub/tool:1", true, false); err != nil {
		t.Fatal(err)
	}
	assertPolicyCode(t, p.AuthorizeImage("python:3.13", false, false), "lock_required")
	assertPolicyCode(t, p.AuthorizeImage("ghcr.io/teamster/tool:1", true, false), "repository_denied")
	assertPolicyCode(t, p.AuthorizeImage("sha256:"+strings.Repeat("a", 64), true, true), "local_image_denied")

	p.AllowLocalImages = true
	if err := p.AuthorizeImage("local-tool:dev", true, true); err != nil {
		t.Fatalf("explicit local exception rejected: %v", err)
	}
	if err := (Policy{}).AuthorizeImage("anything", false, true); err != nil {
		t.Fatalf("unmanaged policy changed behavior: %v", err)
	}
	p = Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/team"}}
	if err := p.AuthorizeResolvedImage("ghcr.io/team/tool:1", "evil.example/tool@sha256:"+strings.Repeat("a", 64), false); err == nil {
		t.Fatal("resolved lock repository bypassed allowlist")
	}
}

func assertPolicyCode(t *testing.T, err error, want string) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != want {
		t.Fatalf("error = %v, want policy code %q", err, want)
	}
}

func testSigningKey(seedByte byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seedByte}, ed25519.SeedSize))
}

func testSigningKeySpec(id string, publicKey ed25519.PublicKey, notBefore, expiresAt string) string {
	return strings.Join([]string{id, base64.StdEncoding.EncodeToString(publicKey), notBefore, expiresAt}, "|")
}

func parseSigningPolicy(t *testing.T, now time.Time, keys, revoked []string) Policy {
	t.Helper()
	quotedKeys := make([]string, len(keys))
	for i, key := range keys {
		quotedKeys[i] = fmt.Sprintf("%q", key)
	}
	quotedRevoked := make([]string, len(revoked))
	for i, id := range revoked {
		quotedRevoked[i] = fmt.Sprintf("%q", id)
	}
	body := "policy_version = 2\nrequire_registry_signature = true\nregistry_signing_keys = [" + strings.Join(quotedKeys, ", ") + "]\n"
	if len(revoked) > 0 {
		body += "revoked_registry_key_ids = [" + strings.Join(quotedRevoked, ", ") + "]\n"
	}
	p, err := parse("policy.toml", []byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testSignatureEnvelope(keyID string, signature []byte) []byte {
	return []byte(fmt.Sprintf("signature_version = 1\nalgorithm = \"ed25519\"\nkey_id = %q\nsignature = %q\n", keyID, base64.StdEncoding.EncodeToString(signature)))
}
