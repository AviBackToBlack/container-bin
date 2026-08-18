package main

import (
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
