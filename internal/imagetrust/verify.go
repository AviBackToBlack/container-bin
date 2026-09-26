// Package imagetrust invokes the administrator-pinned cosign verifier against
// an exact repository digest. It owns only the isolated invocation boundary;
// lock evidence production and runtime evidence authorization remain separate
// callers so neither can accidentally treat process success as authorization.
package imagetrust

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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/policy"
)

const (
	stagingPrefix       = ".container-bin-image-trust-"
	verifierName        = "cosign.exe"
	publicKeyName       = "policy-key.pub"
	verificationTimeout = 2 * time.Minute
	maxVerifierOutput   = 1 << 20
	cosignPayloadType   = "cosign container image signature"
)

// Result is an opaque record of one successful isolated cosign invocation.
// BundleSHA256s identifies any authenticated Rekor bundles cosign returned;
// an online verification may instead have used a live Rekor lookup and return
// no bundle. Callers must not turn Result into lock authorization without the
// later evidence-production policy deciding how that distinction is recorded.
type Result struct {
	mechanism         policy.ImageTrustMechanism
	networkMode       policy.ImageTrustNetworkMode
	repository        string
	digest            string
	signer            string
	issuer            string
	verifierSHA256    string
	policyFingerprint string
	outputSHA256      string
	bundleSHA256s     []string
	verifiedAt        time.Time
	signatureCount    int
}

func (r Result) Mechanism() policy.ImageTrustMechanism     { return r.mechanism }
func (r Result) NetworkMode() policy.ImageTrustNetworkMode { return r.networkMode }
func (r Result) Repository() string                        { return r.repository }
func (r Result) Digest() string                            { return r.digest }
func (r Result) Signer() string                            { return r.signer }
func (r Result) Issuer() string                            { return r.issuer }
func (r Result) VerifierSHA256() string                    { return r.verifierSHA256 }
func (r Result) PolicyFingerprint() string                 { return r.policyFingerprint }
func (r Result) OutputSHA256() string                      { return r.outputSHA256 }
func (r Result) VerifiedAt() time.Time                     { return r.verifiedAt }
func (r Result) SignatureCount() int                       { return r.signatureCount }
func (r Result) BundleSHA256s() []string                   { return append([]string(nil), r.bundleSHA256s...) }

type runner interface {
	Run(context.Context, string, []string, string) ([]byte, []byte, error)
}

type verifier struct {
	runner      runner
	now         func() time.Time
	createStage func() (string, error)
}

type authenticatedSnapshot struct {
	contents []byte
	digest   string
}

func (s authenticatedSnapshot) Bytes() []byte  { return append([]byte(nil), s.contents...) }
func (s authenticatedSnapshot) SHA256() string { return s.digest }
func (s authenticatedSnapshot) Size() int64    { return int64(len(s.contents)) }

type verificationRequest struct {
	resolved          string
	repository        string
	digest            string
	rule              policy.ImageTrustRule
	verifier          authenticatedSnapshot
	key               authenticatedSnapshot
	policyFingerprint string
}

// Verify authenticates and snapshots the administrator-selected verifier and
// key material, executes only protected staged copies, and independently
// validates cosign's bounded JSON result against the exact resolved digest.
// It currently supports online rules only. Offline rules fail before staging
// or process execution until policy can pin the complete trusted-root material
// required to make cosign's offline claim real rather than cosmetic.
func Verify(ctx context.Context, machinePolicy policy.Policy, configured, resolved string) (Result, error) {
	return (verifier{
		runner:      commandRunner{},
		now:         time.Now,
		createStage: createPrivateStage,
	}).Verify(ctx, machinePolicy, configured, resolved)
}

func (v verifier) Verify(ctx context.Context, machinePolicy policy.Policy, configured, resolved string) (result Result, err error) {
	if v.runner == nil || v.now == nil || v.createStage == nil {
		return Result{}, errors.New("image trust verifier is incomplete")
	}
	repository, digest, verificationTarget, rule, err := validateRequest(machinePolicy, configured, resolved)
	if err != nil {
		return Result{}, err
	}
	verifierSnapshot, err := machinePolicy.AuthenticateCosignVerifier()
	if err != nil {
		return Result{}, err
	}
	var keySnapshot policy.FileSnapshot
	if rule.Mechanism == policy.ImageTrustKey {
		keySnapshot, err = machinePolicy.AuthenticateImageTrustPublicKey(configured)
		if err != nil {
			return Result{}, err
		}
	}
	return v.verifyAuthenticated(ctx, verificationRequest{
		resolved:          verificationTarget,
		repository:        repository,
		digest:            digest,
		rule:              rule,
		verifier:          snapshotFromPolicy(verifierSnapshot),
		key:               snapshotFromPolicy(keySnapshot),
		policyFingerprint: machinePolicy.Fingerprint,
	})
}

func snapshotFromPolicy(snapshot policy.FileSnapshot) authenticatedSnapshot {
	return authenticatedSnapshot{contents: snapshot.Bytes(), digest: snapshot.SHA256()}
}

func (v verifier) verifyAuthenticated(ctx context.Context, request verificationRequest) (result Result, err error) {
	if request.rule.NetworkMode != policy.ImageTrustOnline {
		return Result{}, fmt.Errorf("image trust rule for %q requires %s verification, which is not available until machine policy can pin complete offline trusted-root material", request.rule.Repository, request.rule.NetworkMode)
	}

	stageDir, err := v.createStage()
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stageDir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private image trust staging directory: %w", cleanupErr))
		}
	}()

	verifierPath, err := stageSnapshot(stageDir, verifierName, request.verifier, 0o700)
	if err != nil {
		return Result{}, err
	}
	args := []string{"verify", "--output=json", "--max-workers=1"}
	signer, issuer := request.rule.Subject, request.rule.Issuer
	var keyPath string
	switch request.rule.Mechanism {
	case policy.ImageTrustKeyless:
		args = append(args,
			"--certificate-identity="+request.rule.Subject,
			"--certificate-oidc-issuer="+request.rule.Issuer,
		)
	case policy.ImageTrustKey:
		keyPath, err = stageSnapshot(stageDir, publicKeyName, request.key, 0o600)
		if err != nil {
			return Result{}, err
		}
		args = append(args, "--key="+keyPath)
		signer, issuer = request.key.SHA256(), ""
	default:
		return Result{}, fmt.Errorf("image trust rule for %q has unsupported mechanism %q", request.rule.Repository, request.rule.Mechanism)
	}
	args = append(args, request.resolved)

	verifyCtx, cancel := context.WithTimeout(ctx, verificationTimeout)
	defer cancel()
	stdout, stderr, runErr := v.runner.Run(verifyCtx, verifierPath, args, stageDir)
	stageErr := verifyStagedSnapshot(verifierPath, request.verifier)
	if keyPath != "" {
		stageErr = errors.Join(stageErr, verifyStagedSnapshot(keyPath, request.key))
	}
	if stageErr != nil {
		return Result{}, fmt.Errorf("authenticated image trust material changed during verification: %w", stageErr)
	}
	if len(stdout) > maxVerifierOutput || len(stderr) > maxVerifierOutput {
		return Result{}, errors.New("cosign verifier output exceeded the safety limit")
	}
	if runErr != nil {
		if errors.Is(verifyCtx.Err(), context.DeadlineExceeded) {
			return Result{}, errors.New("cosign image verification timed out")
		}
		message := strings.TrimSpace(string(stderr))
		if len(message) > 4096 {
			message = message[:4096] + "..."
		}
		if message != "" {
			return Result{}, fmt.Errorf("cosign image verification failed: %w: %s", runErr, strconv.Quote(message))
		}
		return Result{}, fmt.Errorf("cosign image verification failed: %w", runErr)
	}
	count, bundles, err := validateOutput(stdout, request.digest, request.rule)
	if err != nil {
		return Result{}, err
	}
	outputSum := sha256.Sum256(stdout)
	return Result{
		mechanism:         request.rule.Mechanism,
		networkMode:       request.rule.NetworkMode,
		repository:        request.repository,
		digest:            request.digest,
		signer:            signer,
		issuer:            issuer,
		verifierSHA256:    request.verifier.SHA256(),
		policyFingerprint: request.policyFingerprint,
		outputSHA256:      hex.EncodeToString(outputSum[:]),
		bundleSHA256s:     bundles,
		verifiedAt:        v.now().UTC().Truncate(time.Second),
		signatureCount:    count,
	}, nil
}

func validateRequest(machinePolicy policy.Policy, configured, resolved string) (string, string, string, policy.ImageTrustRule, error) {
	if !machinePolicy.Managed() || !validSHA256(machinePolicy.Fingerprint) {
		return "", "", "", policy.ImageTrustRule{}, errors.New("image trust verification requires a valid managed machine policy fingerprint")
	}
	configuredRepository, digest, verificationTarget, err := canonicalVerificationTarget(configured, resolved)
	if err != nil {
		return "", "", "", policy.ImageTrustRule{}, err
	}
	rule, ok, err := machinePolicy.ImageTrustFor(configured)
	if err != nil {
		return "", "", "", policy.ImageTrustRule{}, fmt.Errorf("select image trust rule for %q: %w", configured, err)
	}
	if !ok {
		return "", "", "", policy.ImageTrustRule{}, fmt.Errorf("image %q has no machine-policy image trust rule", configured)
	}
	return configuredRepository, digest, verificationTarget, rule, nil
}

func canonicalVerificationTarget(configured, resolved string) (string, string, string, error) {
	configuredRepository, err := policy.CanonicalRepository(configured)
	if err != nil {
		return "", "", "", fmt.Errorf("configured image %q: %w", configured, err)
	}
	resolvedRepository, digest, err := splitResolvedDigest(resolved)
	if err != nil {
		return "", "", "", err
	}
	if resolvedRepository != configuredRepository {
		return "", "", "", fmt.Errorf("resolved repository %q does not match configured repository %q", resolvedRepository, configuredRepository)
	}
	return configuredRepository, digest, configuredRepository + "@" + digest, nil
}

func splitResolvedDigest(resolved string) (string, string, error) {
	if strings.Count(resolved, "@") != 1 {
		return "", "", fmt.Errorf("resolved image %q must be an exact repository@sha256 digest", resolved)
	}
	repository, digest, _ := strings.Cut(resolved, "@")
	lastSlash := strings.LastIndexByte(repository, '/')
	if repository == "" || strings.LastIndexByte(repository, ':') > lastSlash || !validDigest(digest) {
		return "", "", fmt.Errorf("resolved image %q must be an exact repository@sha256 digest", resolved)
	}
	canonical, err := policy.CanonicalRepository(repository)
	if err != nil {
		return "", "", fmt.Errorf("resolved image %q: %w", resolved, err)
	}
	return canonical, digest, nil
}

func validDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validSHA256(strings.TrimPrefix(value, "sha256:"))
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func createPrivateStage() (string, error) {
	dir, err := os.MkdirTemp("", stagingPrefix)
	if err != nil {
		return "", fmt.Errorf("create private image trust staging directory: %w", err)
	}
	if err := restrictStagingPath(dir, true); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("protect private image trust staging directory: %w", err)
	}
	return dir, nil
}

func stageSnapshot(dir, name string, snapshot authenticatedSnapshot, mode os.FileMode) (string, error) {
	contents := snapshot.Bytes()
	if int64(len(contents)) != snapshot.Size() || !validSHA256(snapshot.SHA256()) {
		return "", fmt.Errorf("authenticated snapshot for %s is invalid", name)
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != snapshot.SHA256() {
		return "", fmt.Errorf("authenticated snapshot for %s does not match its digest", name)
	}
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return "", fmt.Errorf("create staged %s: %w", name, err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write staged %s: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("flush staged %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close staged %s: %w", name, err)
	}
	if err := restrictStagingPath(path, false); err != nil {
		return "", fmt.Errorf("protect staged %s: %w", name, err)
	}
	if err := verifyStagedSnapshot(path, snapshot); err != nil {
		return "", fmt.Errorf("verify staged %s: %w", name, err)
	}
	return path, nil
}

func verifyStagedSnapshot(path string, snapshot authenticatedSnapshot) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != snapshot.Size() {
		return errors.New("staged path is not the expected regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return errors.New("staged file changed before hashing")
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(file, snapshot.Size()+1))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != snapshot.Size() || n != opened.Size() || hex.EncodeToString(hash.Sum(nil)) != snapshot.SHA256() {
		return errors.New("staged file bytes do not match the authenticated snapshot")
	}
	post, err := os.Lstat(path)
	if err != nil || post.Mode()&os.ModeSymlink != 0 || !post.Mode().IsRegular() || !os.SameFile(info, post) || post.Size() != n {
		return errors.New("staged file changed while hashing")
	}
	return nil
}

type cosignPayload struct {
	Critical struct {
		Image struct {
			Digest string `json:"Docker-manifest-digest"`
		} `json:"Image"`
		Type string `json:"Type"`
	} `json:"Critical"`
	Optional map[string]json.RawMessage `json:"Optional"`
}

func validateOutput(data []byte, digest string, rule policy.ImageTrustRule) (int, []string, error) {
	if len(data) == 0 || len(data) > maxVerifierOutput {
		return 0, nil, errors.New("cosign verifier returned invalid output")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var payloads []cosignPayload
	if err := decoder.Decode(&payloads); err != nil {
		return 0, nil, fmt.Errorf("parse cosign verification result: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return 0, nil, errors.New("cosign verifier returned trailing JSON data")
	}
	if len(payloads) == 0 {
		return 0, nil, errors.New("cosign verifier returned no verified signatures")
	}
	bundleSet := map[string]bool{}
	for i, payload := range payloads {
		if payload.Critical.Type != cosignPayloadType {
			return 0, nil, fmt.Errorf("cosign signature %d has unexpected payload type %q", i+1, payload.Critical.Type)
		}
		if payload.Critical.Image.Digest != digest {
			return 0, nil, fmt.Errorf("cosign signature %d covers digest %q, expected %q", i+1, payload.Critical.Image.Digest, digest)
		}
		if rule.Mechanism == policy.ImageTrustKeyless {
			if err := requireJSONString(payload.Optional, "Subject", rule.Subject); err != nil {
				return 0, nil, fmt.Errorf("cosign signature %d: %w", i+1, err)
			}
			if err := requireJSONString(payload.Optional, "Issuer", rule.Issuer); err != nil {
				return 0, nil, fmt.Errorf("cosign signature %d: %w", i+1, err)
			}
		}
		if raw, ok := payload.Optional["Bundle"]; ok {
			var bundle map[string]json.RawMessage
			if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &bundle) != nil || len(bundle) == 0 {
				return 0, nil, fmt.Errorf("cosign signature %d returned a malformed transparency bundle", i+1)
			}
			sum := sha256.Sum256(raw)
			bundleSet[hex.EncodeToString(sum[:])] = true
		}
	}
	bundles := make([]string, 0, len(bundleSet))
	for digest := range bundleSet {
		bundles = append(bundles, digest)
	}
	sort.Strings(bundles)
	return len(payloads), bundles, nil
}

func requireJSONString(values map[string]json.RawMessage, key, expected string) error {
	raw, ok := values[key]
	if !ok {
		return fmt.Errorf("verified keyless output is missing %s", key)
	}
	var actual string
	if err := json.Unmarshal(raw, &actual); err != nil || actual != expected {
		return fmt.Errorf("verified keyless output %s does not match machine policy", key)
	}
	return nil
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
