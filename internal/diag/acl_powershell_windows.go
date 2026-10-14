package diag

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Follow policy/selfupdate's system-directory resolution convention without
// changing their independent probes. Neither PATH nor environment-provided
// SystemRoot/WINDIR selects the executable for this security diagnostic.
func aclPowerShellExecutable() (string, error) {
	buffer := make([]uint16, 32768)
	getWindowsDirectory := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemWindowsDirectoryW")
	n, _, err := getWindowsDirectory.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 || n >= uintptr(len(buffer)) {
		return "", fmt.Errorf("resolve system Windows directory: %v", err)
	}
	return aclPowerShellExecutableAt(syscall.UTF16ToString(buffer[:n]))
}

func aclPowerShellExecutableAt(windowsDirectory string) (string, error) {
	if !filepath.IsAbs(windowsDirectory) {
		return "", fmt.Errorf("system Windows directory is not absolute")
	}
	path := filepath.Join(windowsDirectory, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("system Windows PowerShell must be a regular file")
	}
	return path, nil
}
