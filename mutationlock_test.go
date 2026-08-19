package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
)

func TestMutationLockPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	lockPath := lockPathForRegistry(cfg)
	mutPath := mutationlock.PathFor(cfg)
	if mutPath == lockPath {
		t.Fatalf("mutation lock path %q must differ from digest lock path %q", mutPath, lockPath)
	}
	if !strings.HasSuffix(mutPath, "container-bin.mutation.lock") {
		t.Fatalf("expected suffix container-bin.mutation.lock, got %q", mutPath)
	}
}

func TestMutationLockWithReleasesOnError(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationlock.PathFor(cfg)

	called := false
	boom := errors.New("boom")
	err := withMutationLock(cfg, func() error {
		called = true
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("lock file not held during fn: %v", statErr)
		}
		return boom
	})
	if err != boom {
		t.Fatalf("expected error %v, got %v", boom, err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("lock file not released after withMutationLock error: %v", statErr)
	}
}
