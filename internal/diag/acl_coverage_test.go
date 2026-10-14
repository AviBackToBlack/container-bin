package diag

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/aclaudit"
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

func TestNativeIncompleteCoverageDoesNotHideProbeFailure(t *testing.T) {
	report := aclaudit.Inspect(context.Background(), aclaudit.Request{Directory: "relative-unqualified-path"})
	for _, status := range []string{"ok", "warn"} {
		checks := installationACLDiagnostics(status, "probe evidence", report)
		if len(checks) < 2 || checks[0].status != status || !strings.Contains(checks[0].message, "probe evidence") {
			t.Fatalf("probe evidence lost: %+v", checks)
		}
		found := false
		for _, check := range checks[1:] {
			if check.status == "ok" {
				t.Fatal("incomplete native audit upgraded to OK")
			}
			if strings.Contains(check.message, "ancestor ACL coverage incomplete") {
				found = true
			}
		}
		if !found {
			t.Fatal("missing coverage warning")
		}
	}
}
