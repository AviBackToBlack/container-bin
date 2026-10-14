package aclaudit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const updater = "S-1-5-21-1-2-3-1001"

func textOf(r Report) string {
	var lines []string
	for _, d := range r.Diagnostics() {
		lines = append(lines, d.Status+" "+d.Message)
	}
	return strings.Join(lines, "\n")
}

func TestCandidateRightsAreNotEffectiveAccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directory bool
		mask      uint32
		want      string
	}{
		{"parent-delete-child", true, 0x40, "parent DELETE_CHILD"},
		{"object-delete", true, 0x10000, "object DELETE"},
		{"creation-only", true, 6, "child creation only; replacement not established"},
		{"file-write", false, 2, "file content write"},
		{"file-append", false, 4, "file content write/append"},
		{"change-acl", false, 0x40000, "WRITE_DAC"},
		{"change-owner", false, 0x80000, "WRITE_OWNER"},
		{"generic-all", true, 0x10000000, "parent DELETE_CHILD"},
		{"modify-parent-is-not-delete-child", true, 0x1301bf, "object DELETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Report{objects: 1}
			node := snapshot{Directory: tc.directory, Security: descriptor{Owner: updater, ACEs: []ace{{Type: 0, Mask: tc.mask, SID: "S-1-5-11"}}}}
			classify(&r, "installation ancestor[1]", node, updater)
			got := textOf(r)
			if r.candidates != 1 || !strings.Contains(got, tc.want) || !strings.Contains(got, fmt.Sprintf("mask=0x%08x", tc.mask)) || !strings.Contains(got, "effective access and replacement exploitability are not established") {
				t.Fatalf("candidate evidence = %s", got)
			}
			if tc.name == "modify-parent-is-not-delete-child" && strings.Contains(got, "parent DELETE_CHILD (child deletion candidate)") {
				t.Fatal("Modify was treated as DELETE_CHILD")
			}
		})
	}
}

func TestDACLAndIdentityFailureSemantics(t *testing.T) {
	for _, tc := range []struct {
		name           string
		d              descriptor
		wantCandidates int
		incomplete     bool
	}{
		{"trusted-read-only", descriptor{Owner: updater, ACEs: []ace{{Mask: 0x1200a9, SID: "S-1-1-0"}}}, 0, false},
		{"trusted-file-full-control", descriptor{Owner: updater, ACEs: []ace{{Mask: 0x1f01ff, SID: updater}}}, 0, false},
		{"administrators-owner", descriptor{Owner: "S-1-5-32-544"}, 0, false},
		{"system-owner", descriptor{Owner: "S-1-5-18"}, 0, false},
		{"deny-does-not-cancel-partial-allow", descriptor{Owner: updater, ACEs: []ace{{Type: 1, Mask: 2, SID: "S-1-1-0"}, {Mask: 0x40000, SID: "S-1-1-0"}}}, 1, false},
		{"inherit-only", descriptor{Owner: updater, ACEs: []ace{{Flags: 8, Mask: 0x1f01ff, SID: "S-1-1-0"}}}, 0, false},
		{"inherited-effective", descriptor{Owner: updater, ACEs: []ace{{Flags: 0x10, Mask: 2, SID: "S-1-1-0"}}}, 1, false},
		{"null-dacl", descriptor{Owner: updater, NullDACL: true}, 1, true},
		{"empty-dacl", descriptor{Owner: updater}, 0, false},
		{"untrusted-owner", descriptor{Owner: "S-1-5-11"}, 1, false},
		{"unknown-owner", descriptor{Owner: "S-1-5-21-9-9-9-9"}, 1, true},
		{"unknown-identity", descriptor{Owner: updater, ACEs: []ace{{SID: "S-1-5-21-9-9-9-9"}}}, 0, true},
		{"creator-owner-unsupported", descriptor{Owner: updater, ACEs: []ace{{Mask: 2, SID: "S-1-3-0"}}}, 1, true},
		{"owner-rights-unsupported", descriptor{Owner: updater, ACEs: []ace{{Mask: 0x40000, SID: "S-1-3-4"}}}, 1, true},
		{"object-or-conditional-ace", descriptor{Owner: updater, ACEs: []ace{{Type: 5}}}, 0, true},
		{"unknown-mask-on-trusted-sid", descriptor{Owner: updater, ACEs: []ace{{Mask: 0x2000000, SID: updater}}}, 0, true},
		{"unknown-flags", descriptor{Owner: updater, ACEs: []ace{{Flags: 0x40, SID: updater}}}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Report{objects: 1}
			classify(&r, "asset", snapshot{Security: tc.d}, updater)
			if r.candidates != tc.wantCandidates || (len(r.incomplete) != 0) != tc.incomplete {
				t.Fatalf("report=%s", textOf(r))
			}
		})
	}
}

func fakeScope(t *testing.T) (Request, scope, reader) {
	t.Helper()
	volume := filepath.VolumeName(t.TempDir())
	profile := filepath.Join(volume+string(filepath.Separator), "users", "alice")
	dir := filepath.Join(profile, "cb")
	read := func(path string) (snapshot, error) {
		return snapshot{Identity: strings.ToLower(path), Resolved: filepath.Clean(path), Filesystem: "NTFS", Directory: true, Security: descriptor{Owner: updater, ACEs: []ace{{Mask: 0x1f01ff, SID: updater}}}}, nil
	}
	return Request{Directory: dir}, scope{Updater: updater, Profile: profile, Qualified: true}, read
}

func TestTraversalCoverageAndFailures(t *testing.T) {
	for _, mode := range []string{"safe", "writable-managed-file", "reparse", "resolved-alias", "missing", "filesystem", "race", "scope", "identity-race"} {
		t.Run(mode, func(t *testing.T) {
			req, s, base := fakeScope(t)
			file := filepath.Join(req.Directory, "tool.exe")
			req.Assets = []Asset{{file, "shim tool"}}
			calls := map[string]int{}
			read := func(path string) (snapshot, error) {
				node, _ := base(path)
				key := strings.ToLower(path)
				calls[key]++
				if strings.EqualFold(path, file) {
					node.Directory = false
					if mode == "writable-managed-file" {
						node.Security.ACEs = append(node.Security.ACEs, ace{Mask: 2, SID: "S-1-1-0"})
					}
				}
				if strings.EqualFold(path, req.Directory) {
					switch mode {
					case "reparse":
						node.Reparse = 0xa0000003
					case "resolved-alias":
						node.Resolved = filepath.Dir(path)
					case "missing":
						return snapshot{}, errors.New("private native error")
					case "filesystem":
						node.Filesystem = "ReFS"
					case "race":
						if calls[key] > 1 {
							node.Security.Owner = "S-1-5-11"
						}
					case "identity-race":
						if calls[key] > 1 {
							node.Identity = "different-volume/object"
						}
					}
				}
				return node, nil
			}
			if mode == "scope" {
				s.Qualified = false
			}
			r := run(context.Background(), req, s, read, func(Report) {})
			got := textOf(r)
			if mode == "safe" {
				if len(r.incomplete) != 0 || r.candidates != 0 || !strings.Contains(got, "within the declared") {
					t.Fatal(got)
				}
			} else if mode == "writable-managed-file" {
				if r.candidates != 1 || !strings.Contains(got, "shim tool") {
					t.Fatal(got)
				}
			} else if len(r.incomplete) == 0 {
				t.Fatal("incomplete scan reported safe: " + got)
			}
			if (mode == "reparse" || mode == "missing" || mode == "filesystem" || mode == "resolved-alias") && calls[strings.ToLower(file)] != 0 {
				t.Fatal("opened descendant of blocked component")
			}
			if strings.Contains(got, req.Directory) || strings.Contains(got, "private native error") {
				t.Fatal("private path/error leak: " + got)
			}
		})
	}
}

func TestBoundsAndDeterministicOutput(t *testing.T) {
	req, s, read := fakeScope(t)
	req.Assets = make([]Asset, maxObjects+1)
	if r := run(context.Background(), req, s, read, func(Report) {}); len(r.incomplete) == 0 {
		t.Fatal("asset count unbounded")
	}
	req, s, read = fakeScope(t)
	req.Directory = filepath.Join(s.Profile, strings.Repeat("child"+string(filepath.Separator), maxDepth+1))
	if r := run(context.Background(), req, s, read, func(Report) {}); len(r.incomplete) == 0 {
		t.Fatal("depth unbounded")
	}
	r := Report{objects: 1}
	for i := 0; i < 1000; i++ {
		r.candidate(fmt.Sprintf("candidate-%d", i))
		r.missing(fmt.Sprintf("reason-%d", i))
	}
	lines := r.Diagnostics()
	if len(lines) > maxLines || lines[len(lines)-1].Status != "warn" || !strings.Contains(lines[len(lines)-1].Message, "coverage incomplete") {
		t.Fatalf("unbounded/misleading output: %+v", lines)
	}
	if !reflect.DeepEqual(lines, r.Diagnostics()) {
		t.Fatal("nondeterministic output")
	}
}

func TestOptionalRecoveryAssetIsInspectedWhenPresent(t *testing.T) {
	for _, mode := range []string{"absent", "writable", "appeared", "inaccessible"} {
		t.Run(mode, func(t *testing.T) {
			req, s, base := fakeScope(t)
			backup := filepath.Join(req.Directory, "container-bin.toml.bak")
			req.OptionalAssets = []Asset{{backup, "registry recovery backup"}}
			calls := 0
			read := func(path string) (snapshot, error) {
				node, _ := base(path)
				if strings.EqualFold(path, backup) {
					calls++
					node.Directory = false
					if mode == "inaccessible" {
						return snapshot{}, fs.ErrPermission
					}
					if mode == "absent" || (mode == "appeared" && calls == 1) {
						return snapshot{}, fs.ErrNotExist
					}
					node.Security.ACEs = append(node.Security.ACEs, ace{Mask: 2, SID: "S-1-1-0"})
				}
				return node, nil
			}
			r := run(context.Background(), req, s, read, func(Report) {})
			if mode == "absent" && len(r.incomplete) != 0 {
				t.Fatal(textOf(r))
			}
			if mode == "writable" && r.candidates != 1 {
				t.Fatal("recovery input not audited: " + textOf(r))
			}
			if (mode == "appeared" || mode == "inaccessible") && len(r.incomplete) == 0 {
				t.Fatal("changed/unreadable recovery input reported complete")
			}
		})
	}
}

func TestBoundedNativeWorkerRetainsEvidenceAndDoesNotAccumulate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	started := make(chan struct{})
	result := make(chan Report, 1)
	go func() {
		result <- bounded(ctx, func(_ context.Context, progress func(Report)) Report {
			r := Report{objects: 1}
			r.candidate("known candidate")
			progress(r)
			close(started)
			<-release
			return r
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("native worker did not start")
	}
	cancel()
	var r Report
	select {
	case r = <-result:
	case <-time.After(time.Second):
		t.Fatal("caller stuck in native I/O after cancellation")
	}
	if r.candidates != 1 || len(r.incomplete) == 0 {
		t.Fatal("lost partial evidence or false completion")
	}
	busy := bounded(context.Background(), func(context.Context, func(Report)) Report {
		t.Error("second worker started while native I/O was pending")
		return Report{}
	})
	if len(busy.incomplete) == 0 {
		t.Fatal("pending worker not reported")
	}
	close(release)
	released = true
	deadline := time.Now().Add(time.Second)
	for len(workerSlot) != 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if len(workerSlot) != 0 {
		t.Fatal("worker resource not released")
	}
}
