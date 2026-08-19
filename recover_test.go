package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLockFile_RecoversFromBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	bak := path + ".bak"
	lf := &LockFile{Version: 1, Images: map[string]LockEntry{
		"node:24-slim": {
			Configured: "node:24-slim",
			Resolved:   "node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}}
	if err := os.WriteFile(bak, renderLockFile(lf), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := loadLockFile(path)
	if err != nil {
		t.Fatalf("loadLockFile: %v", err)
	}
	if got == nil {
		t.Fatal("expected parsed lockfile, got nil")
	}
	if got.Version != 1 || len(got.Images) != 1 {
		t.Fatalf("unexpected lockfile: %#v", got)
	}
	e, ok := got.Images["node:24-slim"]
	if !ok || e.Resolved != lf.Images["node:24-slim"].Resolved {
		t.Fatalf("entry mismatch: %#v", e)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live lockfile missing: %v", err)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Fatalf("backup still exists: %v", err)
	}
}

func TestLoadLockFile_NoFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	got, err := loadLockFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestLoadLockFile_CorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	bak := path + ".bak"
	if err := os.WriteFile(bak, []byte("not a lockfile\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := loadLockFile(path)
	if err == nil {
		t.Fatal("expected error for corrupt backup")
	}
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("backup missing after failed recovery: %v", err)
	}
}
