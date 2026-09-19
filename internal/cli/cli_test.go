package cli

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestAddRequiresExactShape(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"demo"},
		{"demo", "example/demo:1"},
		{"demo", "--image"},
		{"--image", "example/demo:1", "demo"},
		{"demo", "--provider", "stateless"},
		{"demo", "--image", "example/demo:1", "extra"},
		{"demo", "--image", "example/demo:1", "--local", "extra"},
	} {
		err := add(registry.Default(), filepath.Join(t.TempDir(), "container-bin.toml"), args, func(registry.Registry) error {
			t.Fatal("installer called for invalid arguments")
			return nil
		}, policy.Policy{})
		if err == nil || !strings.Contains(err.Error(), "usage: cb add TOOL --image IMAGE") {
			t.Fatalf("Add(%v) error = %v, want usage error", args, err)
		}
	}
}

func TestAddRejectsUnsafeOrExistingNames(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"bad.name", "--image", "example/demo:1"}, want: "invalid tool name"},
		{args: []string{"cb", "--image", "example/demo:1"}, want: "reserved"},
		{args: []string{"jq", "--image", "example/demo:1"}, want: "already exists"},
		{args: []string{"demo", "--image", "example/demo bad:1"}, want: "must not contain whitespace"},
		{args: []string{"demo", "--image", "--privileged"}, want: "must not start with"},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		path := filepath.Join(dir, "container-bin.toml")
		err := add(registry.Default(), path, tt.args, func(registry.Registry) error {
			t.Fatal("installer called for rejected profile")
			return nil
		}, policy.Policy{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("Add(%v) error = %v, want substring %q", tt.args, err, tt.want)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid add mutated registry: %v", statErr)
		}
	}
}

func TestAddCreatesMinimalProfileAndInstalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	var installed registry.Registry
	out, captureErr := captureStdout(func() error {
		return add(registry.Default(), path, []string{"Demo_Tool", "--image", "registry.example/dev/demo:1.2.3"}, func(got registry.Registry) error {
			installed = got
			return nil
		}, policy.Policy{})
	})
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	if installed.Tools == nil {
		t.Fatal("installer was not called")
	}
	tool, ok := installed.Tools["demo_tool"]
	if !ok {
		t.Fatal("installed registry does not contain normalized tool name")
	}
	if tool.Image != "registry.example/dev/demo:1.2.3" || tool.Provider != "stateless" {
		t.Fatalf("added tool = %#v", tool)
	}
	if len(tool.Command) != 0 || len(tool.EnvNames) != 0 || len(tool.ProjectVolumes) != 0 {
		t.Fatalf("add inferred behavior beyond a minimal profile: %#v", tool)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := registry.ParseTOML(string(written))
	if err != nil {
		t.Fatalf("written registry is invalid: %v", err)
	}
	if _, ok := parsed.Tools["demo_tool"]; !ok {
		t.Fatal("written registry missing added tool")
	}
	if !strings.Contains(string(written), "# Added by cb add\n[tools.demo_tool]") {
		t.Fatalf("written registry missing auditable add marker:\n%s", written)
	}
	if !strings.Contains(out, "added demo_tool -> registry.example/dev/demo:1.2.3 (stateless)") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if !strings.Contains(out, "run `cb lock`") {
		t.Fatalf("unlocked add did not recommend pinning:\n%s", out)
	}
}

func TestAddPolicyDenialDoesNotMutateRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	p := policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}
	err := add(registry.Default(), path, []string{"demo", "--image", "ghcr.io/other/demo:1"}, func(registry.Registry) error {
		t.Fatal("installer called for policy-denied profile")
		return nil
	}, p)
	if err == nil || !strings.Contains(err.Error(), "[policy.repository_denied]") {
		t.Fatalf("add policy error = %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("policy-denied add mutated registry: %v", statErr)
	}
}

func TestAddLocalIntentUsesLocalPolicyAndReportsLocalLockCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	p := policy.Policy{SchemaVersion: 1, AllowLocalImages: true, AllowedRepositories: []string{"ghcr.io/acme"}}
	out, err := captureStdout(func() error {
		return add(registry.Default(), path, []string{"demo", "--image", "local/demo:dev", "--local"}, func(registry.Registry) error { return nil }, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "run `cb lock --local demo`") {
		t.Fatalf("local add did not report explicit local lock command:\n%s", out)
	}

	deniedPath := filepath.Join(t.TempDir(), "container-bin.toml")
	p.AllowLocalImages = false
	err = add(registry.Default(), deniedPath, []string{"demo", "--image", "ghcr.io/acme/demo:dev", "--local"}, func(registry.Registry) error {
		t.Fatal("installer called for policy-denied local profile")
		return nil
	}, p)
	if err == nil || !strings.Contains(err.Error(), "[policy.local_image_denied]") {
		t.Fatalf("local add policy error = %v", err)
	}
	if _, statErr := os.Stat(deniedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("policy-denied local add mutated registry: %v", statErr)
	}
}

func TestAuthorizeRegistrySnapshot(t *testing.T) {
	reg, err := registry.ParseTOML("schema_version = 1\n[tools.demo]\nimage = \"ghcr.io/acme/demo:1\"\nprovider = \"stateless\"\n")
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Policy{SchemaVersion: 1, RequireLock: true, AllowedRepositories: []string{"ghcr.io/acme"}}
	if err := authorizeRegistrySnapshot(reg, nil, p); err == nil || !strings.Contains(err.Error(), "[policy.lock_required]") {
		t.Fatalf("missing restored lock error = %v", err)
	}
	lf := &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{
		"ghcr.io/acme/demo:1": {
			Configured: "ghcr.io/acme/demo:1",
			Resolved:   "ghcr.io/acme/demo@sha256:" + strings.Repeat("a", 64),
			Digest:     "sha256:" + strings.Repeat("a", 64),
		},
	}}
	if err := authorizeRegistrySnapshot(reg, lf, p); err != nil {
		t.Fatalf("authorized restored snapshot rejected: %v", err)
	}
	entry := lf.Images["ghcr.io/acme/demo:1"]
	entry.Resolved = "evil.example/demo@sha256:" + strings.Repeat("b", 64)
	lf.Images["ghcr.io/acme/demo:1"] = entry
	if err := authorizeRegistrySnapshot(reg, lf, p); err == nil || !strings.Contains(err.Error(), "resolved lock reference") {
		t.Fatalf("foreign resolved repository error = %v", err)
	}
	entry.Resolved = "ghcr.io/acme/demo@sha256:" + strings.Repeat("a", 64)
	lf.Images["ghcr.io/acme/demo:1"] = entry
	p.AllowedRepositories = []string{"ghcr.io/other"}
	if err := authorizeRegistrySnapshot(reg, lf, p); err == nil || !strings.Contains(err.Error(), "[policy.repository_denied]") {
		t.Fatalf("disallowed restored repository error = %v", err)
	}
}

func TestBulkLockOperationsPreflightAllPolicyTargetsBeforeDocker(t *testing.T) {
	reg, err := registry.ParseTOML("schema_version = 1\n[tools.allowed]\nimage = \"ghcr.io/acme/allowed:1\"\nprovider = \"stateless\"\n[tools.denied]\nimage = \"ghcr.io/zzz/denied:1\"\nprovider = \"stateless\"\n")
	if err != nil {
		t.Fatal(err)
	}
	// If either operation reaches Docker for the alphabetically first allowed
	// image, the empty PATH produces an executable-not-found error instead of
	// the expected policy denial for the later target.
	t.Setenv("PATH", t.TempDir())
	p := policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}
	cfgPath := filepath.Join(t.TempDir(), "container-bin.toml")
	for name, run := range map[string]func() error{
		"lock":       func() error { return Lock(reg, cfgPath, nil, p) },
		"update_all": func() error { return Update(reg, cfgPath, []string{"--all"}, p) },
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil || !strings.Contains(err.Error(), "[policy.repository_denied]") {
				t.Fatalf("bulk operation error = %v, want policy denial before Docker", err)
			}
		})
	}
}

func TestAddReportsIncompleteExistingLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	if err := registry.EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "container-bin.lock"), []byte("present"), 0644); err != nil {
		t.Fatal(err)
	}
	out, captureErr := captureStdout(func() error {
		return add(registry.Default(), path, []string{"demo", "--image", "example/demo:1"}, func(registry.Registry) error { return nil }, policy.Policy{})
	})
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	if !strings.Contains(out, "lockfile is now incomplete; run `cb update demo` or `cb lock`") {
		t.Fatalf("existing lockfile warning missing:\n%s", out)
	}
}

func TestAddInstallerFailureLeavesValidRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-bin.toml")
	wantErr := errors.New("shim directory denied")
	err := add(registry.Default(), path, []string{"demo", "--image", "example/demo:1"}, func(registry.Registry) error { return wantErr }, policy.Policy{})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "profile \"demo\" was added") || !strings.Contains(err.Error(), "cb install") {
		t.Fatalf("installer error = %v", err)
	}
	written, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	parsed, parseErr := registry.ParseTOML(string(written))
	if parseErr != nil {
		t.Fatalf("registry left invalid after installer failure: %v", parseErr)
	}
	if _, ok := parsed.Tools["demo"]; !ok {
		t.Fatal("profile was not preserved for cb install retry")
	}
}

func TestDefaultAliasesAreVisibleInManagementCommands(t *testing.T) {
	reg := registry.Default()
	out, err := captureStdout(func() error { return Trace(reg, []string{"node", "--version"}, policy.Policy{}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tool:       node", "resolved:   node24", "state_group: node24"} {
		if !strings.Contains(out, want) {
			t.Fatalf("trace output missing %q:\n%s", want, out)
		}
	}
	out, err = captureStdout(func() error { return Default(reg, "unused", nil, policy.Policy{}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "node       24  aliases=node,npm,npx  versions=22,24") {
		t.Fatalf("default output did not describe the complete family:\n%s", out)
	}
}

func TestRemovalCommandsRefuseAliasesWithoutMutation(t *testing.T) {
	const config = `schema_version = 2

[defaults.acme]
version = "1"

[tools.acme1]
default_family = "acme"
default_version = "1"
default_alias = "acme"
image = "example/acme:1"
provider = "stateful"
command = ["/cb/npm-global/bin/acme"]
state_group = "acme1"
shared_volumes = ["global:/cb/npm-global"]
`
	reg, err := registry.ParseTOML(config)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(string) error
		want string
	}{
		{name: "uninstall", run: func(path string) error { return Uninstall(reg, path, []string{"acme"}) }, want: "uninstall requires a concrete tool name"},
		{name: "unexpose", run: func(path string) error { return Unexpose(reg, path, []string{"acme"}) }, want: "unexpose requires a concrete tool name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "container-bin.toml")
			if err := os.WriteFile(path, []byte(config), 0644); err != nil {
				t.Fatal(err)
			}
			err := tt.run(path)
			if err == nil || !strings.Contains(err.Error(), `"acme" is an alias for "acme1"`) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != config {
				t.Fatalf("%s mutated the registry before refusing the alias", tt.name)
			}
		})
	}
}

// These tests cover Expose guard paths that need no Docker daemon.
// The Docker-dependent discovery path (discoverGlobalBins onward) remains
// untested here because it requires a real Docker daemon and a populated npm,
// Go or Cargo global volume.

func TestParseLockArgs(t *testing.T) {
	check, local, err := parseLockArgs([]string{"--local", "LOCAL-TOOL", "--local", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if check || !reflect.DeepEqual(local, map[string]bool{"local-tool": true, "other": true}) {
		t.Fatalf("parseLockArgs = check=%v local=%v", check, local)
	}

	check, local, err = parseLockArgs([]string{"--check"})
	if err != nil || !check || len(local) != 0 {
		t.Fatalf("parseLockArgs(--check) = check=%v local=%v err=%v", check, local, err)
	}

	for _, args := range [][]string{
		{"--local"},
		{"--check", "--local", "tool"},
		{"--local", "--all"},
		{"--unknown", "tool"},
	} {
		if _, _, err := parseLockArgs(args); err == nil {
			t.Errorf("parseLockArgs(%v) expected error", args)
		}
	}
}

func TestCheckLockReportsEveryStatusAfterPolicyPreflight(t *testing.T) {
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"present": {Image: "ghcr.io/acme/present:1"},
		"absent":  {Image: "ghcr.io/acme/absent:1"},
		"denied":  {Image: "ghcr.io/other/denied:1"},
		"missing": {Image: "ghcr.io/acme/missing:1"},
	}}
	digest := "sha256:" + strings.Repeat("a", 64)
	path := filepath.Join(t.TempDir(), "container-bin.lock")
	lf := &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{
		"ghcr.io/acme/present:1": {Configured: "ghcr.io/acme/present:1", Resolved: "ghcr.io/acme/present@" + digest, Digest: digest},
		"ghcr.io/acme/absent:1":  {Configured: "ghcr.io/acme/absent:1", Resolved: "ghcr.io/acme/absent@" + digest, Digest: digest},
		"ghcr.io/other/denied:1": {Configured: "ghcr.io/other/denied:1", Resolved: "ghcr.io/other/denied@" + digest, Digest: digest},
	}}
	if err := lockfile.Write(path, lf); err != nil {
		t.Fatal(err)
	}
	var inspected []string
	p := policy.Policy{SchemaVersion: 1, AllowedRepositories: []string{"ghcr.io/acme"}}
	var checkErr error
	out, captureErr := captureStdout(func() error {
		checkErr = checkLock(reg, path, p, func(resolved string) error {
			inspected = append(inspected, resolved)
			if strings.Contains(resolved, "/absent@") {
				return errors.New("not present")
			}
			return nil
		})
		return nil
	})
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	if checkErr == nil || !strings.Contains(checkErr.Error(), "3 image(s)") {
		t.Fatalf("checkLock error = %v, want three failures", checkErr)
	}
	for _, want := range []string{"ABSENT   ghcr.io/acme/absent:1", "MISSING  ghcr.io/acme/missing:1", "OK       ghcr.io/acme/present:1", "DENIED   ghcr.io/other/denied:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	if len(inspected) != 2 {
		t.Fatalf("inspected refs = %v, want only two authorized refs", inspected)
	}
	for _, ref := range inspected {
		if strings.Contains(ref, "/denied@") {
			t.Fatalf("policy-denied ref reached Docker inspection: %s", ref)
		}
	}
}

func TestParseUpdateArgs(t *testing.T) {
	for _, tt := range []struct {
		args       []string
		wantTarget string
		wantMode   string
	}{
		{args: []string{"tool"}, wantTarget: "tool"},
		{args: []string{"--all"}, wantTarget: "--all"},
		{args: []string{"--local", "tool"}, wantTarget: "tool", wantMode: "local"},
		{args: []string{"--registry", "tool"}, wantTarget: "tool", wantMode: "registry"},
	} {
		target, mode, err := parseUpdateArgs(tt.args)
		if err != nil || target != tt.wantTarget || mode != tt.wantMode {
			t.Errorf("parseUpdateArgs(%v) = target=%q mode=%q err=%v, want target=%q mode=%q", tt.args, target, mode, err, tt.wantTarget, tt.wantMode)
		}
	}

	for _, args := range [][]string{
		{},
		{"--local"},
		{"--local", "--all"},
		{"--registry", "--all"},
		{"tool", "extra"},
	} {
		if _, _, err := parseUpdateArgs(args); err == nil {
			t.Errorf("parseUpdateArgs(%v) expected error", args)
		}
	}
}

func TestExposeRequiresSourceTool(t *testing.T) {
	reg := registry.Default()
	if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), nil, policy.Policy{}); err == nil {
		t.Fatal("expected usage error for empty args")
	} else if !strings.Contains(err.Error(), "usage: cb expose TOOL") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestExposeRejectsFlagShapedArgumentsBeforeDocker(t *testing.T) {
	reg := registry.Default()
	for _, args := range [][]string{
		{"--unknown"},
		{"go", "--shared-file", "gobin", "/go/bin/stringer"},
		{"go", "-h"},
	} {
		if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), args, policy.Policy{}); err == nil || !strings.Contains(err.Error(), "usage: cb expose TOOL") {
			t.Fatalf("Expose(%v) error = %v, want usage", args, err)
		}
	}
}

func TestExposeRejectsUnknownSource(t *testing.T) {
	reg := registry.Default()
	if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), []string{"notarealtool"}, policy.Policy{}); err == nil {
		t.Fatal("expected not-found error")
	} else if !strings.Contains(err.Error(), `tool "notarealtool" not found`) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// terraform exists in the registry but is not stateful, so
// Expose must fail closed here before any docker invocation. This also pins
// that the new %q-formatted error carries the source tool's actual name rather
// than an empty string.
func TestExposeRejectsStatelessTool(t *testing.T) {
	reg := registry.Default()
	err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), []string{"terraform"}, policy.Policy{})
	if err == nil {
		t.Fatal("expected error for stateless source tool")
	}
	if !strings.Contains(err.Error(), "is not a stateful profile") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), `"terraform"`) {
		t.Fatalf("error message does not name the source tool: %v", err)
	}
}

func TestExposeSharedFileModeValidatesBeforeDocker(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.acme]
image = "example/acme:1"
provider = "stateful"
state_group = "acme"
shared_volumes = ["tools:/opt/acme"]

[tools.acme-lint]
image = "example/existing:1"
provider = "stateless"
`)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "container-bin.toml")
	if err := Expose(reg, cfgPath, []string{"--shared-file", "acme"}, policy.Policy{}); err == nil || !strings.Contains(err.Error(), "usage: cb expose --shared-file") {
		t.Fatalf("short shared-file args error = %v", err)
	}
	if err := Expose(reg, cfgPath, []string{"--shared-file", "acme-lint", "tools", "/opt/acme/tool"}, policy.Policy{}); err == nil || !strings.Contains(err.Error(), "not a stateful profile") {
		t.Fatalf("stateless shared-file source error = %v", err)
	}
	if err := Expose(reg, cfgPath, []string{"--shared-file", "acme", "tools", "/opt/acme/bin/acme-lint"}, policy.Policy{}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("shared-file collision error = %v", err)
	}
}

func TestExposeStoreForBuiltins(t *testing.T) {
	reg := registry.Default()
	tests := []struct {
		tool      string
		kind      string
		target    string
		binDir    string
		volumeEnd string
		companion string
		compEnd   string
	}{
		{tool: "npm", kind: "npm", target: "/cb/npm-global", binDir: "/cb/npm-global/bin", volumeEnd: "npm-global"},
		{tool: "npm22", kind: "npm", target: "/cb/npm-global", binDir: "/cb/npm-global/bin", volumeEnd: "npm-global"},
		{tool: "go", kind: "Go", target: "/go/bin", binDir: "/go/bin", volumeEnd: "gobin"},
		{tool: "cargo", kind: "Cargo", target: "/cb/cargo-global", binDir: "/cb/cargo-global/bin", volumeEnd: "global"},
		{tool: "uv", kind: "uv tool", target: "/cb/uv-bin", binDir: "/cb/uv-bin", volumeEnd: "tool-bin", companion: "/cb/uv-tools", compEnd: "tools"},
		{tool: "uvx", kind: "uv tool", target: "/cb/uv-bin", binDir: "/cb/uv-bin", volumeEnd: "tool-bin", companion: "/cb/uv-tools", compEnd: "tools"},
		{tool: "dotnet", kind: ".NET tool", target: "/root/.dotnet", binDir: "/root/.dotnet/tools", volumeEnd: "dotnet-home"},
		{tool: "ruby", kind: "RubyGems", target: "/cb/ruby-gems", binDir: "/cb/ruby-gems/bin", volumeEnd: "gems"},
		{tool: "gem", kind: "RubyGems", target: "/cb/ruby-gems", binDir: "/cb/ruby-gems/bin", volumeEnd: "gems"},
		{tool: "bundle", kind: "RubyGems", target: "/cb/ruby-gems", binDir: "/cb/ruby-gems/bin", volumeEnd: "gems"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			tool, _, ok := reg.Resolve(tt.tool)
			if !ok {
				t.Fatalf("tool %q did not resolve", tt.tool)
			}
			store, err := exposeStoreFor(tool)
			if err != nil {
				t.Fatal(err)
			}
			if store.kind != tt.kind || store.mountTarget != tt.target || store.binDirectory != tt.binDir || !strings.HasSuffix(store.volumeName, tt.volumeEnd) {
				t.Fatalf("store = %#v", store)
			}
			if tt.companion == "" {
				if len(store.companionMounts) != 0 {
					t.Fatalf("unexpected companion mounts: %#v", store.companionMounts)
				}
			} else if len(store.companionMounts) != 1 || store.companionMounts[0].mountTarget != tt.companion || !strings.HasSuffix(store.companionMounts[0].volumeName, tt.compEnd) {
				t.Fatalf("companion mounts = %#v", store.companionMounts)
			}
		})
	}
}

func TestExposeStoreRejectsMissingUVToolsVolume(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.demo]
image = "example/demo:1"
provider = "stateful"
state_group = "demo"
shared_volumes = ["bin:/cb/uv-bin"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exposeStoreFor(reg.Tools["demo"]); err == nil || !strings.Contains(err.Error(), "requires a shared volume mounted at /cb/uv-tools") {
		t.Fatalf("missing companion error = %v", err)
	}
}

func TestExposeStoreCanonicalizesVolumeTargets(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.demo]
image = "example/demo:1"
provider = "stateful"
state_group = "demo"
shared_volumes = ["tools:/cb/uv-tools/", "bin:/cb/./uv-bin"]
`)
	if err != nil {
		t.Fatal(err)
	}
	store, err := exposeStoreFor(reg.Tools["demo"])
	if err != nil {
		t.Fatal(err)
	}
	if store.volumeName != "cb-demo-bin" || store.mountTarget != "/cb/uv-bin" || store.binDirectory != "/cb/uv-bin" {
		t.Fatalf("store paths were not canonicalized: %#v", store)
	}
	wantCompanion := []exposeMount{{volumeName: "cb-demo-tools", mountTarget: "/cb/uv-tools"}}
	if !reflect.DeepEqual(store.companionMounts, wantCompanion) {
		t.Fatalf("companion mounts = %#v, want %#v", store.companionMounts, wantCompanion)
	}
}

func TestUVExposeDiscoveryMountsBinAndToolVolumesReadOnly(t *testing.T) {
	reg := registry.Default()
	store, err := exposeStoreFor(reg.Tools["uv"])
	if err != nil {
		t.Fatal(err)
	}
	args, err := exposeDiscoveryArgs(store, "example/uv:1", "discover-script")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--rm", "--pull", "never", "--network", "none", "--read-only",
		"--mount", "type=volume,src=cb-uv012-py313-tool-bin,dst=/cb/uv-bin,readonly",
		"--mount", "type=volume,src=cb-uv012-py313-tools,dst=/cb/uv-tools,readonly",
		"--entrypoint", "sh", "example/uv:1", "-c", "discover-script", "cb-expose", "/cb/uv-bin",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("discovery args = %#v, want %#v", args, want)
	}
}

func TestExposeStoreRejectsAmbiguousProfile(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.demo]
image = "example/demo:1"
provider = "stateful"
state_group = "demo"
shared_volumes = ["npm:/cb/npm-global", "go:/go/bin"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exposeStoreFor(reg.Tools["demo"]); err == nil || !strings.Contains(err.Error(), "more than one supported global binary store") {
		t.Fatalf("ambiguous store error = %v", err)
	}
}

func TestExposeSharedFileFor(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.acme]
image = "example/acme:1"
provider = "stateful"
state_group = "acme"
shared_volumes = ["cache:/root/.cache/acme", "tools:/opt/acme"]
`)
	if err != nil {
		t.Fatal(err)
	}
	store, bin, err := exposeSharedFileFor(reg.Tools["acme"], "TOOLS", "/opt/acme/./bin/Acme-Lint")
	if err != nil {
		t.Fatal(err)
	}
	wantStore := exposeStore{
		kind:         "shared volume tools",
		volumeName:   "cb-acme-tools",
		mountTarget:  "/opt/acme",
		binDirectory: "/opt/acme/bin",
	}
	if !reflect.DeepEqual(store, wantStore) {
		t.Fatalf("store = %#v, want %#v", store, wantStore)
	}
	wantBin := exposedBin{name: "acme-lint", command: "/opt/acme/bin/Acme-Lint"}
	if !reflect.DeepEqual(bin, wantBin) {
		t.Fatalf("bin = %#v, want %#v", bin, wantBin)
	}
}

func TestExposeSharedFileForRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.acme]
image = "example/acme:1"
provider = "stateful"
state_group = "acme"
project_volumes = ["project:/workspace/vendor"]
shared_volumes = ["tools:/opt/acme"]
`)
	if err != nil {
		t.Fatal(err)
	}
	tool := reg.Tools["acme"]
	for _, tc := range []struct {
		name    string
		volume  string
		command string
		want    string
	}{
		{name: "unknown_volume", volume: "missing", command: "/opt/acme/tool", want: "no shared volume"},
		{name: "project_volume", volume: "project", command: "/workspace/vendor/tool", want: "no shared volume"},
		{name: "relative", volume: "tools", command: "bin/tool", want: "absolute container path"},
		{name: "traversal", volume: "tools", command: "/opt/acme/bin/../tool", want: `must not contain ".."`},
		{name: "mount_root", volume: "tools", command: "/opt/acme", want: "is not beneath"},
		{name: "sibling_prefix", volume: "tools", command: "/opt/acme-other/tool", want: "is not beneath"},
		{name: "flag_shim", volume: "tools", command: "/opt/acme/-h", want: "must not begin"},
		{name: "invalid_shim", volume: "tools", command: "/opt/acme/bad.name", want: "cannot be represented"},
		{name: "reserved_shim", volume: "tools", command: "/opt/acme/cb", want: "is reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := exposeSharedFileFor(tool, tc.volume, tc.command)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestExposeSharedFileForRejectsKnownGlobalStore(t *testing.T) {
	reg := registry.Default()
	_, _, err := exposeSharedFileFor(reg.Tools["go"], "gobin", "/go/bin/stringer")
	if err == nil || !strings.Contains(err.Error(), "use `cb expose go") {
		t.Fatalf("known-store error = %v", err)
	}
}

func TestSharedFileDiscoveryArgsAreReadOnlyOfflineAndNoPull(t *testing.T) {
	store := exposeStore{volumeName: "cb-acme-tools", mountTarget: "/opt/acme"}
	args, err := sharedFileDiscoveryArgs(store, "example/acme@sha256:abc", "inspect-script", "/opt/acme/bin/tool")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--rm", "--pull", "never", "--network", "none", "--read-only",
		"--mount", "type=volume,src=cb-acme-tools,dst=/opt/acme,readonly",
		"--entrypoint", "sh",
		"example/acme@sha256:abc", "-c", "inspect-script", "cb-expose", "/opt/acme/bin/tool", "/opt/acme",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("discovery args = %#v, want %#v", args, want)
	}
}

func TestExposeDiscoveryErrorIncludesContainerOutput(t *testing.T) {
	commandErr := errors.New("exit status 127")
	err := exposeDiscoveryError("Cargo", []byte("sh: not found\n"), commandErr)
	if !errors.Is(err, commandErr) {
		t.Fatalf("exposeDiscoveryError() = %v, want wrapped command error", err)
	}
	if !strings.Contains(err.Error(), "sh: not found") {
		t.Fatalf("exposeDiscoveryError() = %q, want container stderr", err)
	}

	err = exposeDiscoveryError("Cargo", nil, commandErr)
	if !errors.Is(err, commandErr) {
		t.Fatalf("exposeDiscoveryError(empty output) = %v, want wrapped command error", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("exposeDiscoveryError(empty output) = %q, unexpectedly invented output", err)
	}
}

func TestRenderExposedSharedFileSection(t *testing.T) {
	reg, err := registry.ParseTOML(`[tools.acme]
image = "example/acme:1"
provider = "stateful"
project_markers = ["acme.toml"]
state_group = "acme"
shared_volumes = ["cache:/root/.cache/acme", "tools:/opt/acme"]
env_names = ["ACME_TOKEN"]
`)
	if err != nil {
		t.Fatal(err)
	}
	source := reg.Tools["acme"]
	section := renderExposedSharedFileSection("acme", "tools", source, "acme-lint", "/opt/acme/bin/acme-lint")
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered shared-file section invalid: %v", err)
	}
	got := parsed.Tools["acme-lint"]
	if got.Role != "exposed" || got.Image != source.Image || got.StateGroup != source.StateGroup {
		t.Fatalf("exposed shared-file identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Command, []string{"/opt/acme/bin/acme-lint"}) || !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) {
		t.Fatalf("exposed shared-file command/state = %#v", got)
	}
	if !reflect.DeepEqual(got.ProjectMarkers, source.ProjectMarkers) || !reflect.DeepEqual(got.EnvNames, source.EnvNames) {
		t.Fatal("exposed shared-file profile did not inherit source project/environment policy")
	}
}

func TestParseExposedBinsPreservesContainerCase(t *testing.T) {
	store := exposeStore{binDirectory: "/go/bin"}
	bins, err := parseExposedBins([]byte("Stringer\x00bad.name\x00cb\x00-h\x00--lint\x00lower\x00"), store)
	if err != nil {
		t.Fatal(err)
	}
	want := []exposedBin{
		{name: "lower", command: "/go/bin/lower"},
		{name: "stringer", command: "/go/bin/Stringer"},
	}
	if !reflect.DeepEqual(bins, want) {
		t.Fatalf("bins = %#v, want %#v", bins, want)
	}
}

func TestParseExposedBinsRejectsCaseCollision(t *testing.T) {
	store := exposeStore{binDirectory: "/go/bin"}
	if _, err := parseExposedBins([]byte("Stringer\x00stringer\x00"), store); err == nil || !strings.Contains(err.Error(), "differ only by case") {
		t.Fatalf("case-collision error = %v", err)
	}
}

func TestSelectExposedBinsReportsEveryMissingRequest(t *testing.T) {
	bins := []exposedBin{
		{name: "cargo-add", command: "/cb/cargo-global/bin/cargo-add"},
		{name: "just", command: "/cb/cargo-global/bin/just"},
	}
	selected, missing := selectExposedBins(bins, map[string]bool{
		"just":   true,
		"typo-z": true,
		"typo-a": true,
	})
	if !reflect.DeepEqual(selected, []exposedBin{bins[1]}) {
		t.Fatalf("selected = %#v", selected)
	}
	if !reflect.DeepEqual(missing, []string{"typo-a", "typo-z"}) {
		t.Fatalf("missing = %#v", missing)
	}

	selected, missing = selectExposedBins(bins, nil)
	if !reflect.DeepEqual(selected, bins) || len(missing) != 0 {
		t.Fatalf("unfiltered selection = %#v, missing = %#v", selected, missing)
	}
}

// TestRenderExposedToolSection proves that the TOML section generated by
// cb expose inherits the source tool's identity (image, state_group,
// shared_volumes and environment settings), not a hardcoded npm default.
// It uses npm22 from the default registry so the parsed tool carries the
// Node 22 runtime identity.
func TestRenderExposedToolSection(t *testing.T) {
	reg := registry.Default()
	source, ok := reg.Tools["npm22"]
	if !ok {
		t.Fatal("npm22 not in default registry")
	}

	const binary = "cowsay"
	section := renderExposedToolSection("npm22", source, binary, "/cb/npm-global/bin/"+binary)
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered section invalid: %v", err)
	}

	got, ok := parsed.Tools[binary]
	if !ok {
		t.Fatal("parsed registry missing exposed tool")
	}
	if got.Image != "node:22-slim" {
		t.Errorf("image = %q, want %q", got.Image, "node:22-slim")
	}
	if got.StateGroup != "node22" {
		t.Errorf("state_group = %q, want %q", got.StateGroup, "node22")
	}
	if !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) {
		t.Errorf("shared_volumes = %v, want %v", got.SharedVolumes, source.SharedVolumes)
	}
	wantCommand := []string{"/cb/npm-global/bin/" + binary}
	if !reflect.DeepEqual(got.Command, wantCommand) {
		t.Errorf("command = %v, want %v", got.Command, wantCommand)
	}
}

func TestRenderGoExposedToolSection(t *testing.T) {
	reg := registry.Default()
	source, ok := reg.Tools["go"]
	if !ok {
		t.Fatal("go not in default registry")
	}
	const binary = "stringer"
	section := renderExposedToolSection("go", source, binary, "/go/bin/"+binary)
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered Go section invalid: %v", err)
	}
	got := parsed.Tools[binary]
	if got.Image != source.Image || got.StateGroup != "go124" {
		t.Fatalf("exposed Go identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Command, []string{"/go/bin/stringer"}) {
		t.Errorf("command = %v", got.Command)
	}
	if !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) || !reflect.DeepEqual(got.EnvNames, source.EnvNames) {
		t.Error("exposed Go profile did not inherit source state/environment")
	}
	if !reflect.DeepEqual(got.ProjectMarkers, source.ProjectMarkers) {
		t.Fatalf("project markers = %v, want %v", got.ProjectMarkers, source.ProjectMarkers)
	}
	if got.Role != "exposed" {
		t.Fatalf("role = %q, want exposed", got.Role)
	}

	moduleRoot := t.TempDir()
	nested := filepath.Join(moduleRoot, "cmd", "api")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module example.test/demo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	root, found := pathmap.FindProjectRoot(nested, pathmap.ProjectMarkersFor(got))
	if !found || root != moduleRoot {
		t.Fatalf("nested Go module root = %q, %v; want %q, true", root, found, moduleRoot)
	}
}

func TestRenderCargoExposedToolSection(t *testing.T) {
	reg := registry.Default()
	source, ok := reg.Tools["cargo"]
	if !ok {
		t.Fatal("cargo not in default registry")
	}
	const binary = "just"
	section := renderExposedToolSection("cargo", source, binary, "/cb/cargo-global/bin/"+binary)
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered Cargo section invalid: %v", err)
	}
	got := parsed.Tools[binary]
	if got.Image != source.Image || got.StateGroup != source.StateGroup {
		t.Fatalf("exposed Cargo identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Command, []string{"/cb/cargo-global/bin/just"}) {
		t.Errorf("command = %v", got.Command)
	}
	if !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) || !reflect.DeepEqual(got.EnvSet, source.EnvSet) {
		t.Error("exposed Cargo profile did not inherit source state/environment")
	}
	if !reflect.DeepEqual(got.ProjectMarkers, source.ProjectMarkers) || got.ProjectRootMode != source.ProjectRootMode {
		t.Fatal("exposed Cargo profile did not inherit project-root policy")
	}
	if got.Role != "exposed" {
		t.Fatalf("role = %q, want exposed", got.Role)
	}
}

func TestRenderUVToolExposedToolSection(t *testing.T) {
	reg := registry.Default()
	source, ok := reg.Tools["uv"]
	if !ok {
		t.Fatal("uv not in default registry")
	}
	const binary = "ruff"
	section := renderExposedToolSection("uv", source, binary, "/cb/uv-bin/"+binary)
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered uv tool section invalid: %v", err)
	}
	got := parsed.Tools[binary]
	if got.Image != source.Image || got.StateGroup != source.StateGroup {
		t.Fatalf("exposed uv tool identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Command, []string{"/cb/uv-bin/ruff"}) {
		t.Errorf("command = %v", got.Command)
	}
	if !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) || !reflect.DeepEqual(got.EnvSet, source.EnvSet) {
		t.Error("exposed uv tool profile did not inherit source state/environment")
	}
	if got.Role != "exposed" {
		t.Fatalf("role = %q, want exposed", got.Role)
	}
}

func TestRenderDotnetExposedToolSection(t *testing.T) {
	reg := registry.Default()
	source := reg.Tools["dotnet"]
	const binary = "dotnet-ef"
	section := renderExposedToolSection("dotnet", source, binary, "/root/.dotnet/tools/"+binary)
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered .NET tool section invalid: %v", err)
	}
	got := parsed.Tools[binary]
	if got.Image != source.Image || got.StateGroup != source.StateGroup {
		t.Fatalf("exposed .NET tool identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Command, []string{"/root/.dotnet/tools/dotnet-ef"}) {
		t.Errorf("command = %v", got.Command)
	}
	if !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) || !reflect.DeepEqual(got.EnvSet, source.EnvSet) {
		t.Error("exposed .NET tool profile did not inherit source state/environment")
	}
	if got.Role != "exposed" {
		t.Fatalf("role = %q, want exposed", got.Role)
	}
}

func TestRenderRubyExposedToolSection(t *testing.T) {
	reg := registry.Default()
	source := reg.Tools["ruby"]
	const binary = "rake"
	section := renderExposedToolSection("ruby", source, binary, "/cb/ruby-gems/bin/"+binary)
	parsed, err := registry.ParseTOML("schema_version = 1\n" + section)
	if err != nil {
		t.Fatalf("rendered RubyGems tool section invalid: %v", err)
	}
	got := parsed.Tools[binary]
	if got.Image != source.Image || got.StateGroup != source.StateGroup {
		t.Fatalf("exposed RubyGems tool identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Command, []string{"/cb/ruby-gems/bin/rake"}) {
		t.Errorf("command = %v", got.Command)
	}
	if !reflect.DeepEqual(got.SharedVolumes, source.SharedVolumes) || !reflect.DeepEqual(got.EnvSet, source.EnvSet) || !reflect.DeepEqual(got.EnvNames, source.EnvNames) {
		t.Error("exposed RubyGems tool profile did not inherit source state/environment")
	}
	for _, name := range got.EnvNames {
		if name == "RUBYGEMS_API_KEY" || name == "HTTP_PROXY_USER" || name == "HTTP_PROXY_PASS" {
			t.Fatalf("exposed RubyGems tool inherited credential variable %q", name)
		}
	}
	if got.Role != "exposed" {
		t.Fatalf("role = %q, want exposed", got.Role)
	}
}

func TestManagedExposedToolRecognition(t *testing.T) {
	for _, tt := range []struct {
		name string
		tool registry.Tool
	}{
		{name: "cowsay", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/npm-global/bin/cowsay"}, SharedVolumes: []string{"npm-global:/cb/npm-global"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/go/bin/Stringer"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "just", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/cargo-global/bin/just"}, SharedVolumes: []string{"global:/cb/cargo-global"}}},
		{name: "ruff", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/uv-bin/ruff"}, SharedVolumes: []string{"tools:/cb/uv-tools", "tool-bin:/cb/uv-bin"}}},
		{name: "dotnet-ef", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/root/.dotnet/tools/dotnet-ef"}, SharedVolumes: []string{"dotnet-home:/root/.dotnet"}}},
		{name: "rake", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/ruby-gems/bin/rake"}, SharedVolumes: []string{"gems:/cb/ruby-gems"}}},
		{name: "acme-lint", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/opt/acme/bin/acme-lint"}, SharedVolumes: []string{"tools:/opt/acme"}}},
	} {
		if !isManagedExposedTool(tt.name, tt.tool) {
			t.Fatalf("expected exposed tool: %#v", tt.tool)
		}
	}
	for _, tt := range []struct {
		name string
		tool registry.Tool
	}{
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Command: []string{"/go/bin/stringer"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateless", Role: "exposed", Command: []string{"/go/bin/stringer"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/go/bin/"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/usr/local/bin/stringer"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/go/bin/other"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/go/bin/stringer"}, SharedVolumes: []string{"cache:/other"}}},
		{name: "stringer", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/go/bin/a", "extra"}, SharedVolumes: []string{"gobin:/go/bin"}}},
		{name: "just", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/cargo-global/bin/just"}, SharedVolumes: []string{"global:/other"}}},
		{name: "ruff", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/uv-bin/ruff"}, SharedVolumes: []string{"tool-bin:/cb/uv-bin"}}},
		{name: "ruff", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/uv-bin/ruff"}, SharedVolumes: []string{"tool-bin:/other"}}},
		{name: "dotnet-ef", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/root/.dotnet/tools/dotnet-ef"}, SharedVolumes: []string{"dotnet-home:/other"}}},
		{name: "rake", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/cb/ruby-gems/bin/rake"}, SharedVolumes: []string{"gems:/other"}}},
		{name: "acme-lint", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/opt/acme/bin/../acme-lint"}, SharedVolumes: []string{"tools:/opt/acme"}}},
		{name: "acme-lint", tool: registry.Tool{Provider: "stateful", Role: "exposed", Command: []string{"/opt/acme-other/acme-lint"}, SharedVolumes: []string{"tools:/opt/acme"}}},
	} {
		if isManagedExposedTool(tt.name, tt.tool) {
			t.Fatalf("unexpected exposed tool: %#v", tt.tool)
		}
	}
}

func TestUnexposeRefusesUnmarkedCustomGoProfile(t *testing.T) {
	const config = `schema_version = 1

[tools.acme]
image = "example/acme:1"
provider = "stateful"
command = ["/go/bin/acme"]
state_group = "acme"
shared_volumes = ["cache:/other"]
`
	reg, err := registry.ParseTOML(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "container-bin.toml")
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Unexpose(reg, path, []string{"acme"}); err == nil || !strings.Contains(err.Error(), "not marked as a cb-exposed") {
		t.Fatalf("unexpose error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != config {
		t.Fatal("unexpose rewrote an unmarked custom profile")
	}
}

// captureStdout redirects os.Stdout to a temp file for the duration of fn,
// then restores it and returns everything fn wrote. Mirrors the helper in
// internal/diag so the new inspect/trace output lines can be asserted without
// changing those functions' signatures just for tests. Safe only because
// none of this package's tests call t.Parallel() — it swaps the process-
// global os.Stdout, so a parallel test using it would interleave captures
// silently; keep every test using it serial (same precondition
// internal/diag's copy documents at its own call site).
func captureStdout(fn func() error) (string, error) {
	f, err := os.CreateTemp("", "cb-cli-test-")
	if err != nil {
		return "", err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	real := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = real }()

	_ = fn()

	if cerr := f.Close(); cerr != nil {
		return "", cerr
	}
	data, rerr := os.ReadFile(tmpPath)
	if rerr != nil {
		return "", rerr
	}
	return string(data), nil
}

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

func TestInspectPrintsHostMountRaw(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%\\.claude:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Inspect(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "host_mount:  %USERPROFILE%\\.claude -> /root/.claude (ro)") {
		t.Fatalf("inspect output missing raw host_mount line:\n%s", out)
	}
}

func TestTracePrintsHostMountExpanded(t *testing.T) {
	dir := t.TempDir()
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, homeDir)

	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/.claude:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	expectedCanon, err := pathmap.CanonicalPath(homeDir + "/.claude")
	if err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	want := "host_mount:  " + expectedCanon + " -> /root/.claude (ro)"
	if !strings.Contains(out, want) {
		t.Fatalf("trace output missing expanded host_mount line (want %q):\n%s", want, out)
	}
}

func TestTraceHostMountResolveErrorIsPrintedNotFatal(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", "")
	} else {
		t.Setenv("HOME", "")
	}

	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/.claude:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "host_mount:  %USERPROFILE%/.claude -> /root/.claude (ro)") {
		t.Fatalf("trace output missing host_mount line:\n%s", out)
	}
	if !strings.Contains(out, "resolve error:") {
		t.Fatalf("trace output missing resolve error marker:\n%s", out)
	}
}

func TestTraceHostMountMissingSourceWouldFail(t *testing.T) {
	dir := t.TempDir()
	homeDir := t.TempDir()
	setTestHome(t, homeDir)

	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/does-not-exist:/root/missing:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "[would fail: source does not exist]") {
		t.Fatalf("trace output missing would-fail annotation:\n%s", out)
	}
}

func TestInspectDefaultOmitsCwdMode(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Inspect(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if strings.Contains(out, "cwd_mode") {
		t.Fatalf("default inspect output should not contain cwd_mode:\n%s", out)
	}
}

func TestInspectPrintsCwdModeIsolated(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateful"
state_group = "g"
cwd_mode = "isolated"
shared_volumes = ["cache:/root/.cache"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Inspect(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "cwd_mode:   isolated") {
		t.Fatalf("inspect output missing cwd_mode: isolated:\n%s", out)
	}
	if !strings.Contains(out, "workdir:    /root") {
		t.Fatalf("inspect output missing /root workdir:\n%s", out)
	}
	if !strings.Contains(out, "project_bind_mount: (none)") {
		t.Fatalf("inspect output missing no-project-mount marker:\n%s", out)
	}
	if strings.Contains(out, "\nroot:") {
		t.Fatalf("isolated inspect output should not print a project root:\n%s", out)
	}
	if strings.Contains(out, "\nworkspace:") {
		t.Fatalf("isolated inspect output should not print a workspace:\n%s", out)
	}
}

func TestTraceDefaultStillPrintsRootAndWorkspace(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "\nroot:") {
		t.Fatalf("default trace output missing root line:\n%s", out)
	}
	if !strings.Contains(out, "\nworkspace:") {
		t.Fatalf("default trace output missing workspace line:\n%s", out)
	}
	if strings.Contains(out, "cwd_mode") {
		t.Fatalf("default trace output should not contain cwd_mode:\n%s", out)
	}
}

func TestTraceIsolatedNoProjectMount(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateful"
state_group = "g"
cwd_mode = "isolated"
shared_volumes = ["cache:/root/.cache"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "cwd_mode:   isolated") {
		t.Fatalf("trace output missing cwd_mode: isolated:\n%s", out)
	}
	if !strings.Contains(out, "workdir:    /root") {
		t.Fatalf("trace output missing /root workdir:\n%s", out)
	}
	if !strings.Contains(out, "project_bind_mount: (none)") {
		t.Fatalf("trace output missing no-project-mount marker:\n%s", out)
	}
	if strings.Contains(out, "\nroot:") {
		t.Fatalf("isolated trace output should not print a project root:\n%s", out)
	}
	if strings.Contains(out, "\nworkspace:") {
		t.Fatalf("isolated trace output should not print a workspace:\n%s", out)
	}
}

func TestTraceHostMountUNCWouldFail(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("UNC resolution is only meaningful on Windows")
	}
	dir := t.TempDir()
	t.Setenv("USERPROFILE", `\\server\share\home`)

	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.ParseTOML(`[tools.demo]
image = "demo:1"
provider = "stateless"
host_mounts = ["%USERPROFILE%/.claude:/root/.claude:ro"]
`)
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}, policy.Policy{}) })
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(out, "[would fail: resolves to a UNC path, which Docker Desktop cannot share]") {
		t.Fatalf("trace output missing UNC would-fail annotation:\n%s", out)
	}
}

func TestParseBackupArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantPath  string
		wantState []string
		wantErr   bool
	}{
		{"plain_default", nil, "", nil, false},
		{"plain_path", []string{"backup.zip"}, "backup.zip", nil, false},
		{"state_default_path", []string{"--state", "cb-demo-cache"}, "", []string{"cb-demo-cache"}, false},
		{"state_named_path", []string{"backup.zip", "--state", "cb-a", "cb-b"}, "backup.zip", []string{"cb-a", "cb-b"}, false},
		{"state_missing_name", []string{"--state"}, "", nil, true},
		{"duplicate_flag", []string{"--state", "cb-a", "--state", "cb-b"}, "", nil, true},
		{"unknown_flag", []string{"--all"}, "", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, state, err := parseBackupArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, tc.wantErr)
			}
			if path != tc.wantPath || !reflect.DeepEqual(state, tc.wantState) {
				t.Fatalf("got path=%q state=%#v, want path=%q state=%#v", path, state, tc.wantPath, tc.wantState)
			}
		})
	}
}

func TestParseRestoreArgs(t *testing.T) {
	path, apply, state, err := parseRestoreArgs([]string{"backup.zip", "--state", "--apply"})
	if err != nil || path != "backup.zip" || !apply || !state {
		t.Fatalf("got path=%q apply=%v state=%v err=%v", path, apply, state, err)
	}
	for _, args := range [][]string{nil, {"--state"}, {"backup.zip", "--apply", "--apply"}, {"backup.zip", "--unknown"}} {
		if _, _, _, err := parseRestoreArgs(args); err == nil {
			t.Fatalf("expected error for %#v", args)
		}
	}
}

func TestPlainBackupIsValidAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "container-bin.toml")
	out := filepath.Join(dir, "backup.zip")
	if err := os.WriteFile(cfg, []byte(registry.DefaultTOML), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Backup(cfg, []string{out}, "test"); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		seen[f.Name] = true
	}
	zr.Close()
	for _, name := range []string{"container-bin.toml", "backup-info.txt"} {
		if !seen[name] {
			t.Fatalf("backup missing %s", name)
		}
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := Backup(cfg, []string{out}, "test"); err == nil || !strings.Contains(err.Error(), "choose a different filename") {
		t.Fatalf("existing-backup error = %v", err)
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("refused backup modified the existing archive")
	}
}
