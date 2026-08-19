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

func TestValidateRegistryBackup(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.bak")
	if err := os.WriteFile(good, []byte(defaultRegistryTOML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateRegistryBackup(good); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	bad := filepath.Join(dir, "bad.bak")
	if err := os.WriteFile(bad, []byte("schema_version = 1\n\n[notools.invalid]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateRegistryBackup(bad); err == nil {
		t.Fatal("expected invalid registry to be rejected")
	}

	// A zero-byte backup is a truncation artifact rather than recoverable data.
	empty := filepath.Join(dir, "empty.bak")
	if err := os.WriteFile(empty, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateRegistryBackup(empty); err == nil {
		t.Fatal("expected empty backup to be rejected")
	}

	// A tool-less registry is rejected too. The rule lives in parseRegistryTOML
	// rather than here, so this pins the guarantee at the boundary that matters
	// and keeps holding if the check ever moves.
	toolless := filepath.Join(dir, "toolless.bak")
	if err := os.WriteFile(toolless, []byte("schema_version = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateRegistryBackup(toolless); err == nil {
		t.Fatal("expected tool-less backup to be rejected")
	}
}
