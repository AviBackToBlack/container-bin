//go:build linux

package wslrun

import (
	"errors"
	"syscall"
)

func platformInputPeerClosed(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ECONNRESET)
}
