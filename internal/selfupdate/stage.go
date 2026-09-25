package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	stagingPrefix       = ".container-bin-update-"
	downloadTimeout     = 2 * time.Minute
	releaseAssetCDNHost = "release-assets.githubusercontent.com"
)

// Staged contains the downloaded inputs for the later verification phase.
// Nothing in Stage changes an installed ContainerBin file.
type Staged struct {
	Dir           string
	BinaryPath    string
	ChecksumsPath string
	Target        string
	owned         bool
}

// Cleanup removes a staging directory returned by Stage. It refuses to remove
// paths that do not have the exact staging layout.
func (s Staged) Cleanup() error {
	if s.Dir == "" {
		return nil
	}
	dir := filepath.Clean(s.Dir)
	binaryName := filepath.Base(filepath.Clean(s.BinaryPath))
	validBinaryName := binaryName == "cb.exe" || binaryName == fmt.Sprintf("container-bin-%s-windows-arm64.zip", s.Target)
	if !s.owned || !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), stagingPrefix) ||
		!validBinaryName || filepath.Clean(s.BinaryPath) != filepath.Join(dir, binaryName) ||
		filepath.Clean(s.ChecksumsPath) != filepath.Join(dir, "SHA256SUMS") {
		return errors.New("refusing to remove an invalid self-update staging layout")
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove self-update staging directory: %w", err)
	}
	return nil
}

type stager struct {
	doer         httpDoer
	restrictPath func(string, bool) error
	removeAll    func(string) error
}

// Stage downloads the selected attested artifact and its checksum manifest
// into a private temporary directory beside the installed management
// executable. The artifact is cb.exe on amd64 and the architecture-specific
// archive on arm64. The caller must authenticate and verify both inputs before
// any extraction or replacement.
func Stage(ctx context.Context, plan Plan, installedExecutable string) (Staged, error) {
	return (stager{doer: newDownloadClient()}).Stage(ctx, plan, installedExecutable)
}

func newDownloadClient() *http.Client {
	return &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) != 1 {
				return errors.New("release asset download used more than one redirect")
			}
			if !isCanonicalReleaseAssetURL(via[0].URL) {
				return errors.New("release asset redirect did not originate at the canonical GitHub release URL")
			}
			if !isReleaseAssetCDNURL(req.URL) {
				return fmt.Errorf("release asset redirect target %q is outside the permitted GitHub asset flow", urlWithoutQuery(req.URL))
			}
			req.Header.Del("Authorization")
			return nil
		},
	}
}

func (s stager) Stage(ctx context.Context, plan Plan, installedExecutable string) (staged Staged, err error) {
	if s.doer == nil {
		return Staged{}, errors.New("self-update stager has no HTTP client")
	}
	if err := validateStagingPlan(plan); err != nil {
		return Staged{}, err
	}
	destinationDir, err := canonicalInstalledExecutableDir(installedExecutable)
	if err != nil {
		return Staged{}, err
	}

	dir, err := os.MkdirTemp(destinationDir, stagingPrefix)
	if err != nil {
		return Staged{}, fmt.Errorf("create private self-update staging directory: %w", err)
	}
	defer func() {
		if err != nil {
			if cleanupErr := s.removeStaging(dir); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove failed self-update staging directory: %w", cleanupErr))
			}
			staged = Staged{}
		}
	}()
	if err = s.restrict(dir, true); err != nil {
		return Staged{}, fmt.Errorf("restrict self-update staging directory: %w", err)
	}
	staged = Staged{
		Dir:           dir,
		BinaryPath:    filepath.Join(dir, plan.Binary.Name),
		ChecksumsPath: filepath.Join(dir, plan.Checksums.Name),
		Target:        plan.Target,
		owned:         true,
	}

	// Fetch the small manifest first so a later binary failure also exercises
	// all-or-nothing staging cleanup.
	if err = s.download(ctx, plan.Current, plan.Checksums, staged.ChecksumsPath); err != nil {
		return Staged{}, err
	}
	if err = s.download(ctx, plan.Current, plan.Binary, staged.BinaryPath); err != nil {
		return Staged{}, err
	}
	return staged, nil
}

func (s stager) download(ctx context.Context, current string, asset Asset, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return fmt.Errorf("create request for release asset %q: %w", asset.Name, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "container-bin/"+current)

	resp, err := s.doer.Do(req)
	if err != nil {
		return fmt.Errorf("download release asset %q: %w", asset.Name, err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("download release asset %q returned an empty response", asset.Name)
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || !isPermittedFinalURL(resp.Request.URL, asset.URL) {
		return fmt.Errorf("release asset %q response is outside the permitted GitHub asset flow", asset.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download release asset %q returned HTTP %d", asset.Name, resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "" {
		return fmt.Errorf("download release asset %q unexpectedly returned a partial response", asset.Name)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != asset.Size {
		return fmt.Errorf("release asset %q Content-Length is %d, expected %d", asset.Name, resp.ContentLength, asset.Size)
	}

	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged release asset %q: %w", asset.Name, err)
	}
	n, copyErr := io.Copy(file, io.LimitReader(resp.Body, asset.Size+1))
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write staged release asset %q: %w", asset.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged release asset %q: %w", asset.Name, closeErr)
	}
	if err := s.restrict(destination, false); err != nil {
		return fmt.Errorf("restrict staged release asset %q: %w", asset.Name, err)
	}
	switch {
	case n < asset.Size:
		return fmt.Errorf("release asset %q was truncated: received %d bytes, expected %d", asset.Name, n, asset.Size)
	case n > asset.Size:
		return fmt.Errorf("release asset %q exceeded its advertised size %d", asset.Name, asset.Size)
	}
	return nil
}

func (s stager) restrict(path string, directory bool) error {
	if s.restrictPath != nil {
		return s.restrictPath(path, directory)
	}
	return restrictStagingPath(path, directory)
}

func (s stager) removeStaging(path string) error {
	if s.removeAll != nil {
		return s.removeAll(path)
	}
	return os.RemoveAll(path)
}

func validateStagingPlan(plan Plan) error {
	current, err := currentVersion(plan.Current)
	if err != nil {
		return fmt.Errorf("invalid self-update staging plan: %w", err)
	}
	target, err := parseVersion(plan.Target)
	if err != nil {
		return fmt.Errorf("invalid self-update staging target: %w", err)
	}
	switch comparison := current.compare(target); {
	case comparison == 0:
		return errors.New("self-update staging target is already installed")
	case comparison > 0 && (plan.downgradeAuthorization.current != current.raw || plan.downgradeAuthorization.target != target.raw):
		return errors.New("self-update staging plan does not authorize the requested downgrade")
	case comparison < 0 && plan.downgradeAuthorization != (downgradeAuthorization{}):
		return errors.New("self-update staging plan has inconsistent downgrade authorization")
	}
	if plan.OS != "windows" || (plan.Arch != "amd64" && plan.Arch != "arm64") {
		return fmt.Errorf("self-update staging has no qualified artifact for %s/%s", plan.OS, plan.Arch)
	}
	if plan.ReleaseURL != releaseWebRoot+"/tag/"+target.raw {
		return errors.New("self-update staging plan has a non-canonical release URL")
	}
	if plan.ExpectedRepo != "AviBackToBlack/container-bin" || plan.ExpectedRef != "refs/tags/"+target.raw || plan.Workflow != ".github/workflows/release.yml" {
		return errors.New("self-update staging plan has an unexpected provenance policy")
	}
	amd64Archive := fmt.Sprintf("container-bin-%s-windows-amd64.zip", target.raw)
	arm64Archive := fmt.Sprintf("container-bin-%s-windows-arm64.zip", target.raw)
	binaryName := "cb.exe"
	binaryLimit := int64(maxBinarySize)
	archiveName := amd64Archive
	wantLayout := checksumLayoutLegacyAMD64
	if plan.Arch == "arm64" {
		binaryName = arm64Archive
		binaryLimit = maxArchiveSize
		archiveName = arm64Archive
		wantLayout = checksumLayoutDualArch
	} else if plan.checksumLayout == checksumLayoutDualArch {
		wantLayout = checksumLayoutDualArch
	}
	if plan.checksumLayout != wantLayout {
		return errors.New("self-update staging plan has an unexpected checksum layout")
	}
	for _, expected := range []struct {
		asset Asset
		name  string
		limit int64
	}{
		{plan.Binary, binaryName, binaryLimit},
		{plan.Archive, archiveName, maxArchiveSize},
		{plan.Checksums, "SHA256SUMS", maxChecksumSize},
	} {
		if err := validateStagingAsset(expected.asset, expected.name, target.raw, expected.limit); err != nil {
			return err
		}
	}
	return nil
}

func validateStagingAsset(asset Asset, name, target string, limit int64) error {
	if asset.Name != name {
		return fmt.Errorf("self-update staging plan expected asset %q, got %q", name, asset.Name)
	}
	if asset.Size <= 0 || asset.Size > limit {
		return fmt.Errorf("self-update staging asset %q has invalid size %d", name, asset.Size)
	}
	wantURL := releaseWebRoot + "/download/" + target + "/" + name
	if asset.URL != wantURL {
		return fmt.Errorf("self-update staging asset %q has a non-canonical download URL", name)
	}
	return nil
}

func canonicalInstalledExecutableDir(installedExecutable string) (string, error) {
	if !filepath.IsAbs(installedExecutable) {
		return "", errors.New("installed ContainerBin executable path must be absolute")
	}
	clean := filepath.Clean(installedExecutable)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("inspect installed ContainerBin executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("installed ContainerBin executable must be a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve installed ContainerBin executable: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make installed ContainerBin executable path absolute: %w", err)
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect resolved installed ContainerBin executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("installed ContainerBin executable must resolve to a regular file")
	}
	return filepath.Dir(resolved), nil
}

func isCanonicalReleaseAssetURL(candidate *url.URL) bool {
	if candidate == nil || candidate.Scheme != "https" || candidate.Host != "github.com" || candidate.User != nil || candidate.RawQuery != "" || candidate.Fragment != "" {
		return false
	}
	return strings.HasPrefix(candidate.EscapedPath(), "/AviBackToBlack/container-bin/releases/download/")
}

func isReleaseAssetCDNURL(candidate *url.URL) bool {
	if candidate == nil || candidate.Scheme != "https" || candidate.Host != releaseAssetCDNHost || candidate.User != nil || candidate.Fragment != "" {
		return false
	}
	return strings.HasPrefix(candidate.EscapedPath(), "/github-production-release-asset/") && candidate.RawQuery != ""
}

func isPermittedFinalURL(candidate *url.URL, canonical string) bool {
	return candidate.String() == canonical || isReleaseAssetCDNURL(candidate)
}

func urlWithoutQuery(candidate *url.URL) string {
	if candidate == nil {
		return ""
	}
	return candidate.Scheme + "://" + candidate.Host + candidate.EscapedPath()
}
