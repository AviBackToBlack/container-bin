package aclaudit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel              = syscall.NewLazyDLL("kernel32.dll")
	advapi              = syscall.NewLazyDLL("advapi32.dll")
	getSecurityInfo     = advapi.NewProc("GetSecurityInfo")
	getDescriptorLength = advapi.NewProc("GetSecurityDescriptorLength")
	validDescriptor     = advapi.NewProc("IsValidSecurityDescriptor")
	localFree           = kernel.NewProc("LocalFree")
	finalPath           = kernel.NewProc("GetFinalPathNameByHandleW")
	volumeInfo          = kernel.NewProc("GetVolumeInformationByHandleW")
	queryDevice         = kernel.NewProc("QueryDosDeviceW")
	driveType           = kernel.NewProc("GetDriveTypeW")
	attributeInfo       = kernel.NewProc("GetFileInformationByHandleEx")
)

func Inspect(parent context.Context, req Request) Report {
	if len(req.Assets)+len(req.OptionalAssets) > maxObjects {
		var r Report
		r.missing("managed-asset count limit reached")
		return r
	}
	ctx, cancel := context.WithTimeout(parent, auditTimeout)
	defer cancel()
	req.Assets = append([]Asset(nil), req.Assets...)
	req.OptionalAssets = append([]Asset(nil), req.OptionalAssets...)
	return bounded(ctx, func(ctx context.Context, progress func(Report)) Report {
		s, early := qualify(ctx, req, progress)
		if len(early.incomplete) != 0 {
			progress(early.copy())
			return early
		}
		return run(ctx, req, s, readSnapshot, progress)
	})
}

func callerScope() (scope, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return scope{}, err
	}
	defer token.Close()
	u, err := token.GetTokenUser()
	if err != nil {
		return scope{}, err
	}
	sid, err := u.User.Sid.String()
	if err != nil {
		return scope{}, err
	}
	profile, err := token.GetUserProfileDirectory()
	if err != nil {
		return scope{}, err
	}
	return scope{Updater: sid, Profile: filepath.Clean(profile)}, nil
}

// Validate a caller-specific updater through both native profile membership and
// actual installation ownership, after checking every intermediate component.
// No caller exemption is inferred for machine-wide/custom paths.
func qualify(ctx context.Context, req Request, updates ...func(Report)) (scope, Report) {
	var r Report
	defer func() {
		if len(updates) != 0 {
			updates[0](r.copy())
		}
	}()
	s, err := callerScope()
	if err != nil {
		r.missing("native updater/profile identity unavailable")
		return s, r
	}
	if !filepath.IsAbs(req.Directory) || !within(req.Directory, s.Profile) || strings.EqualFold(filepath.Clean(req.Directory), s.Profile) {
		r.missing("installation is not a qualified per-user scope; machine-wide/custom managers are not declared")
		return s, r
	}
	path := filepath.Clean(req.Directory)
	var chain []string
	for n := 0; ; n++ {
		if n >= maxDepth {
			r.missing("qualification depth limit reached")
			return s, r
		}
		chain = append(chain, path)
		parent := filepath.Dir(path)
		if path == parent {
			break
		}
		path = parent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			r.missing("audit time limit reached")
			return s, r
		}
		node, err := readSnapshot(chain[i])
		if err != nil {
			r.missing("per-user qualification metadata/security read failed")
			return s, r
		}
		r.objects++
		if len(updates) != 0 {
			updates[0](r.copy())
		}
		if node.Reparse != 0 || !strings.EqualFold(filepath.Clean(node.Resolved), chain[i]) {
			classifyRedirection(&r, fmt.Sprintf("qualification ancestor[%d]", i), node, s.Updater)
			return s, r
		}
		if node.Filesystem != "NTFS" || !node.Directory {
			r.missing("qualification requires local NTFS directory components")
			return s, r
		}
		if i == 0 {
			if node.Security.Owner != s.Updater {
				r.missing("installation owner does not validate the updater SID")
				return s, r
			}
			s.Qualified = true
			s.Anchor = node
		}
	}
	return s, r
}

func qualifiedDrive(path string) error {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'a' && volume[0] <= 'z') || (volume[0] >= 'A' && volume[0] <= 'Z')) {
		return errors.New("non-drive path")
	}
	root, _ := syscall.UTF16PtrFromString(volume + `\`)
	kind, _, _ := driveType.Call(uintptr(unsafe.Pointer(root)))
	if kind != 3 {
		return errors.New("not a local fixed drive")
	}
	name, _ := syscall.UTF16PtrFromString(volume)
	var mapping [512]uint16
	n, _, err := queryDevice.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&mapping[0])), uintptr(len(mapping)))
	if n == 0 || n >= uintptr(len(mapping)) {
		return fmt.Errorf("drive mapping unavailable: %v", err)
	}
	if !qualifiedDeviceMapping(syscall.UTF16ToString(mapping[:])) {
		return errors.New("unqualified DOS device mapping")
	}
	return nil
}

func qualifiedDeviceMapping(mapping string) bool {
	suffix, ok := strings.CutPrefix(mapping, `\Device\HarddiskVolume`)
	if !ok || suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func handlePath(handle syscall.Handle, flags uintptr) (string, error) {
	buffer := make([]uint16, 32768)
	n, _, err := finalPath.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), flags)
	if n == 0 || n >= uintptr(len(buffer)) {
		return "", fmt.Errorf("handle path unavailable: %v", err)
	}
	return syscall.UTF16ToString(buffer[:n]), nil
}

func readSnapshot(path string) (snapshot, error) {
	var node snapshot
	if len(path) > 32760 {
		return node, errors.New("path limit")
	}
	if err := qualifiedDrive(path); err != nil {
		return node, err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return node, err
	}
	h, err := syscall.CreateFile(p, 0x20000|0x80, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, 0x02000000|0x00200000, 0)
	if err != nil {
		return node, err
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return node, err
	}
	node.Directory = info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0
	if node.Directory {
		var caseFlags uint32
		ok, _, _ := attributeInfo.Call(uintptr(h), 23, uintptr(unsafe.Pointer(&caseFlags)), unsafe.Sizeof(caseFlags))
		if ok == 0 || caseFlags != 0 {
			return node, errors.New("directory case semantics unqualified")
		}
	}
	var tags struct{ Attributes, Tag uint32 }
	ok, _, callErr := attributeInfo.Call(uintptr(h), 9, uintptr(unsafe.Pointer(&tags)), unsafe.Sizeof(tags))
	if ok == 0 {
		return node, fmt.Errorf("attribute/tag read: %v", callErr)
	}
	if tags.Attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		node.Reparse = tags.Tag
		if node.Reparse == 0 {
			return node, errors.New("reparse point without tag")
		}
	}
	var filesystem [32]uint16
	var serial, maximum, flags uint32
	ok, _, callErr = volumeInfo.Call(uintptr(h), 0, 0, uintptr(unsafe.Pointer(&serial)), uintptr(unsafe.Pointer(&maximum)), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&filesystem[0])), uintptr(len(filesystem)))
	if ok == 0 {
		return node, fmt.Errorf("volume inspection: %v", callErr)
	}
	node.Filesystem = syscall.UTF16ToString(filesystem[:])
	// A regular file may have several hardlink names. Ask for the opened leaf
	// name instead of comparing an arbitrary normalized sibling link name.
	// Directory chains still require normalized identity agreement; reparse
	// objects remain explicitly blocked regardless of their returned name.
	nameFlags := uintptr(0)
	if !node.Directory {
		nameFlags = 8 // FILE_NAME_OPENED
	}
	resolved, err := handlePath(h, nameFlags)
	if err != nil {
		return node, err
	}
	if !strings.HasPrefix(resolved, `\\?\`) || strings.HasPrefix(resolved, `\\?\UNC\`) {
		return node, errors.New("unqualified resolved namespace")
	}
	node.Resolved = filepath.Clean(strings.TrimPrefix(resolved, `\\?\`))
	guid, err := handlePath(h, 1)
	if err != nil {
		return node, err
	}
	end := strings.Index(guid, "}")
	if !strings.HasPrefix(guid, `\\?\Volume{`) || end < 0 || end > 60 {
		return node, errors.New("volume identity unavailable")
	}
	node.Identity = fmt.Sprintf("%s/%08x/%08x%08x", guid[:end+1], serial, info.FileIndexHigh, info.FileIndexLow)
	var sd *byte
	result, _, _ := getSecurityInfo.Call(uintptr(h), 1, 1|4, 0, 0, 0, 0, uintptr(unsafe.Pointer(&sd)))
	if result != 0 {
		return node, syscall.Errno(result)
	}
	if sd == nil {
		return node, errors.New("missing security descriptor")
	}
	defer localFree.Call(uintptr(unsafe.Pointer(sd)))
	ok, _, _ = validDescriptor.Call(uintptr(unsafe.Pointer(sd)))
	if ok == 0 {
		return node, errors.New("invalid native descriptor")
	}
	length, _, _ := getDescriptorLength.Call(uintptr(unsafe.Pointer(sd)))
	if length < 20 || length > maxDescriptor {
		return node, errors.New("descriptor size limit")
	}
	raw := append([]byte(nil), unsafe.Slice(sd, int(length))...)
	node.Security, err = decodeDescriptor(raw)
	return node, err
}
