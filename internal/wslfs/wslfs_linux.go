//go:build linux

// Package wslfs enforces the native WSL filesystem layout before the frontend
// is enabled. It deliberately does not infer or repair ownership.
package wslfs

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	privateDirMode  = 0o700
	shimDirMode     = 0o755
	privateFileMode = 0o600
	binaryFileMode  = 0o755
)

// Prepare validates the fixed native-WSL layout and creates only missing
// ContainerBin-owned directories. Existing paths are never chmodded, chowned or
// replaced: ambiguous ownership, symlinks and unsafe permissions fail closed.
// The WSL frontend remains gated; later registry/install wiring will call this
// before creating files or shims.
func Prepare(layout hostenv.WSLLayout) (err error) {
	if err := validateLayout(layout); err != nil {
		return err
	}
	uid := uint32(os.Getuid())
	if layout.UID != uid {
		return fmt.Errorf("native WSL layout UID %d does not match current user UID %d", layout.UID, uid)
	}

	homeInfo, err := inspectDirectory(layout.Home, uid, false)
	if err != nil {
		return fmt.Errorf("validate native WSL home: %w", err)
	}
	resolvedHome, err := filepath.EvalSymlinks(layout.Home)
	if err != nil {
		return fmt.Errorf("resolve native WSL home: %w", err)
	}
	if resolvedHome != layout.Home {
		return fmt.Errorf("native WSL home %q resolves to %q; symlinked homes are not accepted", layout.Home, resolvedHome)
	}
	rootInfo, err := os.Stat(string(filepath.Separator))
	if err != nil {
		return fmt.Errorf("inspect distribution root filesystem: %w", err)
	}
	homeDevice, err := filesystemDevice(homeInfo)
	if err != nil {
		return fmt.Errorf("inspect native WSL home filesystem: %w", err)
	}
	rootDevice, err := filesystemDevice(rootInfo)
	if err != nil {
		return fmt.Errorf("inspect distribution root filesystem: %w", err)
	}
	if homeDevice != rootDevice {
		return fmt.Errorf("native WSL home %q is on filesystem device %d, not distribution root device %d", layout.Home, homeDevice, rootDevice)
	}

	created := make([]string, 0, 8)
	defer func() {
		if err == nil {
			return
		}
		for i := len(created) - 1; i >= 0; i-- {
			if removeErr := os.Remove(created[i]); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove newly created directory %s after failure: %w", created[i], removeErr))
			}
		}
	}()

	directories := []struct {
		path       string
		createMode os.FileMode
		private    bool
	}{
		{filepath.Join(layout.Home, ".config"), privateDirMode, false},
		{layout.ConfigDir, privateDirMode, true},
		{filepath.Join(layout.Home, ".local"), privateDirMode, false},
		{filepath.Join(layout.Home, ".local", "state"), privateDirMode, false},
		{layout.StateDir, privateDirMode, true},
		{filepath.Join(layout.Home, ".local", "lib"), privateDirMode, false},
		{filepath.Dir(layout.BinaryPath), privateDirMode, true},
		{layout.ShimDir, shimDirMode, false},
	}
	for _, directory := range directories {
		made, makeErr := ensureDirectory(directory.path, uid, directory.createMode, directory.private)
		if made {
			created = append(created, directory.path)
		}
		if makeErr != nil {
			return makeErr
		}
	}

	for _, file := range []struct {
		path string
		mode os.FileMode
		name string
	}{
		{layout.RegistryPath, privateFileMode, "registry"},
		{layout.LockPath, privateFileMode, "lockfile"},
		{layout.BinaryPath, binaryFileMode, "managed binary"},
	} {
		if err := validateManagedFile(file.path, uid, file.mode, file.name); err != nil {
			return err
		}
	}
	if err := validateManagementShim(layout, uid); err != nil {
		return err
	}
	return nil
}

func validateLayout(layout hostenv.WSLLayout) error {
	home := layout.Home
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == string(filepath.Separator) || strings.ContainsRune(home, '\\') {
		return errors.New("native WSL filesystem layout has an invalid home path")
	}
	expected := map[string]string{
		"managed binary":   layout.BinaryPath,
		"management shim":  layout.ManagementShim,
		"shim directory":   layout.ShimDir,
		"config directory": layout.ConfigDir,
		"registry":         layout.RegistryPath,
		"lockfile":         layout.LockPath,
		"state directory":  layout.StateDir,
	}
	want := map[string]string{
		"managed binary":   filepath.Join(home, ".local", "lib", "container-bin", "cb"),
		"management shim":  filepath.Join(home, ".local", "bin", "cb"),
		"shim directory":   filepath.Join(home, ".local", "bin"),
		"config directory": filepath.Join(home, ".config", "container-bin"),
		"registry":         filepath.Join(home, ".config", "container-bin", "container-bin.toml"),
		"lockfile":         filepath.Join(home, ".config", "container-bin", "container-bin.lock"),
		"state directory":  filepath.Join(home, ".local", "state", "container-bin"),
	}
	for name, value := range expected {
		if value != want[name] {
			return fmt.Errorf("native WSL %s path %q does not match fixed layout %q", name, value, want[name])
		}
	}
	if !strings.HasPrefix(layout.StateNamespace, "wsl2-") || len(layout.StateNamespace) != len("wsl2-")+32 {
		return errors.New("native WSL state namespace is not canonical")
	}
	namespaceDigest := strings.TrimPrefix(layout.StateNamespace, "wsl2-")
	if namespaceDigest != strings.ToLower(namespaceDigest) {
		return errors.New("native WSL state namespace is not canonical")
	}
	if _, err := hex.DecodeString(namespaceDigest); err != nil {
		return errors.New("native WSL state namespace is not canonical")
	}
	return nil
}

func ensureDirectory(path string, uid uint32, createMode os.FileMode, private bool) (bool, error) {
	created := false
	if err := os.Mkdir(path, createMode); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return false, fmt.Errorf("create native WSL directory %s: %w", path, err)
	}
	if _, err := inspectDirectory(path, uid, private); err != nil {
		return created, fmt.Errorf("validate native WSL directory %s: %w", path, err)
	}
	return created, nil
}

func inspectDirectory(path string, uid uint32, private bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("must be a real directory, not a symlink or other object")
	}
	if err := requireOwner(info, uid); err != nil {
		return nil, err
	}
	perm := info.Mode().Perm()
	if private {
		if perm != privateDirMode {
			return nil, fmt.Errorf("must have mode 0700, got %04o", perm)
		}
	} else if perm&0o700 != 0o700 || perm&0o022 != 0 {
		return nil, fmt.Errorf("must be owner-accessible and not writable by group or other, got %04o", perm)
	}
	return info, nil
}

func validateManagedFile(path string, uid uint32, wantMode os.FileMode, name string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect native WSL %s %s: %w", name, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("native WSL %s %s must be a regular non-symlink file", name, path)
	}
	if err := requireOwner(info, uid); err != nil {
		return fmt.Errorf("native WSL %s %s: %w", name, path, err)
	}
	if info.Mode().Perm() != wantMode {
		return fmt.Errorf("native WSL %s %s must have mode %04o, got %04o", name, path, wantMode, info.Mode().Perm())
	}
	return nil
}

func validateManagementShim(layout hostenv.WSLLayout, uid uint32) error {
	info, err := os.Lstat(layout.ManagementShim)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect native WSL management shim %s: %w", layout.ManagementShim, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("native WSL management shim %s collides with a non-symlink object", layout.ManagementShim)
	}
	if err := requireOwner(info, uid); err != nil {
		return fmt.Errorf("native WSL management shim %s: %w", layout.ManagementShim, err)
	}
	target, err := os.Readlink(layout.ManagementShim)
	if err != nil {
		return fmt.Errorf("read native WSL management shim %s: %w", layout.ManagementShim, err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(layout.ManagementShim), target)
	}
	if filepath.Clean(target) != layout.BinaryPath {
		return fmt.Errorf("native WSL management shim %s targets %q instead of managed binary %q", layout.ManagementShim, target, layout.BinaryPath)
	}
	return nil
}

func requireOwner(info os.FileInfo, uid uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("owner could not be determined")
	}
	if stat.Uid != uid {
		return fmt.Errorf("is owned by UID %d, expected current user UID %d", stat.Uid, uid)
	}
	return nil
}

func filesystemDevice(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem device could not be determined")
	}
	return uint64(stat.Dev), nil
}
