package wslpathmap

import (
	"path"
	"reflect"
	"regexp"
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
		mustReject := forbiddenFuzzWSLArg(input, true, project.Root+"/src", project.Root) ||
			forbiddenFuzzWSLArg(literal, false, project.Root+"/src", project.Root)
		// Inject lexical/mount-boundary proof, never the real host filesystem.
		got, cwd, err := mapToolArgs(tool, project, project.Root+"/src", "/workspace/demo", args, testDependencies(project.Root))
		if !reflect.DeepEqual(args, before) {
			t.Fatal("WSL path mapping mutated input argv")
		}
		if mustReject && err == nil {
			t.Fatalf("forbidden WSL argv accepted: %q", args)
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

var fuzzWindowsAbsolute = regexp.MustCompile(`^(?:[A-Za-z]:[\\/]|\\\\)`)

// This rejection oracle does not call the production spelling/path resolver.
// An implementation that turns an outside path into an inside path without
// requesting descendant proof must still fail this harness.
func forbiddenFuzzWSLArg(arg string, forced bool, cwd, root string) bool {
	if fuzzWindowsAbsolute.MatchString(arg) {
		return true
	}
	var candidate string
	switch {
	case path.IsAbs(arg):
		candidate = path.Clean(arg)
	case arg == "..." || strings.HasSuffix(arg, "/..."):
		return false
	case strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") ||
		forced && arg != "" && !strings.HasPrefix(arg, "-"):
		candidate = path.Join(cwd, arg)
	default:
		return false
	}
	return candidate != root && !strings.HasPrefix(candidate, root+"/") ||
		candidate == root+"/mounted/file"
}

func TestForbiddenFuzzWSLArgOracle(t *testing.T) {
	root, cwd := "/home/alice/project", "/home/alice/project/src"
	for _, tc := range []struct {
		arg    string
		forced bool
		want   bool
	}{
		{`C:\work\input`, false, true},
		{`c:/work/input`, true, true},
		{`\\server\share\input`, false, true},
		{"/etc/passwd", false, true},
		{"../../outside", true, true},
		{"../mounted/file", true, true},
		{"../inside", true, false},
		{"./...", true, false},
		{"../../outside/...", false, false},
		{"plain-value", false, false},
		{"--", true, false},
	} {
		if got := forbiddenFuzzWSLArg(tc.arg, tc.forced, cwd, root); got != tc.want {
			t.Errorf("rejection oracle(%q, forced=%v) = %v, want %v", tc.arg, tc.forced, got, tc.want)
		}
	}
}
