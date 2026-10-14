package diag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsHostVersionIgnoresPATHAndWindowsEnvironment(t *testing.T) {
	root := t.TempDir()
	wantExecutable, err := aclPowerShellExecutable()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := windowsHostVersionInfo()
	if err != nil {
		t.Fatal(err)
	}
	if windows, powershell := parseWindowsHostVersionInfo(actual); windows == "" || powershell == "" {
		t.Fatal("native probe did not preserve both version fields")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, shadow := range []string{filepath.Join(root, "powershell.exe"), filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")} {
		if err := os.MkdirAll(filepath.Dir(shadow), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shadow, data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "executed.txt")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CB_TEST_HOST_VERSION_CHILD", "1")
	t.Setenv("CB_TEST_HOST_VERSION_MARKER", marker)
	raw, err := windowsHostVersionInfo()
	if err != nil || raw != actual {
		t.Fatal("PATH substitution changed the native version fields")
	}
	t.Setenv("SystemRoot", root)
	t.Setenv("WINDIR", root)
	cmd, err := windowsHostVersionCommand(context.Background())
	if err != nil || !strings.EqualFold(cmd.Path, wantExecutable) || !filepath.IsAbs(cmd.Path) {
		t.Fatal("version probe did not select the native System32 executable")
	}
	// Spoofed Windows environment variables may break native PowerShell startup,
	// but must never select the owned shadow or fabricate a successful result.
	raw, err = windowsHostVersionInfo()
	if (err == nil && raw != actual) || (err != nil && (raw != "" || strings.Contains(err.Error(), root))) {
		t.Fatal("Windows environment substitution fabricated versions or leaked a path")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shadow executable ran or its marker could not be checked")
	}
	if os.Getenv("SystemRoot") != root || os.Getenv("WINDIR") != root {
		t.Fatal("probe mutated the parent environment")
	}
	t.Log("native windows=/powershell= fields preserved under PATH substitution; SystemRoot/WINDIR cannot select shadows")
}

func TestWindowsHostVersionNativeBoundsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		limit  time.Duration
	}{
		{"stdout_limit", `[Console]::Out.Write(('x' * 65536)); Start-Sleep -Seconds 60`, 15 * time.Second},
		{"stderr_limit", `[Console]::Error.Write(('x' * 65536)); Start-Sleep -Seconds 60`, 15 * time.Second},
		{"exit_failure", `[Console]::Out.Write('windows=PRIVATE_PARTIAL'); [Console]::Error.Write('PRIVATE_CHILD_ERROR'); exit 2`, 15 * time.Second},
		{"stderr_failure", `[Console]::Error.Write('PRIVATE_CHILD_ERROR'); exit 0`, 15 * time.Second},
		{"malformed_output", `[Console]::Out.Write('PRIVATE_MALFORMED_OUTPUT'); exit 0`, 15 * time.Second},
		{"deadline", `Start-Sleep -Seconds 60`, 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.limit)
			defer cancel()
			cmd, err := windowsHostVersionCommand(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Args[len(cmd.Args)-1] = tc.script
			started := time.Now()
			raw, err := runWindowsHostVersionCommand(cmd, cancel)
			if err == nil || raw != "" || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("failed probe returned output or unsanitized child data")
			}
			if strings.HasSuffix(tc.name, "_limit") && err.Error() != "Windows host-version output exceeded limit" {
				t.Fatal("oversized output was not rejected")
			}
			if time.Since(started) > 10*time.Second {
				t.Fatal("failed probe did not stop promptly")
			}
			if cmd.WaitDelay != time.Second {
				t.Fatal("probe pipe wait is not bounded")
			}
		})
	}
}
