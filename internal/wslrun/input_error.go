package wslrun

import (
	"errors"
	"io"
	"net"
)

func inputPeerClosed(err error) bool {
	return errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || platformInputPeerClosed(err)
}
