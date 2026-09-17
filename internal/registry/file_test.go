package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShimCopyFailurePreservesExistingShim(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "container-bin.exe")
	dst := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("new executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing shim"), 0755); err != nil {
		t.Fatal(err)
	}

	replaceCalled := false
	_, err := installShim(exe, dst,
		func(string, string) error { return errors.New("hardlink unavailable") },
		func(_, temporary string) error {
			if err := os.WriteFile(temporary, []byte("partial copy"), 0755); err != nil {
				t.Fatal(err)
			}
			return errors.New("copy interrupted")
		},
		func(string, string) error {
			replaceCalled = true
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected installation failure")
	}
	if replaceCalled {
		t.Fatal("replacement attempted before the temporary shim was complete")
	}
	data, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "existing shim" {
		t.Fatalf("existing shim changed after failed installation: %q", data)
	}
	temps, globErr := filepath.Glob(filepath.Join(dir, ".tool.exe-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary shims were not cleaned up: %v", temps)
	}
}

func TestInstallShimAtomicallyReplacesExistingShim(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "container-bin.exe")
	dst := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("new executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing shim"), 0755); err != nil {
		t.Fatal(err)
	}

	mode, err := installShim(exe, dst, os.Link, copyFile, os.Rename)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "hardlink" {
		t.Fatalf("mode = %q, want hardlink", mode)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new executable" {
		t.Fatalf("replacement shim contents = %q", data)
	}
	if err := os.WriteFile(exe, []byte("updated through hardlink"), 0755); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated through hardlink" {
		t.Fatalf("replacement is not a hardlink: %q", data)
	}
}

func TestInstallShimReplacementFailurePreservesExistingShim(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "container-bin.exe")
	dst := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("new executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing shim"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := installShim(exe, dst, os.Link, copyFile,
		func(string, string) error { return errors.New("replacement unavailable") },
	)
	if err == nil {
		t.Fatal("expected replacement failure")
	}
	data, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "existing shim" {
		t.Fatalf("existing shim changed after failed replacement: %q", data)
	}
	temps, globErr := filepath.Glob(filepath.Join(dir, ".tool.exe-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary shims were not cleaned up: %v", temps)
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
	if err := AppendMissingDefaultTools(path, "dev"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["jq2"]; !ok {
		t.Fatal("custom jq2 was lost")
	}
	for _, name := range []string{"python3", "pip", "pip3", "jq", "yq", "terraform", "ffmpeg", "node24", "npm24", "npx24", "go", "gofmt", "rustc", "cargo", "uv", "uvx", "dotnet", "ruby", "gem", "bundle"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing migrated tool %q", name)
		}
	}
	if _, resolved, ok := reg.Resolve("node"); !ok || resolved != "node24" {
		t.Fatalf("node alias resolved to %q, ok=%v", resolved, ok)
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
	if err := RewriteWithoutTools(path, map[string]bool{"beta": true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseTOML(string(data))
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

func TestRewriteRegistryWithoutToolsRemovesGeneratedExposeComment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	src := `schema_version = 1

# user context stays
# Exposed from acme shared volume tools by cb expose --shared-file

[tools.remove] # generated shared-file profile
image = "remove:1"
provider = "stateless"

# Exposed from go global store by cb expose go

[tools.keep] # generated managed-store profile
image = "keep:1"
provider = "stateless"
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RewriteWithoutTools(path, map[string]bool{"remove": true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "Exposed from acme") {
		t.Fatal("removed tool's generated provenance comment remains")
	}
	if !strings.Contains(got, "# user context stays") || !strings.Contains(got, "# Exposed from go global store by cb expose go\n\n[tools.keep] # generated managed-store profile") {
		t.Fatal("unrelated comments were removed")
	}
}

func TestRewriteRegistryWithoutToolsPreservesFollowingDefaultsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	src := `schema_version = 2

[tools.remove] # remove this section
image = "remove:1"
provider = "stateless"

[defaults.node] # keep this following section
version = "1"

[tools.node1]
default_family = "node"
default_version = "1"
default_alias = "node"
image = "node:1"
provider = "stateless"
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RewriteWithoutTools(path, map[string]bool{"remove": true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["remove"]; ok {
		t.Fatal("removed tool still present")
	}
	if reg.Defaults["node"] != "1" {
		t.Fatalf("node default = %q, want 1", reg.Defaults["node"])
	}
	if _, resolved, ok := reg.Resolve("node"); !ok || resolved != "node1" {
		t.Fatalf("node alias resolved to %q, ok=%v", resolved, ok)
	}
}

func TestRewriteRegistryWithoutToolsRemovesCommentedHeaderAtEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	src := `schema_version = 1

[tools.keep]
image = "keep:1"
provider = "stateless"

[tools.remove] # deprecated
image = "remove:1"
provider = "stateless"`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RewriteWithoutTools(path, map[string]bool{"remove": true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["remove"]; ok {
		t.Fatal("removed tool still present")
	}
	if _, ok := reg.Tools["keep"]; !ok {
		t.Fatal("preceding tool lost")
	}
	if strings.Contains(string(data), "[tools.remove]") {
		t.Fatal("commented tool header still present")
	}
}

func TestRewriteRegistryWithoutToolsRejectsInvalidInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	src := `schema_version = 1

[tools.keep]
image = "keep:1"
provider = "stateless"

[tools.remove]
image = "remove:1"
provider = "stateless"

[tools.remove]
image = "duplicate:1"
provider = "stateless"
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RewriteWithoutTools(path, map[string]bool{"remove": true}); err == nil || !strings.Contains(err.Error(), "refusing registry rewrite") {
		t.Fatalf("invalid input error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != src {
		t.Fatal("invalid registry was rewritten")
	}
}

// A pre-RM-11 registry should be upgraded with every later default profile,
// while existing sections are left untouched.
func TestAppendMissingDefaultToolsUpgradesPreRM11(t *testing.T) {
	sections := DefaultToolSections()
	preRM11 := []string{"python", "python3", "pip", "pip3", "jq", "yq", "terraform", "ffmpeg", "go", "gofmt"}
	var b strings.Builder
	b.WriteString("schema_version = 1\n")
	for _, name := range preRM11 {
		b.WriteString(sections[name])
	}
	for _, pair := range [][2]string{{"node24", "node"}, {"npm24", "npm"}, {"npx24", "npx"}} {
		section := sections[pair[0]]
		section = strings.Replace(section, "[tools."+pair[0]+"]", "[tools."+pair[1]+"]", 1)
		for _, key := range []string{"default_family", "default_version", "default_alias"} {
			start := strings.Index(section, key+" = ")
			end := strings.Index(section[start:], "\n")
			section = section[:start] + section[start+end+1:]
		}
		b.WriteString(section)
	}
	b.WriteString("\n[tools.jq2]\nimage = \"ghcr.io/jqlang/jq:latest\"\nprovider = \"stateless\"\n")

	dir := t.TempDir()
	path := dir + "/container-bin.toml"
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AppendMissingDefaultTools(path, "dev"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Tools["jq2"]; !ok {
		t.Fatal("custom jq2 was lost")
	}
	for _, name := range []string{"node22", "npm22", "npx22", "rustc", "cargo", "uv", "uvx", "dotnet", "ruby", "gem", "bundle"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing upgraded tool %q", name)
		}
	}
	if reg.Tools["node24"].Image != "node:24-slim" {
		t.Fatalf("migrated node24 profile has image %q", reg.Tools["node24"].Image)
	}
	if reg.Tools["node22"].Image != "node:22-slim" {
		t.Fatalf("bad node22 image: %q", reg.Tools["node22"].Image)
	}
	if !strings.Contains(string(data), "# Added by container-bin dev") {
		t.Fatal("upgrade comment missing")
	}
	if reg.SchemaVersion != 2 {
		t.Fatalf("schema = %d, want 2", reg.SchemaVersion)
	}
	if _, resolved, ok := reg.Resolve("npm"); !ok || resolved != "npm24" {
		t.Fatalf("npm alias resolved to %q, ok=%v", resolved, ok)
	}
}

func TestV1UpgradeRecognizesCommentedToolHeaders(t *testing.T) {
	sections := DefaultToolSections()
	var src strings.Builder
	src.WriteString("schema_version = 1\n")
	for _, migration := range []struct {
		oldName string
		newName string
		alias   string
	}{
		{oldName: "node", newName: "node24", alias: "node"},
		{oldName: "npm", newName: "npm24", alias: "npm"},
		{oldName: "npx", newName: "npx24", alias: "npx"},
	} {
		section := strings.Replace(sections[migration.newName], "[tools."+migration.newName+"]", "[tools."+migration.oldName+"] # legacy "+migration.alias, 1)
		for _, key := range []string{"default_family", "default_version", "default_alias"} {
			start := strings.Index(section, key+" = ")
			end := strings.Index(section[start:], "\n")
			section = section[:start] + section[start+end+1:]
		}
		src.WriteString(section)
	}

	path := filepath.Join(t.TempDir(), "container-bin.toml")
	if err := os.WriteFile(path, []byte(src.String()), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AppendMissingDefaultTools(path, "dev"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"node", "node24"}, {"npm", "npm24"}, {"npx", "npx24"}} {
		if _, exists := reg.Tools[pair[0]]; exists {
			t.Fatalf("legacy tool %q still exists", pair[0])
		}
		if _, exists := reg.Tools[pair[1]]; !exists {
			t.Fatalf("migrated tool %q is missing", pair[1])
		}
		if !strings.Contains(string(data), "[tools."+pair[1]+"] # legacy "+pair[0]) {
			t.Fatalf("migrated header/comment for %q was not preserved", pair[1])
		}
	}
}

func TestSetDefaultVersionRewritesOnlySelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	if err := os.WriteFile(path, []byte(DefaultTOML), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultVersion(path, "NODE", "22"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(before), "version = \"24\"", "version = \"22\"", 1)
	if string(after) != want {
		t.Fatal("default update changed content beyond the selected version line")
	}
	reg, err := ParseTOML(string(after))
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"node", "npm", "npx"} {
		if _, resolved, ok := reg.Resolve(alias); !ok || resolved != alias+"22" {
			t.Fatalf("%s resolved to %q, ok=%v", alias, resolved, ok)
		}
	}
	if err := SetDefaultVersion(path, "node", "26"); err == nil || !strings.Contains(err.Error(), "available: 22, 24") {
		t.Fatalf("unexpected unavailable-version error: %v", err)
	}
}

func TestSetDefaultVersionRecognizesCommentedSectionHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	const defaultNode = "[defaults.node]\nversion = \"24\""
	if strings.Count(DefaultTOML, defaultNode) != 1 {
		t.Fatalf("DefaultTOML contains %d exact node-default sections, want 1", strings.Count(DefaultTOML, defaultNode))
	}
	src := strings.Replace(DefaultTOML, defaultNode, "[defaults.node] # selected runtime\n  version = \"24\" # pinned for prod", 1)
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultVersion(path, "node", "22"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(src, "  version = \"24\" # pinned for prod", "  version = \"22\" # pinned for prod", 1)
	if string(after) != want {
		t.Fatal("commented default update changed content beyond the selected version line")
	}
}

func TestV1UpgradeRefusesCustomizedLegacyNode(t *testing.T) {
	sections := DefaultToolSections()
	legacy := strings.Replace(sections["node24"], "[tools.node24]", "[tools.node]", 1)
	legacy = strings.ReplaceAll(legacy, "default_family = \"node\"\n", "")
	legacy = strings.ReplaceAll(legacy, "default_version = \"24\"\n", "")
	legacy = strings.ReplaceAll(legacy, "default_alias = \"node\"\n", "")
	legacy = strings.Replace(legacy, "image = \"node:24-slim\"", "image = \"node:20-slim\"", 1)
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"+legacy), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	err := AppendMissingDefaultTools(path, "dev")
	if err == nil || !strings.Contains(err.Error(), "customized [tools.node]") {
		t.Fatalf("error = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("failed migration modified the registry")
	}
}

func TestValidateRegistryBackup(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.bak")
	if err := os.WriteFile(good, []byte(DefaultTOML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateBackup(good); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	bad := filepath.Join(dir, "bad.bak")
	if err := os.WriteFile(bad, []byte("schema_version = 1\n\n[notools.invalid]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateBackup(bad); err == nil {
		t.Fatal("expected invalid registry to be rejected")
	}

	// A zero-byte backup is a truncation artifact rather than recoverable data.
	empty := filepath.Join(dir, "empty.bak")
	if err := os.WriteFile(empty, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateBackup(empty); err == nil {
		t.Fatal("expected empty backup to be rejected")
	}

	// A tool-less registry is rejected too. The rule lives in ParseTOML
	// rather than here, so this pins the guarantee at the boundary that matters
	// and keeps holding if the check ever moves.
	toolless := filepath.Join(dir, "toolless.bak")
	if err := os.WriteFile(toolless, []byte("schema_version = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateBackup(toolless); err == nil {
		t.Fatal("expected tool-less backup to be rejected")
	}
}
