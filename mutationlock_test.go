package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMutationLockPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	lockPath := lockPathForRegistry(cfg)
	mutPath := mutationLockPathForRegistry(cfg)
	if mutPath == lockPath {
		t.Fatalf("mutation lock path %q must differ from digest lock path %q", mutPath, lockPath)
	}
	if !strings.HasSuffix(mutPath, "container-bin.mutation.lock") {
		t.Fatalf("expected suffix container-bin.mutation.lock, got %q", mutPath)
	}
}

func TestMutationLockAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

	release, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after release: %v", err)
	}
}

func TestMutationLockContention(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")

	release, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	defer release()

	_, err = acquireMutationLock(cfg, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected contention error")
	}
	if !strings.Contains(err.Error(), mutationLockPathForRegistry(cfg)) {
		t.Fatalf("error must name the lock file path: %v", err)
	}

	// The failed attempt must not delete another process's lock.
	if _, statErr := os.Stat(mutationLockPathForRegistry(cfg)); statErr != nil {
		t.Fatalf("holder's lock file was removed by a failed acquire: %v", statErr)
	}
}

func TestMutationLockErrorActionable(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

	release, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	defer release()

	_, err = acquireMutationLock(cfg, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected contention error")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Fatalf("error must contain lock path %q: %v", path, err)
	}
	if !strings.Contains(msg, "delete") || !strings.Contains(msg, "no cb process is running") {
		t.Fatalf("error must state the stale-lock remedy: %v", err)
	}
}

func TestMutationLockReacquire(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")

	release1, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	release1()

	release2, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("re-acquire failed: %v", err)
	}
	release2()
}

func TestMutationLockDoubleRelease(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

	release, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	release()
	release() // must not panic

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after double release: %v", err)
	}
}

func TestMutationLockHolderDiagnostics(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

	release, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	want := strconv.Itoa(os.Getpid())
	if !strings.Contains(string(data), want) {
		t.Fatalf("lock file %q does not contain pid %s", data, want)
	}
}

func TestMutationLockEmptyHolder(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("create empty lock file: %v", err)
	}

	_, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for empty lock file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error must name the lock file path: %v", err)
	}
	if !strings.Contains(err.Error(), "another cb command") {
		t.Fatalf("error must indicate a contended mutation: %v", err)
	}
}

func TestMutationLockConcurrency(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	const wait = 2 * time.Second

	var active int32
	var maxActive int32
	var wg sync.WaitGroup
	workers := 10
	iterations := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				release, err := acquireMutationLock(cfg, wait)
				if err != nil {
					t.Errorf("acquire failed: %v", err)
					return
				}
				cur := atomic.AddInt32(&active, 1)
				for {
					m := atomic.LoadInt32(&maxActive)
					if cur <= m || atomic.CompareAndSwapInt32(&maxActive, m, cur) {
						break
					}
				}
				if cur != 1 {
					t.Errorf("concurrent holders detected: active=%d", cur)
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				release()
			}
		}()
	}
	wg.Wait()

	if m := atomic.LoadInt32(&maxActive); m > 1 {
		t.Fatalf("mutation lock allowed concurrent access: maxActive=%d", m)
	}
}

func TestMutationLockWithReleasesOnError(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

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

// The error message tells users to delete a lock they believe is stale. If they
// do that while the holder is in fact alive, a third process can take the lock —
// and the original holder must not then delete it on the way out, which would
// leave two mutators running unserialized.
func TestMutationLockReleaseKeepsForeignLock(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	path := mutationLockPathForRegistry(cfg)

	release, err := acquireMutationLock(cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	foreign := "pid=999999 token=0123456789abcdef at 2026-01-01T00:00:00Z"
	if err := os.WriteFile(path, []byte(foreign), 0644); err != nil {
		t.Fatalf("overwrite lock: %v", err)
	}

	release()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("release removed a lock it no longer owned: %v", err)
	}
	if string(data) != foreign {
		t.Fatalf("foreign lock content changed: %q", string(data))
	}
}
