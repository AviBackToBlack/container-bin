package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

const (
	rollbackPrefix = ".container-bin-update-rollback-"
	applyTimeout   = 30 * time.Second
)

type applyTransaction struct {
	replace func(string, string, bool) error
	smoke   func(context.Context, string, string) error
}

// ApplyVerified replaces an installed Windows management executable with an
// opaque, previously verified release artifact. It is intended to run only in
// the self-update helper after the invoking ContainerBin process has exited.
// The helper/waiting command surface is a separate slice; exposing this
// transaction does not make ordinary self-update mutate installed files.
func ApplyVerified(ctx context.Context, verified Verified, installedExecutable string) error {
	if runtime.GOOS != "windows" {
		return errors.New("self-update replacement is supported only on native Windows")
	}
	installedExecutable, _, err := canonicalApplyFile(installedExecutable, "installed management executable")
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(installedExecutable), "cb.exe") {
		return fmt.Errorf("installed management executable must be named cb.exe, got %q", filepath.Base(installedExecutable))
	}
	unlock, err := mutationlock.Acquire(filepath.Join(filepath.Dir(installedExecutable), "container-bin.toml"), mutationlock.Wait)
	if err != nil {
		return fmt.Errorf("serialize self-update replacement: %w", err)
	}
	defer unlock()
	return (applyTransaction{replace: replaceManagedFile, smoke: smokeUpdatedBinary}).apply(ctx, verified, installedExecutable)
}

func (tx applyTransaction) apply(ctx context.Context, verified Verified, installedExecutable string) (err error) {
	if tx.replace == nil || tx.smoke == nil {
		return errors.New("self-update replacement transaction is incomplete")
	}
	installedExecutable, installedInfo, err := canonicalApplyFile(installedExecutable, "installed management executable")
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(installedExecutable), "cb.exe") {
		return fmt.Errorf("installed management executable must be named cb.exe, got %q", filepath.Base(installedExecutable))
	}
	target, err := parseVersion(verified.target)
	if err != nil {
		return fmt.Errorf("verified self-update target: %w", err)
	}
	staged, stagedInfo, err := canonicalApplyFile(verified.binaryPath, "verified staged executable")
	if err != nil {
		return err
	}
	installDir := filepath.Dir(installedExecutable)
	stageDir := filepath.Dir(staged)
	if !strings.EqualFold(filepath.Base(staged), "cb.exe") || !strings.EqualFold(filepath.Dir(stageDir), installDir) || !strings.HasPrefix(filepath.Base(stageDir), stagingPrefix) {
		return errors.New("verified executable is outside the exact private same-volume staging layout")
	}
	if !strings.EqualFold(filepath.VolumeName(staged), filepath.VolumeName(installedExecutable)) {
		return errors.New("verified executable is not on the installed executable volume")
	}
	stagedDigest, stagedSize, err := hashVerificationFile(staged, stagedInfo)
	if err != nil {
		return fmt.Errorf("revalidate verified staged executable: %w", err)
	}
	if stagedDigest != verified.digest || stagedSize != verified.size {
		return errors.New("verified staged executable changed before replacement")
	}
	oldDigest, _, err := hashVerificationFile(installedExecutable, installedInfo)
	if err != nil {
		return fmt.Errorf("hash installed management executable: %w", err)
	}
	if installedInfo.Size() <= 0 || installedInfo.Size() > maxBinarySize {
		return fmt.Errorf("installed management executable size %d is outside the self-update safety limit", installedInfo.Size())
	}
	if !strings.EqualFold(installedExecutable, verified.installedPath) ||
		installedInfo.Size() != verified.installedSize ||
		oldDigest != verified.installedDigest {
		return errors.New("installed management executable changed after update verification")
	}
	current, err := currentVersion(verified.installedVersion)
	if err != nil {
		return fmt.Errorf("verified installed version: %w", err)
	}
	if current.compare(target) == 0 {
		return errors.New("verified self-update target is already installed")
	}
	if oldDigest == stagedDigest {
		return errors.New("verified update bytes are identical to the installed management executable")
	}
	shims, err := discoverManagedShims(installDir, installedExecutable, installedInfo, oldDigest)
	if err != nil {
		return err
	}

	rollbackDir, err := os.MkdirTemp(installDir, rollbackPrefix)
	if err != nil {
		return fmt.Errorf("create self-update rollback directory: %w", err)
	}
	keepRollback := false
	defer func() {
		if !keepRollback {
			if cleanupErr := os.RemoveAll(rollbackDir); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove self-update rollback directory: %w", cleanupErr))
			}
		}
	}()
	if err := restrictStagingPath(rollbackDir, true); err != nil {
		return fmt.Errorf("restrict self-update rollback directory: %w", err)
	}
	rollbackBinary := filepath.Join(rollbackDir, "cb.exe")
	if err := copyApplyFile(installedExecutable, rollbackBinary); err != nil {
		return fmt.Errorf("create self-update rollback executable: %w", err)
	}
	if err := restrictStagingPath(rollbackBinary, false); err != nil {
		return fmt.Errorf("restrict self-update rollback executable: %w", err)
	}
	rollbackInfo, err := os.Lstat(rollbackBinary)
	if err != nil {
		return fmt.Errorf("inspect self-update rollback executable: %w", err)
	}
	rollbackDigest, _, err := hashVerificationFile(rollbackBinary, rollbackInfo)
	if err != nil || rollbackDigest != oldDigest {
		return errors.New("self-update rollback executable does not match installed bytes")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("self-update canceled before installed-file mutation: %w", err)
	}

	changed := false
	fail := func(cause error) error {
		if !changed {
			return cause
		}
		rollbackErr := tx.rollback(installedExecutable, rollbackBinary, shims, oldDigest, stagedDigest)
		if rollbackErr != nil {
			keepRollback = true
			return errors.Join(cause, fmt.Errorf("self-update rollback failed; recovery executable preserved at %s: %w", rollbackBinary, rollbackErr))
		}
		return errors.Join(cause, errors.New("installed ContainerBin files were restored to their previous bytes"))
	}

	if err := requireApplyDigest(installedExecutable, oldDigest, "management executable immediately before replacement"); err != nil {
		return err
	}
	if replaceErr := tx.replace(staged, installedExecutable, false); replaceErr != nil {
		digest, digestErr := applyFileDigest(installedExecutable)
		switch {
		case digestErr == nil && digest == oldDigest:
			return fmt.Errorf("replace installed management executable: %w", replaceErr)
		case digestErr == nil && digest == stagedDigest:
			changed = true
			return fail(fmt.Errorf("replace installed management executable reported failure after changing bytes: %w", replaceErr))
		default:
			// The replacement call may have changed the destination, but its state
			// cannot be proved. Enter rollback so an unverifiable restoration keeps
			// the private recovery executable instead of deleting it.
			changed = true
			cause := fmt.Errorf("replace installed management executable left an inconclusive destination state: %w", replaceErr)
			if digestErr != nil {
				cause = errors.Join(cause, fmt.Errorf("inspect installed management executable after reported replacement failure: %w", digestErr))
			} else {
				cause = errors.Join(cause, errors.New("installed management executable contains neither the previous nor verified update bytes"))
			}
			return fail(cause)
		}
	}
	changed = true
	if err := requireApplyDigest(installedExecutable, stagedDigest, "updated management executable"); err != nil {
		return fail(err)
	}
	for _, shim := range shims {
		if err := requireApplyDigest(shim, oldDigest, "managed shim before replacement"); err != nil {
			return fail(err)
		}
		if err := tx.replace(installedExecutable, shim, true); err != nil {
			return fail(fmt.Errorf("replace managed shim %s: %w", shim, err))
		}
	}
	if err := tx.smoke(ctx, installedExecutable, target.raw); err != nil {
		return fail(fmt.Errorf("updated management executable smoke test: %w", err))
	}
	if err := requireApplyDigest(installedExecutable, stagedDigest, "updated management executable after smoke test"); err != nil {
		return fail(err)
	}
	for _, shim := range shims {
		if err := requireApplyDigest(shim, stagedDigest, "managed shim after replacement"); err != nil {
			return fail(err)
		}
	}
	return nil
}

func (tx applyTransaction) rollback(installedExecutable, rollbackBinary string, shims []string, oldDigest, newDigest string) error {
	var result error
	managementRestored := false
	if digest, err := applyFileDigest(installedExecutable); err != nil {
		result = errors.Join(result, err)
	} else if digest == newDigest {
		if replaceErr := tx.replace(rollbackBinary, installedExecutable, false); replaceErr != nil {
			result = errors.Join(result, fmt.Errorf("restore management executable: %w", replaceErr))
		}
		restoredDigest, verifyErr := applyFileDigest(installedExecutable)
		if verifyErr != nil {
			result = errors.Join(result, fmt.Errorf("verify restored management executable: %w", verifyErr))
		} else if restoredDigest != oldDigest {
			result = errors.Join(result, errors.New("restored management executable digest changed"))
		} else {
			managementRestored = true
		}
	} else if digest == oldDigest {
		managementRestored = true
	} else {
		result = errors.Join(result, errors.New("management executable changed outside the update transaction; refusing to overwrite it during rollback"))
	}
	for _, shim := range shims {
		digest, err := applyFileDigest(shim)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if digest == oldDigest {
			continue
		}
		if digest != newDigest {
			result = errors.Join(result, fmt.Errorf("managed shim %s changed outside the update transaction; refusing to overwrite it during rollback", shim))
			continue
		}
		// Prefer the verified-restored management executable so the shim regains
		// normal installation ACLs and hardlink identity. If that executable could
		// not be proved restored, defensively copy the private recovery bytes.
		source := rollbackBinary
		preferHardlink := false
		if managementRestored {
			source = installedExecutable
			preferHardlink = true
		}
		if err := tx.replace(source, shim, preferHardlink); err != nil {
			result = errors.Join(result, fmt.Errorf("restore managed shim %s: %w", shim, err))
		}
	}
	if result == nil {
		if err := requireApplyDigest(installedExecutable, oldDigest, "restored management executable"); err != nil {
			result = errors.Join(result, err)
		}
		for _, shim := range shims {
			if err := requireApplyDigest(shim, oldDigest, "restored managed shim"); err != nil {
				result = errors.Join(result, err)
			}
		}
	}
	return result
}

func discoverManagedShims(dir, installedExecutable string, installedInfo os.FileInfo, oldDigest string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("enumerate installed shims: %w", err)
	}
	var shims []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".exe") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if strings.EqualFold(path, installedExecutable) {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(entry.Name()), strings.ToLower(filepath.Ext(entry.Name())))
		if !registry.ValidToolName(name) || registry.ReservedToolName(name) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect candidate managed shim %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		if os.SameFile(installedInfo, info) {
			shims = append(shims, path)
			continue
		}
		if info.Size() != installedInfo.Size() {
			continue
		}
		digest, _, err := hashVerificationFile(path, info)
		if err != nil {
			return nil, fmt.Errorf("hash candidate managed shim %s: %w", path, err)
		}
		if digest == oldDigest {
			shims = append(shims, path)
		}
	}
	sort.Slice(shims, func(i, j int) bool { return strings.ToLower(shims[i]) < strings.ToLower(shims[j]) })
	return shims, nil
}

func canonicalApplyFile(path, label string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, fmt.Errorf("%s path must be clean and absolute", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s must be a regular non-symlink file", label)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", label, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("make resolved %s absolute: %w", label, err)
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("inspect resolved %s: %w", label, err)
	}
	if !resolvedInfo.Mode().IsRegular() || !os.SameFile(info, resolvedInfo) {
		return "", nil, fmt.Errorf("%s changed while resolving its canonical path", label)
	}
	return resolved, resolvedInfo, nil
}

func applyFileDigest(path string) (string, error) {
	clean, info, err := canonicalApplyFile(path, "managed self-update file")
	if err != nil {
		return "", err
	}
	digest, _, err := hashVerificationFile(clean, info)
	return digest, err
}

func requireApplyDigest(path, want, label string) error {
	digest, err := applyFileDigest(path)
	if err != nil {
		return fmt.Errorf("validate %s: %w", label, err)
	}
	if digest != want {
		return fmt.Errorf("%s digest changed", label)
	}
	return nil
}

func replaceManagedFile(source, destination string, preferHardlink bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+"-update-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	defer os.Remove(tmpPath)
	if preferHardlink {
		if err := os.Link(source, tmpPath); err == nil {
			return replaceExistingFile(tmpPath, destination)
		}
	}
	if err := copyApplyFile(source, tmpPath); err != nil {
		return err
	}
	return replaceExistingFile(tmpPath, destination)
}

func copyApplyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := out.ReadFrom(in)
	syncErr := out.Sync()
	closeErr := out.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

func smokeUpdatedBinary(ctx context.Context, executable, target string) error {
	smokeCtx, cancel := context.WithTimeout(ctx, applyTimeout)
	defer cancel()
	cmd := exec.CommandContext(smokeCtx, executable, "version")
	cmd.Dir = filepath.Dir(executable)
	cmd.Env = []string{}
	output, err := cmd.Output()
	if errors.Is(smokeCtx.Err(), context.DeadlineExceeded) {
		return errors.New("bootstrap version command timed out")
	}
	if err != nil {
		return err
	}
	want := "container-bin " + target + "\n"
	if string(output) != want {
		return fmt.Errorf("bootstrap version output %q does not match %q", output, want)
	}
	return nil
}
