//go:build windows

package selfupdate

import (
	"errors"
	"fmt"
	"os/user"
	"syscall"
	"unsafe"
)

const (
	sddlRevision1            = 1
	seFileObject             = 1
	daclSecurityInformation  = 0x00000004
	protectedDACLInformation = 0x80000000
)

var (
	advapi32                        = syscall.NewLazyDLL("advapi32.dll")
	kernel32                        = syscall.NewLazyDLL("kernel32.dll")
	convertStringSecurityDescriptor = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	getSecurityDescriptorDACL       = advapi32.NewProc("GetSecurityDescriptorDacl")
	setNamedSecurityInfo            = advapi32.NewProc("SetNamedSecurityInfoW")
	localFree                       = kernel32.NewProc("LocalFree")
)

// restrictStagingPath replaces inherited permissions with a protected DACL
// granting full control only to the user running the update. Administrators
// retain the normal Windows ability to take ownership, but receive no ACE here.
func restrictStagingPath(path string, directory bool) error {
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("identify current Windows user: %w", err)
	}
	if current.Uid == "" {
		return errors.New("current Windows user has no SID")
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sddl := fmt.Sprintf("D:P(A;%s;FA;;;%s)", flags, current.Uid)
	sddlPtr, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return fmt.Errorf("encode private staging DACL: %w", err)
	}

	var descriptor uintptr
	result, _, callErr := convertStringSecurityDescriptor.Call(
		uintptr(unsafe.Pointer(sddlPtr)),
		sddlRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 {
		return windowsAPIError("convert private staging DACL", callErr)
	}
	defer localFree.Call(descriptor)

	var present int32
	var defaulted int32
	var dacl uintptr
	result, _, callErr = getSecurityDescriptorDACL.Call(
		descriptor,
		uintptr(unsafe.Pointer(&present)),
		uintptr(unsafe.Pointer(&dacl)),
		uintptr(unsafe.Pointer(&defaulted)),
	)
	if result == 0 {
		return windowsAPIError("read private staging DACL", callErr)
	}
	if present == 0 || dacl == 0 {
		return errors.New("private staging security descriptor has no DACL")
	}

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode staging path: %w", err)
	}
	result, _, _ = setNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		seFileObject,
		daclSecurityInformation|protectedDACLInformation,
		0,
		0,
		dacl,
		0,
	)
	if result != 0 {
		return fmt.Errorf("set protected staging DACL: %w", syscall.Errno(result))
	}
	return nil
}

func windowsAPIError(action string, callErr error) error {
	if errno, ok := callErr.(syscall.Errno); ok && errno == 0 {
		return errors.New(action + " failed")
	}
	return fmt.Errorf("%s: %w", action, callErr)
}
