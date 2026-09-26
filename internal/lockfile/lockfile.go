// Package lockfile owns container-bin.lock: the immutable image lockfile that
// pins every configured image reference to either a repository@sha256 digest
// or a locally built image's sha256 ID, plus the resolution and optional
// structured repository-trust evidence that produced it.
package lockfile

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/atomicio"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/toml"
)

const (
	maxLockVersion            = 2
	imageTrustEvidenceVersion = 1
	imageTrustVerifierCosign  = "cosign"
)

// ImageTrustEvidence records the exact authenticated inputs and result of one
// repository-digest verification. It is deliberately structured rather than a
// boolean so runtime policy can prove that evidence still matches the digest,
// verifier and complete effective policy which authorized it.
type ImageTrustEvidence struct {
	Version           int
	Mechanism         policy.ImageTrustMechanism
	Repository        string
	Digest            string
	Signer            string
	Issuer            string
	BundleSHA256      string
	VerifiedAt        string
	Verifier          string
	VerifierSHA256    string
	PolicyFingerprint string
}

type LockEntry struct {
	Configured string
	Resolved   string
	Digest     string
	Trust      *ImageTrustEvidence
}

type LockFile struct {
	Version int
	Images  map[string]LockEntry // keyed by configured image reference
}

func PathFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "container-bin.lock")
}

func LoadForRegistry() (*LockFile, string, error) {
	cfgPath, err := registry.Path()
	if err != nil {
		return nil, "", err
	}
	path := PathFor(cfgPath)
	lf, err := Load(path)
	return lf, path, err
}

func Load(path string) (*LockFile, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		rec, err := atomicio.RecoverFromBackup(path, func(bak string) error {
			_, err := Load(bak)
			return err
		})
		if err != nil {
			return nil, err
		}
		if !rec {
			return nil, nil
		}
		b, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return parseLockFile(b, maxLockVersion)
}

func parseLockFile(b []byte, maxVersion int) (*LockFile, error) {
	lf := &LockFile{Version: 0, Images: map[string]LockEntry{}}
	var cur *LockEntry
	var curKey string
	var curSeen map[string]bool
	seenVersion := false
	seenSections := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	lineNo := 0
	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.Configured == "" || cur.Resolved == "" || cur.Digest == "" {
			return fmt.Errorf("lock entry %q is incomplete", curKey)
		}
		if curKey != entryID(cur.Configured) {
			return fmt.Errorf("lock entry id %q does not match configured image %q", curKey, cur.Configured)
		}
		if _, duplicate := lf.Images[cur.Configured]; duplicate {
			return fmt.Errorf("duplicate lock entry for configured image %q", cur.Configured)
		}
		if strings.HasPrefix(cur.Resolved, "sha256:") {
			if !validImageID(cur.Resolved) {
				return fmt.Errorf("lock entry %q has invalid local image ID %q", curKey, cur.Resolved)
			}
			if cur.Digest != cur.Resolved {
				return fmt.Errorf("lock entry %q local image digest must match resolved ID", curKey)
			}
			if cur.Trust != nil {
				return fmt.Errorf("lock entry %q local image ID cannot carry repository trust evidence", curKey)
			}
		} else {
			resolvedRepository, resolvedDigest, ok := splitImmutableRepositoryDigest(cur.Resolved)
			if !ok {
				return fmt.Errorf("lock entry %q has invalid immutable repository digest %q", curKey, cur.Resolved)
			}
			if cur.Digest != resolvedDigest {
				return fmt.Errorf("lock entry %q digest does not match resolved repository digest", curKey)
			}
			if matched, ok := matchRepoDigest(cur.Configured, []string{cur.Resolved}); !ok || matched != cur.Resolved {
				return fmt.Errorf("lock entry %q resolved repository does not match configured image %q", curKey, cur.Configured)
			}
			if cur.Trust != nil {
				if lf.Version < 2 {
					return fmt.Errorf("lock entry %q trust evidence requires lock_version 2", curKey)
				}
				if err := validateImageTrustEvidence(curKey, cur.Configured, resolvedRepository, cur.Digest, *cur.Trust); err != nil {
					return err
				}
			}
		}
		lf.Images[cur.Configured] = *cur
		return nil
	}
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(toml.StripComment(sc.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if err := flush(); err != nil {
				return nil, err
			}
			if !seenVersion {
				return nil, fmt.Errorf("line %d: lock_version must precede image sections", lineNo)
			}
			sec := strings.TrimSpace(line[1 : len(line)-1])
			if !strings.HasPrefix(sec, "images.") {
				return nil, fmt.Errorf("line %d: unsupported lock section %q", lineNo, sec)
			}
			curKey = strings.TrimPrefix(sec, "images.")
			if curKey == "" {
				return nil, fmt.Errorf("line %d: empty image lock id", lineNo)
			}
			if seenSections[curKey] {
				return nil, fmt.Errorf("line %d: duplicate image lock section %q", lineNo, curKey)
			}
			seenSections[curKey] = true
			cur = &LockEntry{}
			curSeen = map[string]bool{}
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if cur == nil {
			if key != "lock_version" {
				return nil, fmt.Errorf("line %d: unsupported top-level key %q", lineNo, key)
			}
			if seenVersion {
				return nil, fmt.Errorf("line %d: duplicate top-level key %q", lineNo, key)
			}
			v, err := strconv.Atoi(val)
			if err != nil || v < 1 || v > maxVersion {
				return nil, fmt.Errorf("line %d: unsupported lock_version %q (supported: 1-%d)", lineNo, val, maxVersion)
			}
			lf.Version = v
			seenVersion = true
			continue
		}
		if curSeen[key] {
			return nil, fmt.Errorf("line %d: duplicate lock key %q", lineNo, key)
		}
		curSeen[key] = true
		if key == "evidence_version" {
			v, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("line %d evidence_version: expected integer", lineNo)
			}
			if cur.Trust == nil {
				cur.Trust = &ImageTrustEvidence{}
			}
			cur.Trust.Version = v
			continue
		}
		q, err := toml.ParseQuoted(val)
		if err != nil {
			return nil, fmt.Errorf("line %d %s: %w", lineNo, key, err)
		}
		switch key {
		case "configured":
			cur.Configured = q
		case "resolved":
			cur.Resolved = q
		case "digest":
			cur.Digest = q
		case "evidence_mechanism":
			ensureTrustEvidence(cur).Mechanism = policy.ImageTrustMechanism(q)
		case "evidence_repository":
			ensureTrustEvidence(cur).Repository = q
		case "evidence_digest":
			ensureTrustEvidence(cur).Digest = q
		case "evidence_signer":
			ensureTrustEvidence(cur).Signer = q
		case "evidence_issuer":
			ensureTrustEvidence(cur).Issuer = q
		case "evidence_bundle_sha256":
			ensureTrustEvidence(cur).BundleSHA256 = q
		case "evidence_verified_at":
			ensureTrustEvidence(cur).VerifiedAt = q
		case "evidence_verifier":
			ensureTrustEvidence(cur).Verifier = q
		case "evidence_verifier_sha256":
			ensureTrustEvidence(cur).VerifierSHA256 = q
		case "evidence_policy_fingerprint":
			ensureTrustEvidence(cur).PolicyFingerprint = q
		default:
			return nil, fmt.Errorf("line %d: unsupported lock key %q", lineNo, key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if !seenVersion {
		return nil, errors.New("lock_version is required")
	}
	return lf, nil
}

func ensureTrustEvidence(entry *LockEntry) *ImageTrustEvidence {
	if entry.Trust == nil {
		entry.Trust = &ImageTrustEvidence{}
	}
	return entry.Trust
}

func validateImageTrustEvidence(entryKey, configured, resolvedRepository, digest string, evidence ImageTrustEvidence) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("lock entry %q trust evidence: %s", entryKey, fmt.Sprintf(format, args...))
	}
	if evidence.Version != imageTrustEvidenceVersion {
		return fail("unsupported evidence_version %d (supported: %d)", evidence.Version, imageTrustEvidenceVersion)
	}
	configuredRepository, err := policy.CanonicalRepository(configured)
	if err != nil {
		return fail("configured repository is invalid: %v", err)
	}
	canonicalResolved, err := policy.CanonicalRepository(resolvedRepository)
	if err != nil {
		return fail("resolved repository is invalid: %v", err)
	}
	if evidence.Repository != configuredRepository || evidence.Repository != canonicalResolved {
		return fail("repository %q does not match canonical configured and resolved repository %q", evidence.Repository, configuredRepository)
	}
	if evidence.Digest != digest || !validImageID(evidence.Digest) {
		return fail("digest %q does not match the locked digest", evidence.Digest)
	}
	if err := validateEvidenceText(evidence.Signer); err != nil {
		return fail("signer %v", err)
	}
	switch evidence.Mechanism {
	case policy.ImageTrustKeyless:
		if err := validateEvidenceIssuer(evidence.Issuer); err != nil {
			return fail("issuer %v", err)
		}
	case policy.ImageTrustKey:
		if evidence.Issuer != "" {
			return fail("issuer must be empty for key verification")
		}
		if !validSHA256Hex(evidence.Signer) {
			return fail("key signer must be a 64-character lowercase SHA-256")
		}
	default:
		return fail("unsupported mechanism %q", evidence.Mechanism)
	}
	if !validSHA256Hex(evidence.BundleSHA256) {
		return fail("bundle SHA-256 must be 64 lowercase hexadecimal characters")
	}
	verifiedAt, err := time.Parse(time.RFC3339, evidence.VerifiedAt)
	if err != nil || verifiedAt.Location() != time.UTC || verifiedAt.Format(time.RFC3339) != evidence.VerifiedAt {
		return fail("verified_at must be canonical UTC RFC3339 seconds")
	}
	if evidence.Verifier != imageTrustVerifierCosign {
		return fail("unsupported verifier %q", evidence.Verifier)
	}
	if !validSHA256Hex(evidence.VerifierSHA256) {
		return fail("verifier SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if !validSHA256Hex(evidence.PolicyFingerprint) {
		return fail("policy fingerprint must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func validateEvidenceText(value string) error {
	if value == "" || !utf8.ValidString(value) || len(value) > 1024 || containsControl(value) {
		return errors.New("must be 1-1024 bytes without control characters")
	}
	return nil
}

func validateEvidenceIssuer(value string) error {
	if err := validateEvidenceText(value); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be an HTTPS URL without userinfo, query or fragment")
	}
	if parsed.Host != strings.ToLower(parsed.Host) {
		return errors.New("host must be lowercase")
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func entryID(configured string) string {
	h := sha256.Sum256([]byte(configured))
	return hex.EncodeToString(h[:6])
}

func render(lf *LockFile) []byte {
	var b strings.Builder
	b.WriteString("# container-bin immutable image lockfile\n")
	b.WriteString("# Generated by cb lock / cb update. Do not edit by hand.\n")
	b.WriteString("lock_version = " + strconv.Itoa(lf.Version) + "\n")
	keys := make([]string, 0, len(lf.Images))
	for k := range lf.Images {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, configured := range keys {
		e := lf.Images[configured]
		b.WriteString("\n[images." + entryID(configured) + "]\n")
		b.WriteString("configured = " + toml.Quote(e.Configured) + "\n")
		b.WriteString("resolved = " + toml.Quote(e.Resolved) + "\n")
		b.WriteString("digest = " + toml.Quote(e.Digest) + "\n")
		if e.Trust != nil {
			b.WriteString("evidence_version = " + strconv.Itoa(e.Trust.Version) + "\n")
			b.WriteString("evidence_mechanism = " + toml.Quote(string(e.Trust.Mechanism)) + "\n")
			b.WriteString("evidence_repository = " + toml.Quote(e.Trust.Repository) + "\n")
			b.WriteString("evidence_digest = " + toml.Quote(e.Trust.Digest) + "\n")
			b.WriteString("evidence_signer = " + toml.Quote(e.Trust.Signer) + "\n")
			if e.Trust.Issuer != "" {
				b.WriteString("evidence_issuer = " + toml.Quote(e.Trust.Issuer) + "\n")
			}
			b.WriteString("evidence_bundle_sha256 = " + toml.Quote(e.Trust.BundleSHA256) + "\n")
			b.WriteString("evidence_verified_at = " + toml.Quote(e.Trust.VerifiedAt) + "\n")
			b.WriteString("evidence_verifier = " + toml.Quote(e.Trust.Verifier) + "\n")
			b.WriteString("evidence_verifier_sha256 = " + toml.Quote(e.Trust.VerifierSHA256) + "\n")
			b.WriteString("evidence_policy_fingerprint = " + toml.Quote(e.Trust.PolicyFingerprint) + "\n")
		}
	}
	return []byte(b.String())
}

func ConfiguredImages(reg registry.Registry) []string {
	set := map[string]bool{}
	for _, t := range reg.Tools {
		set[t.Image] = true
	}
	out := make([]string, 0, len(set))
	for image := range set {
		out = append(out, image)
	}
	sort.Strings(out)
	return out
}

func imageRepository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	lastSlash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > lastSlash {
		ref = ref[:colon]
	}
	return ref
}

// canonicalRepository folds away the Docker Hub aliases that the engine
// normalizes out of RepoDigests, so that a registry entry written as
// docker.io/library/python matches the python@sha256:... digest Docker
// reports. Non-Hub registries (ghcr.io/..., private hosts) pass through.
func canonicalRepository(repo string) string {
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		if strings.HasPrefix(repo, p) {
			repo = strings.TrimPrefix(repo, p)
			break
		}
	}
	if strings.HasPrefix(repo, "library/") && strings.Count(repo, "/") == 1 {
		repo = strings.TrimPrefix(repo, "library/")
	}
	return repo
}

// matchRepoDigest selects the RepoDigest whose repository is the same image
// repository as the configured reference, modulo Docker Hub normalization.
// No match is an error condition handled by the caller (fail closed).
func matchRepoDigest(configured string, repoDigests []string) (string, bool) {
	want := canonicalRepository(imageRepository(configured))
	for _, rd := range repoDigests {
		repo, _, ok := splitImmutableRepositoryDigest(rd)
		if !ok {
			continue
		}
		if canonicalRepository(repo) == want {
			return rd, true
		}
	}
	return "", false
}

func splitImmutableRepositoryDigest(ref string) (string, string, bool) {
	if strings.Count(ref, "@") != 1 {
		return "", "", false
	}
	repo, digest, _ := strings.Cut(ref, "@")
	if repo == "" || !validImageID(digest) {
		return "", "", false
	}
	lastSlash := strings.LastIndexByte(repo, '/')
	if strings.LastIndexByte(repo, ':') > lastSlash {
		return "", "", false
	}
	if _, err := policy.CanonicalRepository(repo); err != nil {
		return "", "", false
	}
	return repo, digest, true
}

type imageInspection struct {
	id          string
	repoDigests []string
}

func validImageID(id string) bool {
	if len(id) != len("sha256:")+64 || !strings.HasPrefix(id, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "sha256:"))
	return err == nil
}

// IsLocalResolved reports whether a lock entry resolves directly to a Docker
// image ID rather than a repository digest.
func IsLocalResolved(resolved string) bool {
	return validImageID(resolved)
}

func inspectImage(configured string) (imageInspection, error) {
	cmd := exec.Command("docker", "image", "inspect", "--format", `{{.Id}}{{println}}{{range .RepoDigests}}{{println .}}{{end}}`, configured)
	out, err := cmd.Output()
	if err != nil {
		return imageInspection{}, fmt.Errorf("inspect %s: %w", configured, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || !validImageID(fields[0]) {
		return imageInspection{}, fmt.Errorf("inspect %s returned an invalid image ID", configured)
	}
	return imageInspection{id: fields[0], repoDigests: fields[1:]}, nil
}

func repositoryLockEntry(configured string, inspected imageInspection) (LockEntry, error) {
	if len(inspected.repoDigests) == 0 {
		return LockEntry{}, fmt.Errorf("image %s has no RepoDigests; cannot lock it as a registry image", configured)
	}
	resolved, ok := matchRepoDigest(configured, inspected.repoDigests)
	if !ok {
		// Fail closed: silently locking a digest from a different repository
		// (e.g. a locally re-tagged image) would record an identity the
		// configured reference never had.
		return LockEntry{}, fmt.Errorf("image %s has no RepoDigest for repository %q (locally tagged image?); pull it from its registry before locking", configured, imageRepository(configured))
	}
	_, digest, ok := splitImmutableRepositoryDigest(resolved)
	if !ok {
		return LockEntry{}, fmt.Errorf("image %s returned malformed RepoDigest %q", configured, resolved)
	}
	return LockEntry{Configured: configured, Resolved: resolved, Digest: digest}, nil
}

func localLockEntry(configured string, inspected imageInspection) (LockEntry, error) {
	if !validImageID(inspected.id) {
		return LockEntry{}, fmt.Errorf("image %s has invalid local image ID %q", configured, inspected.id)
	}
	return LockEntry{Configured: configured, Resolved: inspected.id, Digest: inspected.id}, nil
}

// ResolveRepositoryImage refreshes a registry-backed lock entry. Repository
// and local identity are deliberately selected by the CLI, never inferred
// from Docker metadata: current engines can report RepoDigests for both.
func ResolveRepositoryImage(configured string, machinePolicy policy.Policy) (LockEntry, error) {
	if err := machinePolicy.AuthorizeLockTarget(configured, false); err != nil {
		return LockEntry{}, err
	}
	cmd := exec.Command("docker", "pull", configured)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return LockEntry{}, fmt.Errorf("docker pull %s: %w", configured, err)
	}
	inspected, err := inspectImage(configured)
	if err != nil {
		return LockEntry{}, err
	}
	return repositoryLockEntry(configured, inspected)
}

// ResolveLocalImage refreshes a local-image lock by inspecting the configured
// tag only. It never pulls or silently switches an existing local lock to a
// repository identity.
func ResolveLocalImage(configured string, machinePolicy policy.Policy) (LockEntry, error) {
	if err := machinePolicy.AuthorizeLockTarget(configured, true); err != nil {
		return LockEntry{}, err
	}
	inspected, err := inspectImage(configured)
	if err != nil {
		return LockEntry{}, fmt.Errorf("local image %s is not available (build or load it before locking): %w", configured, err)
	}
	return localLockEntry(configured, inspected)
}

func RuntimeImageForTool(t registry.Tool, machinePolicy policy.Policy) (string, error) {
	lf, path, err := LoadForRegistry()
	if err != nil {
		return "", fmt.Errorf("lockfile: %w", err)
	}
	return runtimeImageForTool(t, machinePolicy, lf, path)
}

func runtimeImageForTool(t registry.Tool, machinePolicy policy.Policy, lf *LockFile, path string) (string, error) {
	if lf == nil {
		if err := machinePolicy.AuthorizeImage(t.Image, false, false); err != nil {
			return "", err
		}
		return t.Image, nil
	}
	e, ok := lf.Images[t.Image]
	if !ok || e.Configured != t.Image {
		if err := machinePolicy.AuthorizeImage(t.Image, false, false); err != nil {
			return "", err
		}
		return "", fmt.Errorf("image %q is not locked in %s; run `cb update %s` or `cb lock`", t.Image, path, t.Name)
	}
	if err := machinePolicy.AuthorizeResolvedImage(t.Image, e.Resolved, IsLocalResolved(e.Resolved)); err != nil {
		return "", err
	}
	return e.Resolved, nil
}

func Write(path string, lf *LockFile) error {
	if lf.Version == 0 {
		lf.Version = 1
	}
	if lf.Version < 1 || lf.Version > maxLockVersion {
		return fmt.Errorf("unsupported lock_version %d (supported: 1-%d)", lf.Version, maxLockVersion)
	}
	data := render(lf)
	// Parse our own output before committing it.
	tmp := path + ".validate.tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	_, err := Load(tmp)
	_ = os.Remove(tmp)
	if err != nil {
		return fmt.Errorf("generated lockfile failed validation: %w", err)
	}
	return atomicio.WriteFile(path, data, 0644)
}
