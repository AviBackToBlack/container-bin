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

// TestMountSpecWorkspaceRootComposition is a unit-level check of mountSpec's
// dst branch only: it pairs a comma-carrying workspace root with a src that
// does NOT share that comma. It does not, by itself, prove anything about
// what happens for a real comma-named project directory -- see
// TestRootBindMountCommaFailsClosedOnSrc below for that, and RM-6c in
// docs/windows-paths.md (row P15) for why the two scenarios are not the same.
func TestMountSpecWorkspaceRootComposition(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My, Project")
	tool := Tool{Provider: "stateful"}
	workspaceRoot := workspaceRootFor(tool, root)

	got, err := mountSpec("bind", "/clean/src", workspaceRoot)
	if err == nil {
		t.Fatalf("mountSpec with workspace root containing comma returned nil error; got %q", got)
	}
	if got != "" {
		t.Fatalf("mountSpec with workspace root containing comma returned non-empty string: %q", got)
	}
}

// TestRootBindMountCommaFailsClosedOnSrc pins the real call site (main.go,
// the "bind" mount that runTool builds for every provider's project root):
// src and the input to workspaceRootFor are the SAME root variable, so
// workspaceRoot's comma -- when it has one -- is always a substring of
// root's own characters, and root is passed as mountSpec's src. That means
// the src check (which mountSpec evaluates first) fails closed before the
// dst check for workspaceRoot could ever matter. This is RM-6c's finding
// (docs/windows-paths.md, row P15): no sanitization of workspaceRootFor's
// basename changes this outcome, because the failure never reaches dst.
func TestRootBindMountCommaFailsClosedOnSrc(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My, Project")
	tool := Tool{Provider: "stateful"}
	workspaceRoot := workspaceRootFor(tool, root) // same root as below, exactly like runTool

	got, err := mountSpec("bind", root, workspaceRoot)
	if err == nil {
		t.Fatalf("mountSpec(root, workspaceRoot) with a comma-named project returned nil error; got %q", got)
	}
	if got != "" {
		t.Fatalf("mountSpec(root, workspaceRoot) with a comma-named project returned non-empty string: %q", got)
	}
	// The src check runs first in mountSpec, so the error a user actually
	// sees names root (the offending src), not workspaceRoot -- checking the
	// offending path string itself, like the sibling tests above, ties this
	// to mountSpec's behavior rather than to its message prose.
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("error does not contain the offending src %q: %v", root, err)
	}
	// If the dst branch had fired instead, the message would name
	// workspaceRoot (mountSpec's dst error includes the offending dst
	// string) -- absence of that confirms the src branch is what actually
	// fired, without depending on either message's wording.
	if strings.Contains(err.Error(), workspaceRoot) {
		t.Fatalf("did not expect the error to name workspaceRoot %q (would mean the dst branch fired): %v", workspaceRoot, err)
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
