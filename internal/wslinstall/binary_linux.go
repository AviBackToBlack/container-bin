//go:build linux

package wslinstall

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
)

const maxBootstrapBinarySize = 64 << 20

type sourceBinary struct {
	file *os.File
	info os.FileInfo
	sum  [sha256.Size]byte
}

// BinaryState compares the running bootstrap executable with the fixed managed
// binary after validating both identities.
func BinaryState(layout hostenv.WSLLayout, source string) (State, error) {
	opened, err := openSourceBinary(source, layout.UID)
	if err != nil {
		return "", err
	}
	defer opened.file.Close()
	target, err := openManagedBinary(layout.BinaryPath, layout.UID)
	if errors.Is(err, fs.ErrNotExist) {
		return Create, nil
	}
	if err != nil {
		return "", err
	}
	defer target.file.Close()
	if os.SameFile(opened.info, target.info) || opened.sum == target.sum {
		return Ready, nil
	}
	return Update, nil
}

// InstallBinary atomically publishes the current executable at the fixed
// managed path. The directory and any existing target are validated by wslfs
// immediately before mutation; the private 0700 parent and outer mutation lock
// exclude unrelated writers from the replacement boundary.
func InstallBinary(layout hostenv.WSLLayout, source string) error {
	plan, err := wslfs.Check(layout)
	if err != nil {
		return fmt.Errorf("validate native WSL layout before binary mutation: %w", err)
	}
	if len(plan.MissingDirectories) != 0 {
		return errors.New("native WSL layout requires preparation before binary mutation")
	}
	return installBinaryFile(layout, source)
}

func installBinaryFile(layout hostenv.WSLLayout, source string) error {
	opened, err := openSourceBinary(source, layout.UID)
	if err != nil {
		return err
	}
	defer opened.file.Close()
	target, err := openManagedBinary(layout.BinaryPath, layout.UID)
	if err == nil {
		defer target.file.Close()
		if os.SameFile(opened.info, target.info) || opened.sum == target.sum {
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	directory := filepath.Dir(layout.BinaryPath)
	temporary, err := os.CreateTemp(directory, ".cb-install-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary native WSL managed binary: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	copyErr := stageBinary(temporary, opened)
	closeErr := temporary.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("write temporary native WSL managed binary: %w", err)
	}
	postSource, err := opened.file.Stat()
	if err != nil {
		return fmt.Errorf("reinspect native WSL bootstrap executable: %w", err)
	}
	if !os.SameFile(opened.info, postSource) || postSource.Size() != opened.info.Size() || !postSource.ModTime().Equal(opened.info.ModTime()) {
		return errors.New("native WSL bootstrap executable changed while being installed")
	}
	current, err := openManagedBinary(layout.BinaryPath, layout.UID)
	if err == nil {
		if closeErr := current.file.Close(); closeErr != nil {
			return fmt.Errorf("close existing native WSL managed binary before replacement: %w", closeErr)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("revalidate existing native WSL managed binary before replacement: %w", err)
	}
	if err := os.Rename(temporaryPath, layout.BinaryPath); err != nil {
		return fmt.Errorf("publish native WSL managed binary: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open native WSL binary directory after replacement: %w", err)
	}
	syncErr := dir.Sync()
	closeErr = dir.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync native WSL binary directory after replacement: %w", err)
	}
	installed, err := openManagedBinary(layout.BinaryPath, layout.UID)
	if err != nil {
		return fmt.Errorf("revalidate native WSL managed binary after replacement: %w", err)
	}
	defer installed.file.Close()
	if installed.sum != opened.sum {
		return errors.New("native WSL managed binary digest differs from bootstrap executable after replacement")
	}
	return nil
}

func stageBinary(temporary *os.File, source sourceBinary) error {
	if _, err := source.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind native WSL bootstrap executable: %w", err)
	}
	copiedHash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, copiedHash), io.LimitReader(source.file, maxBootstrapBinarySize+1))
	if err != nil {
		return err
	}
	if written != source.info.Size() {
		return fmt.Errorf("bootstrap executable changed size while copying: copied %d bytes, expected %d", written, source.info.Size())
	}
	var copiedSum [sha256.Size]byte
	copy(copiedSum[:], copiedHash.Sum(nil))
	if copiedSum != source.sum {
		return errors.New("native WSL bootstrap executable bytes changed while being copied")
	}
	if err := temporary.Chmod(0o755); err != nil {
		return err
	}
	return temporary.Sync()
}

func openSourceBinary(path string, uid uint32) (sourceBinary, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || clean != path {
		return sourceBinary{}, fmt.Errorf("native WSL bootstrap executable path %q must be canonical and absolute", path)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return sourceBinary{}, fmt.Errorf("inspect native WSL bootstrap executable %s: %w", path, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return sourceBinary{}, fmt.Errorf("native WSL bootstrap executable %s must be a regular non-symlink file", path)
	}
	mode, owner, err := linuxIdentity(before)
	if err != nil {
		return sourceBinary{}, fmt.Errorf("inspect native WSL bootstrap executable %s: %w", path, err)
	}
	if owner != uid || mode&0o100 == 0 || mode&0o7022 != 0 {
		return sourceBinary{}, fmt.Errorf("native WSL bootstrap executable %s must be UID %d, owner-executable, free of special bits and not writable by group or other", path, uid)
	}
	return openAndHash(path, before, maxBootstrapBinarySize, "native WSL bootstrap executable")
}

func openManagedBinary(path string, uid uint32) (sourceBinary, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return sourceBinary{}, err
	}
	if !before.Mode().IsRegular() {
		return sourceBinary{}, fmt.Errorf("native WSL managed binary %s must be a regular non-symlink file", path)
	}
	mode, owner, err := linuxIdentity(before)
	if err != nil {
		return sourceBinary{}, fmt.Errorf("inspect native WSL managed binary %s: %w", path, err)
	}
	if owner != uid || mode != 0o755 {
		return sourceBinary{}, fmt.Errorf("native WSL managed binary %s must be a UID %d regular file with mode 0755", path, uid)
	}
	return openAndHash(path, before, maxBootstrapBinarySize, "native WSL managed binary")
}

func openAndHash(path string, before os.FileInfo, limit int64, kind string) (sourceBinary, error) {
	if before.Size() <= 0 || before.Size() > limit {
		return sourceBinary{}, fmt.Errorf("%s %s has invalid size %d (maximum %d)", kind, path, before.Size(), limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return sourceBinary{}, fmt.Errorf("open %s %s: %w", kind, path, err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		if err != nil {
			return sourceBinary{}, fmt.Errorf("inspect opened %s %s: %w", kind, path, err)
		}
		return sourceBinary{}, fmt.Errorf("%s %s changed before it was opened", kind, path)
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(file, limit+1))
	if readErr != nil || n != before.Size() {
		_ = file.Close()
		if readErr != nil {
			return sourceBinary{}, fmt.Errorf("hash %s %s: %w", kind, path, readErr)
		}
		return sourceBinary{}, fmt.Errorf("%s %s changed size while hashing", kind, path)
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sourceBinary{file: file, info: opened, sum: sum}, nil
}

func linuxIdentity(info os.FileInfo) (os.FileMode, uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("Linux ownership or mode is unavailable")
	}
	return os.FileMode(stat.Mode & 0o7777), stat.Uid, nil
}
