package diag

type aclDiagnostic struct {
	status  string
	message string
}

// An immediate-directory result must never stand in for an installation audit.
// Emit one coverage warning per Windows doctor run, even when the probe fails.
func shimACLDiagnostics(status, message string) []aclDiagnostic {
	if status != "ok" {
		status = "warn"
	}
	return []aclDiagnostic{
		{status, "immediate shim-directory ACL check: " + message},
		{"warn", "ancestor ACL coverage incomplete: the immediate-directory check does not certify ancestor directories, redirected paths, or all managed files; review the full installation trust boundary before changing permissions: https://github.com/AviBackToBlack/container-bin/blob/main/docs/windows-acl.md"},
	}
}
