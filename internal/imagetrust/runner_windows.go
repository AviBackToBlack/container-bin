//go:build windows

package imagetrust

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var imageTrustKernel32 = syscall.NewLazyDLL("kernel32.dll")
var getWindowsDirectoryW = imageTrustKernel32.NewProc("GetWindowsDirectoryW")

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, executable string, args []string, dir string) ([]byte, []byte, error) {
	windowsDirectory, err := imageTrustWindowsDirectory()
	if err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(executable) || !filepath.IsAbs(dir) || filepath.Dir(executable) != dir {
		return nil, nil, errors.New("cosign verifier must execute from its exact private staging directory")
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"SystemRoot=" + windowsDirectory,
		"WINDIR=" + windowsDirectory,
		"HOME=" + dir,
		"USERPROFILE=" + dir,
		"LOCALAPPDATA=" + dir,
		"TEMP=" + dir,
		"TMP=" + dir,
		"COSIGN_YES=true",
		"NO_COLOR=1",
	}
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxVerifierOutput, maxVerifierOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if stdout.overflow || stderr.overflow {
		return stdout.data, stderr.data, errors.New("cosign verifier output exceeded the safety limit")
	}
	return stdout.data, stderr.data, err
}

func imageTrustWindowsDirectory() (string, error) {
	buffer := make([]uint16, 32768)
	n, _, callErr := getWindowsDirectoryW.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 {
		return "", fmt.Errorf("resolve system Windows directory: %w", callErr)
	}
	if n >= uintptr(len(buffer)) {
		return "", errors.New("resolve system Windows directory: returned path is too long")
	}
	path := syscall.UTF16ToString(buffer[:n])
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("resolved system Windows directory %q is not absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect system Windows directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("system Windows directory %q is not a directory", path)
	}
	return path, nil
}
