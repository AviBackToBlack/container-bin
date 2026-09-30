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
	trustedRootName     = "trusted-root.json"
	verificationTimeout = 2 * time.Minute
	maxVerifierOutput   = 1 << 20
	maxSignatureBundles = 32
	cosignPayloadType   = "https://sigstore.dev/cosign/sign/v1"
)

// Result is an opaque record of one successful isolated cosign verification.
// BundleSHA256s identifies the exact downloaded bundles that the staged cosign
// snapshot reverified against the requested digest, predicate and identity/key.
// Callers still decide how many authenticated bundles their evidence schema can
// represent.
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
	trustedRoot       authenticatedSnapshot
	policyFingerprint string
}

// Verify authenticates and snapshots the administrator-selected verifier and
// key material, executes only protected staged copies, and independently
// validates cosign's bounded JSON result against the exact resolved digest.
// Offline rules additionally authenticate and stage the administrator-pinned
// Sigstore TrustedRoot, then require cosign's offline mode for every local
// bundle verification so missing proof cannot fall back to network lookup.
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
	var trustedRootSnapshot policy.FileSnapshot
	if rule.NetworkMode == policy.ImageTrustOfflineBundle {
		trustedRootSnapshot, err = machinePolicy.AuthenticateImageTrustTrustedRoot()
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
		trustedRoot:       snapshotFromPolicy(trustedRootSnapshot),
		policyFingerprint: machinePolicy.Fingerprint,
	})
}

func snapshotFromPolicy(snapshot policy.FileSnapshot) authenticatedSnapshot {
	return authenticatedSnapshot{contents: snapshot.Bytes(), digest: snapshot.SHA256()}
}

func (v verifier) verifyAuthenticated(ctx context.Context, request verificationRequest) (result Result, err error) {
	if request.rule.NetworkMode != policy.ImageTrustOnline && request.rule.NetworkMode != policy.ImageTrustOfflineBundle {
		return Result{}, fmt.Errorf("image trust rule for %q has unsupported network mode %q", request.rule.Repository, request.rule.NetworkMode)
	}
	if request.rule.NetworkMode == policy.ImageTrustOfflineBundle && request.trustedRoot.Size() == 0 {
		return Result{}, fmt.Errorf("image trust rule for %q requires an authenticated offline trusted root", request.rule.Repository)
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
	signer, issuer := request.rule.Subject, request.rule.Issuer
	var keyPath string
	var trustedRootPath string
	switch request.rule.Mechanism {
	case policy.ImageTrustKeyless:
	case policy.ImageTrustKey:
		keyPath, err = stageSnapshot(stageDir, publicKeyName, request.key, 0o600)
		if err != nil {
			return Result{}, err
		}
		signer, issuer = request.key.SHA256(), ""
	default:
		return Result{}, fmt.Errorf("image trust rule for %q has unsupported mechanism %q", request.rule.Repository, request.rule.Mechanism)
	}
	if request.rule.NetworkMode == policy.ImageTrustOfflineBundle {
		trustedRootPath, err = stageSnapshot(stageDir, trustedRootName, request.trustedRoot, 0o600)
		if err != nil {
			return Result{}, err
		}
	}

	verifyCtx, cancel := context.WithTimeout(ctx, verificationTimeout)
	defer cancel()
	stdout, stderr, runErr := v.runner.Run(verifyCtx, verifierPath, []string{"download", "signature", request.resolved}, stageDir)
	stageErr := verifyStagedSnapshot(verifierPath, request.verifier)
	if keyPath != "" {
		stageErr = errors.Join(stageErr, verifyStagedSnapshot(keyPath, request.key))
	}
	if trustedRootPath != "" {
		stageErr = errors.Join(stageErr, verifyStagedSnapshot(trustedRootPath, request.trustedRoot))
	}
	if stageErr != nil {
		return Result{}, fmt.Errorf("authenticated image trust material changed during verification: %w", stageErr)
	}
	if len(stdout) > maxVerifierOutput || len(stderr) > maxVerifierOutput {
		return Result{}, errors.New("cosign verifier output exceeded the safety limit")
	}
	if runErr != nil {
		return Result{}, verifierRunError(verifyCtx, "download image signature bundles", stderr, runErr)
	}
	downloaded, err := parseDownloadedBundles(stdout)
	if err != nil {
		return Result{}, err
	}
	bundleSet := make(map[string]bool, len(downloaded))
	signatureCount := 0
	var lastVerifyErr error
	for i, bundle := range downloaded {
		bundleSum := sha256.Sum256(bundle)
		bundleDigest := hex.EncodeToString(bundleSum[:])
		bundlePath, stageBundleErr := stageSnapshot(stageDir, fmt.Sprintf("bundle-%03d.sigstore.json", i+1), authenticatedSnapshot{contents: bundle, digest: bundleDigest}, 0o600)
		if stageBundleErr != nil {
			return Result{}, stageBundleErr
		}
		args := []string{
			"verify-blob-attestation",
			"--bundle=" + bundlePath,
			"--digest=" + strings.TrimPrefix(request.digest, "sha256:"),
			"--digestAlg=sha256",
			"--type=" + cosignPayloadType,
		}
		if request.rule.Mechanism == policy.ImageTrustKeyless {
			args = append(args,
				"--certificate-identity="+request.rule.Subject,
				"--certificate-oidc-issuer="+request.rule.Issuer,
			)
		} else {
			args = append(args, "--key="+keyPath)
		}
		if request.rule.NetworkMode == policy.ImageTrustOfflineBundle {
			args = append(args, "--offline=true", "--new-bundle-format=true", "--trusted-root="+trustedRootPath)
		}
		verifyStdout, verifyStderr, verifyErr := v.runner.Run(verifyCtx, verifierPath, args, stageDir)
		stageErr = errors.Join(stageErr, verifyStagedSnapshot(bundlePath, authenticatedSnapshot{contents: bundle, digest: bundleDigest}))
		if len(verifyStdout) > maxVerifierOutput || len(verifyStderr) > maxVerifierOutput {
			return Result{}, errors.New("cosign verifier output exceeded the safety limit")
		}
		if verifyErr != nil {
			if verifyCtx.Err() != nil {
				return Result{}, verifierRunError(verifyCtx, fmt.Sprintf("verify image signature bundle %d", i+1), verifyStderr, verifyErr)
			}
			lastVerifyErr = verifierRunError(verifyCtx, fmt.Sprintf("verify image signature bundle %d", i+1), verifyStderr, verifyErr)
			continue
		}
		signatureCount++
		bundleSet[bundleDigest] = true
	}
	stageErr = errors.Join(stageErr, verifyStagedSnapshot(verifierPath, request.verifier))
	if keyPath != "" {
		stageErr = errors.Join(stageErr, verifyStagedSnapshot(keyPath, request.key))
	}
	if trustedRootPath != "" {
		stageErr = errors.Join(stageErr, verifyStagedSnapshot(trustedRootPath, request.trustedRoot))
	}
	if stageErr != nil {
		return Result{}, fmt.Errorf("authenticated image trust material changed during verification: %w", stageErr)
	}
	if signatureCount == 0 {
		if lastVerifyErr != nil {
			return Result{}, fmt.Errorf("no downloaded image signature bundle satisfied machine policy: %w", lastVerifyErr)
		}
		return Result{}, errors.New("no downloaded image signature bundle satisfied machine policy")
	}
	bundles := make([]string, 0, len(bundleSet))
	for bundleDigest := range bundleSet {
		bundles = append(bundles, bundleDigest)
	}
	sort.Strings(bundles)
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
		signatureCount:    signatureCount,
	}, nil
}

func verifierRunError(ctx context.Context, action string, stderr []byte, runErr error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("cosign %s timed out", action)
	}
	message := strings.TrimSpace(string(stderr))
	if len(message) > 4096 {
		message = message[:4096] + "..."
	}
	if message != "" {
		return fmt.Errorf("cosign %s failed: %w: %s", action, runErr, strconv.Quote(message))
	}
	return fmt.Errorf("cosign %s failed: %w", action, runErr)
}

func parseDownloadedBundles(data []byte) ([][]byte, error) {
	if len(data) == 0 || len(data) > maxVerifierOutput {
		return nil, errors.New("cosign returned invalid signature bundle output")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	bundles := make([][]byte, 0, 1)
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse cosign signature bundle output: %w", err)
		}
		var object map[string]json.RawMessage
		if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || len(object) == 0 {
			return nil, errors.New("cosign returned a malformed signature bundle")
		}
		if len(bundles) == maxSignatureBundles {
			return nil, fmt.Errorf("cosign returned more than %d signature bundles", maxSignatureBundles)
		}
		bundles = append(bundles, append([]byte(nil), raw...))
	}
	if len(bundles) == 0 {
		return nil, errors.New("cosign returned no signature bundles")
	}
	return bundles, nil
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
