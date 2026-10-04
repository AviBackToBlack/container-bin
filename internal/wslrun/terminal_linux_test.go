//go:build linux

package wslrun

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestUsableTerminalSize(t *testing.T) {
	tests := []struct {
		name       string
		height     uint16
		width      uint16
		err        error
		wantHeight uint16
		wantWidth  uint16
		wantOK     bool
	}{
		{name: "measured", height: 24, width: 80, wantHeight: 24, wantWidth: 80, wantOK: true},
		{name: "zero rows", width: 80},
		{name: "zero columns", height: 24},
		{name: "unreadable", height: 24, width: 80, err: errors.New("temporary ioctl failure")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			height, width, ok := usableTerminalSize(0, func(uintptr) (uint16, uint16, error) {
				return test.height, test.width, test.err
			})
			if height != test.wantHeight || width != test.wantWidth || ok != test.wantOK {
				t.Fatalf("usableTerminalSize = (%d, %d, %t), want (%d, %d, %t)", height, width, ok, test.wantHeight, test.wantWidth, test.wantOK)
			}
		})
	}
}

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

func TestInteractiveTerminalPairRequiresBothStreams(t *testing.T) {
	tests := []struct {
		name      string
		stdinTTY  bool
		stdoutTTY bool
		want      bool
	}{
		{name: "both terminals", stdinTTY: true, stdoutTTY: true, want: true},
		{name: "redirected stdin", stdoutTTY: true},
		{name: "redirected stdout", stdinTTY: true},
		{name: "both redirected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := interactiveTerminalPair(1, 2, func(fd uintptr) (syscall.Termios, error) {
				if (fd == 1 && test.stdinTTY) || (fd == 2 && test.stdoutTTY) {
					return syscall.Termios{}, nil
				}
				return syscall.Termios{}, syscall.ENOTTY
			})
			if got != test.want {
				t.Fatalf("interactiveTerminalPair = %t, want %t", got, test.want)
			}
		})
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
