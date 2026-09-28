//go:build linux

package wslinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallBinaryFileCreatesAndUpdatesFixedTarget(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "bootstrap-cb")
	targetDir := filepath.Join(directory, ".local", "lib", "container-bin")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("first binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := installTestLayout()
	layout.UID = uint32(os.Getuid())
	layout.BinaryPath = filepath.Join(targetDir, "cb")
	if state, err := BinaryState(layout, source); err != nil || state != Create {
		t.Fatalf("BinaryState before install = %q, %v", state, err)
	}
	if err := installBinaryFile(layout, source); err != nil {
		t.Fatal(err)
	}
	if state, err := BinaryState(layout, source); err != nil || state != Ready {
		t.Fatalf("BinaryState after install = %q, %v", state, err)
	}
	if info, err := os.Stat(layout.BinaryPath); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("managed binary mode = %v, %v", info, err)
	}
	if err := os.WriteFile(source, []byte("second binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if state, err := BinaryState(layout, source); err != nil || state != Update {
		t.Fatalf("BinaryState before update = %q, %v", state, err)
	}
	if err := installBinaryFile(layout, source); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(layout.BinaryPath)
	if err != nil || string(data) != "second binary" {
		t.Fatalf("managed binary = %q, %v", data, err)
	}
}

func TestBinarySourceRejectsSymlinkAndUnsafeMode(t *testing.T) {
	directory := t.TempDir()
	real := filepath.Join(directory, "real")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openSourceBinary(link, uint32(os.Getuid())); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink error = %v", err)
	}
	if err := os.Chmod(real, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := openSourceBinary(real, uint32(os.Getuid())); err == nil || !strings.Contains(err.Error(), "not writable by group or other") {
		t.Fatalf("unsafe-mode error = %v", err)
	}
}
