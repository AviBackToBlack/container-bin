//go:build windows

package selfupdate

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/user"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	convertSecurityDescriptorToStringForTest = advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	getNamedSecurityInfoForTest              = advapi32.NewProc("GetNamedSecurityInfoW")
)

func TestStageBesideRunningWindowsExecutableUsesProtectedUserOnlyDACL(t *testing.T) {
	installed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plan := stagingPlan()
	doer := doerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case plan.Checksums.URL:
			return assetResponse(req, bytes.Repeat([]byte("s"), int(plan.Checksums.Size))), nil
		case plan.Binary.URL:
			return assetResponse(req, bytes.Repeat([]byte("b"), int(plan.Binary.Size))), nil
		default:
			t.Fatalf("unexpected download URL: %s", req.URL)
			return nil, nil
		}
	})
	staged, err := (stager{doer: doer}).Stage(context.Background(), plan, installed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Cleanup() })

	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for path, flags := range map[string]string{
		staged.Dir:           "OICI",
		staged.BinaryPath:    "",
		staged.ChecksumsPath: "",
	} {
		dacl, err := stagingPathDACL(path)
		if err != nil {
			t.Fatalf("inspect %q DACL: %v", path, err)
		}
		wantACE := "(A;" + flags + ";FA;;;" + current.Uid + ")"
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

func stagingPathDACL(path string) (string, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var descriptor uintptr
	result, _, _ := getNamedSecurityInfoForTest.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		seFileObject,
		daclSecurityInformation,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 {
		return "", syscall.Errno(result)
	}
	defer localFree.Call(descriptor)

	var encoded uintptr
	var encodedLength uint32
	result, _, callErr := convertSecurityDescriptorToStringForTest.Call(
		descriptor,
		sddlRevision1,
		daclSecurityInformation,
		uintptr(unsafe.Pointer(&encoded)),
		uintptr(unsafe.Pointer(&encodedLength)),
	)
	if result == 0 {
		return "", windowsAPIError("encode staging security descriptor", callErr)
	}
	defer localFree.Call(encoded)
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(encoded)), int(encodedLength))), nil
}
