//go:build !windows

package diag

import "errors"

func aclPowerShellExecutable() (string, error) {
	return "", errors.New("Windows PowerShell ACL inspection requires Windows")
}
