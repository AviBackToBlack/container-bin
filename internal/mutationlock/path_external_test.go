// This assertion spans two packages: the mutation lock must never collide with
// the image digest lockfile that sits in the same directory. mutationlock
// cannot import lockfile (lockfile is a higher layer), so the check lives in an
// external test package that is free to import both.
package mutationlock_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
)

func TestMutationLockPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	lockPath := lockfile.PathFor(cfg)
	mutPath := mutationlock.PathFor(cfg)
	if mutPath == lockPath {
		t.Fatalf("mutation lock path %q must differ from digest lock path %q", mutPath, lockPath)
	}
	if !strings.HasSuffix(mutPath, "container-bin.mutation.lock") {
		t.Fatalf("expected suffix container-bin.mutation.lock, got %q", mutPath)
	}
}
