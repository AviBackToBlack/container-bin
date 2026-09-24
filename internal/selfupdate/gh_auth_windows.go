//go:build windows

package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const githubCLIPublisher = "GitHub, Inc."

var (
	getSystemWindowsDirectoryW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemWindowsDirectoryW")
	shGetFolderPathW           = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetFolderPathW")
)

const csidlLocalAppData = 0x001c

func authenticateGitHubCLI(path string) (string, error) {
	canonical, beforeInfo, err := canonicalVerificationFile(path, "GitHub CLI executable")
	if err != nil {
		return "", err
	}
	beforeDigest, _, err := hashRegularFile(canonical, beforeInfo, "GitHub CLI executable")
	if err != nil {
		return "", err
	}

	windowsDirectory, err := systemWindowsDirectory()
	if err != nil {
		return "", err
	}
	powerShell := filepath.Join(windowsDirectory, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	info, err := os.Lstat(powerShell)
	if err != nil {
		return "", fmt.Errorf("resolve system PowerShell: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("system PowerShell is not a regular file")
	}

	const script = `$ErrorActionPreference = 'Stop'; $signature = Get-AuthenticodeSignature -LiteralPath $env:CB_GH_EXECUTABLE; if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or $null -eq $signature.SignerCertificate) { throw 'GitHub CLI Authenticode signature is not valid' }; $publisher = $signature.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false); [Console]::Out.Write($signature.Status.ToString() + '|' + $publisher)`
	cmd := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = []string{
		"SystemRoot=" + windowsDirectory,
		"WINDIR=" + windowsDirectory,
		"CB_GH_EXECUTABLE=" + canonical,
	}
	var stdout, stderr boundedBuffer
	stdout.limit = 4096
	stderr.limit = 4096
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(string(stderr.data))
		if message != "" {
			return "", fmt.Errorf("verify GitHub CLI Authenticode signature: %w: %s", err, message)
		}
		return "", fmt.Errorf("verify GitHub CLI Authenticode signature: %w", err)
	}
	if stdout.overflow || stderr.overflow {
		return "", errors.New("GitHub CLI Authenticode verification output exceeded the safety limit")
	}
	if string(stdout.data) != "Valid|"+githubCLIPublisher {
		return "", fmt.Errorf("GitHub CLI Authenticode publisher is not %q", githubCLIPublisher)
	}

	postPath, afterInfo, err := canonicalVerificationFile(canonical, "GitHub CLI executable")
	if err != nil {
		return "", err
	}
	afterDigest, _, err := hashRegularFile(postPath, afterInfo, "GitHub CLI executable")
	if err != nil {
		return "", err
	}
	if postPath != canonical || beforeDigest != afterDigest {
		return "", errors.New("GitHub CLI executable changed during authentication")
	}
	return beforeDigest, nil
}

func verifierBaseEnvironment() ([]string, error) {
	windowsDirectory, err := systemWindowsDirectory()
	if err != nil {
		return nil, err
	}
	localAppData, err := localAppDataDirectory()
	if err != nil {
		return nil, err
	}
	return []string{
		"SystemRoot=" + windowsDirectory,
		"WINDIR=" + windowsDirectory,
		"LOCALAPPDATA=" + localAppData,
	}, nil
}

func systemWindowsDirectory() (string, error) {
	buffer := make([]uint16, 32768)
	n, _, callErr := getSystemWindowsDirectoryW.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
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
	return path, nil
}

func localAppDataDirectory() (string, error) {
	buffer := make([]uint16, syscall.MAX_PATH)
	hr, _, _ := shGetFolderPathW.Call(
		0,
		csidlLocalAppData,
		0,
		0,
		uintptr(unsafe.Pointer(&buffer[0])),
	)
	if hr != 0 {
		return "", fmt.Errorf("resolve Windows LocalAppData known folder: HRESULT 0x%x", uint32(hr))
	}
	path := syscall.UTF16ToString(buffer)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("resolved Windows LocalAppData directory %q is not absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Windows LocalAppData directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("Windows LocalAppData known folder is not a directory")
	}
	return path, nil
}
