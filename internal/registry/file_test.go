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
	for _, name := range []string{"python3", "pip", "pip3", "jq", "yq", "terraform", "ffmpeg", "node", "npm", "npx", "go", "gofmt"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing migrated tool %q", name)
		}
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

// A pre-RM-11 registry (all built-in tools except the node22 trio) should be
// upgraded to include node22/npm22/npx22, while existing sections are left
// untouched.
func TestAppendMissingDefaultToolsUpgradesPreRM11(t *testing.T) {
	sections := DefaultToolSections()
	preRM11 := []string{"python", "python3", "pip", "pip3", "jq", "yq", "terraform", "ffmpeg", "node", "npm", "npx", "go", "gofmt"}
	var b strings.Builder
	b.WriteString("schema_version = 1\n")
	for _, name := range preRM11 {
		b.WriteString(sections[name])
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
	for _, name := range []string{"node22", "npm22", "npx22"} {
		if _, ok := reg.Tools[name]; !ok {
			t.Fatalf("missing upgraded tool %q", name)
		}
	}
	if reg.Tools["node"].Image != "node:24-slim" {
		t.Fatalf("existing node profile was modified: %q", reg.Tools["node"].Image)
	}
	if reg.Tools["node22"].Image != "node:22-slim" {
		t.Fatalf("bad node22 image: %q", reg.Tools["node22"].Image)
	}
	if !strings.Contains(string(data), "# Added by container-bin dev") {
		t.Fatal("upgrade comment missing")
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
