//go:build linux

package wslshim

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

func TestPinnedShimDirectoryPublishesWithoutReplacing(t *testing.T) {
	home := rootDeviceTempDir(t)
	shimDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	layout := hostenv.WSLLayout{UID: uint32(os.Getuid()), ShimDir: shimDir}
	directory, err := openPinnedShimDirectory(layout)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.close()
	target := filepath.Join(home, ".local", "lib", "container-bin", "cb")
	shim := Shim{Name: "node24", Path: filepath.Join(shimDir, "node24"), Target: target}
	if err := directory.ensure(shim, "tool shim"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(shim.Path); err != nil || got != target {
		t.Fatalf("Readlink() = %q, %v; want %q", got, err, target)
	}
	if err := directory.ensure(shim, "tool shim"); err != nil {
		t.Fatalf("second ensure failed: %v", err)
	}

	collision := filepath.Join(shimDir, "python")
	if err := os.WriteFile(collision, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := directory.ensure(Shim{Name: "python", Path: collision, Target: target}, "tool shim"); err == nil {
		t.Fatal("ensure replaced an unrelated regular file")
	}
	if got, err := os.ReadFile(collision); err != nil || string(got) != "unrelated" {
		t.Fatalf("collision contents = %q, %v", got, err)
	}

	longName := strings.Repeat("a", 215)
	longShim := Shim{Name: longName, Path: filepath.Join(shimDir, longName), Target: target}
	if err := directory.ensure(longShim, "tool shim"); err != nil {
		t.Fatalf("ensure long valid name: %v", err)
	}
	if got, err := os.Readlink(longShim.Path); err != nil || got != target {
		t.Fatalf("long-name Readlink() = %q, %v; want %q", got, err, target)
	}
}

func TestPinnedShimDirectorySurvivesPathSwapWithoutRedirectingMutation(t *testing.T) {
	home := rootDeviceTempDir(t)
	shimDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	layout := hostenv.WSLLayout{UID: uint32(os.Getuid()), ShimDir: shimDir}
	directory, err := openPinnedShimDirectory(layout)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.close()

	pinned := filepath.Join(home, "pinned-bin")
	if err := os.Rename(shimDir, pinned); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".local", "lib", "container-bin", "cb")
	shim := Shim{Name: "node24", Path: filepath.Join(shimDir, "node24"), Target: target}
	if err := directory.ensure(shim, "tool shim"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(shimDir, "node24")); !os.IsNotExist(err) {
		t.Fatalf("replacement directory received shim: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(pinned, "node24")); err != nil || got != target {
		t.Fatalf("pinned directory Readlink() = %q, %v; want %q", got, err, target)
	}
}

func TestOpenPinnedShimDirectoryRejectsSymlinkedAncestorAndUnsafeMode(t *testing.T) {
	home := rootDeviceTempDir(t)
	realLocal := filepath.Join(home, "real-local")
	if err := os.MkdirAll(filepath.Join(realLocal, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realLocal, filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	layout := hostenv.WSLLayout{UID: uint32(os.Getuid()), ShimDir: filepath.Join(home, ".local", "bin")}
	if _, err := openPinnedShimDirectory(layout); err == nil || !strings.Contains(err.Error(), "without following symlinks") {
		t.Fatalf("symlinked-ancestor error = %v", err)
	}

	if err := os.Remove(filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.ShimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(layout.ShimDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := openPinnedShimDirectory(layout); err == nil || !strings.Contains(err.Error(), "not writable by group or other") {
		t.Fatalf("unsafe-mode error = %v", err)
	}
}

func rootDeviceTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	directoryStat, directoryOK := directoryInfo.Sys().(*syscall.Stat_t)
	rootStat, rootOK := rootInfo.Sys().(*syscall.Stat_t)
	if !directoryOK || !rootOK {
		t.Skip("filesystem device identity is unavailable")
	}
	if directoryStat.Dev != rootStat.Dev {
		t.Skipf("temporary directory device %d differs from root device %d", directoryStat.Dev, rootStat.Dev)
	}
	return directory
}
