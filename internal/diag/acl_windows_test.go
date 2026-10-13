package diag

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestShimDirACLPowerShell7Child(t *testing.T) {
	if os.Getenv("CB_TEST_ACL_CHILD") != "1" {
		return
	}
	status, msg := inspectShimDirACL(os.Getenv("CB_TEST_ACL_DIR"), os.Getenv("CB_TEST_ACL_SID"))
	if status != os.Getenv("CB_TEST_ACL_STATUS") {
		t.Fatalf("ACL status = %s: %s", status, msg)
	}
}

func TestShimDirACLPoisonedModulePath(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	poison := filepath.Join(root, "modules")
	moduleDir := filepath.Join(poison, "Microsoft.PowerShell.Security")
	if err := os.MkdirAll(moduleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "Microsoft.PowerShell.Security.psm1"), []byte("throw 'POISON_MODULE_LOADED'"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PSModulePath", poison)
	t.Setenv("CB_DOCTOR_ACL_DIR", "stale path must be replaced")
	for _, unsafe := range []bool{false, true} {
		name, want := "safe", "ok"
		if unsafe {
			name, want = "unsafe", "warn"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, name)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			// Apply a protected DACL only to this owned disposable directory.
			setup := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `
$ErrorActionPreference = 'Stop'
Import-Module ($PSHOME + '\Modules\Microsoft.PowerShell.Security\Microsoft.PowerShell.Security.psd1')
$acl = [System.Security.AccessControl.DirectorySecurity]::new()
$acl.SetAccessRuleProtection($true, $false)
$sid = [System.Security.Principal.SecurityIdentifier]$env:CB_TEST_ACL_SID
$acl.SetOwner($sid)
$acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
if ($env:CB_TEST_ACL_UNSAFE -eq 'true') {
    $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new([System.Security.Principal.SecurityIdentifier]'S-1-1-0', 'Write', 'None', 'None', 'Allow'))
}
Microsoft.PowerShell.Security\Set-Acl -LiteralPath $env:CB_TEST_ACL_DIR -AclObject $acl
`)
			setup.Env = append(os.Environ(), "CB_TEST_ACL_DIR="+dir, "CB_TEST_ACL_SID="+u.Uid, "CB_TEST_ACL_UNSAFE="+map[bool]string{false: "false", true: "true"}[unsafe])
			if out, err := setup.CombinedOutput(); err != nil {
				t.Fatalf("fixture ACL: %v: %s", err, out)
			}
			status, msg := inspectShimDirACL(dir, u.Uid)
			if status != want || (unsafe && !strings.Contains(msg, "S-1-1-0")) {
				t.Fatalf("ACL = %s %q; want %s", status, msg, want)
			}
			if os.Getenv("PSModulePath") != poison || os.Getenv("CB_DOCTOR_ACL_DIR") != "stale path must be replaced" {
				t.Fatal("parent environment changed")
			}
			t.Run("PowerShell7_parent", func(t *testing.T) {
				pwsh, err := exec.LookPath("pwsh.exe")
				if err != nil {
					t.Skip("PowerShell 7 is not installed")
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				parent := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", `$env:PSModulePath = $env:CB_TEST_POISON; & $env:CB_TEST_EXE '-test.run=^TestShimDirACLPowerShell7Child$' '-test.v'; exit $LASTEXITCODE`)
				parent.Env = append(os.Environ(), "CB_TEST_ACL_CHILD=1", "CB_TEST_ACL_DIR="+dir, "CB_TEST_ACL_SID="+u.Uid, "CB_TEST_ACL_STATUS="+want, "CB_TEST_POISON="+poison, "CB_TEST_EXE="+exe)
				if out, err := parent.CombinedOutput(); err != nil {
					t.Fatalf("PowerShell 7 parent: %v: %s", err, out)
				}
			})
		})
	}
	// Unlike the old nonterminating probe, a missing directory must fail the
	// process and carry a sanitized actionable reason instead of stdout "||".
	missing := filepath.Join(root, "missing-private-path")
	cmd := shimDirACLCommand(context.Background(), missing)
	out, err := cmd.Output()
	if err == nil || len(out) != 0 {
		t.Fatalf("missing ACL: stdout %q, error %v", out, err)
	}
	msg := shimDirACLProbeError(err)
	if !strings.Contains(msg, "Get-Acl failed") || strings.Contains(msg, missing) {
		t.Fatalf("unsafe or unhelpful probe error: %s", msg)
	}
	status, _ := inspectShimDirACL(missing, u.Uid)
	if status != "warn" {
		t.Fatalf("failed inspection status = %s", status)
	}
}
