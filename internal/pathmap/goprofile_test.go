package pathmap

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

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
	reg, err := registry.ParseTOML(registry.DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	tool := reg.Tools["go"]
	wr := WorkspaceRootFor(tool, root)

	mustWriteFile(t, filepath.Join(rootRaw, "go.mod"), []byte("module myproj\ngo 1.24\n"))

	assertMapped := func(name string, in, want []string, wantMounts int) {
		t.Helper()
		mapped, mounts, err := MapToolArgs(tool, root, root, wr, in)
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

	// No path_next on the go profile: a bare output name is left alone and resolves
	// against the container working directory, which is the project root.
	assertMapped("go build -o app.exe", []string{"build", "-o", "app.exe"}, []string{"build", "-o", "app.exe"}, 0)

	// Everything after `go run PKG` belongs to the user's program and must survive
	// untouched — this is what forcing -o would have corrupted.
	assertMapped("go run . -o json", []string{"run", ".", "-o", "json"}, []string{"run", ".", "-o", "json"}, 0)

	// Real output paths are still mapped by the general rules.
	assertMapped("go build -o dot-relative", []string{"build", "-o", "./dist/app.exe"}, []string{"build", "-o", wr + "/dist/app.exe"}, 0)
	assertMapped("go build -o <abs>", []string{"build", "-o", filepath.Join(root, "dist", "app.exe")}, []string{"build", "-o", wr + "/dist/app.exe"}, 0)

	// go test ./... is a package pattern, not a path: the trailing "..." guard
	// leaves it untouched so Go resolves it against the container working directory.
	assertMapped("go test ./...", []string{"test", "./..."}, []string{"test", "./..."}, 0)

	// Package patterns and subcommand words that are not explicit paths stay unchanged.
	assertMapped("go test pkg/...", []string{"test", "pkg/..."}, []string{"test", "pkg/..."}, 0)
	assertMapped("go build", []string{"build"}, []string{"build"}, 0)
	assertMapped("go vet", []string{"vet"}, []string{"vet"}, 0)
}

func TestGofmtPathMappingWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path-mapping semantics")
	}

	rootRaw := filepath.Join(t.TempDir(), "myproj")
	if err := os.MkdirAll(rootRaw, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	root := mustCanonical(t, rootRaw)
	reg, err := registry.ParseTOML(registry.DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	tool := reg.Tools["gofmt"]
	wr := WorkspaceRootFor(tool, root)

	check := func(name string, in, want []string) {
		t.Helper()
		mapped, mounts, err := MapToolArgs(tool, root, root, wr, in)
		if err != nil {
			t.Fatalf("%s: err=%v", name, err)
		}
		if !reflect.DeepEqual(mapped, want) {
			t.Fatalf("%s: mapped=%#v, want %#v", name, mapped, want)
		}
		if len(mounts) != 0 {
			t.Fatalf("%s: expected no mounts, got %#v", name, mounts)
		}
	}

	// A trailing rewrite rule must survive: with path_last it would have been
	// turned into a container path and gofmt would reject it.
	check("gofmt -r rule", []string{"-r", "a[b:len(a)] -> a[b:]"}, []string{"-r", "a[b:len(a)] -> a[b:]"})

	// "." and bare file names resolve against the container working directory,
	// which is the project root, so they need no rewriting.
	check("gofmt -l .", []string{"-l", "."}, []string{"-l", "."})
	check("gofmt -w main.go", []string{"-w", "main.go"}, []string{"-w", "main.go"})

	// Explicit relative operands are still mapped by the general rules.
	check("gofmt -w ./pkg/x.go", []string{"-w", "./pkg/x.go"}, []string{"-w", wr + "/pkg/x.go"})
}
