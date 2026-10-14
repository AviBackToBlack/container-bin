package registry

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Elevated CI runners can have privileges that bypass source-file ACL checks.
// Restrict only the test thread: remove privileges and make Administrators
// deny-only. Revert before releasing the OS thread; never alter the process's
// token or create another account.
func withUnprivilegedHardlinkToken(t *testing.T, run func()) {
	t.Helper()
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	var current syscall.Token
	if err := syscall.OpenProcessToken(process, syscall.TOKEN_DUPLICATE|syscall.TOKEN_QUERY, &current); err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	admin, err := syscall.StringToSid("S-1-5-32-544")
	if err != nil {
		t.Fatal(err)
	}
	disabled := syscall.SIDAndAttributes{Sid: admin}
	advapi := syscall.NewLazyDLL("advapi32.dll")
	var restricted syscall.Token
	ok, _, callErr := advapi.NewProc("CreateRestrictedToken").Call(uintptr(current), 1, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatalf("restrict test token: %v", callErr)
	}
	defer restricted.Close()
	runtime.LockOSThread()
	ok, _, callErr = advapi.NewProc("ImpersonateLoggedOnUser").Call(uintptr(restricted))
	if ok == 0 {
		runtime.UnlockOSThread()
		t.Fatalf("impersonate test token: %v", callErr)
	}
	defer func() {
		ok, _, err := advapi.NewProc("RevertToSelf").Call()
		if ok == 0 {
			// A still-impersonating thread must never return to Go's pool.
			panic("cannot revert hardlink test token: " + err.Error())
		}
		runtime.UnlockOSThread()
	}()
	run()
}

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
	// The reduced token must have destination access independently of any
	// inherited Administrators grant on an elevated runner's temporary root.
	grant := exec.Command("icacls.exe", dir, "/grant", "*"+u.Uid+":(OI)(CI)F")
	if out, err := grant.CombinedOutput(); err != nil {
		t.Fatalf("fixture destination ACL: %v: %s", err, out)
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
			var result shimInstallResult
			var err error
			withUnprivilegedHardlinkToken(t, func() {
				f, writeErr := os.OpenFile(exe, os.O_WRONLY, 0)
				if f != nil {
					f.Close()
				}
				if (rights == "RX" && !os.IsPermission(writeErr)) || (rights != "RX" && writeErr != nil) {
					t.Fatalf("source write access for %s: %v", rights, writeErr)
				}
				result, err = installShim(exe, dst, os.Link, copyFile, os.Rename)
			})
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
