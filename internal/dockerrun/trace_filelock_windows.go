//go:build windows

package dockerrun

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileExclusive = 0x00000002
	allLockBytes      = ^uint32(0)
)

var (
	kernel32DLL      = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32DLL.NewProc("LockFileEx")
	unlockFileExProc = kernel32DLL.NewProc("UnlockFileEx")
)

func lockTraceFile(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := lockFileExProc.Call(
		file.Fd(), lockFileExclusive, 0, uintptr(allLockBytes), uintptr(allLockBytes),
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}

func unlockTraceFile(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := unlockFileExProc.Call(
		file.Fd(), 0, uintptr(allLockBytes), uintptr(allLockBytes),
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}
