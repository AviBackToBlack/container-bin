package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func TestParseDefaultRegistry(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Tools) != 11 {
		t.Fatalf("expected 11 tools, got %d", len(reg.Tools))
	}
	jq := reg.Tools["jq"]
	if jq.Provider != "stateless" || jq.Image != "ghcr.io/jqlang/jq:latest" {
		t.Fatalf("bad jq profile: %+v", jq)
	}
	pip := reg.Tools["pip"]
	if pip.Provider != "python" || pip.Role != "pip" {
		t.Fatalf("bad pip profile: %+v", pip)
	}
}

func TestParseCommandAndArgsPrefix(t *testing.T) {
	src := `[tools.demo]
image = "example/demo:1"
provider = "stateless"
command = ["demo", "sub"]
args_prefix = ["--quiet"]
`
	reg, err := parseRegistryTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Tools["demo"]
	if strings.Join(got.Command, "|") != "demo|sub" {
		t.Fatalf("bad command: %#v", got.Command)
	}
	if strings.Join(got.ArgsPrefix, "|") != "--quiet" {
		t.Fatalf("bad prefix: %#v", got.ArgsPrefix)
	}
}

func TestRejectUnknownRegistryKey(t *testing.T) {
	_, err := parseRegistryTOML(`[tools.x]
image = "x"
magic = "y"
`)
	if err == nil {
		t.Fatal("expected error")
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

func TestParseToolSemantics(t *testing.T) {
	src := `[tools.ff]
image = "x/ff:1"
provider = "stateless"
path_next = ["-i", "-attach"]
path_equals = ["-chdir"]
path_last = true
path_last_if_any = ["-i"]
env_prefixes = ["AWS_", "TF_VAR_"]
env_names = ["NO_COLOR"]
`
	reg, err := parseRegistryTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Tools["ff"]
	if !got.PathLast {
		t.Fatal("path_last not parsed")
	}
	if strings.Join(got.PathLastIfAny, "|") != "-i" {
		t.Fatalf("bad path_last_if_any: %#v", got.PathLastIfAny)
	}
	if strings.Join(got.PathNext, "|") != "-i|-attach" {
		t.Fatalf("bad path_next: %#v", got.PathNext)
	}
	if strings.Join(got.PathEquals, "|") != "-chdir" {
		t.Fatalf("bad path_equals: %#v", got.PathEquals)
	}
	if strings.Join(got.EnvPrefixes, "|") != "AWS_|TF_VAR_" {
		t.Fatalf("bad env_prefixes: %#v", got.EnvPrefixes)
	}
	if strings.Join(got.EnvNames, "|") != "NO_COLOR" {
		t.Fatalf("bad env_names: %#v", got.EnvNames)
	}
}

func TestDefaultRegistryHasV06Tools(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "pip", "jq", "yq", "terraform", "ffmpeg", "node", "npm", "npx"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing default tool %q", name)
		}
	}
	if strings.Join(reg.Tools["terraform"].PathEquals, "|") != "-chdir" {
		t.Fatal("terraform -chdir semantics missing")
	}
}

func TestDefaultToolSections(t *testing.T) {
	sections := defaultToolSections()
	for _, name := range []string{"python", "yq", "terraform", "ffmpeg", "node", "npm", "npx"} {
		if !strings.Contains(sections[name], "[tools."+name+"]") {
			t.Fatalf("bad section for %s: %q", name, sections[name])
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
	reg, err := parseRegistryTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["jq2"]; !ok {
		t.Fatal("custom jq2 was lost")
	}
	for _, name := range []string{"python3", "pip", "pip3", "jq", "yq", "terraform", "ffmpeg", "node", "npm", "npx"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing migrated tool %q", name)
		}
	}
}

func TestNormalizePathEqualsSplitByShell(t *testing.T) {
	tool := Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	got := normalizeToolArgs(tool, []string{"-chdir=", `.\\tf-demo`, "validate"})
	want := []string{`-chdir=.\\tf-demo`, "validate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeToolArgs() = %#v, want %#v", got, want)
	}
}

func TestNormalizePathEqualsAlreadyJoined(t *testing.T) {
	tool := Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	in := []string{`-chdir=.\\tf-demo`, "validate"}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("normalizeToolArgs() = %#v, want %#v", got, in)
	}
}

func TestNodeProfilesShareStateGroupAndVolumes(t *testing.T) {
	reg, err := parseRegistryTOML(defaultRegistryTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "npm", "npx"} {
		tool := reg.Tools[name]
		if tool.Provider != "stateful" || tool.StateGroup != "node24" {
			t.Fatalf("bad %s state profile: %+v", name, tool)
		}
		if !containsString(tool.ProjectMarkers, "package.json") {
			t.Fatalf("%s missing package.json marker", name)
		}
		if !containsString(tool.ProjectVolumes, "node-modules:/workspace/node_modules") {
			t.Fatalf("%s missing node_modules volume", name)
		}
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

func TestParseVolumeBinding(t *testing.T) {
	name, dst, err := parseVolumeBinding("node-modules:/workspace/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if name != "node-modules" || dst != "/workspace/node_modules" {
		t.Fatalf("got %q %q", name, dst)
	}
	if _, _, err := parseVolumeBinding("broken"); err == nil {
		t.Fatal("expected invalid binding error")
	}
}

func TestEnvSetValidation(t *testing.T) {
	if !validEnvAssignment("NPM_CONFIG_PREFIX=/cb/npm-global") {
		t.Fatal("valid env rejected")
	}
	if validEnvAssignment("1BAD=x") {
		t.Fatal("invalid env accepted")
	}
	reg, err := parseRegistryTOML(`[tools.x]
image = "x"
provider = "stateless"
env_set = ["A=b"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reg.Tools["x"].EnvSet, "|") != "A=b" {
		t.Fatalf("env_set not parsed")
	}
}

func TestLegacyPythonProfileKeepsPythonMarkers(t *testing.T) {
	legacy := Tool{Name: "python", Provider: "python", Role: "python"}
	markers := projectMarkersFor(legacy)
	if !containsString(markers, "pyproject.toml") || !containsString(markers, ".git") {
		t.Fatalf("legacy python markers lost: %#v", markers)
	}
}

func TestStatefulWorkspacePreservesProjectBasename(t *testing.T) {
	tool := Tool{Provider: "stateful"}
	got := workspaceRootFor(tool, filepath.Join("tmp", "node-demo"))
	if got != "/workspace/node-demo" {
		t.Fatalf("workspaceRootFor() = %q", got)
	}
}

func TestStatelessWorkspaceRemainsStable(t *testing.T) {
	tt := Tool{Provider: "stateless"}
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

func TestTomlArray(t *testing.T) {
	got := tomlArray([]string{"a", "b c"})
	if got != `["a", "b c"]` {
		t.Fatalf("tomlArray = %q", got)
	}
}

func TestExposedNPMProfileParses(t *testing.T) {
	src := `[tools.cowsay]
image = "node:24-slim"
provider = "stateful"
command = ["/cb/npm-global/bin/cowsay"]
state_group = "node24"
shared_volumes = ["npm-cache:/root/.npm", "npm-global:/cb/npm-global"]
env_set = ["NPM_CONFIG_PREFIX=/cb/npm-global"]
`
	reg, err := parseRegistryTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.Tools["cowsay"]
	if got.Provider != "stateful" || got.StateGroup != "node24" {
		t.Fatalf("bad exposed profile: %+v", got)
	}
	if strings.Join(got.Command, "|") != "/cb/npm-global/bin/cowsay" {
		t.Fatalf("bad command: %#v", got.Command)
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
	reg, err := parseRegistryTOML(string(data))
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

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup unexpectedly remains: %v", err)
	}
}

func TestRegistrySchemaVersion(t *testing.T) {
	reg, err := parseRegistryTOML("schema_version = 1\n\n[tools.x]\nimage = \"x:1\"\nprovider = \"stateless\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != 1 {
		t.Fatalf("schema=%d", reg.SchemaVersion)
	}
	if _, err := parseRegistryTOML("schema_version = 2\n\n[tools.x]\nimage = \"x:1\"\nprovider = \"stateless\"\n"); err == nil {
		t.Fatal("expected newer schema rejection")
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
	reg := Registry{Tools: map[string]Tool{
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

func TestRejectDuplicateToolSections(t *testing.T) {
	_, err := parseRegistryTOML(`[tools.jq]
image = "a:1"
provider = "stateless"

[tools.jq]
image = "b:1"
provider = "stateless"
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate section rejection, got %v", err)
	}
}

func TestReservedToolNames(t *testing.T) {
	reserved := []string{"cb", "container-bin", "con", "prn", "aux", "nul", "com1", "com9", "lpt1", "lpt9"}
	for _, name := range reserved {
		if !reservedToolName(name) {
			t.Fatalf("%q should be reserved", name)
		}
		if _, err := parseRegistryTOML("[tools." + name + "]\nimage = \"x:1\"\nprovider = \"stateless\"\n"); err == nil {
			t.Fatalf("registry accepted reserved tool name %q", name)
		}
	}
	for _, name := range []string{"python", "cowsay", "com", "lpt", "com0", "lpt0", "com10", "conx", "nul2"} {
		if reservedToolName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
	}
}

func TestNormalizePathEqualsSplitAtEndKeptAsIs(t *testing.T) {
	tool := Tool{Name: "terraform", PathEquals: []string{"-chdir"}}
	in := []string{"validate", "-chdir="}
	got := normalizeToolArgs(tool, in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("normalizeToolArgs() = %#v, want unchanged %#v", got, in)
	}
}
