//go:build windows

package policy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func verifyOwnership(path string) error {
	for _, candidate := range []string{filepath.Dir(path), path} {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", `$item = Get-Item -LiteralPath $env:CB_POLICY_ACL_PATH -Force; if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse point' }; $acl = Get-Acl -LiteralPath $env:CB_POLICY_ACL_PATH; $owner = $acl.Owner; try { $owner = ([System.Security.Principal.NTAccount]$acl.Owner).Translate([System.Security.Principal.SecurityIdentifier]).Value } catch {}; "OWNER|$owner"; $acl.Access | ForEach-Object { $sid = $_.IdentityReference.Value; try { $sid = $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value } catch {}; "$sid|$($_.AccessControlType)|$($_.FileSystemRights)" }`)
		cmd.Env = append(os.Environ(), "CB_POLICY_ACL_PATH="+candidate)
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("inspect %s permissions: %w", candidate, err)
		}
		if err := secureWindowsACLVerdict(string(out), candidate == path); err != nil {
			return fmt.Errorf("%s: %w", candidate, err)
		}
	}
	return nil
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
			blocked = append(blocked, "Write", "CreateFiles", "AppendData")
		}
		for _, right := range blocked {
			if strings.Contains(rights, right) {
				return fmt.Errorf("untrusted principal %s has %s", parts[0], right)
			}
		}
	}
	return nil
}
