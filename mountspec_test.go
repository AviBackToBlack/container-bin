package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMountSpecBindSuccess(t *testing.T) {
	got, err := mountSpec("bind", "/some/src", "/some/dst")
	if err != nil {
		t.Fatalf("mountSpec returned unexpected error: %v", err)
	}
	want := "type=bind,src=/some/src,dst=/some/dst"
	if got != want {
		t.Fatalf("mountSpec output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestMountSpecVolumeSuccess(t *testing.T) {
	src := "cb-python-313-abc123def456"
	got, err := mountSpec("volume", src, "/venv")
	if err != nil {
		t.Fatalf("mountSpec returned unexpected error: %v", err)
	}
	want := "type=volume,src=" + src + ",dst=/venv"
	if got != want {
		t.Fatalf("mountSpec output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestMountSpecSourceComma(t *testing.T) {
	src := "/some, bad/src"
	got, err := mountSpec("bind", src, "/some/dst")
	if err == nil {
		t.Fatalf("mountSpec with comma in src returned nil error")
	}
	if got != "" {
		t.Fatalf("mountSpec with comma in src returned non-empty string: %q", got)
	}
	if !strings.Contains(err.Error(), src) {
		t.Fatalf("error does not contain offending src %q: %v", src, err)
	}
}

func TestMountSpecDestinationComma(t *testing.T) {
	dst := "/some, bad/dst"
	got, err := mountSpec("bind", "/some/src", dst)
	if err == nil {
		t.Fatalf("mountSpec with comma in dst returned nil error")
	}
	if got != "" {
		t.Fatalf("mountSpec with comma in dst returned non-empty string: %q", got)
	}
	if !strings.Contains(err.Error(), dst) {
		t.Fatalf("error does not contain offending dst %q: %v", dst, err)
	}
}

// The two failures must be tellable apart: a caller staring at a malformed
// mount needs to know which side carries the comma, and the messages are the
// only signal it gets.
func TestMountSpecCommaMessagesAreDistinguishable(t *testing.T) {
	_, srcErr := mountSpec("bind", "/bad, src", "/clean/dst")
	_, dstErr := mountSpec("bind", "/clean/src", "/bad, dst")
	if srcErr == nil || dstErr == nil {
		t.Fatalf("expected both to error: src=%v dst=%v", srcErr, dstErr)
	}
	if srcErr.Error() == dstErr.Error() {
		t.Fatalf("src and dst errors are indistinguishable: %v", srcErr)
	}
	if !strings.Contains(srcErr.Error(), "source") {
		t.Fatalf("src error does not name the source side: %v", srcErr)
	}
	if !strings.Contains(dstErr.Error(), "destination") {
		t.Fatalf("dst error does not name the destination side: %v", dstErr)
	}
}

func TestMountSpecBothCommas(t *testing.T) {
	src := "/bad, src"
	dst := "/bad, dst"
	got, err := mountSpec("bind", src, dst)
	if err == nil {
		t.Fatalf("mountSpec with commas in src and dst returned nil error")
	}
	if got != "" {
		t.Fatalf("mountSpec with commas returned non-empty string: %q", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, ",") {
		t.Fatalf("error does not identify comma problem: %v", err)
	}
}

func TestMountSpecWorkspaceRootComposition(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My, Project")
	tool := Tool{Provider: "stateful"}
	workspaceRoot := workspaceRootFor(tool, root)

	// The generated workspace root carries the comma from the project name and
	// therefore cannot be used as a Docker --mount dst, even when the src is
	// otherwise clean. This pins the real-world stateful project-directory case.
	got, err := mountSpec("bind", "/clean/src", workspaceRoot)
	if err == nil {
		t.Fatalf("mountSpec with workspace root containing comma returned nil error; got %q", got)
	}
	if got != "" {
		t.Fatalf("mountSpec with workspace root containing comma returned non-empty string: %q", got)
	}
}

func TestMountSpecOtherPunctuationAllowed(t *testing.T) {
	src := "/some path/with=colon:and spaces"
	dst := "/another path/with=equals:and spaces"
	got, err := mountSpec("bind", src, dst)
	if err != nil {
		t.Fatalf("mountSpec rejected comma-free path with other punctuation: %v", err)
	}
	want := "type=bind,src=" + src + ",dst=" + dst
	if got != want {
		t.Fatalf("mountSpec output mismatch:\n got: %q\nwant: %q", got, want)
	}
}
