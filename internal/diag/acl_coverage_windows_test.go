package diag

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestShimACLCoverageWarnsAfterNativeProbeSuccessAndFailure(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, path := range []string{dir, filepath.Join(dir, "missing")} {
		status, message := inspectShimDirACL(path, u.Uid)
		checks := shimACLDiagnostics(status, message)
		if len(checks) != 2 || checks[1].status != "warn" || !strings.Contains(checks[1].message, "coverage incomplete") {
			t.Fatalf("native probe omitted coverage warning: %+v", checks)
		}
		if path != dir && (checks[0].status != "warn" || !strings.Contains(checks[0].message, "could not inspect")) {
			t.Fatalf("failed native probe upgraded or hidden: %+v", checks)
		}
	}
}
