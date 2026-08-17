package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverFromBackup_Recovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live")
	bak := path + ".bak"
	want := []byte("precious user data")
	if err := os.WriteFile(bak, want, 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := recoverFromBackup(path, func(bp string) error {
		b, err := os.ReadFile(bp)
		if err != nil {
			return err
		}
		if len(b) == 0 {
			return errors.New("empty backup")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected recovery")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("path content = %q, want %q", got, want)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Fatalf("backup still exists: %v", err)
	}
}

func TestRecoverFromBackup_NoOpWhenLiveExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live")
	bak := path + ".bak"
	live := []byte("live content")
	backup := []byte("backup content")
	if err := os.WriteFile(path, live, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bak, backup, 0644); err != nil {
		t.Fatal(err)
	}
	called := false
	ok, err := recoverFromBackup(path, func(bp string) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no recovery")
	}
	if called {
		t.Fatal("validator called for existing live file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(live) {
		t.Fatalf("live content changed: %q", got)
	}
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestRecoverFromBackup_NoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live")
	ok, err := recoverFromBackup(path, func(bp string) error {
		t.Fatalf("validator called with no backup: %s", bp)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no recovery")
	}
}

func TestRecoverFromBackup_InvalidBackupFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live")
	bak := path + ".bak"
	backup := []byte("corrupt backup")
	if err := os.WriteFile(bak, backup, 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := recoverFromBackup(path, func(bp string) error {
		return errors.New("invalid backup")
	})
	if err == nil {
		t.Fatal("expected error for invalid backup")
	}
	if ok {
		t.Fatal("expected no recovery")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("live path should be missing: %v", err)
	}
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(backup) {
		t.Fatalf("backup content changed: %q", got)
	}
}

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
}
