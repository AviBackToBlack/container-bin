//go:build windows

package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticateGitHubCLIRejectsUnsignedExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gh.exe")
	if err := os.WriteFile(path, []byte("not a signed GitHub CLI executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := authenticateGitHubCLI(path)
	if err == nil || !strings.Contains(err.Error(), "Authenticode") {
		t.Fatalf("authenticateGitHubCLI error = %v", err)
	}
}
