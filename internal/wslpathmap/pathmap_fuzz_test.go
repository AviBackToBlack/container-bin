package wslpathmap

import (
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

func FuzzMapToolArgs(f *testing.F) {
	for _, seed := range []string{"", "-", "./input", "../outside", "../../outside", "./...", "/etc/passwd", "/home/alice/project/mounted/file", `C:\work\input`, `\\server\share\input`, "\x00\xff"} {
		f.Add(seed, "literal")
	}
	f.Fuzz(func(t *testing.T, input, literal string) {
		project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
		tool := registry.Tool{Name: "demo", PathNext: []string{"-i"}}
		args := []string{"-i", input, "--", literal}
		before := append([]string(nil), args...)
		// Inject lexical/mount-boundary proof, never the real host filesystem.
		got, cwd, err := mapToolArgs(tool, project, project.Root+"/src", "/workspace/demo", args, testDependencies(project.Root))
		if !reflect.DeepEqual(args, before) {
			t.Fatal("WSL path mapping mutated input argv")
		}
		if err != nil {
			return
		}
		if len(got) != len(args) || got[0] != "-i" || got[2] != "--" || cwd != "/workspace/demo/src" {
			t.Fatalf("mapping changed argv shape or working directory: %q, %q", got, cwd)
		}
		for index, arg := range got {
			if isWindowsAbsolutePath(args[index]) {
				t.Fatal("Windows absolute path accepted in native WSL argv")
			}
			if path.IsAbs(arg) && arg != "/workspace/demo" && !strings.HasPrefix(arg, "/workspace/demo/") {
				t.Fatalf("mapped path escaped workspace: %q", arg)
			}
		}
	})
}
