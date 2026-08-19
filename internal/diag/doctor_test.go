package diag

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerOSTypeVerdict(t *testing.T) {
	cases := []struct {
		name, raw, status string
	}{
		{"linux", "linux", "ok"},
		{"linux_with_newline", "linux\n", "ok"},
		{"linux_crlf", "Linux\r\n", "ok"},
		{"linux_padded", "  linux  ", "ok"},
		{"windows", "windows", "fail"},
		{"windows_crlf", "Windows\r\n", "fail"},
		{"empty", "", "warn"},
		{"whitespace_only", "   \r\n", "warn"},
		{"unknown", "moby-something-unknown", "warn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _ := dockerOSTypeVerdict(c.raw)
			if status != c.status {
				t.Errorf("dockerOSTypeVerdict(%q) status = %q, want %q", c.raw, status, c.status)
			}
		})
	}

	_, windowsMsg := dockerOSTypeVerdict("windows")
	if !strings.Contains(strings.ToLower(windowsMsg), "switch to linux containers") {
		t.Errorf("windows message %q does not mention switching to Linux containers", windowsMsg)
	}

	_, unknownMsg := dockerOSTypeVerdict("moby-something-unknown")
	if !strings.Contains(unknownMsg, "moby-something-unknown") {
		t.Errorf("unknown message %q does not echo the value", unknownMsg)
	}

	allMessages := []string{
		"linux",
		"linux\n",
		"Linux\r\n",
		"  linux  ",
		"windows",
		"Windows\r\n",
		"",
		"   \r\n",
		"moby-something-unknown",
	}
	for _, raw := range allMessages {
		_, msg := dockerOSTypeVerdict(raw)
		if strings.Contains(msg, "%") {
			t.Errorf("message for %q contains %%: %q", raw, msg)
		}
	}
}

func TestShimDirACLVerdict(t *testing.T) {
	currentUser := "S-1-5-21-1111111111-2222222222-3333333333-1001"
	cases := []struct {
		name           string
		raw            string
		currentUserSID string
		wantStatus     string
		wantContains   []string
	}{
		{
			name: "only_trusted_full_control",
			raw: currentUser + "|Allow|FullControl\n" +
				"S-1-5-18|Allow|FullControl\n" +
				"S-1-5-32-544|Allow|FullControl",
			currentUserSID: currentUser,
			wantStatus:     "ok",
			wantContains:   []string{"not writable"},
		},
		{
			name:           "untrusted_full_control",
			raw:            "S-1-1-0|Allow|FullControl",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-1-0", "container-bin.toml", "installed shim"},
		},
		{
			name:           "untrusted_read_only",
			raw:            "S-1-1-0|Allow|ReadAndExecute, Synchronize",
			currentUserSID: currentUser,
			wantStatus:     "ok",
			wantContains:   []string{"not writable"},
		},
		{
			name:           "untrusted_write_alone",
			raw:            "S-1-5-32-545|Allow|Write",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-5-32-545"},
		},
		{
			name:           "untrusted_delete_subdirectories_and_files",
			raw:            "S-1-1-0|Allow|ReadAndExecute, DeleteSubdirectoriesAndFiles, Synchronize",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-1-0"},
		},
		{
			name:           "untrusted_take_ownership",
			raw:            "S-1-1-0|Allow|TakeOwnership",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-1-0"},
		},
		{
			name:           "untrusted_change_permissions",
			raw:            "S-1-1-0|Allow|ChangePermissions",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-1-0"},
		},
		{
			// If user.Current() fails, currentUserSID is "". An ACE whose SID field
			// also renders empty must still be treated as untrusted, not silently
			// matched to the empty currentUserSID fallback (main.go isTrusted guards
			// this with `sid != "" && sid == currentUserSID`).
			name:           "empty_sid_field_never_matches_empty_current_user_sid",
			raw:            "|Allow|FullControl",
			currentUserSID: "",
			wantStatus:     "warn",
			wantContains:   []string{"writable by other principal"},
		},
		{
			name: "deny_overrides_allow",
			raw: "S-1-1-0|Allow|FullControl\n" +
				"S-1-1-0|Deny|Write",
			currentUserSID: currentUser,
			wantStatus:     "ok",
			wantContains:   []string{"not writable"},
		},
		{
			name: "deny_before_allow",
			raw: "S-1-1-0|Deny|Write\n" +
				"S-1-1-0|Allow|FullControl",
			currentUserSID: currentUser,
			wantStatus:     "ok",
			wantContains:   []string{"not writable"},
		},
		{
			name: "two_untrusted_stable_order",
			raw: "S-1-5-21-1111111111-2222222222-3333333333-1002|Allow|FullControl\n" +
				"S-1-1-0|Allow|FullControl",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-1-0", "S-1-5-21-1111111111-2222222222-3333333333-1002"},
		},
		{
			name:           "empty_raw",
			raw:            "",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"could not determine"},
		},
		{
			name: "malformed_lines_skipped",
			raw: "garbage line\n" +
				"S-1-1-0|Allow|FullControl\n" +
				"too|many|fields|here\n" +
				"S-1-1-0\n" +
				"S-1-1-0|Unknown|FullControl",
			currentUserSID: currentUser,
			wantStatus:     "warn",
			wantContains:   []string{"S-1-1-0"},
		},
		{
			name:           "empty_current_user_sid_fallback",
			raw:            "S-1-5-18|Allow|FullControl\nS-1-5-32-544|Allow|FullControl",
			currentUserSID: "",
			wantStatus:     "ok",
			wantContains:   []string{"not writable"},
		},
	}

	var allMessages []string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, msg := shimDirACLVerdict(c.raw, c.currentUserSID)
			if status != c.wantStatus {
				t.Errorf("shimDirACLVerdict status = %q, want %q", status, c.wantStatus)
			}
			for _, want := range c.wantContains {
				if !strings.Contains(msg, want) {
					t.Errorf("shimDirACLVerdict message %q does not contain %q", msg, want)
				}
			}
		})
		_, msg := shimDirACLVerdict(c.raw, c.currentUserSID)
		allMessages = append(allMessages, msg)
	}

	// Determinism: same set of SIDs in opposite original order must produce the same message.
	forward := "S-1-1-0|Allow|FullControl\n" +
		"S-1-5-21-1111111111-2222222222-3333333333-1002|Allow|FullControl"
	reverse := "S-1-5-21-1111111111-2222222222-3333333333-1002|Allow|FullControl\n" +
		"S-1-1-0|Allow|FullControl"
	_, msgForward := shimDirACLVerdict(forward, currentUser)
	_, msgReverse := shimDirACLVerdict(reverse, currentUser)
	if msgForward != msgReverse {
		t.Errorf("shimDirACLVerdict is not deterministic: forward=%q reverse=%q", msgForward, msgReverse)
	}
	allMessages = append(allMessages, msgForward, msgReverse)

	for _, msg := range allMessages {
		if strings.Contains(msg, "%") {
			t.Errorf("message contains %%: %q", msg)
		}
	}
}

func TestReparsePointVerdict(t *testing.T) {
	cases := []struct {
		name         string
		literal      string
		resolved     string
		wantStatus   string
		wantContains []string
	}{
		{
			name:         "identical",
			literal:      `C:/Proj`,
			resolved:     `C:/Proj`,
			wantStatus:   "ok",
			wantContains: []string{"does not sit behind a reparse point"},
		},
		{
			name:         "case_difference_only",
			literal:      `C:/Proj`,
			resolved:     `c:/proj`,
			wantStatus:   "ok",
			wantContains: []string{"does not sit behind a reparse point"},
		},
		{
			name:         "trailing_separator_normalized",
			literal:      `C:/Proj/`,
			resolved:     `C:/Proj`,
			wantStatus:   "ok",
			wantContains: []string{"does not sit behind a reparse point"},
		},
		{
			name:     "dotdot_normalized",
			literal:  `C:/Proj/sub/../..`,
			resolved: `C:/`,
			// The literal C:/Proj/sub/../.. cleans to C:/, not C:/Proj, so both
			// forms match after Clean. This still exercises Clean normalization.
			wantStatus:   "ok",
			wantContains: []string{"does not sit behind a reparse point"},
		},
		{
			name:       "different_resolved_paths",
			literal:    `C:/link/proj`,
			resolved:   `C:/real/proj`,
			wantStatus: "warn",
			wantContains: []string{
				"does not resolve to itself",
				"P1",
				"P3",
				"supported",
				"do not traverse from inside the container",
			},
		},
	}

	var allMessages []string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, msg := reparsePointVerdict("current directory", c.literal, c.resolved)
			if status != c.wantStatus {
				t.Errorf("reparsePointVerdict status = %q, want %q", status, c.wantStatus)
			}
			for _, want := range c.wantContains {
				if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
					t.Errorf("reparsePointVerdict message %q does not contain %q", msg, want)
				}
			}
			if c.wantStatus == "warn" {
				wantLiteral := filepath.Clean(c.literal)
				wantResolved := filepath.Clean(c.resolved)
				if !strings.Contains(msg, wantLiteral) {
					t.Errorf("reparsePointVerdict message %q does not contain literal form %q", msg, wantLiteral)
				}
				if !strings.Contains(msg, wantResolved) {
					t.Errorf("reparsePointVerdict message %q does not contain resolved form %q", msg, wantResolved)
				}
			}
		})
		_, msg := reparsePointVerdict("current directory", c.literal, c.resolved)
		allMessages = append(allMessages, msg)
	}

	for _, msg := range allMessages {
		if strings.Contains(msg, "%") {
			t.Errorf("reparsePointVerdict message contains %%: %q", msg)
		}
	}
}

func TestNetworkStorageVerdict(t *testing.T) {
	cases := []struct {
		name         string
		path         string
		driveType    string
		wantStatus   string
		wantContains []string
	}{
		{
			name:         "unc_ignores_drive_type",
			path:         `\\server\share\proj`,
			driveType:    "Fixed",
			wantStatus:   "warn",
			wantContains: []string{`\\server\share\proj`, "P8", "UNC"},
		},
		{
			name:         "fixed_drive",
			path:         `C:/proj`,
			driveType:    "Fixed",
			wantStatus:   "ok",
			wantContains: []string{"Fixed"},
		},
		{
			name:         "network_drive",
			path:         `Z:/proj`,
			driveType:    "Network",
			wantStatus:   "warn",
			wantContains: []string{"Z:/proj", "P10", "mapped network drive"},
		},
		{
			name:         "removable_drive",
			path:         `D:/proj`,
			driveType:    "Removable",
			wantStatus:   "ok",
			wantContains: []string{"Removable"},
		},
		{
			name:         "empty_drive_type",
			path:         `C:/proj`,
			driveType:    "",
			wantStatus:   "warn",
			wantContains: []string{"could not determine"},
		},
		{
			// DriveInfo's own admission that it could not classify the drive is
			// the same "the probe did not succeed" situation as an empty result
			// and must not fall through to the ok branch.
			name:         "unknown_drive_type",
			path:         `C:/proj`,
			driveType:    "Unknown",
			wantStatus:   "warn",
			wantContains: []string{"could not determine"},
		},
		{
			name:         "no_root_directory_drive_type",
			path:         `C:/proj`,
			driveType:    "NoRootDirectory",
			wantStatus:   "warn",
			wantContains: []string{"could not determine"},
		},
	}

	var allMessages []string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, msg := networkStorageVerdict("current directory", c.path, c.driveType)
			if status != c.wantStatus {
				t.Errorf("networkStorageVerdict status = %q, want %q", status, c.wantStatus)
			}
			for _, want := range c.wantContains {
				if !strings.Contains(msg, want) {
					t.Errorf("networkStorageVerdict message %q does not contain %q", msg, want)
				}
			}
		})
		_, msg := networkStorageVerdict("current directory", c.path, c.driveType)
		allMessages = append(allMessages, msg)
	}

	for _, msg := range allMessages {
		if strings.Contains(msg, "%") {
			t.Errorf("networkStorageVerdict message contains %%: %q", msg)
		}
	}
}
