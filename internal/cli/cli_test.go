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

	"github.com/AviBackToBlack/container-bin/internal/pathmap"
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
	} {
		err := add(registry.Default(), filepath.Join(t.TempDir(), "container-bin.toml"), args, func(registry.Registry) error {
			t.Fatal("installer called for invalid arguments")
			return nil
		})
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
		})
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
		})
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
		return add(registry.Default(), path, []string{"demo", "--image", "example/demo:1"}, func(registry.Registry) error { return nil })
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
	err := add(registry.Default(), path, []string{"demo", "--image", "example/demo:1"}, func(registry.Registry) error { return wantErr })
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

// These tests cover Expose guard paths that need no Docker daemon.
// The Docker-dependent discovery path (discoverNPMGlobalBins onward) remains
// untested here because it requires a real Docker daemon and a populated
// npm-global volume.

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
	if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), nil); err == nil {
		t.Fatal("expected usage error for empty args")
	} else if !strings.Contains(err.Error(), "usage: cb expose TOOL") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestExposeRejectsUnknownSource(t *testing.T) {
	reg := registry.Default()
	if err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), []string{"notarealtool"}); err == nil {
		t.Fatal("expected not-found error")
	} else if !strings.Contains(err.Error(), `tool "notarealtool" not found`) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// terraform exists in the registry but is not npm-shaped (stateless), so
// Expose must fail closed here before any docker invocation. This also pins
// that the new %q-formatted error carries the source tool's actual name rather
// than an empty string.
func TestExposeRejectsNonNpmShapedTool(t *testing.T) {
	reg := registry.Default()
	err := Expose(reg, filepath.Join(t.TempDir(), "container-bin.toml"), []string{"terraform"})
	if err == nil {
		t.Fatal("expected error for non-npm-shaped source tool")
	}
	if !strings.Contains(err.Error(), "is not a stateful npm-shaped profile") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), `"terraform"`) {
		t.Fatalf("error message does not name the source tool: %v", err)
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
	section := renderExposedToolSection("npm22", source, binary)
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

	out, err := captureStdout(func() error { return Inspect(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Inspect(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Inspect(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}) })
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

	out, err := captureStdout(func() error { return Trace(reg, []string{"demo"}) })
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
