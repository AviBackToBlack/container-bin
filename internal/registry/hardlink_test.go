package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

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
