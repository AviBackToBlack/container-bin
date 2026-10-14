//go:build windows

package policy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func verifyOwnership(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, candidate := range []string{filepath.Dir(path), path} {
		label := []string{"directory", "file"}[i]
		cmd, err := policyOwnershipCommand(ctx, candidate)
		if err != nil {
			return fmt.Errorf("machine-policy ownership inspector is unavailable")
		}
		out, err := cmd.Output()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("machine-policy %s inspection timed out", label)
			}
			return fmt.Errorf("machine-policy %s inspection failed: %s", label, policyOwnershipProbeError(err))
		}
		if err := secureWindowsACLVerdict(string(out), candidate == path); err != nil {
			return fmt.Errorf("machine-policy %s ownership or permissions are not trusted", label)
		}
	}
	return nil
}

// Keep this authorization probe independent of doctor's warning-only boundary.
func policyOwnershipCommand(ctx context.Context, path string) (*exec.Cmd, error) {
	powerShell, err := powerShellExecutable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, powerShell, "-NoProfile", "-NonInteractive", "-Command", `
$ErrorActionPreference = 'Stop'
$stage = 'module'
try {
    Import-Module ($PSHOME + '\Modules\Microsoft.PowerShell.Security\Microsoft.PowerShell.Security.psd1') -ErrorAction Stop
    Import-Module ($PSHOME + '\Modules\Microsoft.PowerShell.Management\Microsoft.PowerShell.Management.psd1') -ErrorAction Stop
    $stage = 'item'
    $item = Microsoft.PowerShell.Management\Get-Item -LiteralPath $env:CB_POLICY_ACL_PATH -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse point' }
    $stage = 'acl'
    $acl = Microsoft.PowerShell.Security\Get-Acl -LiteralPath $env:CB_POLICY_ACL_PATH -ErrorAction Stop
    $stage = 'identity'
    $owner = $acl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value
    if ([string]::IsNullOrEmpty($owner)) { throw 'missing owner' }
    $lines = @("OWNER|$owner")
    foreach ($rule in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) {
        $sid = $rule.IdentityReference.Value
        if ([string]::IsNullOrEmpty($sid)) { throw 'missing identity' }
        $lines += "$sid|$($rule.AccessControlType)|$($rule.FileSystemRights)"
    }
    $lines
} catch {
    [Console]::Error.WriteLine('CB_POLICY_ACL_' + $stage)
    exit 1
}
`)
	cmd.Env = policyOwnershipEnvironment(os.Environ(), path)
	cmd.WaitDelay = time.Second
	return cmd, nil
}

func policyOwnershipEnvironment(parent []string, path string) []string {
	child := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PSModulePath") || strings.EqualFold(key, "CB_POLICY_ACL_PATH") {
			continue
		}
		child = append(child, entry)
	}
	return append(child, "CB_POLICY_ACL_PATH="+path)
}

func policyOwnershipProbeError(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch strings.TrimSpace(string(exit.Stderr)) {
		case "CB_POLICY_ACL_module":
			return "inbox Windows PowerShell modules could not load"
		case "CB_POLICY_ACL_item":
			return "object could not be inspected or is a reparse point"
		case "CB_POLICY_ACL_acl":
			return "security descriptor could not be read"
		case "CB_POLICY_ACL_identity":
			return "security identity could not be determined"
		}
	}
	return "Windows PowerShell ownership probe could not complete"
}

var getSystemWindowsDirectoryW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemWindowsDirectoryW")

func powerShellExecutable() (string, error) {
	buffer := make([]uint16, 32768)
	n, _, callErr := getSystemWindowsDirectoryW.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 {
		return "", fmt.Errorf("resolve system Windows directory: %w", callErr)
	}
	if n >= uintptr(len(buffer)) {
		return "", fmt.Errorf("resolve system Windows directory: returned path is too long")
	}
	return powerShellExecutableAt(syscall.UTF16ToString(buffer[:n]))
}

func powerShellExecutableAt(windowsDirectory string) (string, error) {
	if !filepath.IsAbs(windowsDirectory) {
		return "", fmt.Errorf("resolve PowerShell: Windows directory %q is not absolute", windowsDirectory)
	}
	path := filepath.Join(windowsDirectory, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("resolve PowerShell %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("resolve PowerShell %s: executable must be a regular file", path)
	}
	return path, nil
}

func secureWindowsACLVerdict(raw string, policyFile bool) error {
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "OWNER|") {
		return fmt.Errorf("owner could not be determined")
	}
	owner := strings.TrimPrefix(lines[0], "OWNER|")
	if owner != "S-1-5-18" && owner != "S-1-5-32-544" {
		return fmt.Errorf("owner %q is not SYSTEM or Administrators", owner)
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 || parts[0] == "" || (parts[1] != "Allow" && parts[1] != "Deny") {
			return fmt.Errorf("permission entry could not be interpreted")
		}
		if parts[1] != "Allow" || parts[0] == "S-1-5-18" || parts[0] == "S-1-5-32-544" {
			continue
		}
		rights := parts[2]
		blocked := []string{"FullControl", "Modify", "Delete", "DeleteSubdirectoriesAndFiles", "TakeOwnership", "ChangePermissions"}
		if policyFile {
			blocked = append(blocked, "Write", "WriteData", "CreateFiles", "AppendData")
		}
		for _, right := range blocked {
			if strings.Contains(rights, right) {
				return fmt.Errorf("untrusted principal %s has %s", parts[0], right)
			}
		}
	}
	return nil
}
