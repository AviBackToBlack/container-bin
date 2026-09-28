//go:build linux

// Package wslfs enforces the native WSL filesystem layout before the frontend
// is enabled. It deliberately does not infer or repair ownership.
package wslfs

import (
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
func Prepare(layout hostenv.WSLLayout) error {
	runtime, err := hostenv.Current()
	if err != nil {
		return fmt.Errorf("classify native WSL runtime: %w", err)
	}
	machineID, err := readMachineIDFile("/etc/machine-id")
	if err != nil {
		return fmt.Errorf("read native WSL machine identity: %w", err)
	}
	return prepare(layout, runtime, machineID)
}

// Check validates the fixed native-WSL layout without changing it and reports
// the directories a subsequent Prepare call would create.
func Check(layout hostenv.WSLLayout) (Plan, error) {
	runtime, err := hostenv.Current()
	if err != nil {
		return Plan{}, fmt.Errorf("classify native WSL runtime: %w", err)
	}
	machineID, err := readMachineIDFile("/etc/machine-id")
	if err != nil {
		return Plan{}, fmt.Errorf("read native WSL machine identity: %w", err)
	}
	return check(layout, runtime, machineID)
}

// CheckRegistryRecovery validates the optional backup that registry.LoadAt
// would promote when the fixed registry is missing. A parse-valid backup is
// not sufficient: it must retain the same private WSL file identity.
func CheckRegistryRecovery(layout hostenv.WSLLayout) error {
	runtime, err := hostenv.Current()
	if err != nil {
		return fmt.Errorf("classify native WSL runtime: %w", err)
	}
	machineID, err := readMachineIDFile("/etc/machine-id")
	if err != nil {
		return fmt.Errorf("read native WSL machine identity: %w", err)
	}
	return checkRegistryRecovery(layout, runtime, machineID)
}

func checkRegistryRecovery(layout hostenv.WSLLayout, runtime hostenv.Runtime, machineID string) error {
	uid, rootDevice, err := validatePreflight(layout, runtime, machineID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(layout.RegistryPath); err == nil {
		return validateManagedFile(layout.RegistryPath, uid, rootDevice, privateFileMode, "registry")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect native WSL registry %s before recovery: %w", layout.RegistryPath, err)
	}
	return validateManagedFile(layout.RegistryPath+".bak", uid, rootDevice, privateFileMode, "registry backup")
}

func prepare(layout hostenv.WSLLayout, runtime hostenv.Runtime, machineID string) (err error) {
	uid, rootDevice, err := validatePreflight(layout, runtime, machineID)
	if err != nil {
		return err
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

	for _, directory := range layoutDirectories(layout) {
		made, makeErr := ensureDirectory(directory.path, uid, rootDevice, directory.createMode, directory.private)
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
		if err := validateManagedFile(file.path, uid, rootDevice, file.mode, file.name); err != nil {
			return err
		}
	}
	if err := validateManagementShim(layout, uid, rootDevice); err != nil {
		return err
	}
	return nil
}

func check(layout hostenv.WSLLayout, runtime hostenv.Runtime, machineID string) (Plan, error) {
	uid, rootDevice, err := validatePreflight(layout, runtime, machineID)
	if err != nil {
		return Plan{}, err
	}
	result := Plan{Layout: layout}
	for _, directory := range layoutDirectories(layout) {
		if _, err := inspectDirectory(directory.path, uid, directory.private, &rootDevice); errors.Is(err, os.ErrNotExist) {
			result.MissingDirectories = append(result.MissingDirectories, directory.path)
		} else if err != nil {
			return Plan{}, fmt.Errorf("validate native WSL directory %s: %w", directory.path, err)
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
		if err := validateManagedFile(file.path, uid, rootDevice, file.mode, file.name); err != nil {
			return Plan{}, err
		}
	}
	if err := validateManagementShim(layout, uid, rootDevice); err != nil {
		return Plan{}, err
	}
	return result, nil
}

func validatePreflight(layout hostenv.WSLLayout, runtime hostenv.Runtime, machineID string) (uint32, uint64, error) {
	if err := validateLayout(layout); err != nil {
		return 0, 0, err
	}
	uid := uint32(os.Getuid())
	if layout.UID != uid {
		return 0, 0, fmt.Errorf("native WSL layout UID %d does not match current user UID %d", layout.UID, uid)
	}
	expected, err := runtime.NativeWSLLayout(layout.Home, layout.UID, machineID)
	if err != nil {
		return 0, 0, fmt.Errorf("validate native WSL state identity: %w", err)
	}
	if layout.Distro != expected.Distro || layout.StateNamespace != expected.StateNamespace {
		return 0, 0, fmt.Errorf("native WSL state namespace %q does not match current distribution, machine and user identity", layout.StateNamespace)
	}

	homeInfo, err := inspectDirectory(layout.Home, uid, false, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("validate native WSL home: %w", err)
	}
	resolvedHome, err := filepath.EvalSymlinks(layout.Home)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve native WSL home: %w", err)
	}
	if resolvedHome != layout.Home {
		return 0, 0, fmt.Errorf("native WSL home %q resolves to %q; symlinked homes are not accepted", layout.Home, resolvedHome)
	}
	rootInfo, err := os.Stat(string(filepath.Separator))
	if err != nil {
		return 0, 0, fmt.Errorf("inspect distribution root filesystem: %w", err)
	}
	homeDevice, err := filesystemDevice(homeInfo)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect native WSL home filesystem: %w", err)
	}
	rootDevice, err := filesystemDevice(rootInfo)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect distribution root filesystem: %w", err)
	}
	if homeDevice != rootDevice {
		return 0, 0, fmt.Errorf("native WSL home %q is on filesystem device %d, not distribution root device %d", layout.Home, homeDevice, rootDevice)
	}
	return uid, rootDevice, nil
}

type layoutDirectory struct {
	path       string
	createMode os.FileMode
	private    bool
}

func layoutDirectories(layout hostenv.WSLLayout) []layoutDirectory {
	return []layoutDirectory{
		{filepath.Join(layout.Home, ".config"), privateDirMode, false},
		{layout.ConfigDir, privateDirMode, true},
		{filepath.Join(layout.Home, ".local"), privateDirMode, false},
		{filepath.Join(layout.Home, ".local", "state"), privateDirMode, false},
		{layout.StateDir, privateDirMode, true},
		{filepath.Join(layout.Home, ".local", "lib"), privateDirMode, false},
		{filepath.Dir(layout.BinaryPath), privateDirMode, true},
		{layout.ShimDir, shimDirMode, false},
	}
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
	if layout.Distro == "" || layout.StateNamespace == "" {
		return errors.New("native WSL filesystem layout is missing its state identity")
	}
	return nil
}

func ensureDirectory(path string, uid uint32, rootDevice uint64, createMode os.FileMode, private bool) (bool, error) {
	created := false
	if err := os.Mkdir(path, createMode); err == nil {
		created = true
		if err := os.Chmod(path, createMode); err != nil {
			return true, fmt.Errorf("set native WSL directory mode on %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrExist) {
		return false, fmt.Errorf("create native WSL directory %s: %w", path, err)
	}
	if _, err := inspectDirectory(path, uid, private, &rootDevice); err != nil {
		return created, fmt.Errorf("validate native WSL directory %s: %w", path, err)
	}
	return created, nil
}

func inspectDirectory(path string, uid uint32, private bool, rootDevice *uint64) (os.FileInfo, error) {
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
	if rootDevice != nil {
		if err := requireDevice(info, *rootDevice); err != nil {
			return nil, err
		}
	}
	perm, err := exactMode(info)
	if err != nil {
		return nil, err
	}
	if private {
		if perm != privateDirMode {
			return nil, fmt.Errorf("must have mode 0700, got %04o", perm)
		}
	} else if perm&0o700 != 0o700 || perm&0o7022 != 0 {
		return nil, fmt.Errorf("must be owner-accessible and not writable by group or other, got %04o", perm)
	}
	return info, nil
}

func validateManagedFile(path string, uid uint32, rootDevice uint64, wantMode os.FileMode, name string) error {
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
	if err := requireDevice(info, rootDevice); err != nil {
		return fmt.Errorf("native WSL %s %s: %w", name, path, err)
	}
	mode, err := exactMode(info)
	if err != nil {
		return fmt.Errorf("native WSL %s %s: %w", name, path, err)
	}
	if mode != wantMode {
		return fmt.Errorf("native WSL %s %s must have mode %04o, got %04o", name, path, wantMode, mode)
	}
	return nil
}

func validateManagementShim(layout hostenv.WSLLayout, uid uint32, rootDevice uint64) error {
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
	if err := requireDevice(info, rootDevice); err != nil {
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

func requireDevice(info os.FileInfo, rootDevice uint64) error {
	device, err := filesystemDevice(info)
	if err != nil {
		return err
	}
	if device != rootDevice {
		return fmt.Errorf("is on filesystem device %d, not distribution root device %d", device, rootDevice)
	}
	return nil
}

func exactMode(info os.FileInfo) (os.FileMode, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("file mode could not be determined")
	}
	return os.FileMode(stat.Mode & 0o7777), nil
}
