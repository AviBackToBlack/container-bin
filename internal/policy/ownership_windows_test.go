//go:build windows

package policy

import "testing"

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
