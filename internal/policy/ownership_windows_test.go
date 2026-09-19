//go:build windows

package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPowerShellExecutableAtRequiresAbsoluteRegularSystemBinary(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := powerShellExecutableAt(root)
	if err != nil || got != executable {
		t.Fatalf("powerShellExecutableAt(%q) = (%q, %v), want %q", root, got, err, executable)
	}
	if _, err := powerShellExecutableAt("relative-windows"); err == nil {
		t.Fatal("relative Windows directory accepted")
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(executable, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := powerShellExecutableAt(root); err == nil {
		t.Fatal("non-regular PowerShell executable accepted")
	}
}

func TestSecureWindowsACLVerdict(t *testing.T) {
	secure := "OWNER|S-1-5-32-544\nS-1-5-18|Allow|FullControl\nS-1-5-32-544|Allow|FullControl\nS-1-5-32-545|Allow|ReadAndExecute, Synchronize"
	if err := secureWindowsACLVerdict(secure, true); err != nil {
		t.Fatalf("secure ACL rejected: %v", err)
	}
	if err := secureWindowsACLVerdict("OWNER|S-1-5-21-1\n", true); err == nil {
		t.Fatal("untrusted owner accepted")
	}
	if err := secureWindowsACLVerdict("OWNER|S-1-5-18\nS-1-1-0|Allow|Write", true); err == nil {
		t.Fatal("untrusted file writer accepted")
	}
	if err := secureWindowsACLVerdict("OWNER|S-1-5-18\nS-1-1-0|Allow|CreateFiles", false); err != nil {
		t.Fatalf("safe parent create right rejected: %v", err)
	}
	if err := secureWindowsACLVerdict("OWNER|S-1-5-18\nS-1-1-0|Allow|DeleteSubdirectoriesAndFiles", false); err == nil {
		t.Fatal("untrusted parent delete right accepted")
	}
	if err := secureWindowsACLVerdict("OWNER|S-1-5-18\nmalformed", true); err == nil {
		t.Fatal("malformed permission entry accepted")
	}
}
