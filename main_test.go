package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestPythonEnvIDStable(t *testing.T) {
	a := pythonEnvID(`C:\Work\Demo`, true)
	b := pythonEnvID(`c:\work\demo`, true)
	if a != b {
		t.Fatalf("expected case-insensitive stable ID: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "cb-python-313-") {
		t.Fatalf("bad prefix: %s", a)
	}
}

func TestGlobalPythonEnv(t *testing.T) {
	if got := pythonEnvID(`C:\Whatever`, false); got != "cb-python-313-global" {
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

func TestAppendMissingDefaultToolsPreservesCustom(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/container-bin.toml"
	old := `[tools.python]
image = "python:3.13-slim"
provider = "python"
role = "python"

[tools.jq2]
image = "ghcr.io/jqlang/jq:latest"
provider = "stateless"
`
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendMissingDefaultTools(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["jq2"]; !ok {
		t.Fatal("custom jq2 was lost")
	}
	for _, name := range []string{"python3", "pip", "pip3", "jq", "yq", "terraform", "ffmpeg", "node", "npm", "npx", "go", "gofmt"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing migrated tool %q", name)
		}
	}
}

func TestNormalizePathEqualsSplitByShell(t *testing.T) {
	tool := registry.Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	got := normalizeToolArgs(tool, []string{"-chdir=", `.\\tf-demo`, "validate"})
	want := []string{`-chdir=.\\tf-demo`, "validate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeToolArgs() = %#v, want %#v", got, want)
	}
}

func TestNormalizePathEqualsAlreadyJoined(t *testing.T) {
	tool := registry.Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	in := []string{`-chdir=.\\tf-demo`, "validate"}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("normalizeToolArgs() = %#v, want %#v", got, in)
	}
}

func TestStatefulProjectVolumeIDsAreScopedByRoot(t *testing.T) {
	a := statefulProjectVolumeID("node24", "node-modules", `C:\Work\A`, false)
	b := statefulProjectVolumeID("node24", "node-modules", `C:\Work\B`, false)
	if a == b {
		t.Fatalf("different cwd roots must not share project volumes: %q", a)
	}
	if !strings.HasPrefix(a, "cb-node24-node-modules-") {
		t.Fatalf("bad id %q", a)
	}
}

func TestSharedVolumeIDStable(t *testing.T) {
	if got := statefulSharedVolumeID("node24", "npm-cache"); got != "cb-node24-npm-cache" {
		t.Fatalf("unexpected shared volume id %q", got)
	}
}

func TestLegacyPythonProfileKeepsPythonMarkers(t *testing.T) {
	legacy := registry.Tool{Name: "python", Provider: "python", Role: "python"}
	markers := projectMarkersFor(legacy)
	if !containsString(markers, "pyproject.toml") || !containsString(markers, ".git") {
		t.Fatalf("legacy python markers lost: %#v", markers)
	}
}

func TestStatefulWorkspacePreservesProjectBasename(t *testing.T) {
	tool := registry.Tool{Provider: "stateful"}
	got := workspaceRootFor(tool, filepath.Join("tmp", "node-demo"))
	if got != "/workspace/node-demo" {
		t.Fatalf("workspaceRootFor() = %q", got)
	}
}

func TestStatelessWorkspaceRemainsStable(t *testing.T) {
	tt := registry.Tool{Provider: "stateless"}
	if got := workspaceRootFor(tt, filepath.Join("tmp", "demo")); got != "/workspace" {
		t.Fatalf("workspaceRootFor() = %q", got)
	}
}

func TestStatefulWorkspaceDestination(t *testing.T) {
	got := statefulWorkspaceDestination("/workspace/node_modules", "/workspace/node-demo")
	if got != "/workspace/node-demo/node_modules" {
		t.Fatalf("statefulWorkspaceDestination() = %q", got)
	}
}

func TestRewriteRegistryWithoutToolsPreservesOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	src := `# custom comment
[tools.alpha]
image = "a:1"
provider = "stateless"

# keep me
[tools.beta]
image = "b:1"
provider = "stateless"

[tools.gamma]
image = "g:1"
provider = "stateless"
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteRegistryWithoutTools(path, map[string]bool{"beta": true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["beta"]; ok {
		t.Fatal("beta still present")
	}
	if _, ok := reg.Tools["alpha"]; !ok {
		t.Fatal("alpha lost")
	}
	if _, ok := reg.Tools["gamma"]; !ok {
		t.Fatal("gamma lost")
	}
	if !strings.Contains(string(data), "# custom comment") {
		t.Fatal("leading comments lost")
	}
}

func TestLockFileRoundTrip(t *testing.T) {
	lf := &LockFile{Version: 1, Images: map[string]LockEntry{
		"node:24-slim": {
			Configured: "node:24-slim",
			Resolved:   "node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		"ghcr.io/jqlang/jq:latest": {
			Configured: "ghcr.io/jqlang/jq:latest",
			Resolved:   "ghcr.io/jqlang/jq@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}}
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	if err := writeLockFile(path, lf); err != nil {
		t.Fatal(err)
	}
	got, err := loadLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Images) != 2 {
		t.Fatalf("unexpected lock: %#v", got)
	}
	if got.Images["node:24-slim"].Resolved != lf.Images["node:24-slim"].Resolved {
		t.Fatalf("node resolved mismatch: %#v", got.Images["node:24-slim"])
	}
}

func TestLockFileRejectsWrongEntryID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.lock")
	s := `lock_version = 1

[images.deadbeef]
configured = "node:24-slim"
resolved = "node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
`
	if err := os.WriteFile(path, []byte(s), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLockFile(path); err == nil {
		t.Fatal("expected wrong entry id rejection")
	}
}

func TestConfiguredImagesDeduplicatesSharedImage(t *testing.T) {
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"node": {Image: "node:24-slim"},
		"npm":  {Image: "node:24-slim"},
		"jq":   {Image: "ghcr.io/jqlang/jq:latest"},
	}}
	got := configuredImages(reg)
	want := []string{"ghcr.io/jqlang/jq:latest", "node:24-slim"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestImageRepository(t *testing.T) {
	cases := map[string]string{
		"node:24-slim":                         "node",
		"ghcr.io/jqlang/jq:latest":             "ghcr.io/jqlang/jq",
		"registry.example:5000/a/b:tag":        "registry.example:5000/a/b",
		"registry.example:5000/a/b@sha256:abc": "registry.example:5000/a/b",
	}
	for in, want := range cases {
		if got := imageRepository(in); got != want {
			t.Fatalf("imageRepository(%q)=%q want %q", in, got, want)
		}
	}
}

func TestVersionDefaultIsDev(t *testing.T) {
	// Release builds override this via -ldflags "-X main.version=vX.Y.Z".
	if version != "dev" {
		t.Fatalf("default version = %q, want \"dev\"", version)
	}
}

func TestNormalizePathEqualsSplitAtEndKeptAsIs(t *testing.T) {
	tool := registry.Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	in := []string{"validate", "-chdir="}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("normalizeToolArgs() = %#v, want unchanged %#v", got, in)
	}
}

func TestCanonicalRepository(t *testing.T) {
	cases := map[string]string{
		"python":                       "python",
		"library/python":               "python",
		"docker.io/library/python":     "python",
		"index.docker.io/library/node": "node",
		"docker.io/mikefarah/yq":       "mikefarah/yq",
		"ghcr.io/jqlang/jq":            "ghcr.io/jqlang/jq",
		"registry.example:5000/a/b":    "registry.example:5000/a/b",
		"lscr.io/linuxserver/ffmpeg":   "lscr.io/linuxserver/ffmpeg",
	}
	for in, want := range cases {
		if got := canonicalRepository(in); got != want {
			t.Fatalf("canonicalRepository(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMatchRepoDigestNormalizesDockerHubRefs(t *testing.T) {
	digests := []string{"python@sha256:" + strings.Repeat("a", 64)}
	for _, configured := range []string{"python:3.13-slim", "library/python:3.13-slim", "docker.io/library/python:3.13-slim"} {
		got, ok := matchRepoDigest(configured, digests)
		if !ok || got != digests[0] {
			t.Fatalf("matchRepoDigest(%q) = %q, %v; want %q, true", configured, got, ok, digests[0])
		}
	}
}

func TestMatchRepoDigestFailsClosedOnForeignRepo(t *testing.T) {
	digests := []string{"someone/else@sha256:" + strings.Repeat("b", 64)}
	if got, ok := matchRepoDigest("python:3.13-slim", digests); ok {
		t.Fatalf("expected no match for foreign repo digest, got %q", got)
	}
	if _, ok := matchRepoDigest("python:3.13-slim", nil); ok {
		t.Fatal("expected no match for empty digest list")
	}
	if _, ok := matchRepoDigest("python:3.13-slim", []string{"python@md5:oops", "garbage"}); ok {
		t.Fatal("expected malformed digests to be ignored")
	}
}

func TestInvokedNameIsCaseInsensitive(t *testing.T) {
	// Bare names only: filepath.Base separator handling is OS-specific and
	// dispatch only depends on the final path element anyway.
	cases := map[string]string{
		"python.exe":    "python",
		"PYTHON.EXE":    "python",
		"Cb.ExE":        "cb",
		"cb":            "cb",
		"terraform.exe": "terraform",
		"cb-v1.0.0.exe": "cb-v1.0.0",
	}
	for in, want := range cases {
		if got := invokedName(in); got != want {
			t.Fatalf("invokedName(%q)=%q want %q", in, got, want)
		}
	}
}
