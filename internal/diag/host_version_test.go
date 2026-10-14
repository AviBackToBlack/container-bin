package diag

import (
	"context"
	"strings"
	"testing"
)

func TestHostVersionOutputBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &hostVersionOutput{cancel: cancel}
	for _, chunk := range []string{strings.Repeat("a", hostVersionOutputLimit-1), "b"} {
		if n, err := out.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatal("bounded output was truncated")
		}
	}
	if ctx.Err() != nil || len(out.data) != hostVersionOutputLimit {
		t.Fatal("output at the limit was rejected")
	}
	if n, err := out.Write([]byte("overflow")); n != 0 || err == nil || ctx.Err() == nil || !out.exceeded || len(out.data) > hostVersionOutputLimit {
		t.Fatal("overflow was retained or failed to cancel the child")
	}
}

func TestWindowsHostVersionOutputProtocol(t *testing.T) {
	for _, raw := range []string{
		"windows=Microsoft Windows NT 10.0.26200.0\npowershell=5.1.22621.3672\n",
		"powershell=5.1.22621.3672\r\nwindows=Microsoft Windows NT 10.0.26200.0\r\n",
	} {
		if !validWindowsHostVersionOutput(raw) {
			t.Fatal("legitimate version fields were rejected")
		}
	}
	for _, raw := range []string{
		"", "PRIVATE_MALFORMED_OUTPUT", "windows=\npowershell=5.1",
		"windows=Windows\n", "powershell=5.1\n",
		"windows=Windows\npowershell=5.1\nPRIVATE=extra",
		"windows=Windows\npowershell=5.1\nwindows=duplicate",
		"windows=Win\x00dows\npowershell=5.1",
		"windows=Win\x1bdows\npowershell=5.1",
		"windows=Win\rPRIVATE\npowershell=5.1",
		"windows=Win\xffdows\npowershell=5.1",
	} {
		if validWindowsHostVersionOutput(raw) {
			t.Fatal("malformed or ambiguous version response was accepted")
		}
	}
}
