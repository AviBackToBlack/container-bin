package aclaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// All writes below are fixture setup. Production contains no descriptor setter.
func fixtureSecurity(t *testing.T, path, sddl string, assignOwner ...bool) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	var sd *byte
	ok, _, err := advapi.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW").Call(uintptr(unsafe.Pointer(p)), 1, uintptr(unsafe.Pointer(&sd)), 0)
	if ok == 0 {
		t.Fatalf("fixture SDDL: %v", err)
	}
	defer localFree.Call(uintptr(unsafe.Pointer(sd)))
	var owner, dacl *byte
	var present, defaulted int32
	ok, _, err = advapi.NewProc("GetSecurityDescriptorOwner").Call(uintptr(unsafe.Pointer(sd)), uintptr(unsafe.Pointer(&owner)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 {
		t.Fatalf("fixture owner: %v", err)
	}
	ok, _, err = advapi.NewProc("GetSecurityDescriptorDacl").Call(uintptr(unsafe.Pointer(sd)), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 || present == 0 {
		t.Fatalf("fixture DACL: %v", err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	information := uintptr(4 | 0x80000000)
	if len(assignOwner) != 0 && assignOwner[0] {
		information |= 1
	}
	code, _, _ := advapi.NewProc("SetNamedSecurityInfoW").Call(uintptr(unsafe.Pointer(name)), 1, information, uintptr(unsafe.Pointer(owner)), 0, uintptr(unsafe.Pointer(dacl)), 0)
	if code != 0 {
		t.Fatalf("set owned fixture descriptor: %v", syscall.Errno(code))
	}
}

func nativeFixture(t *testing.T) (string, string) {
	t.Helper()
	s, err := callerScope()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(s.Profile, "cb-acl-audit-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !within(dir, s.Profile) || !strings.HasPrefix(filepath.Base(dir), "cb-acl-audit-test-") {
			t.Error("unsafe fixture cleanup target")
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("owned fixture cleanup: %v", err)
		}
	})
	fixtureSecurity(t, dir, fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", s.Updater, s.Updater), true)
	return dir, s.Updater
}

func TestNativeReaderPreservesNumericDescriptorAndDoesNotMutate(t *testing.T) {
	dir, sid := nativeFixture(t)
	file := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(file, []byte("owned fixture bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSecurity(t, file, fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;0x00000002;;;WD)", sid, sid), true)
	before, err := readSnapshot(file)
	if err != nil {
		t.Fatal(err)
	}
	if before.Filesystem != "NTFS" || before.Security.Owner != sid || before.Identity == "" || before.Security.Control&0x1000 == 0 {
		t.Fatalf("missing native evidence: %+v", before)
	}
	found := false
	for _, a := range before.Security.ACEs {
		if a.SID == "S-1-1-0" && a.Mask == 2 && a.Type == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("numeric ACE not preserved: %+v", before.Security.ACEs)
	}
	r := Inspect(context.Background(), Request{Directory: dir, Assets: []Asset{{file, "shim tool"}}})
	if !strings.Contains(textOf(r), "shim tool: ACE[") || !strings.Contains(textOf(r), "file content write") {
		t.Fatal("writable managed file missed: " + textOf(r))
	}
	after, err := readSnapshot(file)
	if err != nil || !sameSnapshot(before, after) {
		t.Fatalf("reader mutated fixture: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "owned fixture bytes" {
		t.Fatal("fixture bytes changed")
	}
}

func TestNativeHardlinkedShimUsesOpenedNameAndSharedIdentity(t *testing.T) {
	dir, sid := nativeFixture(t)
	binary := filepath.Join(dir, "cb.exe")
	shim := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(binary, []byte("owned hardlink fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSecurity(t, binary, fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;0x2;;;WD)", sid, sid), true)
	if err := os.Link(binary, shim); err != nil {
		t.Fatal(err)
	}
	first, err := readSnapshot(binary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readSnapshot(shim)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity != second.Identity || !strings.EqualFold(first.Resolved, binary) || !strings.EqualFold(second.Resolved, shim) || first.Security.Hash != second.Security.Hash {
		t.Fatal("hardlink opened names or shared native identity not preserved")
	}
	r := Inspect(context.Background(), Request{Directory: dir, Assets: []Asset{{binary, "cb.exe"}, {shim, "shim python"}}})
	text := textOf(r)
	if strings.Contains(text, "resolved path identity differs") || !strings.Contains(text, "cb.exe: ACE[") || !strings.Contains(text, "shim python: ACE[") {
		t.Fatal(text)
	}
	after, err := readSnapshot(shim)
	if err != nil || !sameSnapshot(second, after) {
		t.Fatal("hardlink snapshot changed during read-only audit")
	}
}

func TestNativeOrdinaryAbsentLockfileIsOptional(t *testing.T) {
	dir, _ := nativeFixture(t)
	lock := Asset{filepath.Join(dir, "container-bin.lock"), "lockfile"}
	r := Inspect(context.Background(), Request{Directory: dir, OptionalAssets: []Asset{lock}})
	for _, reason := range r.incomplete {
		if strings.Contains(reason, "lockfile") {
			t.Fatal("ordinary absent lockfile reported as inspection failure: " + reason)
		}
	}
}

func TestNativeQualificationDoesNotTrustArbitraryCallerPaths(t *testing.T) {
	dir, _ := nativeFixture(t)
	if _, err := readSnapshot(filepath.VolumeName(dir) + `\`); err != nil {
		t.Fatalf("native volume-root metadata under qualification token: %v", err)
	}
	s, r := qualify(context.Background(), Request{Directory: dir})
	if !s.Qualified || len(r.incomplete) != 0 {
		t.Fatal("owned per-user scope did not qualify: " + textOf(r))
	}
	profile, err := callerScope()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{profile.Profile, filepath.VolumeName(dir) + `\`, "relative"} {
		r := Inspect(context.Background(), Request{Directory: path})
		if len(r.incomplete) == 0 {
			t.Fatalf("unqualified path trusted: %s", path)
		}
	}
	var elevation uint32
	var length uint32
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	if err := syscall.GetTokenInformation(token, syscall.TokenElevation, (*byte)(unsafe.Pointer(&elevation)), 4, &length); err != nil {
		t.Fatal(err)
	}
	t.Logf("native qualification: architecture=%s elevated=%t (no privilege changes)", runtime.GOARCH, elevation != 0)
}

func checkNonElevatedQualification(t *testing.T) {
	t.Helper()
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	var elevated, length uint32
	if err := syscall.GetTokenInformation(token, syscall.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &length); err != nil {
		t.Fatal(err)
	}
	if elevated != 0 {
		t.Fatal("qualification child is still elevated")
	}
	dir, _ := nativeFixture(t)
	if _, err := readSnapshot(filepath.VolumeName(dir) + `\`); err != nil {
		t.Fatalf("native volume-root metadata under qualification token: %v", err)
	}
	s, r := qualify(context.Background(), Request{Directory: dir})
	if !s.Qualified || len(r.incomplete) != 0 {
		t.Fatal("nonelevated native qualification failed: " + textOf(r))
	}
	before, err := readSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = Inspect(context.Background(), Request{Directory: dir})
	after, err := readSnapshot(dir)
	if err != nil || !sameSnapshot(before, after) {
		t.Fatalf("nonelevated scan changed fixture: %v", err)
	}
	t.Logf("qualified native Windows/%s with TokenElevation=0; no separate identity or replacement experiment", runtime.GOARCH)
}

func TestNativeNonElevatedAuditChild(t *testing.T) {
	if os.Getenv("CB_ACL_AUDIT_CHILD") != "1" {
		return
	}
	checkNonElevatedQualification(t)
}

func TestNativeNonElevatedQualification(t *testing.T) {
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	var token syscall.Token
	if err := syscall.OpenProcessToken(process, syscall.TOKEN_QUERY|syscall.TOKEN_DUPLICATE|syscall.TOKEN_ASSIGN_PRIMARY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	var elevated, length uint32
	if err := syscall.GetTokenInformation(token, syscall.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &length); err != nil {
		t.Fatal(err)
	}
	if elevated == 0 {
		checkNonElevatedQualification(t)
	}
	// Elevated CI must qualify the real native reader under a reduced primary
	// token too. This removes privileges/Admin authority; it creates no account
	// and never elevates a child or modifies the parent's token.
	admin, err := syscall.StringToSid("S-1-5-32-544")
	if err != nil {
		t.Fatal(err)
	}
	disabled := syscall.SIDAndAttributes{Sid: admin}
	var reduced syscall.Token
	ok, _, err := advapi.NewProc("CreateRestrictedToken").Call(uintptr(token), 1|4, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&reduced)))
	if ok == 0 {
		t.Fatalf("create nonelevated qualification token: %v", err)
	}
	defer reduced.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestNativeNonElevatedAuditChild$", "-test.v", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "CB_ACL_AUDIT_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: reduced}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nonelevated native qualification child: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

func TestNativeRegistryLockAndRecoveryAssetsAreAuditedIndividually(t *testing.T) {
	dir, sid := nativeFixture(t)
	request := Request{Directory: dir}
	for _, asset := range []Asset{{filepath.Join(dir, "container-bin.toml"), "registry"}, {filepath.Join(dir, "container-bin.lock"), "lockfile"}} {
		if err := os.WriteFile(asset.Path, []byte("owned fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		fixtureSecurity(t, asset.Path, fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;0x2;;;WD)", sid, sid), true)
		request.Assets = append(request.Assets, asset)
	}
	backup := Asset{filepath.Join(dir, "container-bin.toml.bak"), "registry recovery backup"}
	request.OptionalAssets = []Asset{backup}
	r := Inspect(context.Background(), request)
	for _, label := range []string{"registry", "lockfile"} {
		if !strings.Contains(textOf(r), label+": ACE[") {
			t.Fatal("writable managed configuration missed: " + textOf(r))
		}
	}
	for _, reason := range r.incomplete {
		if strings.Contains(reason, "registry recovery backup") {
			t.Fatal("ordinary absent backup treated as inspection error: " + reason)
		}
	}
	if err := os.WriteFile(backup.Path, []byte("owned recovery input"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSecurity(t, backup.Path, fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;0x2;;;WD)", sid, sid), true)
	r = Inspect(context.Background(), request)
	if !strings.Contains(textOf(r), "registry recovery backup: ACE[") {
		t.Fatal("present recovery input missed: " + textOf(r))
	}
}

func TestNativeNullAndEmptyDACLRemainDistinct(t *testing.T) {
	dir, sid := nativeFixture(t)
	file := filepath.Join(dir, "data")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	// Restore access to this owned fixture before cleanup if the empty DACL
	// prevents an ordinary metadata read. Owner WRITE_DAC is sufficient.
	defer fixtureSecurity(t, file, fmt.Sprintf("O:%sD:P(A;;FA;;;%s)", sid, sid))
	fixtureSecurity(t, file, fmt.Sprintf("O:%sD:NO_ACCESS_CONTROL", sid))
	node, err := readSnapshot(file)
	if err != nil || !node.Security.NullDACL {
		t.Fatalf("null DACL: %v %+v", err, node.Security)
	}
	fixtureSecurity(t, file, fmt.Sprintf("O:%sD:P", sid))
	node, err = readSnapshot(file)
	if err == nil && node.Security.NullDACL {
		t.Fatal("empty DACL confused with null DACL")
	}
	r := Inspect(context.Background(), Request{Directory: dir, Assets: []Asset{{file, "empty-dacl asset"}}})
	if err != nil && len(r.incomplete) == 0 {
		t.Fatal("unreadable empty-DACL metadata reported complete")
	}
}

func TestNativeJunctionAndSymlinkCoverageIsIncomplete(t *testing.T) {
	dir, sid := nativeFixture(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"junction", "symlink", "volume-root-junction"} {
		t.Run(kind, func(t *testing.T) {
			link := filepath.Join(dir, kind)
			t.Cleanup(func() {
				if within(link, dir) {
					if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
						t.Errorf("remove owned redirection object: %v", err)
					}
				}
			})
			destination := target
			if kind == "volume-root-junction" {
				destination = filepath.VolumeName(dir) + `\`
			}
			if kind == "symlink" {
				if err := os.Symlink(destination, link); err != nil {
					if errors.Is(err, syscall.Errno(1314)) {
						t.Skip("symlink fixture privilege unavailable; incomplete semantics covered by model tests")
					}
					t.Fatal(err)
				}
			} else {
				cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, destination)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("owned junction fixture: %v %s", err, out)
				}
			}
			node, err := readSnapshot(link)
			if err != nil {
				t.Fatal(err)
			}
			r := Inspect(context.Background(), Request{Directory: link})
			if len(r.incomplete) == 0 || !strings.Contains(textOf(r), "redirection") {
				t.Fatal("redirection reported safe: " + textOf(r))
			}
			if node.Security.Owner == sid && strings.Contains(textOf(r), "owner SID "+sid+" may control") {
				t.Fatal("validated redirection owner reported untrusted: " + textOf(r))
			}
		})
	}
}

func TestNativeDOSDeviceMappingsAreNotAssumedLocalIdentity(t *testing.T) {
	// Query only: never modify an existing host mapping. An intentionally
	// unqualified SUBST/network namespace is tested through the same numeric
	// mapping classifier as qualifiedDrive.
	for _, mapping := range []string{`\??\C:\owned-fixture`, `\Device\Mup\server\share`, `\Device\LanmanRedirector\share`} {
		if qualifiedDeviceMapping(mapping) {
			t.Fatalf("redirected DOS mapping accepted: %q", mapping)
		}
	}
	if !qualifiedDeviceMapping(`\Device\HarddiskVolume3`) {
		t.Fatal("local volume mapping rejected")
	}
}

func TestNativeOwnedSUBSTIsIncomplete(t *testing.T) {
	dir, _ := nativeFixture(t)
	var name string
	for _, candidate := range []string{"Y:", "X:", "W:", "V:", "U:"} {
		p, _ := syscall.UTF16PtrFromString(candidate)
		var mapping [1024]uint16
		n, _, err := queryDevice.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&mapping[0])), uintptr(len(mapping)))
		if n == 0 && errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			name = candidate
			break
		}
	}
	if name == "" {
		t.Skip("no unused DOS drive letter for isolated SUBST fixture")
	}
	device, _ := syscall.UTF16PtrFromString(name)
	target, _ := syscall.UTF16PtrFromString(`\??\` + dir)
	define := kernel.NewProc("DefineDosDeviceW")
	ok, _, err := define.Call(1|8, uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(target)))
	if ok == 0 {
		t.Fatalf("create owned SUBST fixture: %v", err)
	}
	t.Cleanup(func() {
		// Exact-target removal cannot remove another workload's mapping.
		ok, _, err := define.Call(1|2|4|8, uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(target)))
		if ok == 0 {
			t.Errorf("remove exact owned SUBST fixture: %v", err)
		}
	})
	if err := qualifiedDrive(name + `\`); err == nil {
		t.Fatal("native SUBST mapping qualified as a physical volume")
	}
	r := Inspect(context.Background(), Request{Directory: name + `\`})
	if len(r.incomplete) == 0 {
		t.Fatal("SUBST inspection reported complete")
	}
}
