package diag

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const hostVersionOutputLimit = 4096

func windowsHostVersionInfo() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := windowsHostVersionCommand(ctx)
	if err != nil {
		return "", err
	}
	return runWindowsHostVersionCommand(cmd, cancel)
}

func windowsHostVersionCommand(ctx context.Context) (*exec.Cmd, error) {
	powerShell, err := aclPowerShellExecutable()
	if err != nil {
		return nil, errors.New("Windows host-version executable unavailable")
	}
	cmd := exec.CommandContext(ctx, powerShell, "-NoProfile", "-NonInteractive", "-Command",
		`"windows=$([System.Environment]::OSVersion.VersionString)"; "powershell=$($PSVersionTable.PSVersion.ToString())"`)
	cmd.WaitDelay = time.Second
	return cmd, nil
}

func runWindowsHostVersionCommand(cmd *exec.Cmd, cancel context.CancelFunc) (string, error) {
	// Keep stdout and stderr separate and bounded. Neither a child error nor
	// partial output is returned on failure; it could contain private paths.
	stdout := &hostVersionOutput{cancel: cancel}
	stderr := &hostVersionOutput{cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return "", errors.New("Windows host-version output exceeded limit")
	}
	if err != nil || len(stderr.data) != 0 {
		return "", errors.New("Windows host-version probe failed")
	}
	raw := string(stdout.data)
	if !validWindowsHostVersionOutput(raw) {
		return "", errors.New("Windows host-version response invalid")
	}
	return raw, nil
}

func validWindowsHostVersionOutput(raw string) bool {
	if !utf8.ValidString(raw) {
		return false
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "windows" && key != "powershell") || seen[key] || strings.TrimSpace(value) == "" {
			return false
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return false
			}
		}
		seen[key] = true
	}
	return seen["windows"] && seen["powershell"]
}

type hostVersionOutput struct {
	data     []byte
	cancel   context.CancelFunc
	exceeded bool
}

func (out *hostVersionOutput) Write(p []byte) (int, error) {
	if len(p) > hostVersionOutputLimit-len(out.data) {
		out.exceeded = true
		out.cancel()
		return 0, io.ErrShortBuffer
	}
	out.data = append(out.data, p...)
	return len(p), nil
}
