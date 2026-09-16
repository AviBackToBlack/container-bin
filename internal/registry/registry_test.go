package registry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/toml"
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
	if len(reg.Tools) != 24 {
		t.Fatalf("expected 24 tools, got %d", len(reg.Tools))
	}
	if len(reg.ToolNames()) != 27 {
		t.Fatalf("expected 27 invokable shim names, got %d", len(reg.ToolNames()))
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

func TestParseRegistrySectionHeadersUseSharedSyntax(t *testing.T) {
	src := `schema_version = 1

[tools.demo] # inline section comment
image = "example/demo:1"
provider = "stateless"
args_prefix = ["literal # value"] # actual comment
`
	reg, err := ParseTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(reg.Tools["demo"].ArgsPrefix, "|"); got != "literal # value" {
		t.Fatalf("args_prefix = %q, want quoted hash preserved", got)
	}

	for _, malformed := range []string{
		"[[tools.demo]]\n",
		"[tools.demo] trailing\n",
		"[tools.demo]]\n",
	} {
		if _, err := ParseTOML(malformed); err == nil {
			t.Fatalf("ParseTOML(%q) unexpectedly succeeded", malformed)
		}
	}
}

func TestSectionsFromTOMLUsesSharedHeaderParser(t *testing.T) {
	src := `[tools.alpha] # keep this comment
image = "alpha:1"

[defaults.node] # next section
version = "24"
`
	sections, err := sectionsFromTOML(src, "tools.")
	if err != nil {
		t.Fatal(err)
	}
	want := "\n[tools.alpha] # keep this comment\nimage = \"alpha:1\"\n\n"
	if sections["alpha"] != want {
		t.Fatalf("alpha section = %q, want %q", sections["alpha"], want)
	}
	if len(sections) != 1 {
		t.Fatalf("sections = %#v, want only alpha", sections)
	}
	if _, err := sectionsFromTOML("[[tools.alpha]]\n", "tools."); err == nil || !strings.Contains(err.Error(), "array table") {
		t.Fatalf("array-table error = %v", err)
	}
}

func TestDefaultAliasesSwitchAsOneFamily(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	reg.Defaults["node"] = "22"
	for _, alias := range []string{"node", "npm", "npx"} {
		tool, resolved, ok := reg.Resolve(alias)
		if !ok || resolved != alias+"22" {
			t.Fatalf("%s resolved to %q, ok=%v", alias, resolved, ok)
		}
		if tool.StateGroup != "node22" || tool.Image != "node:22-slim" {
			t.Fatalf("%s resolved with wrong state/image: %+v", alias, tool)
		}
	}
	if _, resolved, ok := reg.Resolve("node24"); !ok || resolved != "node24" {
		t.Fatalf("stable node24 profile changed: resolved=%q ok=%v", resolved, ok)
	}
}

func TestDefaultAliasValidationFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"schema_v1", strings.Replace(DefaultTOML, "schema_version = 2", "schema_version = 1", 1), "requires schema_version = 2"},
		{"unavailable_version", strings.Replace(DefaultTOML, "version = \"24\"", "version = \"26\"", 1), "selects unavailable version"},
		{"partial_metadata", strings.Replace(DefaultTOML, "default_alias = \"node\"\n", "", 1), "must be declared together"},
		{"incomplete_family", strings.Replace(DefaultTOML, DefaultToolSections()["npx22"], "", 1), "does not supply the same aliases"},
		{"concrete_collision", DefaultTOML + "\n[tools.node]\nimage = \"node:99\"\nprovider = \"stateless\"\n", "collides with a concrete tool profile"},
		{"cross_family_collision", DefaultTOML + "\n[defaults.alt]\nversion = \"1\"\n[tools.alt1]\ndefault_family = \"alt\"\ndefault_version = \"1\"\ndefault_alias = \"node\"\nimage = \"alt:1\"\n", "declared by both"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTOML(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
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
	for _, name := range []string{"python", "pip", "jq", "yq", "terraform", "ffmpeg", "node24", "npm24", "npx24", "go", "gofmt", "rustc", "cargo", "uv", "uvx", "dotnet", "ruby", "gem", "bundle"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing default tool %q", name)
		}
	}
	for _, alias := range []string{"node", "npm", "npx"} {
		if _, resolved, ok := reg.Resolve(alias); !ok || resolved != alias+"24" {
			t.Fatalf("alias %q resolved to %q, ok=%v", alias, resolved, ok)
		}
	}
	if strings.Join(reg.Tools["terraform"].PathEquals, "|") != "-chdir" {
		t.Fatal("terraform -chdir semantics missing")
	}
}

func TestDefaultToolSections(t *testing.T) {
	sections := DefaultToolSections()
	for _, name := range []string{"python", "yq", "terraform", "ffmpeg", "node24", "node22", "npm24", "npm22", "npx24", "npx22", "go", "gofmt", "rustc", "cargo", "uv", "uvx", "dotnet", "ruby", "gem", "bundle"} {
		if !strings.Contains(sections[name], "[tools."+name+"]") {
			t.Fatalf("bad section for %s: %q", name, sections[name])
		}
	}
	if section := DefaultFamilySections()["node"]; !strings.Contains(section, "[defaults.node]") {
		t.Fatalf("bad default family section: %q", section)
	}
}

func TestNodeProfilesShareStateGroupAndVolumes(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node24", "npm24", "npx24"} {
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
	for _, spec := range []string{
		"data:/venv",
		"data:/venv/bin",
		"data:/venv/./bin",
		"cache:/root/.cache/pip",
		"cache:/root/.cache/pip/http",
	} {
		if _, _, err := ParseVolumeBinding(spec); err == nil || !strings.Contains(err.Error(), "reserved for python provider state") {
			t.Errorf("ParseVolumeBinding(%q) error = %v, want reserved-path error", spec, err)
		}
	}
	for _, spec := range []string{
		"data:/venv/..",
		"cache:/root/.cache/pip/..",
		"data:/safe/../venv",
		"data:/safe/../x",
	} {
		if _, _, err := ParseVolumeBinding(spec); err == nil || !strings.Contains(err.Error(), "must not contain \"..\"") {
			t.Errorf("ParseVolumeBinding(%q) error = %v, want traversal error", spec, err)
		}
	}
	for _, spec := range []string{"data:/", "data:/./"} {
		if _, _, err := ParseVolumeBinding(spec); err == nil || !strings.Contains(err.Error(), "must not be the filesystem root") {
			t.Errorf("ParseVolumeBinding(%q) error = %v, want filesystem-root error", spec, err)
		}
	}
	for _, spec := range []string{"data:/venv-data", "cache:/root/.cache/pipeline"} {
		if _, _, err := ParseVolumeBinding(spec); err != nil {
			t.Errorf("ParseVolumeBinding(%q) unexpected error: %v", spec, err)
		}
	}
}

func TestRegistryRejectsProviderReservedVolumeDestinations(t *testing.T) {
	base := `[tools.x]
image = "x:1"
provider = "stateful"
state_group = "xgroup"
`
	for _, field := range []string{"project_volumes", "shared_volumes"} {
		for _, dst := range []string{"/venv", "/venv/bin", "/root/.cache/pip", "/root/.cache/pip/http"} {
			t.Run(field+strings.ReplaceAll(dst, "/", "_"), func(t *testing.T) {
				src := base + field + " = " + toml.Array([]string{"data:" + dst}) + "\n"
				_, err := ParseTOML(src)
				if err == nil || !strings.Contains(err.Error(), "reserved for python provider state") {
					t.Fatalf("ParseTOML error = %v, want reserved-path error", err)
				}
			})
		}
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
	if _, err := ParseTOML("schema_version = 3\n\n[tools.x]\nimage = \"x:1\"\nprovider = \"stateless\"\n"); err == nil {
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

func TestVersionedBinaryNameMatchesReservation(t *testing.T) {
	for _, name := range []string{"cb-v0", "cb-v1.2.3", "cb-v9-preview"} {
		if !IsVersionedBinaryName(name) {
			t.Fatalf("%q should be recognized as a versioned binary", name)
		}
		if !ReservedToolName(name) {
			t.Fatalf("%q should be reserved", name)
		}
	}
	for _, name := range []string{"cb-v", "cb-vault", "cb-version", "cb-vx", "cb-x", "cbv", "vault"} {
		if IsVersionedBinaryName(name) {
			t.Fatalf("%q should not be recognized as a versioned binary", name)
		}
		if ReservedToolName(name) {
			t.Fatalf("%q should not be reserved", name)
		}
		if _, err := ParseTOML("[tools." + name + "]\nimage = \"x:1\"\nprovider = \"stateless\"\n"); err != nil {
			t.Fatalf("registry rejected available tool name %q: %v", name, err)
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

func TestRustCargoProfiles(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}

	const image = "rust:1.98.1-slim-bookworm"
	rustc := reg.Tools["rustc"]
	if rustc.Image != image || rustc.Provider != "stateless" || !reflect.DeepEqual(rustc.Command, []string{"rustc"}) {
		t.Fatalf("bad rustc profile: %+v", rustc)
	}

	cargo := reg.Tools["cargo"]
	if cargo.Image != image || cargo.Provider != "stateful" || cargo.StateGroup != "rust198" || !reflect.DeepEqual(cargo.Command, []string{"cargo"}) {
		t.Fatalf("bad cargo profile: %+v", cargo)
	}
	wantVolumes := []string{
		"registry:/usr/local/cargo/registry",
		"git:/usr/local/cargo/git",
		"global:/cb/cargo-global",
	}
	if !reflect.DeepEqual(cargo.SharedVolumes, wantVolumes) {
		t.Fatalf("cargo shared_volumes = %#v, want %#v", cargo.SharedVolumes, wantVolumes)
	}
	if len(cargo.ProjectVolumes) != 0 {
		t.Fatalf("cargo has unexpected project_volumes: %#v", cargo.ProjectVolumes)
	}

	wantMarkers := []string{"Cargo.toml", "rust-toolchain.toml", "rust-toolchain", ".git"}
	for _, tool := range []Tool{rustc, cargo} {
		if !reflect.DeepEqual(tool.ProjectMarkers, wantMarkers) {
			t.Fatalf("%s project_markers = %#v, want %#v", tool.Name, tool.ProjectMarkers, wantMarkers)
		}
	}
	if cargo.ProjectRootMode != "outermost" {
		t.Fatalf("cargo project_root_mode = %q, want outermost", cargo.ProjectRootMode)
	}
	wantPathOptions := []string{"--target-dir", "--manifest-path"}
	if !reflect.DeepEqual(cargo.PathNext, wantPathOptions) || !reflect.DeepEqual(cargo.PathEquals, wantPathOptions) {
		t.Fatalf("bad cargo path semantics: next=%#v equals=%#v", cargo.PathNext, cargo.PathEquals)
	}
	for _, entry := range []string{
		"CARGO_INSTALL_ROOT=/cb/cargo-global",
		"PATH=/cb/cargo-global/bin:/usr/local/cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	} {
		if !containsString(cargo.EnvSet, entry) {
			t.Fatalf("cargo env_set missing %q: %#v", entry, cargo.EnvSet)
		}
	}
	if !containsString(cargo.EnvPrefixes, "CARGO_REGISTRIES_") {
		t.Fatalf("cargo env_prefixes missing CARGO_REGISTRIES_: %#v", cargo.EnvPrefixes)
	}
	for _, pathVariable := range []string{"CARGO_HOME", "CARGO_TARGET_DIR", "RUSTUP_HOME", "RUSTC_WRAPPER"} {
		if containsString(cargo.EnvNames, pathVariable) {
			t.Fatalf("cargo env allowlist must not include path-valued %q", pathVariable)
		}
	}
}

func TestUVProfiles(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}

	const image = "ghcr.io/astral-sh/uv:0.12-python3.13-trixie-slim"
	uv := reg.Tools["uv"]
	uvx := reg.Tools["uvx"]
	for name, tool := range map[string]Tool{"uv": uv, "uvx": uvx} {
		if tool.Image != image || tool.Provider != "stateful" || tool.StateGroup != "uv012-py313" || !reflect.DeepEqual(tool.Command, []string{name}) {
			t.Fatalf("bad %s profile: %+v", name, tool)
		}
		wantShared := []string{"cache:/root/.cache/uv", "tools:/cb/uv-tools", "tool-bin:/cb/uv-bin"}
		if !reflect.DeepEqual(tool.SharedVolumes, wantShared) {
			t.Fatalf("%s shared_volumes = %#v, want %#v", name, tool.SharedVolumes, wantShared)
		}
		for _, entry := range []string{
			"UV_CACHE_DIR=/root/.cache/uv",
			"UV_TOOL_DIR=/cb/uv-tools",
			"UV_TOOL_BIN_DIR=/cb/uv-bin",
			"UV_LINK_MODE=copy",
			"UV_PYTHON_DOWNLOADS=never",
		} {
			if !containsString(tool.EnvSet, entry) {
				t.Fatalf("%s env_set missing %q: %#v", name, entry, tool.EnvSet)
			}
		}
		if !containsString(tool.EnvPrefixes, "UV_INDEX_") {
			t.Fatalf("%s env_prefixes missing UV_INDEX_: %#v", name, tool.EnvPrefixes)
		}
		for _, envName := range []string{"UV_INDEX", "UV_DEFAULT_INDEX", "UV_NO_CACHE", "UV_OFFLINE", "HTTP_PROXY"} {
			if !containsString(tool.EnvNames, envName) {
				t.Fatalf("%s env_names missing %q: %#v", name, envName, tool.EnvNames)
			}
		}
		if !containsString(tool.ProjectMarkers, "pyproject.toml") || !containsString(tool.ProjectMarkers, "uv.lock") {
			t.Fatalf("%s project markers do not cover uv projects: %#v", name, tool.ProjectMarkers)
		}
		wantPathEquals := []string{"--project", "--directory", "--config-file", "--cache-dir"}
		if len(tool.PathNext) != 0 || !reflect.DeepEqual(tool.PathEquals, wantPathEquals) || tool.PathLast {
			t.Fatalf("%s path semantics = %+v, want path_equals %#v only", name, tool, wantPathEquals)
		}
		for _, pathVariable := range []string{"UV_PROJECT", "UV_WORKING_DIR", "UV_CONFIG_FILE", "UV_CACHE_DIR", "UV_TOOL_DIR", "UV_TOOL_BIN_DIR", "UV_PROJECT_ENVIRONMENT", "VIRTUAL_ENV", "PATH"} {
			if containsString(tool.EnvNames, pathVariable) {
				t.Fatalf("%s env allowlist must not include path-valued %q", name, pathVariable)
			}
		}
	}

	if !reflect.DeepEqual(uv.ProjectVolumes, []string{"project-env:/cb/uv-project-env"}) {
		t.Fatalf("uv project_volumes = %#v", uv.ProjectVolumes)
	}
	if len(uvx.ProjectVolumes) != 0 {
		t.Fatalf("uvx must not allocate an unused project environment: %#v", uvx.ProjectVolumes)
	}
	for _, entry := range []string{
		"UV_PROJECT_ENVIRONMENT=/cb/uv-project-env",
		"VIRTUAL_ENV=/cb/uv-project-env",
		"PATH=/cb/uv-project-env/bin:/cb/uv-bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin",
	} {
		if !containsString(uv.EnvSet, entry) {
			t.Fatalf("uv env_set missing %q: %#v", entry, uv.EnvSet)
		}
	}
}

func TestDotnetProfile(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}

	dotnet := reg.Tools["dotnet"]
	if dotnet.Image != "mcr.microsoft.com/dotnet/sdk:10.0" || dotnet.Provider != "stateful" || dotnet.StateGroup != "dotnet10" || !reflect.DeepEqual(dotnet.Command, []string{"dotnet"}) {
		t.Fatalf("bad dotnet profile: %+v", dotnet)
	}
	wantVolumes := []string{
		"nuget-packages:/root/.nuget/packages",
		"nuget-config:/root/.nuget/NuGet",
		"dotnet-home:/root/.dotnet",
	}
	if !reflect.DeepEqual(dotnet.SharedVolumes, wantVolumes) {
		t.Fatalf("dotnet shared_volumes = %#v, want %#v", dotnet.SharedVolumes, wantVolumes)
	}
	if len(dotnet.ProjectVolumes) != 0 {
		t.Fatalf("dotnet build output must stay host-visible, got project_volumes %#v", dotnet.ProjectVolumes)
	}
	for _, entry := range []string{
		"DOTNET_CLI_HOME=/root",
		"NUGET_PACKAGES=/root/.nuget/packages",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_NOLOGO=1",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"PATH=/root/.dotnet/tools:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	} {
		if !containsString(dotnet.EnvSet, entry) {
			t.Fatalf("dotnet env_set missing %q: %#v", entry, dotnet.EnvSet)
		}
	}
	if !containsString(dotnet.EnvPrefixes, "NUGETPACKAGESOURCECREDENTIALS_") {
		t.Fatalf("dotnet env_prefixes missing NuGet source credentials: %#v", dotnet.EnvPrefixes)
	}
	if !containsString(dotnet.EnvNames, "DOTNET_USE_POLLING_FILE_WATCHER") {
		t.Fatalf("dotnet env_names missing polling watcher opt-in: %#v", dotnet.EnvNames)
	}
	for _, pathVariable := range []string{"DOTNET_ROOT", "DOTNET_CLI_HOME", "NUGET_PACKAGES", "NUGET_HTTP_CACHE_PATH", "NUGET_PLUGIN_PATHS", "NUGET_CREDENTIALPROVIDERS_PATH", "MSBuildSDKsPath"} {
		if containsString(dotnet.EnvNames, pathVariable) {
			t.Fatalf("dotnet env allowlist must not include path-valued %q", pathVariable)
		}
	}
	if len(dotnet.PathNext) != 0 || len(dotnet.PathEquals) != 0 || dotnet.PathLast {
		t.Fatalf("dotnet must not force path semantics: %+v", dotnet)
	}
}

func TestRubyGemBundleProfiles(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}

	const image = "ruby:4.0-trixie"
	for _, name := range []string{"ruby", "gem", "bundle"} {
		tool := reg.Tools[name]
		if tool.Image != image || tool.Provider != "stateful" || tool.StateGroup != "ruby40" || !reflect.DeepEqual(tool.Command, []string{name}) {
			t.Fatalf("bad %s profile: %+v", name, tool)
		}
		if !containsString(tool.ProjectMarkers, "Gemfile") || !containsString(tool.ProjectMarkers, "Gemfile.lock") {
			t.Fatalf("%s project markers do not cover Bundler projects: %#v", name, tool.ProjectMarkers)
		}
		if !containsString(tool.SharedVolumes, "gems:/cb/ruby-gems") {
			t.Fatalf("%s missing shared gem home: %#v", name, tool.SharedVolumes)
		}
		for _, entry := range []string{
			"GEM_HOME=/cb/ruby-gems",
			"GEM_PATH=/cb/ruby-gems:/usr/local/lib/ruby/gems/4.0.0",
			"PATH=/cb/ruby-gems/bin:/usr/local/bundle/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin",
		} {
			if !containsString(tool.EnvSet, entry) {
				t.Fatalf("%s env_set missing %q: %#v", name, entry, tool.EnvSet)
			}
		}
		if len(tool.PathNext) != 0 || len(tool.PathEquals) != 0 || tool.PathLast {
			t.Fatalf("%s must not force path semantics: %+v", name, tool)
		}
		for _, pathVariable := range []string{"GEM_HOME", "GEM_PATH", "GEM_SPEC_CACHE", "GEMRC", "RUBYGEMS_GEMDEPS", "BUNDLE_PATH", "BUNDLE_GEMFILE", "BUNDLE_APP_CONFIG", "BUNDLE_USER_CACHE"} {
			if containsString(tool.EnvNames, pathVariable) {
				t.Fatalf("%s env allowlist must not include path-valued %q", name, pathVariable)
			}
		}
	}

	if len(reg.Tools["ruby"].ProjectVolumes) != 0 || len(reg.Tools["gem"].ProjectVolumes) != 0 {
		t.Fatal("ruby and gem must not allocate Bundler project state")
	}
	bundle := reg.Tools["bundle"]
	if !reflect.DeepEqual(bundle.ProjectVolumes, []string{"bundle:/cb/bundle"}) {
		t.Fatalf("bundle project_volumes = %#v", bundle.ProjectVolumes)
	}
	for _, entry := range []string{
		"GEM_SPEC_CACHE=/cb/ruby-spec-cache",
		"BUNDLE_PATH=/cb/bundle",
		"BUNDLE_APP_CONFIG=/cb/bundle/.config",
		"BUNDLE_USER_CACHE=/cb/bundle-cache",
		"BUNDLE_SILENCE_ROOT_WARNING=1",
	} {
		if !containsString(bundle.EnvSet, entry) {
			t.Fatalf("bundle env_set missing %q: %#v", entry, bundle.EnvSet)
		}
	}
}

// Node 24 is the default runtime, but it is not a guarantee that every npm
// package is ABI-compatible with it. Node 22 is the supported LTS alternative,
// with fully isolated state even though the logical volume names are identical.
func TestNode22ProfilesParseAndVolumes(t *testing.T) {
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node22", "npm22", "npx22"} {
		tool, ok := reg.Tools[name]
		if !ok {
			t.Fatalf("missing default tool %q", name)
		}
		if tool.Image != "node:22-slim" {
			t.Fatalf("%s image = %q, want node:22-slim", name, tool.Image)
		}
		if tool.StateGroup != "node22" {
			t.Fatalf("%s state_group = %q, want node22", name, tool.StateGroup)
		}
	}
	pairs := []struct{ old, new string }{
		{"node24", "node22"},
		{"npm24", "npm22"},
		{"npx24", "npx22"},
	}
	for _, p := range pairs {
		oldTool, newTool := reg.Tools[p.old], reg.Tools[p.new]
		if !reflect.DeepEqual(oldTool.ProjectVolumes, newTool.ProjectVolumes) {
			t.Fatalf("%s and %s project_volumes differ: %#v vs %#v", p.old, p.new, oldTool.ProjectVolumes, newTool.ProjectVolumes)
		}
		if !reflect.DeepEqual(oldTool.SharedVolumes, newTool.SharedVolumes) {
			t.Fatalf("%s and %s shared_volumes differ: %#v vs %#v", p.old, p.new, oldTool.SharedVolumes, newTool.SharedVolumes)
		}
	}
}

func TestParseHostMount(t *testing.T) {
	cases := []struct {
		name      string
		spec      string
		wantErr   bool
		wantSrc   string
		wantDst   string
		wantMode  string
		errSubstr string
	}{
		{
			name:     "ro_ok",
			spec:     "D:\\Video:/root/videos:ro",
			wantSrc:  "D:\\Video",
			wantDst:  "/root/videos",
			wantMode: "ro",
		},
		{
			name:     "rw_ok",
			spec:     "D:\\Video:/root/videos:rw",
			wantSrc:  "D:\\Video",
			wantDst:  "/root/videos",
			wantMode: "rw",
		},
		{
			name:     "userprofile_ok",
			spec:     "%USERPROFILE%\\.claude:/root/.claude:ro",
			wantSrc:  "%USERPROFILE%\\.claude",
			wantDst:  "/root/.claude",
			wantMode: "ro",
		},
		{
			name:      "missing_mode",
			spec:      "D:\\Video:/root/videos",
			wantErr:   true,
			errSubstr: "missing a :ro or :rw mode suffix",
		},
		{
			name:      "invalid_mode",
			spec:      "D:\\Video:/root/videos:rx",
			wantErr:   true,
			errSubstr: "mode must be",
		},
		{
			name:      "non_absolute_target",
			spec:      "D:\\Video:relative:ro",
			wantErr:   true,
			errSubstr: "absolute",
		},
		{
			name:      "empty_source",
			spec:      ":/root/videos:ro",
			wantErr:   true,
			errSubstr: "expected SOURCE:/absolute/container/path",
		},
		{
			name:      "comma_in_source",
			spec:      "D:\\Video, here:/root/videos:ro",
			wantErr:   true,
			errSubstr: "comma",
		},
		{
			name:      "comma_in_target",
			spec:      "D:\\Video:/root/videos, here:ro",
			wantErr:   true,
			errSubstr: "comma",
		},
		{
			// The mode suffix is found via the LAST ":" in the remainder, so
			// this would otherwise parse as target "/root/a:b", mode "ro"
			// without error -- an unintended shape outside the documented
			// SOURCE:/CONTAINER_PATH:MODE grammar.
			name:      "colon_in_target",
			spec:      "D:\\Video:/root/a:b:ro",
			wantErr:   true,
			errSubstr: "must not contain",
		},
		{
			name:      "userprofile_no_separator_concatenates_into_sibling",
			spec:      "%USERPROFILE%foo:/root/foo:ro",
			wantErr:   true,
			errSubstr: "followed immediately by",
		},
		{
			name:     "userprofile_bare_token_ok",
			spec:     "%USERPROFILE%:/root/home:ro",
			wantSrc:  "%USERPROFILE%",
			wantDst:  "/root/home",
			wantMode: "ro",
		},
		{
			name:      "other_variable_rejected",
			spec:      "%APPDATA%\\x:/root/x:ro",
			wantErr:   true,
			errSubstr: "unsupported",
		},
		{
			name:      "other_variable_after_userprofile",
			spec:      "%USERPROFILE%\\%APPDATA%\\x:/root/x:ro",
			wantErr:   true,
			errSubstr: "unsupported",
		},
		{
			name:      "userprofile_not_at_start",
			spec:      "C:\\%USERPROFILE%\\.claude:/root/.claude:ro",
			wantErr:   true,
			errSubstr: "unsupported",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, dst, mode, err := ParseHostMount(c.spec)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseHostMount(%q) expected error, got src=%q dst=%q mode=%q", c.spec, src, dst, mode)
				}
				if c.errSubstr != "" && !strings.Contains(err.Error(), c.errSubstr) {
					t.Fatalf("ParseHostMount(%q) error %q does not contain %q", c.spec, err.Error(), c.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHostMount(%q) unexpected error: %v", c.spec, err)
			}
			if src != c.wantSrc || dst != c.wantDst || mode != c.wantMode {
				t.Fatalf("ParseHostMount(%q) = src=%q dst=%q mode=%q, want src=%q dst=%q mode=%q", c.spec, src, dst, mode, c.wantSrc, c.wantDst, c.wantMode)
			}
		})
	}
}

func TestParseHostMountWindowsLiteralWithForwardSlashDrive(t *testing.T) {
	// The "X:\" form is unambiguous with the ":/" source/target delimiter.
	src, dst, mode, err := ParseHostMount("D:/Video:/root/videos:ro")
	if err == nil {
		t.Fatalf("expected ambiguity to be rejected, got src=%q dst=%q mode=%q", src, dst, mode)
	}
}

func TestValidateHostMounts(t *testing.T) {
	base := `[tools.x]
image = "x:1"
provider = "stateless"
`

	mustFail := func(label, extra string) {
		t.Helper()
		_, err := ParseTOML(base + extra)
		if err == nil {
			t.Fatalf("%s: expected error", label)
		}
	}
	mustPass := func(label, extra string) {
		t.Helper()
		_, err := ParseTOML(base + extra)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", label, err)
		}
	}

	mustFail("workspace_root_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/workspace:ro"})+"\n")
	mustFail("workspace_child_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/workspace/foo:ro"})+"\n")
	mustFail("cb_root_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/cb:ro"})+"\n")
	mustFail("cb_child_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/cb/global:ro"})+"\n")
	mustFail("venv_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/venv:ro"})+"\n")
	mustFail("pip_cache_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/root/.cache/pip:ro"})+"\n")
	// /venv and /root/.cache/pip must be reserved by prefix, not exact match
	// only: the python provider's bootstrap script depends on their full
	// structure (it checks /venv/bin/python specifically), so a sub-path
	// target like /venv/bin would otherwise validate cleanly and only break
	// the tool at run time.
	mustFail("venv_subpath_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/venv/bin:ro"})+"\n")
	mustFail("pip_cache_subpath_reserved", "host_mounts = "+toml.Array([]string{"C:\\\\Video:/root/.cache/pip/http:ro"})+"\n")
	mustFail("duplicate_target", "host_mounts = "+toml.Array([]string{"C:\\\\A:/root/.x:ro", "C:\\\\B:/root/.x:rw"})+"\n")
	mustFail("duplicate_target_equivalent", "host_mounts = "+toml.Array([]string{"C:\\\\A:/root/./.x:ro", "C:\\\\B:/root/.x:rw"})+"\n")
	mustFail("shared_volume_collision", "shared_volumes = "+toml.Array([]string{"cache:/root/.cache"})+"\nhost_mounts = "+toml.Array([]string{"C:\\\\Video:/root/.cache:ro"})+"\n")
	mustFail("shared_volume_collision_equivalent", "shared_volumes = "+toml.Array([]string{"cache:/root/./.cache"})+"\nhost_mounts = "+toml.Array([]string{"C:\\\\Video:/root/.cache:ro"})+"\n")

	// path.Clean("/workspace/..") == "/", which is not itself in the reserved
	// list -- a naive clean-then-compare order would let a ".."-traversal
	// target escape the reserved-namespace check entirely and mount at the
	// filesystem root, shadowing every container-bin-managed mount. These
	// must be rejected outright, before any normalization collapses them.
	mustFail("workspace_traversal_to_root", "host_mounts = "+toml.Array([]string{"C:\\\\data:/workspace/..:ro"})+"\n")
	mustFail("cb_traversal_to_root", "host_mounts = "+toml.Array([]string{"C:\\\\data:/cb/..:ro"})+"\n")
	mustFail("bare_traversal_segment", "host_mounts = "+toml.Array([]string{"C:\\\\data:/root/../etc:ro"})+"\n")
	// No ".." segment is present in either of these, so they reach path.Clean
	// unrejected by the traversal loop and must be caught by the separate
	// bare-"/" defense-in-depth check instead -- locking in that branch so a
	// future refactor that assumes the ".." loop alone is sufficient breaks a
	// test rather than silently reopening the root-mount hole.
	mustFail("bare_root_target", "host_mounts = "+toml.Array([]string{"C:\\\\data:/:ro"})+"\n")
	mustFail("dot_only_target_to_root", "host_mounts = "+toml.Array([]string{"C:\\\\data:/./.:ro"})+"\n")

	// ParseVolumeBinding places no requirement that a project_volumes
	// destination live under /workspace -- that's only true by convention
	// for this repo's built-in profiles, not enforced by the schema -- so a
	// stateful profile's project_volumes destination must be checked for
	// host_mounts collisions too, not just shared_volumes.
	statefulBase := `[tools.y]
image = "y:1"
provider = "stateful"
state_group = "ygroup"
`
	_, pvErr := ParseTOML(statefulBase + "project_volumes = " + toml.Array([]string{"data:/root/.ydata"}) + "\nhost_mounts = " + toml.Array([]string{"C:\\\\Video:/root/.ydata:ro"}) + "\n")
	if pvErr == nil {
		t.Fatal("project_volume_collision: expected error")
	}

	mustPass("valid_multi_entry", "host_mounts = "+toml.Array([]string{
		"%USERPROFILE%\\\\.claude:/root/.claude:ro",
		"%USERPROFILE%\\\\.codex:/root/.codex:ro",
	})+"\n")

	// stateless provider is allowed to use host_mounts
	_, err := ParseTOML(base + "host_mounts = " + toml.Array([]string{"%USERPROFILE%\\\\.claude:/root/.claude:ro"}) + "\n")
	if err != nil {
		t.Fatalf("stateless tool with host_mounts should parse: %v", err)
	}

	// Ensure the parsed value is retained.
	reg, err := ParseTOML(base + "host_mounts = " + toml.Array([]string{"%USERPROFILE%\\\\.claude:/root/.claude:ro"}) + "\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reg.Tools["x"].HostMounts) != 1 {
		t.Fatalf("expected 1 host_mount, got %#v", reg.Tools["x"].HostMounts)
	}
}

func TestCwdModeParsing(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"", ""},
		{"project", "project"},
		{"PROJECT", "project"},
		{"isolated", "isolated"},
		{"Isolated", "isolated"},
	}
	for _, tc := range cases {
		src := `[tools.x]
image = "x:1"
provider = "stateless"
`
		if tc.mode != "" {
			src += "cwd_mode = " + toml.Quote(tc.mode) + "\n"
		}
		reg, err := ParseTOML(src)
		if err != nil {
			t.Fatalf("mode %q: %v", tc.mode, err)
		}
		if got := reg.Tools["x"].CwdMode; got != tc.want {
			t.Fatalf("mode %q: CwdMode = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestCwdModeInvalidRejected(t *testing.T) {
	_, err := ParseTOML(`[tools.x]
image = "x:1"
provider = "stateless"
cwd_mode = "limbo"
`)
	if err == nil {
		t.Fatal("expected error for invalid cwd_mode")
	}
	if !strings.Contains(err.Error(), `cwd_mode must be "project" or "isolated"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsolatedRejectsProjectVolumes(t *testing.T) {
	_, err := ParseTOML(`[tools.x]
image = "x:1"
provider = "stateful"
state_group = "g"
cwd_mode = "isolated"
project_volumes = ["data:/workspace/data"]
shared_volumes = ["cache:/root/.cache"]
`)
	if err == nil {
		t.Fatal("expected error for project_volumes with cwd_mode = isolated")
	}
	if !strings.Contains(err.Error(), `cwd_mode = "isolated" cannot declare project_volumes`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsolatedAllowsSharedVolumes(t *testing.T) {
	reg, err := ParseTOML(`[tools.x]
image = "x:1"
provider = "stateful"
state_group = "g"
cwd_mode = "isolated"
shared_volumes = ["cache:/root/.cache"]
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.Tools["x"].CwdMode != "isolated" {
		t.Fatalf("CwdMode = %q, want isolated", reg.Tools["x"].CwdMode)
	}
}

func TestIsolatedRejectsPythonProvider(t *testing.T) {
	_, err := ParseTOML(`[tools.x]
image = "python:3.13-slim"
provider = "python"
role = "python"
cwd_mode = "isolated"
`)
	if err == nil {
		t.Fatal("expected error for python provider with cwd_mode = isolated")
	}
	if !strings.Contains(err.Error(), `cwd_mode = "isolated" is not supported for the python provider`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsolatedRejectsProjectMarkers(t *testing.T) {
	_, err := ParseTOML(`[tools.x]
image = "x:1"
provider = "stateless"
cwd_mode = "isolated"
project_markers = [".git"]
`)
	if err == nil {
		t.Fatal("expected error for project_markers with cwd_mode = isolated")
	}
	if !strings.Contains(err.Error(), `cwd_mode = "isolated" cannot declare project_markers`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHostMountDefaultTOMLComment(t *testing.T) {
	// The default TOML must still parse and the host_mounts comment must not
	// create an extra tool or confuse the parser.
	reg, err := ParseTOML(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Tools) != 24 {
		t.Fatalf("expected 24 tools, got %d", len(reg.Tools))
	}
}
