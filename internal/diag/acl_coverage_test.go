package diag

import (
	"reflect"
	"strings"
	"testing"
)

func TestShimACLDiagnosticsPreserveProbeAndWarnOnceForCoverage(t *testing.T) {
	for _, status := range []string{"ok", "warn", "failed", ""} {
		t.Run(status, func(t *testing.T) {
			checks := shimACLDiagnostics(status, "probe result")
			want := "warn"
			if status == "ok" {
				want = "ok"
			}
			if len(checks) != 2 || checks[0].status != want || checks[0].message != "immediate shim-directory ACL check: probe result" {
				t.Fatalf("probe result lost or upgraded: %+v", checks)
			}
			coverage := checks[1]
			if coverage.status != "warn" {
				t.Fatalf("incomplete coverage = %s", coverage.status)
			}
			for _, subject := range []string{"ancestor ACL coverage incomplete", "ancestor directories", "redirected paths", "all managed files", "https://"} {
				if !strings.Contains(coverage.message, subject) {
					t.Errorf("coverage diagnostic omits %q", subject)
				}
			}
			if strings.Contains(coverage.message, `C:\`) || strings.Contains(coverage.message, `D:\`) || strings.Contains(coverage.message, "installation secure") {
				t.Fatalf("private path or false assurance: %s", coverage.message)
			}
			if !reflect.DeepEqual(checks, shimACLDiagnostics(status, "probe result")) {
				t.Fatal("nondeterministic diagnostic")
			}
		})
	}
}
