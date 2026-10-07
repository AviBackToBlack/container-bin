//go:build linux

package lockfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteModeUsesRequestedPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.lock")
	if err := WriteMode(path, &LockFile{Version: 1, Images: map[string]LockEntry{}}, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("lockfile mode = %04o, want 0600", got)
	}
}
