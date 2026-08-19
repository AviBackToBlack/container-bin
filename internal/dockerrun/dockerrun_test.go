package dockerrun

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

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
	uncHome := `\\server\share\home`
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", uncHome)
	} else {
		t.Setenv("HOME", uncHome)
	}

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
