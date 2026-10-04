//go:build linux

package wslreconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

func leaseTestLayout(t *testing.T) hostenv.WSLLayout {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return hostenv.WSLLayout{UID: uint32(os.Geteuid()), StateDir: dir}
}

func TestFileLeaseLifecycle(t *testing.T) {
	layout := leaseTestLayout(t)
	runID := strings.Repeat("a", 32)
	held, err := createFileLease(layout, runID)
	if err != nil {
		t.Fatal(err)
	}
	status, observed, err := probeFileLease(layout, runID)
	if err != nil || status != leaseActive || observed != nil {
		t.Fatalf("held lease probe = (%v, %v, %v), want active", status, observed, err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	status, observed, err = probeFileLease(layout, runID)
	if err != nil || status != leaseOrphaned || observed == nil {
		t.Fatalf("released lease probe = (%v, %v, %v), want orphaned", status, observed, err)
	}
	if err := observed.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := observed.Close(); err != nil {
		t.Fatal(err)
	}
	status, observed, err = probeFileLease(layout, runID)
	if err != nil || status != leaseMissing || observed != nil {
		t.Fatalf("removed lease probe = (%v, %v, %v), want missing", status, observed, err)
	}
}

func TestFileLeaseRejectsUnsafePaths(t *testing.T) {
	layout := leaseTestLayout(t)
	runID := strings.Repeat("b", 32)
	name, err := leaseName(runID)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(layout.StateDir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(layout.StateDir, name)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeFileLease(layout, runID); err == nil {
		t.Fatal("symlink lease was accepted")
	}
	if err := os.Remove(filepath.Join(layout.StateDir, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.StateDir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeFileLease(layout, runID); err == nil {
		t.Fatal("world-readable lease was accepted")
	}
	if _, err := createFileLease(layout, "not-a-run-id"); err == nil {
		t.Fatal("invalid run identity was accepted")
	}
}

func TestFileCoordinatorHonorsContext(t *testing.T) {
	layout := leaseTestLayout(t)
	first, err := acquireFileCoordinator(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireFileCoordinator(ctx, layout); err == nil {
		t.Fatal("second coordinator unexpectedly acquired held lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(layout.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read-only coordinator created state entries: %v", entries)
	}
}
