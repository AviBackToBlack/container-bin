//go:build windows

package policy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
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

func TestPolicyOwnershipChildEnvironment(t *testing.T) {
	parent := []string{"PATH=retained", "PSModulePath=bad", "psmodulepath=also-bad", "cb_policy_acl_path=stale", "OTHER_PSModulePath=retained"}
	before := append([]string(nil), parent...)
	want := []string{"PATH=retained", "OTHER_PSModulePath=retained", "CB_POLICY_ACL_PATH=fixture"}
	if got := policyOwnershipEnvironment(parent, "fixture"); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(parent, before) {
		t.Fatalf("child environment = %q; parent changed=%t", got, !reflect.DeepEqual(parent, before))
	}
}

func TestPolicyOwnershipErrorsAreSanitized(t *testing.T) {
	for _, stage := range []string{"module", "item", "acl", "identity", "unknown"} {
		err := &exec.ExitError{Stderr: []byte("CB_POLICY_ACL_" + stage)}
		if got := policyOwnershipProbeError(err); strings.Contains(got, "CB_POLICY") || got == "" {
			t.Fatal(got)
		}
	}
	secret := `PRIVATE_PATH_AND_IDENTITY`
	for _, err := range []error{errors.New(secret), &exec.ExitError{Stderr: []byte(secret)}} {
		if strings.Contains(policyOwnershipProbeError(err), secret) {
			t.Fatal("raw error leaked")
		}
	}
}

func policyFixture(t *testing.T, administratorOwner, writableFile, writableParent bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "owned-policy")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\nrequire_lock = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ps, err := powerShellExecutable()
	if err != nil {
		t.Fatal(err)
	}
	setup := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", `
$ErrorActionPreference = 'Stop'
Import-Module ($PSHOME + '\Modules\Microsoft.PowerShell.Security\Microsoft.PowerShell.Security.psd1') -ErrorAction Stop
$caller = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$owner = $caller
if ($env:CB_TEST_POLICY_ADMIN -eq 'true') { $owner = [System.Security.Principal.SecurityIdentifier]'S-1-5-32-544' }
foreach ($file in @($false, $true)) {
    if ($file) { $acl = [System.Security.AccessControl.FileSecurity]::new(); $target = $env:CB_POLICY_ACL_PATH }
    else { $acl = [System.Security.AccessControl.DirectorySecurity]::new(); $target = [IO.Path]::GetDirectoryName($env:CB_POLICY_ACL_PATH) }
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($owner)
    foreach ($sid in @('S-1-5-18','S-1-5-32-544')) {
        $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new([System.Security.Principal.SecurityIdentifier]$sid, 'FullControl', 'Allow'))
    }
    if ($env:CB_TEST_POLICY_ADMIN -ne 'true') { $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new($caller, 'FullControl', 'Allow')) }
    $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new([System.Security.Principal.SecurityIdentifier]'S-1-5-32-545', 'ReadAndExecute', 'Allow'))
    if (($file -and $env:CB_TEST_POLICY_FILE_WRITE -eq 'true') -or (-not $file -and $env:CB_TEST_POLICY_PARENT_WRITE -eq 'true')) {
        $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new([System.Security.Principal.SecurityIdentifier]'S-1-1-0', 'Modify', 'Allow'))
    }
    Microsoft.PowerShell.Security\Set-Acl -LiteralPath $target -AclObject $acl -ErrorAction Stop
}
`)
	setup.Env = append(policyOwnershipEnvironment(os.Environ(), path), "CB_TEST_POLICY_ADMIN="+map[bool]string{true: "true", false: "false"}[administratorOwner], "CB_TEST_POLICY_FILE_WRITE="+map[bool]string{true: "true", false: "false"}[writableFile], "CB_TEST_POLICY_PARENT_WRITE="+map[bool]string{true: "true", false: "false"}[writableParent])
	if err := setup.Run(); err != nil {
		t.Fatalf("owned fixture ACL setup failed: %v", err)
	}
	return path
}

func TestPolicyOwnershipInboxModulesAndTerminatingFailures(t *testing.T) {
	path := policyFixture(t, false, false, false)
	poison := filepath.Join(t.TempDir(), "modules", "Microsoft.PowerShell.Security")
	if err := os.MkdirAll(poison, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(poison, "Microsoft.PowerShell.Security.psm1"), []byte("throw 'PRIVATE_POISON_MODULE'"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PSModulePath", filepath.Dir(poison))
	t.Setenv("CB_POLICY_ACL_PATH", "stale-private-path")
	cmd, err := policyOwnershipCommand(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil || !strings.HasPrefix(string(out), "OWNER|S-1-") {
		t.Fatal("inbox ownership inspection failed")
	}
	if os.Getenv("PSModulePath") != filepath.Dir(poison) || os.Getenv("CB_POLICY_ACL_PATH") != "stale-private-path" {
		t.Fatal("parent environment changed")
	}
	pwsh, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal("PowerShell 7 is required for native qualification")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", `$env:PSModulePath = $PSHOME + '\Modules'; & $env:CB_POLICY_TEST_EXE '-test.run=^TestPolicyOwnershipPowerShell7Child$' '-test.v'; exit $LASTEXITCODE`)
	parent.Env = append(os.Environ(), "CB_POLICY_TEST_CHILD=1", "CB_POLICY_TEST_PATH="+path, "CB_POLICY_TEST_EXE="+exe, "CB_POLICY_TEST_VALID=false")
	if err := parent.Run(); err != nil {
		t.Fatal("PowerShell 7 inherited inspection failed")
	}
	if err := verifyOwnership(path); err == nil || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "S-1-") {
		t.Fatal("untrusted owner was accepted or leaked private evidence")
	}
	for _, stage := range []string{"item", "identity"} {
		cmd, err := policyOwnershipCommand(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if stage == "item" {
			cmd.Env = policyOwnershipEnvironment(os.Environ(), filepath.Join(t.TempDir(), "private-missing-policy"))
		} else {
			cmd.Args[len(cmd.Args)-1] = strings.Replace(cmd.Args[len(cmd.Args)-1], "$owner = $acl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value", "throw 'PRIVATE_IDENTITY_FAILURE'", 1)
		}
		out, err := cmd.Output()
		if err == nil || len(out) != 0 || strings.Contains(policyOwnershipProbeError(err), "PRIVATE") {
			t.Fatalf("%s failure was nonterminating or leaked output", stage)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd, err = policyOwnershipCommand(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Output(); err == nil {
		t.Fatal("cancelled inspection succeeded")
	}
}

func TestPolicyOwnershipPowerShell7Child(t *testing.T) {
	if os.Getenv("CB_POLICY_TEST_CHILD") != "1" {
		return
	}
	before := os.Getenv("PSModulePath")
	path := os.Getenv("CB_POLICY_TEST_PATH")
	cmd, err := policyOwnershipCommand(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil || !strings.HasPrefix(string(out), "OWNER|S-1-") {
		t.Fatal("PowerShell 7 inherited native inspection failed")
	}
	p, err := loadAt(path, verifyOwnership, time.Now())
	wantValid := os.Getenv("CB_POLICY_TEST_VALID") == "true"
	if wantValid && (err != nil || !p.RequireLock) {
		t.Fatal("legitimate policy was rejected under PowerShell 7 inheritance")
	}
	if !wantValid && err == nil {
		t.Fatal("invalid policy was accepted under PowerShell 7 inheritance")
	}
	if os.Getenv("PSModulePath") != before {
		t.Fatal("PowerShell 7 parent environment changed")
	}
}

func TestPolicyOwnershipAdministratorFixturesAndPowerShell7(t *testing.T) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	var elevated, length uint32
	if err := syscall.GetTokenInformation(token, syscall.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &length); err != nil {
		t.Fatal(err)
	}
	if elevated == 0 {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("native CI must exercise administrator-owned policy fixtures")
		}
		t.Skip("administrator-owned fixture requires an existing elevated test context; no elevation attempted")
	}
	pwsh, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal("PowerShell 7 is required for native qualification")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                 string
		owner, fileWrite, parentWrite, valid bool
	}{
		{"legitimate", true, false, false, true}, {"untrusted-owner", false, false, false, false}, {"file-writable", true, true, false, false}, {"parent-writable", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := policyFixture(t, tc.owner, tc.fileWrite, tc.parentWrite)
			p, err := loadAt(path, verifyOwnership, time.Now())
			if tc.valid && (err != nil || !p.RequireLock) {
				t.Fatal("legitimate fixture rejected")
			}
			if !tc.valid && err == nil {
				t.Fatal("invalid fixture accepted")
			}
			parent := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", `$env:PSModulePath = $PSHOME + '\Modules'; & $env:CB_POLICY_TEST_EXE '-test.run=^TestPolicyOwnershipPowerShell7Child$' '-test.v'; exit $LASTEXITCODE`)
			parent.Env = append(os.Environ(), "CB_POLICY_TEST_CHILD=1", "CB_POLICY_TEST_PATH="+path, "CB_POLICY_TEST_EXE="+exe, "CB_POLICY_TEST_VALID="+map[bool]string{true: "true", false: "false"}[tc.valid])
			if err := parent.Run(); err != nil {
				t.Fatal("PowerShell 7 inherited policy qualification failed")
			}
		})
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
	if err := secureWindowsACLVerdict("OWNER|S-1-5-18\nS-1-1-0|Allow|WriteData", true); err == nil {
		t.Fatal("untrusted file WriteData grant accepted")
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
