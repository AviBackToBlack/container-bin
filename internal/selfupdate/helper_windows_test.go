//go:build windows

package selfupdate

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperCoordinatorUsesProtectedUserOnlyDACL(t *testing.T) {
	fixture := newHelperFixture(t)
	var helperExecutable, requestPath string
	if err := (helperCoordinator{
		restrict: restrictStagingPath,
		start: func(executable, request string) error {
			helperExecutable, requestPath = executable, request
			return nil
		},
	}).launch(fixture.plan, fixture.staged, fixture.verified, fixture.installed, fixture.gh, 4242); err != nil {
		t.Fatal(err)
	}
	helperDir := filepath.Dir(helperExecutable)
	t.Cleanup(func() { _ = os.RemoveAll(helperDir) })
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for path, flags := range map[string]string{helperDir: "OICI", helperExecutable: "", requestPath: ""} {
		dacl, err := stagingPathDACL(path)
		if err != nil {
			t.Fatalf("inspect %q DACL: %v", path, err)
		}
		canonical, err := canonicalDACL("D:P(A;" + flags + ";FA;;;" + current.Uid + ")")
		if err != nil {
			t.Fatalf("canonicalize expected DACL: %v", err)
		}
		aceStart := strings.Index(canonical, "(")
		if aceStart < 0 {
			t.Fatalf("canonical expected DACL has no ACE: %q", canonical)
		}
		wantACE := canonical[aceStart:]
		if !strings.HasPrefix(dacl, "D:P") || !strings.HasSuffix(dacl, wantACE) || strings.Count(dacl, "(") != 1 {
			t.Fatalf("%q DACL = %q, want one protected ACE %q", path, dacl, wantACE)
		}
	}
}

func TestWaitForParentExitRejectsCurrentProcess(t *testing.T) {
	if err := waitForParentExit(context.Background(), os.Getpid()); err == nil || !strings.Contains(err.Error(), "invalid invoking process ID") {
		t.Fatalf("waitForParentExit(current) = %v", err)
	}
}
