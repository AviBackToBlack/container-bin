package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
)

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
