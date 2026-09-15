//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package dockerrun

import (
	"os"
	"syscall"
)

func lockTraceFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockTraceFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
