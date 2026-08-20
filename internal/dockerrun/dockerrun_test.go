package dockerrun

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

func TestMountSpecMode(t *testing.T) {
	got, err := MountSpecMode("bind", "/some/src", "/some/dst", "ro")
	if err != nil {
		t.Fatalf("MountSpecMode ro returned error: %v", err)
	}
	want := "type=bind,src=/some/src,dst=/some/dst,readonly"
	if got != want {
		t.Fatalf("MountSpecMode ro = %q, want %q", got, want)
	}

	got, err = MountSpecMode("bind", "/some/src", "/some/dst", "rw")
	if err != nil {
		t.Fatalf("MountSpecMode rw returned error: %v", err)
	}
	want = "type=bind,src=/some/src,dst=/some/dst"
	if got != want {
		t.Fatalf("MountSpecMode rw = %q, want %q", got, want)
	}

	_, err = MountSpecMode("bind", "/some/src", "/some/dst", "unsupported")
	if err == nil {
		t.Fatal("expected error for unsupported mode")
	}
}

func TestMountSpecModeCommaRejection(t *testing.T) {
	_, err := MountSpecMode("bind", "/bad, src", "/clean/dst", "ro")
	if err == nil {
		t.Fatal("expected comma in src to be rejected")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Fatalf("error should name the source side: %v", err)
	}

	_, err = MountSpecMode("bind", "/clean/src", "/bad, dst", "ro")
	if err == nil {
		t.Fatal("expected comma in dst to be rejected")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Fatalf("error should name the destination side: %v", err)
	}
}

func TestExpandHostMountSource(t *testing.T) {
	homeDir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", homeDir)
	} else {
		t.Setenv("HOME", homeDir)
	}

	src := "%USERPROFILE%/.config"
	got, err := ExpandHostMountSource(src)
	if err != nil {
		t.Fatalf("ExpandHostMountSource(%q) error: %v", src, err)
	}
	want := homeDir + "/.config"
	if got != want {
		t.Fatalf("ExpandHostMountSource(%q) = %q, want %q", src, got, want)
	}

	literal := `C:\Users\x\.claude`
	got, err = ExpandHostMountSource(literal)
	if err != nil {
		t.Fatalf("ExpandHostMountSource(%q) error: %v", literal, err)
	}
	if got != literal {
		t.Fatalf("ExpandHostMountSource(%q) = %q, want %q", literal, got, literal)
	}
}

func TestBuildHostMountArgs(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", homeDir)
	} else {
		t.Setenv("HOME", homeDir)
	}

	reg, err := registry.ParseTOML(`[tools.x]
image = "x:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/.claude:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}
	tool := reg.Tools["x"]

	args, err := buildHostMountArgs(tool.HostMounts)
	if err != nil {
		t.Fatalf("buildHostMountArgs error: %v", err)
	}
	if len(args) != 2 || args[0] != "--mount" {
		t.Fatalf("unexpected args: %#v", args)
	}
	mount := args[1]
	if !strings.HasPrefix(mount, "type=bind,src=") {
		t.Fatalf("unexpected mount string: %q", mount)
	}
	if !strings.Contains(mount, ",dst=/root/.claude,readonly") {
		t.Fatalf("mount string missing readonly dst: %q", mount)
	}
}

func TestBuildHostMountArgsRejectsMissingSource(t *testing.T) {
	homeDir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", homeDir)
	} else {
		t.Setenv("HOME", homeDir)
	}

	reg, err := registry.ParseTOML(`[tools.x]
image = "x:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/does-not-exist:/root/missing:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	_, err = buildHostMountArgs(reg.Tools["x"].HostMounts)
	if err == nil {
		t.Fatal("expected error for missing host_mount source")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error does not mention missing source: %v", err)
	}
}

func TestBuildHostMountArgsRejectsUNCSource(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("UNC path detection is only meaningful on Windows")
	}
	uncHome := `\\server\share\home`
	t.Setenv("USERPROFILE", uncHome)

	reg, err := registry.ParseTOML(`[tools.x]
image = "x:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/.claude:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	_, err = buildHostMountArgs(reg.Tools["x"].HostMounts)
	if err == nil {
		t.Fatal("expected error for UNC host_mount source")
	}
	if !strings.Contains(err.Error(), "UNC") {
		t.Fatalf("error does not mention UNC: %v", err)
	}
}

func TestBuildDockerArgsDefaultProjectModeIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	root, err := pathmap.CanonicalPath(dir)
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}
	ctx := runContext{
		cwd:           root,
		root:          root,
		workspaceRoot: "/workspace",
		containerWD:   "/workspace",
		found:         false,
	}
	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless"}
	got, err := buildDockerArgs(tool, nil, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs: %v", err)
	}
	want := []string{
		"run", "--rm", "-i",
		"--workdir", "/workspace",
		"--mount", "type=bind,src=" + root + ",dst=/workspace",
		"demo:1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default project args mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestBuildDockerArgsEmptyCwdModeMatchesProject(t *testing.T) {
	dir := t.TempDir()
	root, err := pathmap.CanonicalPath(dir)
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}
	ctx := runContext{
		cwd:           root,
		root:          root,
		workspaceRoot: "/workspace",
		containerWD:   "/workspace",
		found:         false,
	}
	empty := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless", CwdMode: ""}
	project := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless", CwdMode: "project"}
	emptyArgs, err := buildDockerArgs(empty, nil, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs empty: %v", err)
	}
	projectArgs, err := buildDockerArgs(project, nil, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs project: %v", err)
	}
	if !reflect.DeepEqual(emptyArgs, projectArgs) {
		t.Fatalf("empty and project cwd_mode differ:\nempty  %#v\nproject %#v", emptyArgs, projectArgs)
	}
}

// TestResolveRunContextIsolated exercises resolveRunContext's own isolated
// branch directly (Copilot flagged that the other isolated tests all
// hand-build a runContext, the same gap GLM caught for the default-mode
// tests in round 2 -- a regression in resolveRunContext's isolated branch
// could silently restore a real root/workdir while those builder-only tests
// stayed green).
func TestResolveRunContextIsolated(t *testing.T) {
	dir := t.TempDir()
	cwd, err := pathmap.CanonicalPath(dir)
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}
	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless", CwdMode: "isolated"}
	ctx, err := resolveRunContext(tool, cwd)
	if err != nil {
		t.Fatalf("resolveRunContext: %v", err)
	}
	want := runContext{
		cwd:           cwd,
		root:          IsolatedRoot,
		workspaceRoot: "/root",
		containerWD:   "/root",
		found:         false,
	}
	if !reflect.DeepEqual(ctx, want) {
		t.Fatalf("resolveRunContext isolated mismatch:\ngot  %+v\nwant %+v", ctx, want)
	}

	args, err := buildDockerArgs(tool, nil, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs: %v", err)
	}
	wantArgs := []string{"run", "--rm", "-i", "--workdir", "/root", "demo:1"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("end-to-end isolated args = %#v, want %#v", args, wantArgs)
	}
	if strings.Contains(strings.Join(args, " "), cwd) {
		t.Fatalf("end-to-end isolated args must not contain the host CWD; got %q", args)
	}
}

func TestBuildDockerArgsIsolatedNoCwdMount(t *testing.T) {
	dir := t.TempDir()
	cwd, err := pathmap.CanonicalPath(dir)
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}
	ctx := runContext{
		cwd:           cwd,
		root:          IsolatedRoot,
		workspaceRoot: "/root",
		containerWD:   "/root",
		found:         false,
	}
	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless", CwdMode: "isolated"}
	args, err := buildDockerArgs(tool, nil, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs: %v", err)
	}
	want := []string{
		"run", "--rm", "-i",
		"--workdir", "/root",
		"demo:1",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("isolated args = %#v, want %#v", args, want)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, cwd) {
		t.Fatalf("isolated args must not contain the host CWD; got %q", joined)
	}
}

func TestBuildDockerArgsIsolatedExternalPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path mapping is required for external-path assertions")
	}

	rootDir := t.TempDir()
	root, err := pathmap.CanonicalPath(rootDir)
	if err != nil {
		t.Fatalf("canonical root: %v", err)
	}
	extDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(extDir, "file.txt"), []byte{}, 0644); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	ext, err := pathmap.CanonicalPath(extDir)
	if err != nil {
		t.Fatalf("canonical ext: %v", err)
	}

	ctx := runContext{
		cwd:           root,
		root:          IsolatedRoot,
		workspaceRoot: "/root",
		containerWD:   "/root",
		found:         false,
	}
	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless", CwdMode: "isolated"}
	args, err := buildDockerArgs(tool, []string{filepath.Join(ext, "file.txt")}, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs: %v", err)
	}

	want := []string{
		"run", "--rm", "-i",
		"--workdir", "/root",
		"--mount", "type=bind,src=" + ext + ",dst=/cb/mounts/0",
		"demo:1",
		"/cb/mounts/0/file.txt",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("isolated external args mismatch:\ngot  %#v\nwant %#v", args, want)
	}
}

func TestResolveRunContextDefaultNoMarker(t *testing.T) {
	dir := t.TempDir()
	cwd, err := pathmap.CanonicalPath(dir)
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}

	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless"}
	ctx, err := resolveRunContext(tool, cwd)
	if err != nil {
		t.Fatalf("resolveRunContext: %v", err)
	}

	want := runContext{
		cwd:           cwd,
		root:          cwd,
		workspaceRoot: "/workspace",
		containerWD:   "/workspace",
		found:         false,
	}
	if !reflect.DeepEqual(ctx, want) {
		t.Fatalf("resolveRunContext no-marker mismatch:\ngot  %+v\nwant %+v", ctx, want)
	}
}

func TestResolveRunContextNestedUnderMarker(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, ".git"), []byte{}, 0644); err != nil {
		t.Fatalf("write .git marker: %v", err)
	}
	child := filepath.Join(parent, "subdir")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}

	cwd, err := pathmap.CanonicalPath(child)
	if err != nil {
		t.Fatalf("canonical child: %v", err)
	}
	parentCanon, err := pathmap.CanonicalPath(parent)
	if err != nil {
		t.Fatalf("canonical parent: %v", err)
	}

	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless"}
	ctx, err := resolveRunContext(tool, cwd)
	if err != nil {
		t.Fatalf("resolveRunContext: %v", err)
	}

	want := runContext{
		cwd:           cwd,
		root:          parentCanon,
		workspaceRoot: "/workspace",
		containerWD:   "/workspace/subdir",
		found:         true,
	}
	if !reflect.DeepEqual(ctx, want) {
		t.Fatalf("resolveRunContext nested mismatch:\ngot  %+v\nwant %+v", ctx, want)
	}
}

func TestDefaultModeEndToEndArgsUnchanged(t *testing.T) {
	dir := t.TempDir()
	cwd, err := pathmap.CanonicalPath(dir)
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}

	tool := registry.Tool{Name: "demo", Image: "demo:1", Provider: "stateless"}
	ctx, err := resolveRunContext(tool, cwd)
	if err != nil {
		t.Fatalf("resolveRunContext: %v", err)
	}

	args, err := buildDockerArgs(tool, nil, ctx, "demo:1", false)
	if err != nil {
		t.Fatalf("buildDockerArgs: %v", err)
	}

	want := []string{
		"run", "--rm", "-i",
		"--workdir", "/workspace",
		"--mount", "type=bind,src=" + cwd + ",dst=/workspace",
		"demo:1",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("default end-to-end args mismatch:\ngot  %#v\nwant %#v", args, want)
	}
}

func TestBuildHostMountArgsCanonicalizesBeforeStat(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, homeDir)

	reg, err := registry.ParseTOML(`[tools.x]
image = "x:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/.claude/.:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	args, err := buildHostMountArgs(reg.Tools["x"].HostMounts)
	if err != nil {
		t.Fatalf("buildHostMountArgs error: %v", err)
	}
	mount := args[1]
	if strings.Contains(mount, "/./") || strings.HasSuffix(mount, "/.") {
		t.Fatalf("mount string should be canonicalized, got %q", mount)
	}
	if !strings.Contains(mount, ",dst=/root/.claude,readonly") {
		t.Fatalf("mount string missing readonly dst: %q", mount)
	}
}
