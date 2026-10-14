package diag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An owned copy of the test executable makes any PATH/environment selection
// regression observable before Go tries to parse PowerShell's command flags.
func TestMain(m *testing.M) {
	if os.Getenv("CB_TEST_DRIVE_PATH_CHILD") == "1" {
		if err := os.WriteFile(os.Getenv("CB_TEST_DRIVE_PATH_MARKER"), []byte("shadow executed"), 0600); err != nil {
			os.Exit(2)
		}
		fmt.Println("Network")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsDriveTypeIgnoresPATHAndWindowsEnvironment(t *testing.T) {
	root := t.TempDir()
	drive := filepath.VolumeName(root)
	wantExecutable, err := aclPowerShellExecutable()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := windowsDriveType(drive)
	if err != nil || actual != "Fixed" {
		t.Fatal("owned native fixture must be on a fixed local drive")
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
	t.Setenv("CB_DOCTOR_DRIVE", "stale-private-drive")
	t.Setenv("CB_TEST_DRIVE_PATH_CHILD", "1")
	t.Setenv("CB_TEST_DRIVE_PATH_MARKER", marker)
	got, err := windowsDriveType(drive)
	if err != nil || got != actual {
		t.Fatalf("PATH substitution changed the native classification: %q, %v", got, err)
	}
	t.Setenv("SystemRoot", root)
	t.Setenv("WINDIR", root)
	cmd, err := windowsDriveTypeCommand(context.Background(), drive)
	if err != nil || !strings.EqualFold(cmd.Path, wantExecutable) || !filepath.IsAbs(cmd.Path) {
		t.Fatal("drive probe did not select the native System32 executable")
	}
	// Spoofed Windows environment variables can also break PowerShell startup.
	// They must never select the shadow or yield a fabricated classification.
	got, err = windowsDriveType(drive)
	if (err == nil && got != actual) || (err != nil && got != "") {
		t.Fatal("Windows environment substitution fabricated a drive classification")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shadow executable ran or its marker could not be checked")
	}
	if os.Getenv("CB_DOCTOR_DRIVE") != "stale-private-drive" || os.Getenv("SystemRoot") != root {
		t.Fatal("probe mutated the parent environment")
	}
	t.Log("native fixed-drive result preserved under PATH substitution; SystemRoot/WINDIR cannot select shadows and startup failures remain unclassified")
}

func TestWindowsDriveTypeNativeProtocolAndFailures(t *testing.T) {
	// Exercise the real trusted PowerShell/.NET enum protocol for Network even
	// on runners without a mapped SMB drive. No network mapping is created.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := windowsDriveTypeCommand(ctx, "Z:")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args[len(cmd.Args)-1] = `[System.IO.DriveType]::Network`
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("trusted native network enum protocol failed")
	}
	got, err := windowsDriveTypeOutput(string(out))
	status, message := networkStorageVerdict("fixture", `Z:\fixture`, got)
	if err != nil || got != "Network" || status != "warn" || !strings.Contains(message, "mapped network drive") {
		t.Fatal("legitimate Network classification changed")
	}
	if got, err := windowsDriveType("PRIVATE_INVALID_DRIVE"); got != "" || err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("failed native classification was accepted or leaked input")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	cmd, err = windowsDriveTypeCommand(cancelled, "C:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Output(); err == nil {
		t.Fatal("cancelled native probe succeeded")
	}
	if cmd.WaitDelay != time.Second {
		t.Fatal("probe pipe wait is not bounded")
	}
}
