//go:build linux

package wslfs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const testMachineID = "0123456789abcdef0123456789abcdef"

func testLayout(t *testing.T) hostenv.WSLLayout {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	homeInfo, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	homeDevice, err := filesystemDevice(homeInfo)
	if err != nil {
		t.Fatal(err)
	}
	rootDevice, err := filesystemDevice(rootInfo)
	if err != nil {
		t.Fatal(err)
	}
	if homeDevice != rootDevice {
		t.Skipf("temporary directory device %d differs from distribution root device %d", homeDevice, rootDevice)
	}
	layout, err := (hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}).NativeWSLLayout(home, uint32(os.Getuid()), testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

func prepareTest(layout hostenv.WSLLayout) error {
	return prepare(layout, hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: layout.Distro}, testMachineID)
}

func TestPrepareCreatesFixedDirectoriesAndIsIdempotent(t *testing.T) {
	layout := testLayout(t)
	if err := prepareTest(layout); err != nil {
		t.Fatal(err)
	}
	for path, wantMode := range map[string]os.FileMode{
		layout.ConfigDir:                0o700,
		layout.StateDir:                 0o700,
		filepath.Dir(layout.BinaryPath): 0o700,
		layout.ShimDir:                  0o755,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != wantMode {
			t.Errorf("directory %s mode = %v, want %04o", path, info.Mode(), wantMode)
		}
	}
	if err := prepareTest(layout); err != nil {
		t.Fatalf("idempotent Prepare() error = %v", err)
	}
}

func TestPrepareCreatesExactModesDespiteRestrictiveUmask(t *testing.T) {
	layout := testLayout(t)
	oldUmask := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(oldUmask) })
	if err := prepareTest(layout); err != nil {
		t.Fatal(err)
	}
	for path, wantMode := range map[string]os.FileMode{
		layout.ConfigDir: 0o700,
		layout.ShimDir:   0o755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		mode, err := exactMode(info)
		if err != nil {
			t.Fatal(err)
		}
		if mode != wantMode {
			t.Errorf("directory %s mode = %04o, want %04o", path, mode, wantMode)
		}
	}
}

func TestPrepareValidatesManagedFilesAndManagementShim(t *testing.T) {
	layout := testLayout(t)
	if err := prepareTest(layout); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		layout.RegistryPath: 0o600,
		layout.LockPath:     0o600,
		layout.BinaryPath:   0o755,
	} {
		if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "lib", "container-bin", "cb"), layout.ManagementShim); err != nil {
		t.Fatal(err)
	}
	if err := prepareTest(layout); err != nil {
		t.Fatalf("Prepare() rejected valid managed endpoints: %v", err)
	}
	if err := os.Remove(layout.ManagementShim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("unrelated", layout.ManagementShim); err != nil {
		t.Fatal(err)
	}
	if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "targets") {
		t.Fatalf("wrong-target shim Prepare() error = %v", err)
	}
}

func TestPrepareRejectsUnsafeObjectsWithoutRepairingThem(t *testing.T) {
	t.Run("symlinked home", func(t *testing.T) {
		parent := t.TempDir()
		realHome := filepath.Join(parent, "real-home")
		if err := os.Mkdir(realHome, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedHome := filepath.Join(parent, "linked-home")
		if err := os.Symlink(realHome, linkedHome); err != nil {
			t.Fatal(err)
		}
		layout, err := (hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}).NativeWSLLayout(linkedHome, uint32(os.Getuid()), testMachineID)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("symlinked-home Prepare() error = %v", err)
		}
	})

	t.Run("private directory mode", func(t *testing.T) {
		layout := testLayout(t)
		if err := prepareTest(layout); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(layout.ConfigDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "mode 0700") {
			t.Fatalf("insecure config Prepare() error = %v", err)
		}
		info, err := os.Stat(layout.ConfigDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("Prepare() repaired unrelated permissions: mode=%v", info.Mode())
		}
	})

	t.Run("private directory special mode", func(t *testing.T) {
		layout := testLayout(t)
		if err := prepareTest(layout); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(layout.ConfigDir, os.ModeSticky|0o700); err != nil {
			t.Fatal(err)
		}
		if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "mode 0700") {
			t.Fatalf("special-mode config Prepare() error = %v", err)
		}
	})

	t.Run("writable shim directory", func(t *testing.T) {
		layout := testLayout(t)
		if err := prepareTest(layout); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(layout.ShimDir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "writable by group or other") {
			t.Fatalf("writable shim directory Prepare() error = %v", err)
		}
		info, err := os.Stat(layout.ShimDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o777 {
			t.Fatalf("Prepare() repaired shim permissions: mode=%v", info.Mode())
		}
	})

	t.Run("symlinked config directory", func(t *testing.T) {
		layout := testLayout(t)
		if err := os.Mkdir(filepath.Join(layout.Home, ".config"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), layout.ConfigDir); err != nil {
			t.Fatal(err)
		}
		if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("symlinked config Prepare() error = %v", err)
		}
	})

	t.Run("wrong current user", func(t *testing.T) {
		layout := testLayout(t)
		layout.UID++
		if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "does not match current user") {
			t.Fatalf("wrong-UID Prepare() error = %v", err)
		}
		if _, err := os.Stat(layout.ConfigDir); !os.IsNotExist(err) {
			t.Fatalf("wrong-UID Prepare() mutated layout: %v", err)
		}
	})
}

func TestPrepareRejectsHomeOnDifferentFilesystem(t *testing.T) {
	const sharedMemoryRoot = "/dev/shm"
	if _, err := os.Stat(sharedMemoryRoot); err != nil {
		t.Skipf("shared-memory filesystem unavailable: %v", err)
	}
	home, err := os.MkdirTemp(sharedMemoryRoot, "container-bin-wslfs-")
	if err != nil {
		t.Skipf("cannot create cross-filesystem fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}

	homeInfo, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	homeDevice, err := filesystemDevice(homeInfo)
	if err != nil {
		t.Fatal(err)
	}
	rootDevice, err := filesystemDevice(rootInfo)
	if err != nil {
		t.Fatal(err)
	}
	if homeDevice == rootDevice {
		t.Skip("fixture shares the distribution root filesystem device")
	}

	layout, err := (hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}).NativeWSLLayout(home, uint32(os.Getuid()), testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "not distribution root device") {
		t.Fatalf("cross-filesystem Prepare() error = %v", err)
	}
}

func TestInspectDirectoryRejectsNestedDifferentFilesystem(t *testing.T) {
	const sharedMemoryRoot = "/dev/shm"
	info, err := os.Stat(sharedMemoryRoot)
	if err != nil {
		t.Skipf("shared-memory filesystem unavailable: %v", err)
	}
	rootInfo, err := os.Stat(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	rootDevice, err := filesystemDevice(rootInfo)
	if err != nil {
		t.Fatal(err)
	}
	device, err := filesystemDevice(info)
	if err != nil {
		t.Fatal(err)
	}
	if device == rootDevice {
		t.Skip("fixture shares the distribution root filesystem device")
	}
	if _, err := inspectDirectory(sharedMemoryRoot, uint32(os.Getuid()), false, &rootDevice); err == nil || !strings.Contains(err.Error(), "not distribution root device") {
		t.Fatalf("nested cross-filesystem directory error = %v", err)
	}
}

func TestPrepareCleansOnlyDirectoriesCreatedByFailedAttempt(t *testing.T) {
	layout := testLayout(t)
	for _, path := range []string{filepath.Join(layout.Home, ".local"), layout.ShimDir} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(layout.ManagementShim, []byte("unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision Prepare() error = %v", err)
	}
	for _, path := range []string{layout.ConfigDir, layout.StateDir, filepath.Dir(layout.BinaryPath)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("failed Prepare() left newly created directory %s: %v", path, err)
		}
	}
	if _, err := os.Stat(layout.ManagementShim); err != nil {
		t.Fatalf("failed Prepare() removed pre-existing collision: %v", err)
	}
}

func TestPrepareRejectsForeignStateNamespaceBeforeMutation(t *testing.T) {
	layout := testLayout(t)
	layout.StateNamespace = "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "does not match current distribution") {
		t.Fatalf("foreign namespace Prepare() error = %v", err)
	}
	if _, err := os.Stat(layout.ConfigDir); !os.IsNotExist(err) {
		t.Fatalf("foreign namespace Prepare() mutated layout: %v", err)
	}
}

func TestPrepareRejectsExistingManagedFileModeAndCollision(t *testing.T) {
	layout := testLayout(t)
	if err := prepareTest(layout); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.RegistryPath, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(layout.RegistryPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("insecure registry Prepare() error = %v", err)
	}
	if err := os.Remove(layout.RegistryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.ManagementShim, []byte("unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareTest(layout); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("management-shim collision Prepare() error = %v", err)
	}
}
