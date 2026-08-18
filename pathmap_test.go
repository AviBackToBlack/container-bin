package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func mustCanonical(t *testing.T, p string) string {
	t.Helper()
	c, err := canonicalPath(p)
	if err != nil {
		t.Fatalf("canonicalPath(%q): %v", p, err)
	}
	return c
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func TestNormalizeToolArgsJoinsSplitValue(t *testing.T) {
	tool := Tool{Name: "demo", PathEquals: []string{"--file"}}
	in := []string{"--file=", "value", "rest"}
	want := []string{"--file=value", "rest"}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("A1: got %#v, want %#v", got, want)
	}
}

func TestNormalizeToolArgsTrailingBarePrefix(t *testing.T) {
	tool := Tool{Name: "demo", PathEquals: []string{"--file"}}
	in := []string{"validate", "--file="}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("A2: got %#v, want unchanged %#v", got, in)
	}
}

func TestNormalizeToolArgsEmptyPathEquals(t *testing.T) {
	tool := Tool{Name: "demo"}
	in := []string{"--file=", "value", "x"}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("A3: got %#v, want unchanged %#v", got, in)
	}
}

func TestMapToolArgsNonWindowsContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("documents the non-windows early return in mapToolArgs")
	}
	tool := Tool{Name: "demo", PathEquals: []string{"--file"}}
	in := []string{"--file=", "value", "rest"}
	mapped, mounts, err := mapToolArgs(tool, "root", "cwd", "/workspace", in)
	if err != nil {
		t.Fatalf("A4: err=%v", err)
	}
	want := []string{"--file=value", "rest"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("A4: mapped=%#v, want %#v", mapped, want)
	}
	if mounts != nil {
		t.Fatalf("A4: mounts=%#v, want nil", mounts)
	}
}

func TestPathWithin(t *testing.T) {
	dir := t.TempDir()
	rootRaw := filepath.Join(dir, "root")
	if err := os.MkdirAll(rootRaw, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	root := mustCanonical(t, rootRaw)
	child := mustCanonical(t, filepath.Join(root, "sub", "file.txt"))
	sibling := mustCanonical(t, filepath.Join(filepath.Dir(root), "sibling", "x.txt"))

	wantRel := filepath.Join("sub", "file.txt")
	if rel, ok := pathWithin(root, child); !ok || rel != wantRel {
		t.Fatalf("A5 child: got (%q, %v), want (%q, true)", rel, ok, wantRel)
	}
	if _, ok := pathWithin(root, sibling); ok {
		t.Fatalf("A5 sibling: expected false, got true")
	}
	if rel, ok := pathWithin(root, root); !ok || rel != "." {
		t.Fatalf("A5 root: got (%q, %v), want (\".\", true)", rel, ok)
	}
}

func TestExternalMountRoot(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.MkdirAll(existing, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file := filepath.Join(existing, "x.txt")
	if err := os.WriteFile(file, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := externalMountRoot(existing)
	if err != nil || got != existing {
		t.Fatalf("A6a: got (%q, %v), want (%q, nil)", got, err, existing)
	}

	got, err = externalMountRoot(file)
	if err != nil || got != existing {
		t.Fatalf("A6b: got (%q, %v), want (%q, nil)", got, err, existing)
	}

	got, err = externalMountRoot(filepath.Join(existing, "a", "b", "c"))
	if err != nil || got != existing {
		t.Fatalf("A6c: got (%q, %v), want (%q, nil)", got, err, existing)
	}
}

func TestWorkspaceRootFor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myproj")
	cases := []struct {
		provider string
		want     string
	}{
		{"stateless", "/workspace"},
		{"python", "/workspace"},
		{"stateful", "/workspace/myproj"},
	}
	for _, tc := range cases {
		got := workspaceRootFor(Tool{Provider: tc.provider}, root)
		if got != tc.want {
			t.Fatalf("A7 provider %q: got %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func windowsFixtures(t *testing.T) (root, ext string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}

	rootDir := t.TempDir()
	rootRaw := filepath.Join(rootDir, "proj")
	if err := os.MkdirAll(filepath.Join(rootRaw, "sub"), 0755); err != nil {
		t.Fatalf("MkdirAll root: %v", err)
	}
	mustWriteFile(t, filepath.Join(rootRaw, "sub", "a.txt"), []byte{})
	root = mustCanonical(t, rootRaw)

	extRaw := t.TempDir()
	mustWriteFile(t, filepath.Join(extRaw, "b.txt"), []byte{})
	mustWriteFile(t, filepath.Join(extRaw, "c.txt"), []byte{})
	ext = mustCanonical(t, extRaw)

	if _, ok := pathWithin(root, ext); ok {
		t.Fatalf("precondition: ext %q must not be under root %q", ext, root)
	}
	return
}

func TestMapToolArgsAbsoluteInsideRoot(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{filepath.Join(root, "sub", "a.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B1: err=%v", err)
	}
	want := []string{"/workspace/sub/a.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B1: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 0 {
		t.Fatalf("B1: expected no mounts, got %#v", mounts)
	}
}

func TestMapToolArgsRootItself(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{root}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B2: err=%v", err)
	}
	want := []string{"/workspace"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B2: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 0 {
		t.Fatalf("B2: expected no mounts, got %#v", mounts)
	}
}

func TestMapToolArgsNonPathArgs(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{"-n", "hello", "{.items}"}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B3: err=%v", err)
	}
	if !reflect.DeepEqual(mapped, in) {
		t.Fatalf("B3: mapped=%#v, want unchanged %#v", mapped, in)
	}
	if len(mounts) != 0 {
		t.Fatalf("B3: expected no mounts, got %#v", mounts)
	}
}

func TestMapToolArgsExternalAbsolute(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{filepath.Join(ext, "b.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B4: err=%v", err)
	}
	want := []string{"/cb/mounts/0/b.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B4: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("B4: mounts=%#v", mounts)
	}
}

func TestMapToolArgsExternalDedup(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{filepath.Join(ext, "b.txt"), filepath.Join(ext, "c.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B5: err=%v", err)
	}
	want := []string{"/cb/mounts/0/b.txt", "/cb/mounts/0/c.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B5: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("B5: mounts=%#v", mounts)
	}
}

func TestMapToolArgsExternalDedupCaseInsensitive(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	lower := filepath.Join(ext, "b.txt")
	upper := filepath.Join(strings.ToUpper(ext), "B.TXT")
	in := []string{lower, upper}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B6: err=%v", err)
	}
	want := []string{"/cb/mounts/0/b.txt", "/cb/mounts/0/b.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B6: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("B6: mounts=%#v", mounts)
	}
}

func TestMapToolArgsDistinctExternalRoots(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}
	root := mustCanonical(t, t.TempDir())
	ext1Raw := t.TempDir()
	mustWriteFile(t, filepath.Join(ext1Raw, "x1.txt"), []byte{})
	ext1 := mustCanonical(t, ext1Raw)
	ext2Raw := t.TempDir()
	mustWriteFile(t, filepath.Join(ext2Raw, "x2.txt"), []byte{})
	ext2 := mustCanonical(t, ext2Raw)

	if _, ok := pathWithin(root, ext1); ok {
		t.Fatalf("precondition: ext %q must not be under root %q", ext1, root)
	}
	if _, ok := pathWithin(root, ext2); ok {
		t.Fatalf("precondition: ext %q must not be under root %q", ext2, root)
	}

	tool := Tool{Name: "demo"}
	in := []string{filepath.Join(ext1, "x1.txt"), filepath.Join(ext2, "x2.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B7: err=%v", err)
	}
	want := []string{"/cb/mounts/0/x1.txt", "/cb/mounts/1/x2.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B7: mapped=%#v, want %#v", mapped, want)
	}
	wantMounts := []PathMount{{Source: ext1, Target: "/cb/mounts/0"}, {Source: ext2, Target: "/cb/mounts/1"}}
	if !reflect.DeepEqual(mounts, wantMounts) {
		t.Fatalf("B7: mounts=%#v, want %#v", mounts, wantMounts)
	}
}

func TestMapToolArgsPathNext(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := Tool{Name: "demo", PathNext: []string{"-f"}}

	file := filepath.Join(ext, "b.txt")
	in := []string{"-f", file}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B8 absolute: err=%v", err)
	}
	want := []string{"-f", "/cb/mounts/0/b.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B8 absolute: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("B8 absolute: mounts=%#v", mounts)
	}

	in2 := []string{"-f", "data.json"}
	mapped2, mounts2, err2 := mapToolArgs(tool, root, ext, "/workspace", in2)
	if err2 != nil {
		t.Fatalf("B8 relative: err=%v", err2)
	}
	want2 := []string{"-f", "/cb/mounts/0/data.json"}
	if !reflect.DeepEqual(mapped2, want2) {
		t.Fatalf("B8 relative: mapped=%#v, want %#v", mapped2, want2)
	}
	if len(mounts2) != 1 || mounts2[0].Source != ext || mounts2[0].Target != "/cb/mounts/0" {
		t.Fatalf("B8 relative: mounts=%#v", mounts2)
	}
}

func TestMapToolArgsPathNextDangling(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "tf", PathNext: []string{"-f"}}
	in := []string{"-f"}
	_, _, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err == nil {
		t.Fatalf("B9: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, tool.Name) || !strings.Contains(msg, "-f") {
		t.Fatalf("B9: bad error: %v", err)
	}
}

func TestMapToolArgsPathEquals(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "demo", PathEquals: []string{"--file"}}
	in := []string{"--file=" + filepath.Join(root, "sub", "a.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B10: err=%v", err)
	}
	want := []string{"--file=/workspace/sub/a.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B10: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 0 {
		t.Fatalf("B10: expected no mounts, got %#v", mounts)
	}
}

func TestMapToolArgsPathLast(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "demo", PathLast: true}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"final non-option", []string{"-o", filepath.Join(root, "sub", "a.txt")}, []string{"-o", "/workspace/sub/a.txt"}},
		{"final dash", []string{"-o", "-"}, []string{"-o", "-"}},
		{"final option", []string{"-o", "--out"}, []string{"-o", "--out"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", tc.in)
			if err != nil {
				t.Fatalf("B11 %s: err=%v", tc.name, err)
			}
			if !reflect.DeepEqual(mapped, tc.want) {
				t.Fatalf("B11 %s: mapped=%#v, want %#v", tc.name, mapped, tc.want)
			}
			if len(mounts) != 0 {
				t.Fatalf("B11 %s: expected no mounts, got %#v", tc.name, mounts)
			}
		})
	}
}

func TestMapToolArgsPathLastIfAny(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := Tool{Name: "demo", PathLast: true, PathLastIfAny: []string{"-o"}}

	in1 := []string{"-o", "data.json"}
	mapped1, mounts1, err1 := mapToolArgs(tool, root, ext, "/workspace", in1)
	if err1 != nil {
		t.Fatalf("B12 with -o: err=%v", err1)
	}
	want1 := []string{"-o", "/cb/mounts/0/data.json"}
	if !reflect.DeepEqual(mapped1, want1) {
		t.Fatalf("B12 with -o: mapped=%#v, want %#v", mapped1, want1)
	}
	if len(mounts1) != 1 || mounts1[0].Source != ext || mounts1[0].Target != "/cb/mounts/0" {
		t.Fatalf("B12 with -o: mounts=%#v", mounts1)
	}

	in2 := []string{"data.json"}
	mapped2, mounts2, err2 := mapToolArgs(tool, root, ext, "/workspace", in2)
	if err2 != nil {
		t.Fatalf("B12 without -o: err=%v", err2)
	}
	if !reflect.DeepEqual(mapped2, in2) {
		t.Fatalf("B12 without -o: mapped=%#v, want unchanged %#v", mapped2, in2)
	}
	if len(mounts2) != 0 {
		t.Fatalf("B12 without -o: expected no mounts, got %#v", mounts2)
	}
}

func TestMapToolArgsExternalMissingAncestor(t *testing.T) {
	root, ext := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{filepath.Join(ext, "nope", "out.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace", in)
	if err != nil {
		t.Fatalf("B13: err=%v", err)
	}
	want := []string{"/cb/mounts/0/nope/out.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B13: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 1 || mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0" {
		t.Fatalf("B13: mounts=%#v", mounts)
	}
}

func TestMapToolArgsStatefulWorkspaceRoot(t *testing.T) {
	root, _ := windowsFixtures(t)
	tool := Tool{Name: "demo"}
	in := []string{filepath.Join(root, "sub", "a.txt")}
	mapped, mounts, err := mapToolArgs(tool, root, root, "/workspace/proj", in)
	if err != nil {
		t.Fatalf("B14: err=%v", err)
	}
	want := []string{"/workspace/proj/sub/a.txt"}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("B14: mapped=%#v, want %#v", mapped, want)
	}
	if len(mounts) != 0 {
		t.Fatalf("B14: expected no mounts, got %#v", mounts)
	}
}

func TestPackagePatternSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"...", true},
		{"./...", true},
		{"../...", true},
		{`.\...`, true},
		{"./cmd/...", true},
		{`.\cmd\...`, true},
		{`D:\proj\...`, true},
		{"foo...", false},
		{"..", false},
		{".", false},
		{"./cmd", false},
		{"", false},
	}
	for _, tc := range cases {
		got := hasPackagePatternSuffix(tc.in)
		if got != tc.want {
			t.Errorf("hasPackagePatternSuffix(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMapToolArgsPackagePattern(t *testing.T) {
	root, ext := windowsFixtures(t)
	demo := Tool{Name: "demo"}

	cases := []struct {
		name       string
		tool       Tool
		in         []string
		want       []string
		wantMounts int
	}{
		{"dotdotdot", demo, []string{"..."}, []string{"..."}, 0},
		{"dot slash dotdotdot", demo, []string{"./..."}, []string{"./..."}, 0},
		{"dotdot slash dotdotdot", demo, []string{"../..."}, []string{"../..."}, 0},
		{"dot backslash dotdotdot", demo, []string{`.\...`}, []string{`.\...`}, 0},
		{"dotdot backslash dotdotdot", demo, []string{`..\...`}, []string{`..\...`}, 0},
		{"cmd slash dotdotdot", demo, []string{"./cmd/..."}, []string{"./cmd/..."}, 0},
		{"cmd backslash dotdotdot", demo, []string{`.\cmd\...`}, []string{`.\cmd\...`}, 0},
		{"path next still declines", Tool{Name: "demo", PathNext: []string{"-o"}}, []string{"-o", "./..."}, []string{"-o", "./..."}, 0},
		{"regression subdir", demo, []string{`.\sub`}, []string{"/workspace/sub"}, 0},
		{"regression absolute inside", demo, []string{filepath.Join(root, "sub", "a.txt")}, []string{"/workspace/sub/a.txt"}, 0},
		{"regression absolute outside", demo, []string{filepath.Join(ext, "b.txt")}, []string{"/cb/mounts/0/b.txt"}, 1},
		{"foo triple dot not wildcard", demo, []string{"foo..."}, []string{"foo..."}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, mounts, err := mapToolArgs(tc.tool, root, root, "/workspace", tc.in)
			if err != nil {
				t.Fatalf("PP %s: err=%v", tc.name, err)
			}
			if !reflect.DeepEqual(mapped, tc.want) {
				t.Fatalf("PP %s: mapped=%#v, want %#v", tc.name, mapped, tc.want)
			}
			if len(mounts) != tc.wantMounts {
				t.Fatalf("PP %s: len(mounts)=%d, want %d; mounts=%#v", tc.name, len(mounts), tc.wantMounts, mounts)
			}
			if tc.wantMounts == 1 && (mounts[0].Source != ext || mounts[0].Target != "/cb/mounts/0") {
				t.Fatalf("PP %s: mounts=%#v", tc.name, mounts)
			}
		})
	}
}
