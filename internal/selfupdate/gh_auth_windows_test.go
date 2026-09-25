//go:build windows

package selfupdate

import (
	"context"
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
	_, err := authenticateGitHubCLI(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "Authenticode") {
		t.Fatalf("authenticateGitHubCLI error = %v", err)
	}
}

func TestAuthenticateGitHubCLIHonorsCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gh.exe")
	if err := os.WriteFile(path, []byte("not a signed GitHub CLI executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authenticateGitHubCLI(ctx, path)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("authenticateGitHubCLI canceled-context error = %v", err)
	}
}
