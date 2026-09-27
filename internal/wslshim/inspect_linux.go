//go:build linux

package wslshim

import (
	"errors"
	"os"
	"syscall"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

// Inspect validates the fixed binary/shim boundary and reports whether each
// registry-derived tool shim is already ready or still missing. It never
// creates, replaces or removes a filesystem object.
func Inspect(layout hostenv.WSLLayout, names []string) (Result, error) {
	return inspect(layout, names, dependencies{
		currentRuntime: hostenv.Current,
		currentUID:     func() uint32 { return uint32(os.Getuid()) },
		lstat:          lstat,
		readlink:       os.Readlink,
	})
}

func lstat(path string) (fileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileInfo{}, errors.New("filesystem ownership is unavailable")
	}
	return fileInfo{Mode: info.Mode(), UID: stat.Uid}, nil
}
