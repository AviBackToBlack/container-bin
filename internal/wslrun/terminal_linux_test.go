//go:build linux

package wslrun

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestInteractiveHostTerminalRejectsNonTerminalCharacterDevice(t *testing.T) {
	device, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	original := os.Stdin
	os.Stdin = device
	defer func() { os.Stdin = original }()

	if interactiveHostTerminal() {
		t.Fatal("/dev/null was classified as an interactive terminal")
	}
}

func TestStartHostEventsInterceptsSIGPIPE(t *testing.T) {
	events, stop, err := startHostEvents(false)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGPIPE); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.err != nil || event.signal != int(syscall.SIGPIPE) || event.resize {
			t.Fatalf("SIGPIPE event = %#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGPIPE was not intercepted")
	}
}
