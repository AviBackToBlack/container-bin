//go:build windows

package imagetrust

import (
	"os"
	"os/user"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	convertImageTrustDescriptorToStringForTest = imageTrustAdvapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	getImageTrustNamedSecurityInfoForTest      = imageTrustAdvapi32.NewProc("GetNamedSecurityInfoW")
)

func TestCreatePrivateStageAndSnapshotsUseProtectedUserOnlyDACL(t *testing.T) {
	dir, err := createPrivateStage()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	file, err := stageSnapshot(dir, verifierName, testSnapshot("cosign bytes"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for path, flags := range map[string]string{dir: "OICI", file: ""} {
		dacl, err := imageTrustPathDACL(path)
		if err != nil {
			t.Fatalf("inspect %q DACL: %v", path, err)
		}
		canonical, err := canonicalImageTrustDACL("D:P(A;" + flags + ";FA;;;" + current.Uid + ")")
		if err != nil {
			t.Fatalf("canonicalize expected DACL: %v", err)
		}
		aceStart := strings.Index(canonical, "(")
		if aceStart < 0 {
			t.Fatalf("canonical expected DACL has no ACE: %q", canonical)
		}
		wantACE := canonical[aceStart:]
		if !strings.HasPrefix(dacl, "D:P") || !strings.HasSuffix(dacl, wantACE) || strings.Count(dacl, "(") != 1 {
			t.Fatalf("%q DACL = %q, want one protected ACE %q", path, dacl, wantACE)
		}
		for _, broad := range []string{";;;WD)", ";;;AU)", ";;;BU)"} {
			if strings.Contains(dacl, broad) {
				t.Fatalf("%q DACL grants broad principal: %q", path, dacl)
			}
		}
	}
}

func canonicalImageTrustDACL(sddl string) (string, error) {
	sddlPtr, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return "", err
	}
	var descriptor uintptr
	result, _, callErr := convertImageTrustSecurityDescriptor.Call(
		uintptr(unsafe.Pointer(sddlPtr)),
		stagingSDDLRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 {
		return "", imageTrustWindowsAPIError("convert expected image trust DACL", callErr)
	}
	defer imageTrustLocalFree.Call(descriptor)
	return encodeImageTrustDACL(descriptor)
}

func imageTrustPathDACL(path string) (string, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var descriptor uintptr
	result, _, _ := getImageTrustNamedSecurityInfoForTest.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		stagingSEFileObject,
		stagingDACLSecurityInformation,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 {
		return "", syscall.Errno(result)
	}
	defer imageTrustLocalFree.Call(descriptor)
	return encodeImageTrustDACL(descriptor)
}

func encodeImageTrustDACL(descriptor uintptr) (string, error) {
	var encoded uintptr
	var encodedLength uint32
	result, _, callErr := convertImageTrustDescriptorToStringForTest.Call(
		descriptor,
		stagingSDDLRevision1,
		stagingDACLSecurityInformation,
		uintptr(unsafe.Pointer(&encoded)),
		uintptr(unsafe.Pointer(&encodedLength)),
	)
	if result == 0 {
		return "", imageTrustWindowsAPIError("encode image trust staging descriptor", callErr)
	}
	defer imageTrustLocalFree.Call(encoded)
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(encoded)), int(encodedLength))), nil
}
