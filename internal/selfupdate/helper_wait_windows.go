//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	processSynchronize = 0x00100000
	waitObject0        = 0x00000000
	waitTimeout        = 0x00000102
	waitFailed         = 0xffffffff
	errorInvalidPID    = syscall.Errno(87) // ERROR_INVALID_PARAMETER
)

var (
	openProcess         = syscall.NewLazyDLL("kernel32.dll").NewProc("OpenProcess")
	waitForSingleObject = syscall.NewLazyDLL("kernel32.dll").NewProc("WaitForSingleObject")
	closeHandle         = syscall.NewLazyDLL("kernel32.dll").NewProc("CloseHandle")
)

func waitForParentExit(ctx context.Context, pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return errors.New("invalid invoking process ID")
	}
	handle, _, callErr := openProcess.Call(processSynchronize, 0, uintptr(uint32(pid)))
	if handle == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorInvalidPID {
			return nil // The parent exited before the helper opened its handle.
		}
		return windowsAPIError("open invoking process", callErr)
	}
	defer closeHandle.Call(handle)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, _, callErr := waitForSingleObject.Call(handle, 100)
		switch result {
		case waitObject0:
			return nil
		case waitTimeout:
			continue
		case waitFailed:
			return windowsAPIError("wait for invoking process", callErr)
		default:
			return fmt.Errorf("wait for invoking process returned unexpected status 0x%x", result)
		}
	}
}

func launchHelperCleanup(helperDir string, parentPID int) error {
	if parentPID <= 0 || parentPID != os.Getpid() {
		return errors.New("invalid self-update helper cleanup parent PID")
	}
	helperDir = filepath.Clean(helperDir)
	if !filepath.IsAbs(helperDir) || !stringsHasHelperPrefix(filepath.Base(helperDir)) {
		return errors.New("invalid self-update helper cleanup directory")
	}
	windowsDirectory, err := systemWindowsDirectory()
	if err != nil {
		return err
	}
	powerShell := filepath.Join(windowsDirectory, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	info, err := os.Lstat(powerShell)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("system PowerShell is not an available regular file")
	}
	const script = `$ErrorActionPreference='Stop'; $p=Get-Process -Id ([int]$env:CB_HELPER_PID) -ErrorAction SilentlyContinue; if($null -ne $p){$p.WaitForExit()}; Remove-Item -LiteralPath $env:CB_HELPER_DIR -Recurse -Force`
	cmd := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Dir = windowsDirectory
	cmd.Env = []string{
		"SystemRoot=" + windowsDirectory,
		"WINDIR=" + windowsDirectory,
		"CB_HELPER_PID=" + helperPIDString(parentPID),
		"CB_HELPER_DIR=" + helperDir,
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func stringsHasHelperPrefix(name string) bool {
	return len(name) > len(helperDirPrefix) && strings.HasPrefix(name, helperDirPrefix)
}
