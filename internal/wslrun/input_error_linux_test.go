//go:build linux

package wslrun

import (
	"bytes"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestCopyToolInputTreatsLinuxPeerCloseAsCompletion(t *testing.T) {
	tests := []struct {
		name          string
		writeErr      error
		closeWriteErr error
	}{
		{name: "write EPIPE", writeErr: linuxNetworkError(syscall.EPIPE)},
		{name: "write reset", writeErr: linuxNetworkError(syscall.ECONNRESET)},
		{name: "half-close ENOTCONN", closeWriteErr: linuxNetworkError(syscall.ENOTCONN)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &fakeAttach{
				reader: bytes.NewReader(nil), writeDone: make(chan struct{}),
				writeErr: test.writeErr, closeWriteErr: test.closeWriteErr,
			}
			err := copyToolInput(stream, bytes.NewBufferString("input"))
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("copyToolInput() error = %v, want closed-pipe completion", err)
			}
			if !stream.closedWrite {
				t.Fatal("copyToolInput() did not attempt the stdin half-close")
			}
		})
	}
}

func linuxNetworkError(err error) error {
	return &net.OpError{Op: "write", Net: "unix", Err: err}
}
