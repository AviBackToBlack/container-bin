//go:build !linux

package wslrun

func platformInputPeerClosed(error) bool {
	return false
}
