package registry

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShimWindowsSourcePermissions(t *testing.T) {
	dir := t.TempDir()
	exe, dst := filepath.Join(dir, "cb.exe"), filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("fixture executable bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	setRights := func(t *testing.T, rights string) {
		t.Helper()
		// icacls changes only the DACL, without persisting owner/SACL state or
		// requiring SeSecurityPrivilege. Only this owned source is modified.
		cmd := exec.Command("icacls.exe", exe, "/inheritance:r", "/grant:r", "*"+u.Uid+":"+rights)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture ACL: %v: %s", err, out)
		}
	}
	// This touches only a newly created source fixture. Its owner can restore
	// WRITE_DAC even when its explicit ACE grants only read/execute.
	defer setRights(t, "F")
	for _, rights := range []string{"RX", "(GR,GE,GW)"} {
		t.Run(rights, func(t *testing.T) {
			setRights(t, rights)
			result, err := installShim(exe, dst, os.Link, copyFile, os.Rename)
			if err != nil {
				t.Fatal(err)
			}
			want := "hardlink"
			if rights == "RX" {
				want = "copy"
				if !strings.Contains(result.linkFailure, "access denied") || !strings.Contains(result.linkFailure, "source cb.exe") {
					t.Fatalf("missing source permission diagnostic: %+v", result)
				}
			}
			if result.mode != want {
				t.Fatalf("rights %s: result %+v, want %s", rights, result, want)
			}
			sourceInfo, err := os.Stat(exe)
			if err != nil {
				t.Fatal(err)
			}
			shimInfo, err := os.Stat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(sourceInfo, shimInfo) != (want == "hardlink") {
				t.Fatal("unexpected file identity")
			}
			data, err := os.ReadFile(dst)
			if err != nil || string(data) != "fixture executable bytes" {
				t.Fatalf("fallback bytes = %q, error %v", data, err)
			}
		})
	}
}
