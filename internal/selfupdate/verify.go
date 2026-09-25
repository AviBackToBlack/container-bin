package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	attestationTimeout      = 2 * time.Minute
	maxVerifierOutput       = 1 << 20
	provenancePredicate     = "https://slsa.dev/provenance/v1"
	expectedReleaseRepo     = "AviBackToBlack/container-bin"
	expectedReleaseWorkflow = ".github/workflows/release.yml"
)

// Verified is an opaque result binding a successful checksum and provenance
// verification to the exact staged executable digest. A later replacement
// phase must re-hash the file and match this result before changing installed
// bytes.
type Verified struct {
	binaryPath string
	target     string
	digest     string
	size       int64
}

func (v Verified) BinaryPath() string { return v.binaryPath }
func (v Verified) Target() string     { return v.target }
func (v Verified) SHA256() string     { return v.digest }
func (v Verified) Size() int64        { return v.size }

type attestationRunner interface {
	Run(context.Context, string, []string) ([]byte, []byte, error)
}

type verifier struct {
	runner       attestationRunner
	authenticate func(context.Context, string) (string, error)
}

// Verify checks the release checksum and GitHub build-provenance attestation
// for a staged cb.exe. ghExecutable must name an explicitly selected absolute,
// regular executable with a valid GitHub, Inc. Authenticode signature. Verify
// never searches PATH, passes inherited verifier configuration, or falls back
// to checksum-only acceptance.
func Verify(ctx context.Context, plan Plan, binaryPath, checksumsPath, ghExecutable string) (Verified, error) {
	return (verifier{
		runner:       commandAttestationRunner{},
		authenticate: authenticateGitHubCLI,
	}).Verify(ctx, plan, binaryPath, checksumsPath, ghExecutable)
}

func (v verifier) Verify(ctx context.Context, plan Plan, binaryPath, checksumsPath, ghExecutable string) (Verified, error) {
	if v.runner == nil {
		return Verified{}, errors.New("self-update verifier has no attestation runner")
	}
	if v.authenticate == nil {
		return Verified{}, errors.New("self-update verifier has no GitHub CLI authenticator")
	}
	target, err := validateVerificationPlan(plan)
	if err != nil {
		return Verified{}, err
	}
	binaryPath, binaryInfo, err := canonicalVerificationFile(binaryPath, "staged executable")
	if err != nil {
		return Verified{}, err
	}
	checksumsPath, checksumsInfo, err := canonicalVerificationFile(checksumsPath, "checksum manifest")
	if err != nil {
		return Verified{}, err
	}
	if filepath.Base(binaryPath) != plan.Binary.Name || filepath.Base(checksumsPath) != plan.Checksums.Name || filepath.Dir(binaryPath) != filepath.Dir(checksumsPath) {
		return Verified{}, errors.New("self-update verification inputs do not have the exact staged layout")
	}
	if binaryInfo.Size() != plan.Binary.Size {
		return Verified{}, fmt.Errorf("staged executable size is %d, expected %d", binaryInfo.Size(), plan.Binary.Size)
	}
	if checksumsInfo.Size() != plan.Checksums.Size {
		return Verified{}, fmt.Errorf("checksum manifest size is %d, expected %d", checksumsInfo.Size(), plan.Checksums.Size)
	}
	ghExecutable, _, err = canonicalVerificationFile(ghExecutable, "GitHub CLI executable")
	if err != nil {
		return Verified{}, err
	}
	ghDigest, err := v.authenticate(ctx, ghExecutable)
	if err != nil {
		return Verified{}, fmt.Errorf("authenticate GitHub CLI executable: %w", err)
	}

	digest, size, err := hashVerificationFile(binaryPath, binaryInfo)
	if err != nil {
		return Verified{}, err
	}
	manifest, err := readExactVerificationFile(checksumsPath, checksumsInfo)
	if err != nil {
		return Verified{}, err
	}
	if err := verifyChecksumManifest(manifest, plan, digest); err != nil {
		return Verified{}, err
	}

	args := []string{
		"attestation", "verify", binaryPath,
		"--hostname", "github.com",
		"--repo", plan.ExpectedRepo,
		"--cert-identity", "https://github.com/" + plan.ExpectedRepo + "/" + plan.Workflow + "@" + plan.ExpectedRef,
		"--source-ref", plan.ExpectedRef,
		"--cert-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--digest-alg", "sha256",
		"--predicate-type", provenancePredicate,
		"--format", "json",
	}
	verifyCtx, cancel := context.WithTimeout(ctx, attestationTimeout)
	defer cancel()
	stdout, stderr, err := v.runner.Run(verifyCtx, ghExecutable, args)
	if err != nil {
		if errors.Is(verifyCtx.Err(), context.DeadlineExceeded) {
			return Verified{}, errors.New("GitHub attestation verification timed out")
		}
		message := strings.TrimSpace(string(stderr))
		if len(message) > 4096 {
			message = message[:4096] + "..."
		}
		if message != "" {
			return Verified{}, fmt.Errorf("GitHub attestation verification failed: %w: %s", err, message)
		}
		return Verified{}, fmt.Errorf("GitHub attestation verification failed: %w", err)
	}
	if len(stdout) > maxVerifierOutput || len(stderr) > maxVerifierOutput {
		return Verified{}, errors.New("GitHub attestation verifier output exceeded the safety limit")
	}
	if err := validateAttestationResult(stdout, digest); err != nil {
		return Verified{}, err
	}
	postGHDigest, err := v.authenticate(ctx, ghExecutable)
	if err != nil {
		return Verified{}, fmt.Errorf("re-authenticate GitHub CLI executable: %w", err)
	}
	if postGHDigest != ghDigest {
		return Verified{}, errors.New("GitHub CLI executable changed during verification")
	}

	postPath, postInfo, err := canonicalVerificationFile(binaryPath, "staged executable")
	if err != nil {
		return Verified{}, err
	}
	postDigest, postSize, err := hashVerificationFile(postPath, postInfo)
	if err != nil {
		return Verified{}, err
	}
	if postPath != binaryPath || postSize != size || postDigest != digest {
		return Verified{}, errors.New("staged executable changed during verification")
	}
	return Verified{binaryPath: binaryPath, target: target.raw, digest: digest, size: size}, nil
}

func validateVerificationPlan(plan Plan) (semanticVersion, error) {
	if _, err := currentVersion(plan.Current); err != nil {
		return semanticVersion{}, fmt.Errorf("invalid self-update verification plan: %w", err)
	}
	target, err := parseVersion(plan.Target)
	if err != nil {
		return semanticVersion{}, fmt.Errorf("invalid self-update verification target: %w", err)
	}
	if plan.OS != "windows" || plan.Arch != "amd64" {
		return semanticVersion{}, fmt.Errorf("self-update verification has no qualified artifact for %s/%s", plan.OS, plan.Arch)
	}
	if plan.ExpectedRepo != expectedReleaseRepo || plan.ExpectedRef != "refs/tags/"+target.raw || plan.Workflow != expectedReleaseWorkflow {
		return semanticVersion{}, errors.New("self-update verification plan has an unexpected provenance policy")
	}
	archiveName := fmt.Sprintf("container-bin-%s-windows-amd64.zip", target.raw)
	if plan.Binary.Name != "cb.exe" || plan.Archive.Name != archiveName || plan.Checksums.Name != "SHA256SUMS" {
		return semanticVersion{}, errors.New("self-update verification plan has an unexpected asset layout")
	}
	if plan.ReleaseURL != releaseWebRoot+"/tag/"+target.raw ||
		plan.Binary.URL != releaseWebRoot+"/download/"+target.raw+"/cb.exe" ||
		plan.Archive.URL != releaseWebRoot+"/download/"+target.raw+"/"+archiveName ||
		plan.Checksums.URL != releaseWebRoot+"/download/"+target.raw+"/SHA256SUMS" {
		return semanticVersion{}, errors.New("self-update verification plan has a non-canonical release URL")
	}
	if plan.Binary.Size <= 0 || plan.Binary.Size > maxBinarySize || plan.Archive.Size <= 0 || plan.Archive.Size > maxArchiveSize || plan.Checksums.Size <= 0 || plan.Checksums.Size > maxChecksumSize {
		return semanticVersion{}, errors.New("self-update verification plan has an invalid asset size")
	}
	return target, nil
}

func canonicalVerificationFile(path, label string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) {
		return "", nil, fmt.Errorf("%s path must be absolute", label)
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s must be a regular non-symlink file", label)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", label, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("make %s path absolute: %w", label, err)
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("inspect resolved %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s must resolve to a regular file", label)
	}
	return resolved, info, nil
}

func hashVerificationFile(path string, expected os.FileInfo) (string, int64, error) {
	return hashRegularFile(path, expected, "staged executable")
}

func hashRegularFile(path string, expected os.FileInfo, label string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s for verification: %w", label, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("inspect opened %s: %w", label, err)
	}
	if !os.SameFile(expected, opened) || !opened.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s changed before hashing", label)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", label, err)
	}
	if n != expected.Size() || n != opened.Size() {
		return "", 0, fmt.Errorf("%s changed size while hashing", label)
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

func readExactVerificationFile(path string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open checksum manifest: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened checksum manifest: %w", err)
	}
	if !os.SameFile(expected, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("checksum manifest changed before reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxChecksumSize+1))
	if err != nil {
		return nil, fmt.Errorf("read checksum manifest: %w", err)
	}
	if int64(len(data)) != expected.Size() || len(data) > maxChecksumSize {
		return nil, errors.New("checksum manifest changed while reading")
	}
	return data, nil
}

func verifyChecksumManifest(data []byte, plan Plan, binaryDigest string) error {
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.Contains(data, []byte{'\r'}) {
		return errors.New("checksum manifest must use canonical LF-terminated lines")
	}
	wantNames := map[string]bool{plan.Binary.Name: false, plan.Archive.Name: false}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if len(line) < sha256.Size*2+2 || line[sha256.Size*2:sha256.Size*2+2] != "  " {
			return errors.New("checksum manifest has a non-canonical entry")
		}
		digest := line[:sha256.Size*2]
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || digest != strings.ToLower(digest) {
			return errors.New("checksum manifest has an invalid SHA-256 digest")
		}
		name := line[sha256.Size*2+2:]
		seen, expected := wantNames[name]
		if !expected {
			return fmt.Errorf("checksum manifest contains unexpected asset %q", name)
		}
		if seen {
			return fmt.Errorf("checksum manifest contains duplicate asset %q", name)
		}
		wantNames[name] = true
		if name == plan.Binary.Name && digest != binaryDigest {
			return errors.New("staged executable checksum does not match SHA256SUMS")
		}
	}
	for _, name := range []string{plan.Binary.Name, plan.Archive.Name} {
		if !wantNames[name] {
			return fmt.Errorf("checksum manifest is missing asset %q", name)
		}
	}
	return nil
}

type attestationOutput []struct {
	VerificationResult struct {
		Statement struct {
			PredicateType string `json:"predicateType"`
			Subject       []struct {
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
		} `json:"statement"`
	} `json:"verificationResult"`
}

func validateAttestationResult(data []byte, digest string) error {
	if len(data) == 0 || len(data) > maxVerifierOutput {
		return errors.New("GitHub attestation verifier returned invalid output")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var output attestationOutput
	if err := decoder.Decode(&output); err != nil {
		return fmt.Errorf("parse GitHub attestation verification result: %w", err)
	}
	if len(output) == 0 {
		return errors.New("GitHub attestation verifier returned no verified attestations")
	}
	for _, result := range output {
		if result.VerificationResult.Statement.PredicateType != provenancePredicate {
			return errors.New("GitHub attestation result has an unexpected predicate type")
		}
		matched := false
		for _, subject := range result.VerificationResult.Statement.Subject {
			if subject.Digest["sha256"] == digest {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("GitHub attestation result does not cover the staged executable digest")
		}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("GitHub attestation verifier returned trailing JSON data")
	}
	return nil
}

type commandAttestationRunner struct{}

func (commandAttestationRunner) Run(ctx context.Context, executable string, args []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	env, err := attestationEnvironment()
	if err != nil {
		return nil, nil, err
	}
	cmd.Env = env
	volume := filepath.VolumeName(executable)
	cmd.Dir = volume + string(os.PathSeparator)
	var stdout, stderr boundedBuffer
	stdout.limit = maxVerifierOutput
	stderr.limit = maxVerifierOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if stdout.overflow || stderr.overflow {
		return stdout.data, stderr.data, errors.New("verifier output exceeded the safety limit")
	}
	return stdout.data, stderr.data, err
}

func attestationEnvironment() ([]string, error) {
	env, err := verifierBaseEnvironment()
	if err != nil {
		return nil, err
	}
	env = append(env, "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	if token := os.Getenv("GH_TOKEN"); token != "" {
		env = append(env, "GH_TOKEN="+token)
		return env, nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
		return env, nil
	}
	return nil, errors.New("GitHub attestation verification requires an explicit GH_TOKEN or GITHUB_TOKEN")
}

type boundedBuffer struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		copyLength := len(p)
		if copyLength > remaining {
			copyLength = remaining
		}
		b.data = append(b.data, p[:copyLength]...)
	}
	if len(p) > remaining {
		b.overflow = true
	}
	return len(p), nil
}
