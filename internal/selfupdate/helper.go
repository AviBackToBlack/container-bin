package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	helperRequestVersion    = 1
	helperDirPrefix         = ".container-bin-update-helper-"
	helperExecutableName    = "cb-update-helper.exe"
	helperRequestName       = "request.json"
	maxHelperRequestSize    = 64 << 10
	helperParentExitTimeout = 2 * time.Minute
)

type helperPlan struct {
	Current          string `json:"current"`
	Target           string `json:"target"`
	Channel          string `json:"channel"`
	Status           string `json:"status"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	ReleaseURL       string `json:"release_url"`
	Binary           Asset  `json:"binary"`
	Archive          Asset  `json:"archive"`
	Checksums        Asset  `json:"checksums"`
	ExpectedRepo     string `json:"expected_repo"`
	ExpectedRef      string `json:"expected_ref"`
	Workflow         string `json:"workflow"`
	DowngradeCurrent string `json:"downgrade_current,omitempty"`
	DowngradeTarget  string `json:"downgrade_target,omitempty"`
	ChecksumLayout   uint8  `json:"checksum_layout"`
}

type helperVerification struct {
	BinaryPath       string `json:"binary_path"`
	Target           string `json:"target"`
	Digest           string `json:"digest"`
	Size             int64  `json:"size"`
	InstalledPath    string `json:"installed_path"`
	InstalledVersion string `json:"installed_version"`
	InstalledDigest  string `json:"installed_digest"`
	InstalledSize    int64  `json:"installed_size"`
}

type helperRequest struct {
	Version             int                `json:"version"`
	ParentPID           int                `json:"parent_pid"`
	HelperDir           string             `json:"helper_dir"`
	HelperExecutable    string             `json:"helper_executable"`
	HelperSHA256        string             `json:"helper_sha256"`
	InstalledExecutable string             `json:"installed_executable"`
	GitHubExecutable    string             `json:"github_executable"`
	Staged              Staged             `json:"staged"`
	Plan                helperPlan         `json:"plan"`
	Expected            helperVerification `json:"expected"`
}

type helperCoordinator struct {
	restrict func(string, bool) error
	start    func(string, string) error
}

type helperRunner struct {
	currentExecutable func() (string, error)
	waitParent        func(context.Context, int) error
	verify            func(context.Context, Plan, string, string, string, string) (Verified, error)
	apply             func(context.Context, Verified, string) error
	cleanupStaged     func(Staged) error
	launchCleanup     func(string, int) error
}

// IsHelperInvocation recognizes only the private copied helper executable and
// its hidden dispatch marker. Ordinary tool shims must never intercept this
// argument merely because they share the ContainerBin binary.
func IsHelperInvocation(invoked string, args []string) bool {
	return strings.EqualFold(invoked, strings.TrimSuffix(helperExecutableName, filepath.Ext(helperExecutableName))) &&
		len(args) > 0 && args[0] == "__self-update-helper"
}

// LaunchHelper copies the currently installed management executable into a
// private same-volume helper directory and starts it with a bounded request.
// The caller must already have staged and verified the update. On success the
// helper owns staging cleanup; the invoking process must exit promptly so the
// helper can acquire the installed executable for replacement.
func LaunchHelper(ctx context.Context, plan Plan, staged Staged, verified Verified, installedExecutable, ghExecutable string) error {
	if runtime.GOOS != "windows" {
		return errors.New("self-update helper is supported only on native Windows")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("launch self-update helper: %w", err)
	}
	return (helperCoordinator{
		restrict: restrictStagingPath,
		start:    startSelfUpdateHelper,
	}).launch(plan, staged, verified, installedExecutable, ghExecutable, os.Getpid())
}

func (c helperCoordinator) launch(plan Plan, staged Staged, verified Verified, installedExecutable, ghExecutable string, parentPID int) (err error) {
	if c.restrict == nil || c.start == nil {
		return errors.New("self-update helper launcher is incomplete")
	}
	if parentPID <= 0 {
		return errors.New("self-update helper parent PID is invalid")
	}
	if _, err := validateVerificationPlan(plan); err != nil {
		return err
	}
	installedExecutable, installedInfo, err := canonicalApplyFile(installedExecutable, "installed management executable")
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(installedExecutable), "cb.exe") {
		return fmt.Errorf("installed management executable must be named cb.exe, got %q", filepath.Base(installedExecutable))
	}
	ghExecutable, _, err = canonicalVerificationFile(ghExecutable, "GitHub CLI executable")
	if err != nil {
		return err
	}
	if err := validateHelperStaging(plan, staged, verified, installedExecutable); err != nil {
		return err
	}
	if verified.installedPath != installedExecutable || verified.installedSize != installedInfo.Size() {
		return errors.New("verified installed executable identity does not match helper launch target")
	}
	installedDigest, installedSize, err := hashVerificationFile(installedExecutable, installedInfo)
	if err != nil {
		return err
	}
	if installedDigest != verified.installedDigest || installedSize != verified.installedSize {
		return errors.New("installed management executable changed before helper launch")
	}

	helperDir, err := os.MkdirTemp(filepath.Dir(installedExecutable), helperDirPrefix)
	if err != nil {
		return fmt.Errorf("create private self-update helper directory: %w", err)
	}
	launched := false
	defer func() {
		if !launched {
			if cleanupErr := os.RemoveAll(helperDir); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove failed self-update helper directory: %w", cleanupErr))
			}
		}
	}()
	if err := c.restrict(helperDir, true); err != nil {
		return fmt.Errorf("restrict self-update helper directory: %w", err)
	}
	helperExecutable := filepath.Join(helperDir, helperExecutableName)
	if err := copyApplyFile(installedExecutable, helperExecutable); err != nil {
		return fmt.Errorf("copy self-update helper executable: %w", err)
	}
	if err := c.restrict(helperExecutable, false); err != nil {
		return fmt.Errorf("restrict self-update helper executable: %w", err)
	}
	helperPath, helperInfo, err := canonicalVerificationFile(helperExecutable, "self-update helper executable")
	if err != nil {
		return err
	}
	helperDigest, helperSize, err := hashVerificationFile(helperPath, helperInfo)
	if err != nil {
		return err
	}
	if helperDigest != verified.installedDigest || helperSize != verified.installedSize {
		return errors.New("self-update helper copy does not match the verified installed executable")
	}

	request := helperRequest{
		Version:             helperRequestVersion,
		ParentPID:           parentPID,
		HelperDir:           helperDir,
		HelperExecutable:    helperPath,
		HelperSHA256:        helperDigest,
		InstalledExecutable: installedExecutable,
		GitHubExecutable:    ghExecutable,
		Staged:              staged,
		Plan:                helperPlanFromPlan(plan),
		Expected:            helperVerificationFromVerified(verified),
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode self-update helper request: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxHelperRequestSize {
		return errors.New("self-update helper request exceeds the safety limit")
	}
	requestPath := filepath.Join(helperDir, helperRequestName)
	requestFile, err := os.OpenFile(requestPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create self-update helper request: %w", err)
	}
	_, writeErr := requestFile.Write(data)
	syncErr := requestFile.Sync()
	closeErr := requestFile.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write self-update helper request: %w", err)
	}
	if err := c.restrict(requestPath, false); err != nil {
		return fmt.Errorf("restrict self-update helper request: %w", err)
	}
	if err := c.start(helperPath, requestPath); err != nil {
		return fmt.Errorf("start self-update helper: %w", err)
	}
	launched = true
	return nil
}

func validateHelperStaging(plan Plan, staged Staged, verified Verified, installedExecutable string) error {
	if !staged.owned || staged.Target != plan.Target || staged.Dir == "" {
		return errors.New("self-update helper requires the exact owned staging result")
	}
	dir := filepath.Clean(staged.Dir)
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), stagingPrefix) || !strings.EqualFold(filepath.Dir(dir), filepath.Dir(installedExecutable)) {
		return errors.New("self-update helper staging directory is outside the exact same-volume layout")
	}
	if filepath.Clean(staged.BinaryPath) != filepath.Join(dir, plan.Binary.Name) || filepath.Clean(staged.ChecksumsPath) != filepath.Join(dir, plan.Checksums.Name) {
		return errors.New("self-update helper staging inputs do not match the selected plan")
	}
	verifiedPath := filepath.Clean(verified.binaryPath)
	if !strings.EqualFold(filepath.Dir(verifiedPath), dir) || !strings.EqualFold(filepath.Base(verifiedPath), "cb.exe") {
		return errors.New("verified self-update executable is outside the owned staging directory")
	}
	if verified.target != plan.Target {
		return errors.New("verified self-update target does not match the selected plan")
	}
	return nil
}

func helperPlanFromPlan(plan Plan) helperPlan {
	return helperPlan{
		Current:          plan.Current,
		Target:           plan.Target,
		Channel:          plan.Channel,
		Status:           plan.Status,
		OS:               plan.OS,
		Arch:             plan.Arch,
		ReleaseURL:       plan.ReleaseURL,
		Binary:           plan.Binary,
		Archive:          plan.Archive,
		Checksums:        plan.Checksums,
		ExpectedRepo:     plan.ExpectedRepo,
		ExpectedRef:      plan.ExpectedRef,
		Workflow:         plan.Workflow,
		DowngradeCurrent: plan.downgradeAuthorization.current,
		DowngradeTarget:  plan.downgradeAuthorization.target,
		ChecksumLayout:   uint8(plan.checksumLayout),
	}
}

func (p helperPlan) plan() Plan {
	return Plan{
		Current:                p.Current,
		Target:                 p.Target,
		Channel:                p.Channel,
		Status:                 p.Status,
		OS:                     p.OS,
		Arch:                   p.Arch,
		ReleaseURL:             p.ReleaseURL,
		Binary:                 p.Binary,
		Archive:                p.Archive,
		Checksums:              p.Checksums,
		ExpectedRepo:           p.ExpectedRepo,
		ExpectedRef:            p.ExpectedRef,
		Workflow:               p.Workflow,
		downgradeAuthorization: downgradeAuthorization{current: p.DowngradeCurrent, target: p.DowngradeTarget},
		checksumLayout:         checksumLayout(p.ChecksumLayout),
	}
}

func helperVerificationFromVerified(verified Verified) helperVerification {
	return helperVerification{
		BinaryPath:       verified.binaryPath,
		Target:           verified.target,
		Digest:           verified.digest,
		Size:             verified.size,
		InstalledPath:    verified.installedPath,
		InstalledVersion: verified.installedVersion,
		InstalledDigest:  verified.installedDigest,
		InstalledSize:    verified.installedSize,
	}
}

func (v helperVerification) matches(verified Verified) bool {
	return v == helperVerificationFromVerified(verified)
}

// RunHelper executes the hidden child-process half of self-update. It is not a
// public command: the request must prove the exact private helper/staging layout
// and is re-verified after the invoking process exits before any mutation.
func RunHelper(ctx context.Context, args []string, out io.Writer) error {
	if runtime.GOOS != "windows" {
		return errors.New("self-update helper is supported only on native Windows")
	}
	requestPath, err := parseHelperArgs(args)
	if err != nil {
		return err
	}
	return (helperRunner{
		currentExecutable: os.Executable,
		waitParent:        waitForParentExit,
		verify:            Verify,
		apply:             ApplyVerified,
		cleanupStaged:     func(staged Staged) error { return staged.Cleanup() },
		launchCleanup:     launchHelperCleanup,
	}).run(ctx, requestPath, out)
}

func parseHelperArgs(args []string) (string, error) {
	if len(args) != 2 || args[0] != "--request" || args[1] == "" {
		return "", errors.New("invalid internal self-update helper invocation")
	}
	if !filepath.IsAbs(args[1]) || filepath.Clean(args[1]) != args[1] || filepath.Base(args[1]) != helperRequestName {
		return "", errors.New("self-update helper request path must be the exact clean absolute request filename")
	}
	return args[1], nil
}

func (r helperRunner) run(ctx context.Context, requestPath string, out io.Writer) (err error) {
	if r.currentExecutable == nil || r.waitParent == nil || r.verify == nil || r.apply == nil || r.cleanupStaged == nil || r.launchCleanup == nil {
		return errors.New("self-update helper runtime is incomplete")
	}
	request, err := loadHelperRequest(requestPath, r.currentExecutable)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := r.launchCleanup(request.HelperDir, os.Getpid()); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("launch self-update helper cleanup: %w", cleanupErr))
		}
	}()
	defer func() {
		if cleanupErr := r.cleanupStaged(request.Staged); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	waitCtx, cancelWait := context.WithTimeout(ctx, helperParentExitTimeout)
	defer cancelWait()
	if err := r.waitParent(waitCtx, request.ParentPID); err != nil {
		return fmt.Errorf("wait for invoking ContainerBin process: %w", err)
	}
	plan := request.Plan.plan()
	verified, err := r.verify(ctx, plan, request.Staged.BinaryPath, request.Staged.ChecksumsPath, request.InstalledExecutable, request.GitHubExecutable)
	if err != nil {
		return fmt.Errorf("re-verify staged self-update in helper: %w", err)
	}
	if !request.Expected.matches(verified) {
		return errors.New("self-update helper re-verification does not match the parent-bound result")
	}
	if err := r.apply(ctx, verified, request.InstalledExecutable); err != nil {
		return err
	}
	if out != nil {
		fmt.Fprintf(out, "self-update applied %s; managed executable and shims verified\n", verified.target)
	}
	return nil
}

func loadHelperRequest(requestPath string, currentExecutable func() (string, error)) (helperRequest, error) {
	requestPath, requestInfo, err := canonicalVerificationFile(requestPath, "self-update helper request")
	if err != nil {
		return helperRequest{}, err
	}
	if requestInfo.Size() <= 0 || requestInfo.Size() > maxHelperRequestSize {
		return helperRequest{}, errors.New("self-update helper request size is outside the safety limit")
	}
	data, err := readExactVerificationFile(requestPath, requestInfo)
	if err != nil {
		return helperRequest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request helperRequest
	if err := decoder.Decode(&request); err != nil {
		return helperRequest{}, fmt.Errorf("parse self-update helper request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return helperRequest{}, errors.New("self-update helper request contains trailing data")
	}
	if request.Version != helperRequestVersion || request.ParentPID <= 0 {
		return helperRequest{}, errors.New("self-update helper request has an unsupported version or invalid parent PID")
	}
	helperDir := filepath.Clean(request.HelperDir)
	if !filepath.IsAbs(helperDir) || !strings.HasPrefix(filepath.Base(helperDir), helperDirPrefix) || filepath.Clean(requestPath) != filepath.Join(helperDir, helperRequestName) {
		return helperRequest{}, errors.New("self-update helper request is outside the exact private helper layout")
	}
	installedPath := filepath.Clean(request.InstalledExecutable)
	if !filepath.IsAbs(installedPath) || !strings.EqualFold(filepath.Dir(helperDir), filepath.Dir(installedPath)) ||
		!strings.EqualFold(filepath.VolumeName(helperDir), filepath.VolumeName(installedPath)) {
		return helperRequest{}, errors.New("self-update helper directory is outside the installed executable directory")
	}
	if filepath.Clean(request.HelperExecutable) != filepath.Join(helperDir, helperExecutableName) {
		return helperRequest{}, errors.New("self-update helper executable is outside the exact private helper layout")
	}
	currentPath, err := currentExecutable()
	if err != nil {
		return helperRequest{}, fmt.Errorf("resolve running self-update helper: %w", err)
	}
	currentPath, currentInfo, err := canonicalVerificationFile(currentPath, "running self-update helper")
	if err != nil {
		return helperRequest{}, err
	}
	requestHelper, requestHelperInfo, err := canonicalVerificationFile(request.HelperExecutable, "requested self-update helper")
	if err != nil {
		return helperRequest{}, err
	}
	if !os.SameFile(currentInfo, requestHelperInfo) || !strings.EqualFold(currentPath, requestHelper) {
		return helperRequest{}, errors.New("running executable is not the requested private self-update helper")
	}
	helperDigest, _, err := hashVerificationFile(requestHelper, requestHelperInfo)
	if err != nil {
		return helperRequest{}, err
	}
	if helperDigest != request.HelperSHA256 || helperDigest != request.Expected.InstalledDigest {
		return helperRequest{}, errors.New("self-update helper digest does not match the parent-bound installed executable")
	}
	if request.InstalledExecutable != request.Expected.InstalledPath {
		return helperRequest{}, errors.New("self-update helper installed path does not match the parent-bound result")
	}
	plan := request.Plan.plan()
	if _, err := validateVerificationPlan(plan); err != nil {
		return helperRequest{}, err
	}
	request.Staged.owned = true
	if err := validateHelperStaging(plan, request.Staged, Verified{
		binaryPath:       request.Expected.BinaryPath,
		target:           request.Expected.Target,
		digest:           request.Expected.Digest,
		size:             request.Expected.Size,
		installedPath:    request.Expected.InstalledPath,
		installedVersion: request.Expected.InstalledVersion,
		installedDigest:  request.Expected.InstalledDigest,
		installedSize:    request.Expected.InstalledSize,
	}, request.InstalledExecutable); err != nil {
		return helperRequest{}, err
	}
	return request, nil
}

func startSelfUpdateHelper(helperExecutable, requestPath string) error {
	env, err := attestationEnvironment()
	if err != nil {
		return err
	}
	cmd := exec.Command(helperExecutable, "__self-update-helper", "--request", requestPath)
	cmd.Dir = filepath.Dir(helperExecutable)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func helperPIDString(pid int) string { return strconv.Itoa(pid) }
