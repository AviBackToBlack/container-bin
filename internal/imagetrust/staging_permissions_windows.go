//go:build windows

package imagetrust

import (
	"errors"
	"fmt"
	"os/user"
	"syscall"
	"unsafe"
)

const (
	stagingSDDLRevision1            = 1
	stagingSEFileObject             = 1
	stagingDACLSecurityInformation  = 0x00000004
	stagingProtectedDACLInformation = 0x80000000
)

var (
	imageTrustAdvapi32                    = syscall.NewLazyDLL("advapi32.dll")
	convertImageTrustSecurityDescriptor   = imageTrustAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	getImageTrustSecurityDescriptorDACL   = imageTrustAdvapi32.NewProc("GetSecurityDescriptorDacl")
	setImageTrustNamedSecurityInformation = imageTrustAdvapi32.NewProc("SetNamedSecurityInfoW")
	imageTrustLocalFree                   = imageTrustKernel32.NewProc("LocalFree")
)

// restrictStagingPath replaces inherited permissions with a protected DACL
// granting full control only to the user running verification. Administrators
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
		return fmt.Errorf("encode private image trust DACL: %w", err)
	}

	var descriptor uintptr
	result, _, callErr := convertImageTrustSecurityDescriptor.Call(
		uintptr(unsafe.Pointer(sddlPtr)),
		stagingSDDLRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 {
		return imageTrustWindowsAPIError("convert private image trust DACL", callErr)
	}
	defer imageTrustLocalFree.Call(descriptor)

	var present int32
	var defaulted int32
	var dacl uintptr
	result, _, callErr = getImageTrustSecurityDescriptorDACL.Call(
		descriptor,
		uintptr(unsafe.Pointer(&present)),
		uintptr(unsafe.Pointer(&dacl)),
		uintptr(unsafe.Pointer(&defaulted)),
	)
	if result == 0 {
		return imageTrustWindowsAPIError("read private image trust DACL", callErr)
	}
	if present == 0 || dacl == 0 {
		return errors.New("private image trust security descriptor has no DACL")
	}

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode image trust staging path: %w", err)
	}
	result, _, _ = setImageTrustNamedSecurityInformation.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		stagingSEFileObject,
		stagingDACLSecurityInformation|stagingProtectedDACLInformation,
		0,
		0,
		dacl,
		0,
	)
	if result != 0 {
		return fmt.Errorf("set protected image trust DACL: %w", syscall.Errno(result))
	}
	return nil
}

func imageTrustWindowsAPIError(action string, callErr error) error {
	if errno, ok := callErr.(syscall.Errno); ok && errno == 0 {
		return errors.New(action + " failed")
	}
	return fmt.Errorf("%s: %w", action, callErr)
}
