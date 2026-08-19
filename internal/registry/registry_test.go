package registry

import (
	"reflect"
	"strings"
	"testing"
)

// containsString mirrors the tiny helper these profile assertions used while
// they lived in package main. The production copy now belongs to the
// path-mapping package, which is a layer above this one, so the assertions
// keep a local copy rather than inverting the dependency for six lines.
func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestParseDefaultRegistry(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Tools) != 13 {
		t.Fatalf("expected 13 tools, got %d", len(reg.Tools))
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
	reg, err := ParseTOML(src)
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
	_, err := ParseTOML(`[tools.x]
image = "x"
magic = "y"
`)
	if err == nil {
		t.Fatal("expected error")
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
	reg, err := ParseTOML(src)
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
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "pip", "jq", "yq", "terraform", "ffmpeg", "node", "npm", "npx", "go", "gofmt"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing default tool %q", name)
		}
	}
	if strings.Join(reg.Tools["terraform"].PathEquals, "|") != "-chdir" {
		t.Fatal("terraform -chdir semantics missing")
	}
}

func TestDefaultToolSections(t *testing.T) {
	sections := DefaultToolSections()
	for _, name := range []string{"python", "yq", "terraform", "ffmpeg", "node", "npm", "npx", "go", "gofmt"} {
		if !strings.Contains(sections[name], "[tools."+name+"]") {
			t.Fatalf("bad section for %s: %q", name, sections[name])
		}
	}
}

func TestNodeProfilesShareStateGroupAndVolumes(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
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

func TestParseVolumeBinding(t *testing.T) {
	name, dst, err := ParseVolumeBinding("node-modules:/workspace/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if name != "node-modules" || dst != "/workspace/node_modules" {
		t.Fatalf("got %q %q", name, dst)
	}
	if _, _, err := ParseVolumeBinding("broken"); err == nil {
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
	reg, err := ParseTOML(`[tools.x]
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

func TestExposedNPMProfileParses(t *testing.T) {
	src := `[tools.cowsay]
image = "node:24-slim"
provider = "stateful"
command = ["/cb/npm-global/bin/cowsay"]
state_group = "node24"
shared_volumes = ["npm-cache:/root/.npm", "npm-global:/cb/npm-global"]
env_set = ["NPM_CONFIG_PREFIX=/cb/npm-global"]
`
	reg, err := ParseTOML(src)
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

func TestRegistrySchemaVersion(t *testing.T) {
	reg, err := ParseTOML("schema_version = 1\n\n[tools.x]\nimage = \"x:1\"\nprovider = \"stateless\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != 1 {
		t.Fatalf("schema=%d", reg.SchemaVersion)
	}
	if _, err := ParseTOML("schema_version = 2\n\n[tools.x]\nimage = \"x:1\"\nprovider = \"stateless\"\n"); err == nil {
		t.Fatal("expected newer schema rejection")
	}
}

func TestRejectDuplicateToolSections(t *testing.T) {
	_, err := ParseTOML(`[tools.jq]
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
		if !ReservedToolName(name) {
			t.Fatalf("%q should be reserved", name)
		}
		if _, err := ParseTOML("[tools." + name + "]\nimage = \"x:1\"\nprovider = \"stateless\"\n"); err == nil {
			t.Fatalf("registry accepted reserved tool name %q", name)
		}
	}
	for _, name := range []string{"python", "cowsay", "com", "lpt", "com0", "lpt0", "com10", "conx", "nul2"} {
		if ReservedToolName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
	}
}

func TestReservedCbVersionPrefixNames(t *testing.T) {
	for _, name := range []string{"cb-v", "cb-vault", "cb-version"} {
		if !ReservedToolName(name) {
			t.Fatalf("%q should be reserved (cb-v* never dispatches to a tool)", name)
		}
	}
	for _, name := range []string{"cb-x", "cbv", "vault"} {
		if ReservedToolName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
	}
}

func TestGoProfilesParseAndImage(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
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
	reg, err := ParseTOML(DefaultTOML)
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
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Tools["go"].ProjectVolumes) != 0 {
		t.Fatalf("go has unexpected project_volumes: %#v", reg.Tools["go"].ProjectVolumes)
	}
}

func TestGoSharedVolumesParse(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "gofmt"} {
		for _, spec := range reg.Tools[name].SharedVolumes {
			if _, _, err := ParseVolumeBinding(spec); err != nil {
				t.Fatalf("%s shared volume %q: %v", name, spec, err)
			}
		}
	}
}

func TestGoEnvAllowlistExcludesPathVariables(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
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
		if ReservedToolName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
		if !ValidToolName(name) {
			t.Fatalf("%q should be a valid tool name", name)
		}
	}
}

// Both profiles deliberately declare no forced path semantics. Forcing is
// positionally blind, so it would rewrite arguments the tool never treats as
// paths: the value after "go run PKG -o", and the trailing rewrite rule of
// "gofmt -r". Real paths are still handled by the general shape rules.
func TestGoProfilesDeclareNoForcedPathSemantics(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	goTool := reg.Tools["go"]
	if len(goTool.PathNext) != 0 {
		t.Fatalf("go must not declare path_next, got %#v", goTool.PathNext)
	}
	if len(goTool.PathEquals) != 0 || goTool.PathLast {
		t.Fatalf("go must not declare forced path semantics: %+v", goTool)
	}
	gofmtTool := reg.Tools["gofmt"]
	if gofmtTool.PathLast {
		t.Fatal("gofmt must not declare path_last; it would force the -r rewrite rule through path mapping")
	}
	if len(gofmtTool.PathNext) != 0 || len(gofmtTool.PathEquals) != 0 {
		t.Fatalf("gofmt must not declare forced path semantics: %+v", gofmtTool)
	}
}
