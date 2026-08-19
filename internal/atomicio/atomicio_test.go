package atomicio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup unexpectedly remains: %v", err)
	}
}

func TestRecoverFromBackup_Recovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live")
	bak := path + ".bak"
	want := []byte("precious user data")
	if err := os.WriteFile(bak, want, 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := RecoverFromBackup(path, func(bp string) error {
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
	ok, err := RecoverFromBackup(path, func(bp string) error {
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
	ok, err := RecoverFromBackup(path, func(bp string) error {
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
	ok, err := RecoverFromBackup(path, func(bp string) error {
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
