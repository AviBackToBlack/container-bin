package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestGoProfilesParseAndImage(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "gofmt"} {
		tool, ok := reg.Tools[name]
		if !ok {
			t.Fatalf("missing default tool %q", name)
		}
		if tool.Provider != "stateful" {
			t.Fatalf("%s provider = %q, want stateful", name, tool.Provider)
		}
		if tool.Image != "golang:1.24" {
			t.Fatalf("%s image = %q, want golang:1.24", name, tool.Image)
		}
		if tool.StateGroup != "go124" {
			t.Fatalf("%s state_group = %q, want go124", name, tool.StateGroup)
		}
	}
}

func TestGoProfilesShareStateGroupAndVolumes(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "gofmt"} {
		tool := reg.Tools[name]
		if tool.Provider != "stateful" || tool.StateGroup != "go124" {
			t.Fatalf("bad %s state profile: %+v", name, tool)
		}
		want := []string{"gomodcache:/go/pkg/mod", "gobuild:/root/.cache/go-build", "gobin:/go/bin"}
		if !reflect.DeepEqual(tool.SharedVolumes, want) {
			t.Fatalf("%s shared_volumes = %#v, want %#v", name, tool.SharedVolumes, want)
		}
	}
}

func TestGoProfileHasNoProjectVolumes(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Tools["go"].ProjectVolumes) != 0 {
		t.Fatalf("go has unexpected project_volumes: %#v", reg.Tools["go"].ProjectVolumes)
	}
}

func TestGoSharedVolumesParse(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "gofmt"} {
		for _, spec := range reg.Tools[name].SharedVolumes {
			if _, _, err := parseVolumeBinding(spec); err != nil {
				t.Fatalf("%s shared volume %q: %v", name, spec, err)
			}
		}
	}
}

func TestGoEnvAllowlistExcludesPathVariables(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, name := range reg.Tools["go"].EnvNames {
		allowed[name] = true
	}
	for _, name := range reg.Tools["gofmt"].EnvNames {
		allowed[name] = true
	}
	must := []string{"GOFLAGS", "GOOS"}
	for _, name := range must {
		if !allowed[name] {
			t.Fatalf("env allowlist missing %q", name)
		}
	}
	excluded := []string{"GOPATH", "GOROOT", "GOBIN", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "GOENV"}
	for _, name := range excluded {
		if allowed[name] {
			t.Fatalf("env allowlist must not include path-valued %q", name)
		}
	}
}

func TestGoNamesAreInstallable(t *testing.T) {
	for _, name := range []string{"go", "gofmt"} {
		if reservedToolName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
		if !validToolName(name) {
			t.Fatalf("%q should be a valid tool name", name)
		}
	}
}

func TestGoPathMappingWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}

	rootDir := t.TempDir()
	rootRaw := filepath.Join(rootDir, "myproj")
	if err := os.MkdirAll(filepath.Join(rootRaw, "pkg"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	root := mustCanonical(t, rootRaw)
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	tool := reg.Tools["go"]
	wr := workspaceRootFor(tool, root)

	mustWriteFile(t, filepath.Join(rootRaw, "go.mod"), []byte("module myproj\ngo 1.24\n"))

	assertMapped := func(name string, in, want []string, wantMounts int) {
		t.Helper()
		mapped, mounts, err := mapToolArgs(tool, root, root, wr, in)
		if err != nil {
			t.Fatalf("%s: err=%v", name, err)
		}
		if !reflect.DeepEqual(mapped, want) {
			t.Fatalf("%s: mapped=%#v, want %#v", name, mapped, want)
		}
		if len(mounts) != wantMounts {
			t.Fatalf("%s: mounts=%#v, want %d mounts", name, mounts, wantMounts)
		}
	}

	// go build -o app.exe should force-map the output path but leave the -o flag.
	assertMapped("go build -o app.exe", []string{"build", "-o", "app.exe"}, []string{"build", "-o", wr + "/app.exe"}, 0)

	// go test ./... is an explicit relative pattern; it maps into the workspace.
	assertMapped("go test ./...", []string{"test", "./..."}, []string{"test", wr + "/..."}, 0)

	// Package patterns and subcommand words that are not explicit paths stay unchanged.
	assertMapped("go test pkg/...", []string{"test", "pkg/..."}, []string{"test", "pkg/..."}, 0)
	assertMapped("go build", []string{"build"}, []string{"build"}, 0)
	assertMapped("go vet", []string{"vet"}, []string{"vet"}, 0)
}
