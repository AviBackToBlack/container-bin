//go:build windows

package terminal

import (
	"os"
	"syscall"
)

func platformTerminalPair(stdin, stdout *os.File) bool {
	return consolePair(stdin.Fd(), stdout.Fd(), syscall.GetConsoleMode)
}

func consolePair(stdin, stdout uintptr, query func(syscall.Handle, *uint32) error) bool {
	var mode uint32
	if err := query(syscall.Handle(stdin), &mode); err != nil {
		return false
	}
	return query(syscall.Handle(stdout), &mode) == nil
}
