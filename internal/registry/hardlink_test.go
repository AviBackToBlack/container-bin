package registry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInstallShimNamesFallbackSummary(t *testing.T) {
	for _, failLast := range []bool{false, true} {
		name := "complete"
		if failLast {
			name = "later_failure"
		}
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			wantErr := errors.New("later shim failed")
			install := func(_, dst string) (shimInstallResult, error) {
				switch filepath.Base(dst) {
				case "a.exe", "b.exe":
					return shimInstallResult{mode: "copy", linkFailure: "access denied"}, nil
				case "c.exe":
					return shimInstallResult{mode: "copy", linkFailure: "OS error 17"}, nil
				default:
					if failLast {
						return shimInstallResult{}, wantErr
					}
					return shimInstallResult{mode: "hardlink"}, nil
				}
			}
			err := installShimNamesAt(filepath.Join(t.TempDir(), "cb.exe"), []string{"z", "c", "b", "a"}, false, install, &out)
			if (failLast && !errors.Is(err, wantErr)) || (!failLast && err != nil) {
				t.Fatalf("installation error = %v", err)
			}
			got := out.String()
			first := "copy fallback for 1 shim(s): hardlink failed: OS error 17;"
			second := "copy fallback for 2 shim(s): hardlink failed: access denied;"
			if strings.Count(got, "hardlink failed:") != 2 || strings.Count(got, first) != 1 || strings.Count(got, second) != 1 || strings.Index(got, first) > strings.Index(got, second) {
				t.Fatalf("missing, repeated, miscounted or unsorted summaries:\n%s", got)
			}
			if strings.Count(got, "(copy fallback)") != 3 {
				t.Fatalf("successful copies missing from output:\n%s", got)
			}
		})
	}
}

func TestInstallShimReportsLinkFailureAndPreservesCopy(t *testing.T) {
	dir := t.TempDir()
	exe, dst := filepath.Join(dir, "cb.exe"), filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("executable bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	link := func(string, string) error {
		return &os.LinkError{Op: "link", Old: "private source", New: "private temporary path", Err: os.ErrPermission}
	}
	result, err := installShim(exe, dst, link, copyFile, os.Rename)
	if err != nil {
		t.Fatal(err)
	}
	if result.mode != "copy" || !strings.Contains(result.linkFailure, "access denied") || !strings.Contains(result.linkFailure, "source cb.exe") || strings.Contains(result.linkFailure, "private") {
		t.Fatalf("copy diagnostic = %+v", result)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "executable bytes" {
		t.Fatalf("copy bytes = %q, error %v", data, err)
	}
	copyErr := errors.New("copy interrupted")
	_, err = installShim(exe, dst, link, func(string, string) error { return copyErr }, os.Rename)
	if !errors.Is(err, copyErr) || !strings.Contains(err.Error(), "hardlink failed (access denied") || !strings.Contains(err.Error(), "copy fallback failed: copy interrupted") || strings.Contains(err.Error(), "private") {
		t.Fatalf("combined error = %v", err)
	}
}

func TestHardlinkFailureReasonSanitizesPathsAndUnknownText(t *testing.T) {
	for _, err := range []error{
		&os.LinkError{Old: "private source", New: "private temporary", Err: syscall.Errno(17)},
		errors.New("private arbitrary error with secrets"),
	} {
		reason := hardlinkFailureReason(err)
		if strings.Contains(reason, "private") || strings.Contains(reason, "secrets") || reason == "" {
			t.Fatalf("unsafe reason = %q", reason)
		}
	}
}
