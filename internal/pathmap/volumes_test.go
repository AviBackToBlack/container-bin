package pathmap

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestPythonEnvIDStable(t *testing.T) {
	a := PythonEnvID(`C:\Work\Demo`, true)
	b := PythonEnvID(`c:\work\demo`, true)
	if a != b {
		t.Fatalf("expected case-insensitive stable ID: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "cb-python-313-") {
		t.Fatalf("bad prefix: %s", a)
	}
}

func TestGlobalPythonEnv(t *testing.T) {
	if got := PythonEnvID(`C:\Whatever`, false); got != "cb-python-313-global" {
		t.Fatalf("unexpected global env %q", got)
	}
}

func TestIsWindowsAbsPath(t *testing.T) {
	cases := map[string]bool{
		`D:\TEMP\foo.py`: true, `d:/temp/foo.py`: true, `foo.py`: false,
		`/tmp/foo.py`: false, `--output=D:\x`: false, `D:relative.txt`: false,
	}
	for in, want := range cases {
		if got := isWindowsAbsPath(in); got != want {
			t.Fatalf("isWindowsAbsPath(%q)=%v want %v", in, got, want)
		}
	}
}

func TestExplicitWindowsRelPath(t *testing.T) {
	cases := map[string]bool{
		`.\\foo.json`:      true,
		`..\\foo.json`:     true,
		`./foo.json`:       true,
		`../foo.json`:      true,
		`foo.json`:         false,
		`subdir\\foo.json`: false,
		`--regex=\\d+`:     false,
		`.`:                false,
		`..`:               false,
	}
	for in, want := range cases {
		if got := isExplicitWindowsRelPath(in); got != want {
			t.Fatalf("isExplicitWindowsRelPath(%q)=%v want %v", in, got, want)
		}
	}
}

func TestNormalizePathEqualsSplitByShell(t *testing.T) {
	tool := registry.Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	got := NormalizeToolArgs(tool, []string{"-chdir=", `.\\tf-demo`, "validate"})
	want := []string{`-chdir=.\\tf-demo`, "validate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeToolArgs() = %#v, want %#v", got, want)
	}
}

func TestNormalizePathEqualsAlreadyJoined(t *testing.T) {
	tool := registry.Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	in := []string{`-chdir=.\\tf-demo`, "validate"}
	got := NormalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("normalizeToolArgs() = %#v, want %#v", got, in)
	}
}

func TestNormalizePathEqualsSplitAtEndKeptAsIs(t *testing.T) {
	tool := registry.Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	in := []string{"validate", "-chdir="}
	got := NormalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("normalizeToolArgs() = %#v, want unchanged %#v", got, in)
	}
}

func TestStatefulProjectVolumeIDsAreScopedByRoot(t *testing.T) {
	a := StatefulProjectVolumeID("node24", "node-modules", `C:\Work\A`, false)
	b := StatefulProjectVolumeID("node24", "node-modules", `C:\Work\B`, false)
	if a == b {
		t.Fatalf("different cwd roots must not share project volumes: %q", a)
	}
	if !strings.HasPrefix(a, "cb-node24-node-modules-") {
		t.Fatalf("bad id %q", a)
	}
}

func TestSharedVolumeIDStable(t *testing.T) {
	if got := StatefulSharedVolumeID("node24", "npm-cache"); got != "cb-node24-npm-cache" {
		t.Fatalf("unexpected shared volume id %q", got)
	}
}

func TestLegacyPythonProfileKeepsPythonMarkers(t *testing.T) {
	legacy := registry.Tool{Name: "python", Provider: "python", Role: "python"}
	markers := ProjectMarkersFor(legacy)
	if !containsString(markers, "pyproject.toml") || !containsString(markers, ".git") {
		t.Fatalf("legacy python markers lost: %#v", markers)
	}
}

func TestStatefulWorkspacePreservesProjectBasename(t *testing.T) {
	tool := registry.Tool{Provider: "stateful"}
	got := WorkspaceRootFor(tool, filepath.Join("tmp", "node-demo"))
	if got != "/workspace/node-demo" {
		t.Fatalf("workspaceRootFor() = %q", got)
	}
}

func TestStatelessWorkspaceRemainsStable(t *testing.T) {
	tt := registry.Tool{Provider: "stateless"}
	if got := WorkspaceRootFor(tt, filepath.Join("tmp", "demo")); got != "/workspace" {
		t.Fatalf("workspaceRootFor() = %q", got)
	}
}

func TestStatefulWorkspaceDestination(t *testing.T) {
	got := StatefulWorkspaceDestination("/workspace/node_modules", "/workspace/node-demo")
	if got != "/workspace/node-demo/node_modules" {
		t.Fatalf("statefulWorkspaceDestination() = %q", got)
	}
}
