package diag

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Investigation-only fixture: a copied test binary acts as the PATH executable.
func TestMain(m *testing.M) {
	if os.Getenv("CB_TEST_DRIVE_PATH_CHILD") == "1" {
		if err := os.WriteFile(os.Getenv("CB_TEST_DRIVE_PATH_MARKER"), []byte("owned fixture executed"), 0600); err != nil {
			os.Exit(2)
		}
		fmt.Println("Network")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsDriveTypePATHReproduction(t *testing.T) {
	root := t.TempDir()
	drive := filepath.VolumeName(root)
	trusted, err := aclPowerShellExecutable()
	if err != nil {
		t.Fatal(err)
	}
	control := exec.Command(trusted, "-NoProfile", "-NonInteractive", "-Command", `try { [System.IO.DriveInfo]::new($env:CB_DOCTOR_DRIVE).DriveType } catch { '' }`)
	control.Env = append(os.Environ(), "CB_DOCTOR_DRIVE="+drive)
	out, err := control.Output()
	if err != nil {
		t.Fatal("trusted drive-type control failed")
	}
	actual := strings.TrimSpace(string(out))
	if actual != "Fixed" {
		t.Fatal("reproduction requires its owned fixture on a fixed local drive")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "powershell.exe")
	if err := os.WriteFile(fake, data, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "executed.txt")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CB_TEST_DRIVE_PATH_CHILD", "1")
	t.Setenv("CB_TEST_DRIVE_PATH_MARKER", marker)
	got, err := windowsDriveType(drive)
	if err != nil || got != "Network" {
		t.Fatal("PATH substitution did not reproduce")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("owned PATH executable did not run")
	}
	t.Logf("trusted=%s; PATH-substituted=%s; owned executable marker verified", actual, got)
}
