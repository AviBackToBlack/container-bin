package diag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShimDirACLUsesSystemPowerShell(t *testing.T) {
	want, err := aclPowerShellExecutable()
	if err != nil {
		t.Fatal(err)
	}
	shadow := t.TempDir()
	if err := os.WriteFile(filepath.Join(shadow, "powershell.exe"), []byte("hostile PATH placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shadow+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SystemRoot", shadow)
	t.Setenv("WINDIR", shadow)
	cmd, err := shimDirACLCommand(context.Background(), shadow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(cmd.Path, want) || !filepath.IsAbs(cmd.Path) {
		t.Fatalf("probe executable = %q, want system executable %q", cmd.Path, want)
	}
}

func TestACLPowerShellExecutableAtFailsClosed(t *testing.T) {
	if _, err := aclPowerShellExecutableAt("relative"); err == nil {
		t.Fatal("relative system directory accepted")
	}
	root := t.TempDir()
	if _, err := aclPowerShellExecutableAt(root); err == nil {
		t.Fatal("missing executable accepted")
	}
	path := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := aclPowerShellExecutableAt(root); err == nil {
		t.Fatal("directory accepted as executable")
	}
}
