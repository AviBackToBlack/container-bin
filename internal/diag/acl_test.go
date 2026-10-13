package diag

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestShimDirACLChildEnvironment(t *testing.T) {
	t.Setenv("PSModulePath", "private incompatible module path")
	t.Setenv("CB_DOCTOR_ACL_DIR", "stale directory")
	cmd := shimDirACLCommand(context.Background(), "requested directory")
	count := 0
	for _, entry := range cmd.Env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PSModulePath") {
			t.Fatal("child inherits PSModulePath")
		}
		if strings.EqualFold(key, "CB_DOCTOR_ACL_DIR") {
			count++
			if value != "requested directory" {
				t.Fatalf("child ACL path = %q", value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("ACL path environment count = %d", count)
	}
}

func TestShimDirACLProbeErrorsAreSanitized(t *testing.T) {
	for _, stage := range []string{"module-load", "acl-read", "identity-translation", "private unexpected stderr\nwith secrets"} {
		msg := shimDirACLProbeError(&exec.ExitError{Stderr: []byte("CB_ACL_ERROR|" + stage)})
		if !strings.Contains(msg, "could not inspect") || strings.Contains(msg, "private") || strings.Contains(msg, "secrets") {
			t.Fatalf("probe message = %q", msg)
		}
	}
}
