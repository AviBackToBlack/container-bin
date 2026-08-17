package main

import (
	"strings"
	"testing"
)

func TestDockerOSTypeVerdict(t *testing.T) {
	cases := []struct {
		raw    string
		status string
	}{
		{"linux", "ok"},
		{"linux\n", "ok"},
		{"Linux\r\n", "ok"},
		{"  linux  ", "ok"},
		{"windows", "fail"},
		{"Windows\r\n", "fail"},
		{"", "warn"},
		{"   \r\n", "warn"},
		{"moby-something-unknown", "warn"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
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
