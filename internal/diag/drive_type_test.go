package diag

import (
	"strings"
	"testing"
)

func TestWindowsDriveTypeOutputPreservesClassifications(t *testing.T) {
	for _, tc := range []struct {
		driveType, status string
	}{
		{"Unknown", "warn"}, {"NoRootDirectory", "warn"}, {"Removable", "ok"},
		{"Fixed", "ok"}, {"Network", "warn"}, {"CDRom", "ok"}, {"Ram", "ok"},
	} {
		t.Run(tc.driveType, func(t *testing.T) {
			got, err := windowsDriveTypeOutput("\r\n" + tc.driveType + "\r\n")
			if err != nil || got != tc.driveType {
				t.Fatalf("classification = %q, %v", got, err)
			}
			status, message := networkStorageVerdict("fixture", `Z:\fixture`, got)
			if status != tc.status {
				t.Fatalf("classification %s changed diagnostic status to %s", got, status)
			}
			if got == "Network" && !strings.Contains(message, "mapped network drive") {
				t.Fatal("network classification lost its mapped-drive warning")
			}
		})
	}
}

func TestWindowsDriveTypeOutputFailsClosed(t *testing.T) {
	for _, raw := range []string{"", "\r\n", "fixed", "Fixed\nNetwork", "7", "PRIVATE_OUTPUT"} {
		got, err := windowsDriveTypeOutput(raw)
		if err == nil || got != "" || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("invalid classification was accepted or leaked probe output")
		}
		if status, _ := networkStorageVerdict("fixture", `Z:\fixture`, got); status != "warn" {
			t.Fatal("failed classification produced a safe diagnostic")
		}
	}
}
