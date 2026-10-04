package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/projectconfig"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/selfupdate"
)

func TestVersionDefaultIsDev(t *testing.T) {
	// Release builds override this via -ldflags "-X main.version=vX.Y.Z".
	if version != "dev" {
		t.Fatalf("default version = %q, want \"dev\"", version)
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

func TestProjectReviewCommandsSelectReadOnlyRegistryLoad(t *testing.T) {
	for _, tc := range []struct {
		invoked string
		args    []string
		want    bool
	}{
		{invoked: "cb", args: []string{"trust", "--check"}, want: true},
		{invoked: "cb", args: []string{"trust"}, want: true},
		{invoked: "cb", args: []string{"inspect", "--project"}, want: true},
		{invoked: "cb", args: []string{"inspect", "node"}, want: false},
		{invoked: "cb", args: []string{"doctor"}, want: false},
		{invoked: "node", args: []string{"trust", "--check"}, want: false},
	} {
		if got := useReadOnlyRegistryLoad(tc.invoked, tc.args); got != tc.want {
			t.Errorf("useReadOnlyRegistryLoad(%q, %v) = %t, want %t", tc.invoked, tc.args, got, tc.want)
		}
	}
}

func TestBootstrapCommandsSkipHostPolicyAndRegistry(t *testing.T) {
	oldArgs := os.Args
	oldLoadRegistry := loadRegistry
	oldRequireHostFrontend := requireHostFrontend
	oldLoadPolicy := loadPolicy
	defer func() {
		os.Args = oldArgs
		loadRegistry = oldLoadRegistry
		requireHostFrontend = oldRequireHostFrontend
		loadPolicy = oldLoadPolicy
	}()

	loadRegistry = func(registry.Authenticator) (registry.Registry, string, error) {
		panic("bootstrap command attempted to load the registry")
	}
	requireHostFrontend = func() error {
		panic("bootstrap command attempted host enforcement")
	}
	loadPolicy = func() (policy.Policy, error) {
		panic("bootstrap command attempted to load machine policy")
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no_args", args: []string{"cb.exe"}, want: "Commands:"},
		{name: "version", args: []string{"cb.exe", "version"}, want: "container-bin dev"},
		{name: "version_long", args: []string{"cb.exe", "--version"}, want: "container-bin dev"},
		{name: "version_short", args: []string{"cb.exe", "-V"}, want: "container-bin dev"},
		{name: "help", args: []string{"cb.exe", "help"}, want: "Commands:"},
		{name: "help_long", args: []string{"cb.exe", "--help"}, want: "Commands:"},
		{name: "help_short", args: []string{"cb.exe", "-h"}, want: "Commands:"},
		{name: "config", args: []string{"cb.exe", "config"}, want: "container-bin.toml"},
		{name: "long_binary_name", args: []string{"container-bin.exe", "--help"}, want: "Commands:"},
		{name: "versioned_binary_name", args: []string{"cb-v1.2.3.exe", "--version"}, want: "container-bin dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Args = tt.args
			out := captureMainStdout(t, main)
			if !strings.Contains(out, tt.want) {
				t.Fatalf("output %q does not contain %q", out, tt.want)
			}
		})
	}
}

func TestWSLPreflightSkipsGeneralHostPolicyAndRegistry(t *testing.T) {
	oldArgs := os.Args
	oldRunWSL := runWSL
	oldLoadRegistry := loadRegistry
	oldRequireHostFrontend := requireHostFrontend
	oldLoadPolicy := loadPolicy
	defer func() {
		os.Args = oldArgs
		runWSL = oldRunWSL
		loadRegistry = oldLoadRegistry
		requireHostFrontend = oldRequireHostFrontend
		loadPolicy = oldLoadPolicy
	}()

	loadRegistry = func(registry.Authenticator) (registry.Registry, string, error) {
		panic("WSL preflight attempted to load the registry")
	}
	requireHostFrontend = func() error {
		panic("WSL preflight attempted general host enforcement")
	}
	loadPolicy = func() (policy.Policy, error) {
		panic("WSL preflight attempted to load machine policy")
	}
	var gotArgs []string
	runWSL = func(args []string, out io.Writer) error {
		gotArgs = append([]string(nil), args...)
		_, err := io.WriteString(out, "native WSL preflight\n")
		return err
	}
	os.Args = []string{"cb", "wsl", "prepare", "--check"}

	out := captureMainStdout(t, main)
	if got := strings.Join(gotArgs, " "); got != "prepare --check" {
		t.Fatalf("WSL preflight args = %q, want %q", got, "prepare --check")
	}
	if out != "native WSL preflight\n" {
		t.Fatalf("WSL preflight output = %q", out)
	}
}

func TestNativeWSLToolDispatchSkipsWindowsRegistryAndRuntime(t *testing.T) {
	oldArgs := os.Args
	oldRequireHostFrontend := requireHostFrontend
	oldCurrentHostRuntime := currentHostRuntime
	oldRunWSLTool := runWSLTool
	oldLoadRegistry := loadRegistry
	oldLoadPolicy := loadPolicy
	oldExit := osExit
	defer func() {
		os.Args = oldArgs
		requireHostFrontend = oldRequireHostFrontend
		currentHostRuntime = oldCurrentHostRuntime
		runWSLTool = oldRunWSLTool
		loadRegistry = oldLoadRegistry
		loadPolicy = oldLoadPolicy
		osExit = oldExit
	}()

	requireHostFrontend = func() error { return nil }
	currentHostRuntime = func() (hostenv.Runtime, error) {
		return hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}, nil
	}
	loadRegistry = func(registry.Authenticator) (registry.Registry, string, error) {
		panic("native WSL dispatch attempted the Windows registry loader")
	}
	loadPolicy = func() (policy.Policy, error) {
		panic("native WSL dispatch attempted the Windows runtime path")
	}
	called := false
	runWSLTool = func(_ context.Context, invoked string, args []string) (int, error) {
		called = true
		if invoked != "node" || strings.Join(args, " ") != "--version" {
			t.Fatalf("WSL dispatch = %q %#v", invoked, args)
		}
		return 42, nil
	}
	type exitCode int
	osExit = func(code int) { panic(exitCode(code)) }
	os.Args = []string{"node", "--version"}

	defer func() {
		if got := recover(); got != exitCode(42) {
			t.Fatalf("main panic = %v, want tool exit 42", got)
		}
		if !called {
			t.Fatal("native WSL tool runner was not called")
		}
	}()
	main()
}

func TestHostBoundaryPrecedesPolicyAndRegistryLoad(t *testing.T) {
	oldArgs := os.Args
	oldLoadRegistry := loadRegistry
	oldLoadPolicy := loadPolicy
	oldRequireHostFrontend := requireHostFrontend
	oldExit := osExit
	defer func() {
		os.Args = oldArgs
		loadRegistry = oldLoadRegistry
		loadPolicy = oldLoadPolicy
		requireHostFrontend = oldRequireHostFrontend
		osExit = oldExit
	}()

	called := false
	requireHostFrontend = func() error {
		called = true
		return errors.New("unsupported host")
	}
	loadRegistry = func(registry.Authenticator) (registry.Registry, string, error) {
		panic("host boundary attempted to load the registry")
	}
	loadPolicy = func() (policy.Policy, error) {
		panic("host boundary attempted to load machine policy")
	}
	type exitCode int
	osExit = func(code int) { panic(exitCode(code)) }
	os.Args = []string{"cb.exe", "doctor"}

	defer func() {
		got := recover()
		if got != exitCode(exitCbFailure) {
			t.Fatalf("main panic = %v, want exit %d", got, exitCbFailure)
		}
		if !called {
			t.Fatal("host boundary was not called")
		}
	}()
	main()
}

func TestSelfUpdateEnforcesHostBoundaryAndSkipsPolicyAndRegistry(t *testing.T) {
	oldArgs := os.Args
	oldLoadRegistry := loadRegistry
	oldLoadPolicy := loadPolicy
	oldRequireHostFrontend := requireHostFrontend
	oldRunSelfUpdate := runSelfUpdate
	oldCurrentHostRuntime := currentHostRuntime
	defer func() {
		os.Args = oldArgs
		loadRegistry = oldLoadRegistry
		loadPolicy = oldLoadPolicy
		requireHostFrontend = oldRequireHostFrontend
		runSelfUpdate = oldRunSelfUpdate
		currentHostRuntime = oldCurrentHostRuntime
	}()

	hostChecked := false
	requireHostFrontend = func() error {
		hostChecked = true
		return nil
	}
	currentHostRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.WindowsNative}, nil }
	loadRegistry = func(registry.Authenticator) (registry.Registry, string, error) {
		panic("self-update check attempted to load the registry")
	}
	loadPolicy = func() (policy.Policy, error) {
		panic("self-update check attempted to load machine policy")
	}
	runSelfUpdate = func(_ context.Context, current string, args []string, out io.Writer) error {
		if current != "dev" {
			t.Fatalf("current version = %q, want dev", current)
		}
		if strings.Join(args, " ") != "--check --version v1.1.0" {
			t.Fatalf("self-update args = %q", args)
		}
		_, err := io.WriteString(out, "self-update seam reached\n")
		return err
	}
	os.Args = []string{"cb.exe", "self-update", "--check", "--version", "v1.1.0"}

	out := captureMainStdout(t, main)
	if !strings.Contains(out, "self-update seam reached") {
		t.Fatalf("output %q does not contain self-update marker", out)
	}
	if !hostChecked {
		t.Fatal("self-update check skipped host enforcement")
	}
}

func TestSelfUpdateHelperDispatchIsPrivateAndSkipsPolicyAndRegistry(t *testing.T) {
	oldArgs := os.Args
	oldLoadRegistry := loadRegistry
	oldLoadPolicy := loadPolicy
	oldRequireHostFrontend := requireHostFrontend
	oldRunSelfUpdateHelper := runSelfUpdateHelper
	oldCurrentHostRuntime := currentHostRuntime
	defer func() {
		os.Args = oldArgs
		loadRegistry = oldLoadRegistry
		loadPolicy = oldLoadPolicy
		requireHostFrontend = oldRequireHostFrontend
		runSelfUpdateHelper = oldRunSelfUpdateHelper
		currentHostRuntime = oldCurrentHostRuntime
	}()

	hostChecked := false
	requireHostFrontend = func() error { hostChecked = true; return nil }
	currentHostRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.WindowsNative}, nil }
	loadRegistry = func(registry.Authenticator) (registry.Registry, string, error) {
		panic("self-update helper attempted to load the registry")
	}
	loadPolicy = func() (policy.Policy, error) {
		panic("self-update helper attempted to load machine policy")
	}
	called := false
	runSelfUpdateHelper = func(_ context.Context, args []string, out io.Writer) error {
		called = true
		if strings.Join(args, " ") != `--request C:\private\request.json` {
			t.Fatalf("helper args = %q", args)
		}
		_, err := io.WriteString(out, "helper seam reached\n")
		return err
	}
	os.Args = []string{"cb-update-helper.exe", "__self-update-helper", "--request", `C:\private\request.json`}
	out := captureMainStdout(t, main)
	if !called || !hostChecked || !strings.Contains(out, "helper seam reached") {
		t.Fatalf("helper called=%t host=%t output=%q", called, hostChecked, out)
	}
	if !selfupdate.IsHelperInvocation("cb-update-helper", os.Args[1:]) {
		t.Fatal("private helper invocation was not recognized")
	}
}

func TestProjectDoctorStatus(t *testing.T) {
	tests := []struct {
		name    string
		ctx     projectconfig.Context
		wantErr string
		wantOut string
	}{
		{name: "absent", ctx: projectconfig.Context{Status: projectconfig.Absent}, wantOut: "OK       project overlay: absent"},
		{name: "trusted", ctx: projectconfig.Context{Status: projectconfig.Trusted}, wantOut: "OK       project overlay: trusted"},
		{name: "untrusted", ctx: projectconfig.Context{Status: projectconfig.Untrusted}, wantErr: "not trusted", wantOut: "FAIL     project overlay: untrusted"},
		{name: "changed", ctx: projectconfig.Context{Status: projectconfig.Changed}, wantErr: "changed after trust", wantOut: "FAIL     project overlay: changed"},
		{name: "invalid", ctx: projectconfig.Context{Status: projectconfig.Invalid, Err: errors.New("bad overlay")}, wantErr: "escaped diagnostic", wantOut: "FAIL     project overlay: invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotErr error
			out := captureMainStdout(t, func() { gotErr = projectDoctorStatus(tc.ctx) })
			if !strings.Contains(out, tc.wantOut) {
				t.Fatalf("output %q does not contain %q", out, tc.wantOut)
			}
			if tc.wantErr == "" && gotErr != nil {
				t.Fatalf("projectDoctorStatus() error = %v", gotErr)
			}
			if tc.wantErr != "" && (gotErr == nil || !strings.Contains(gotErr.Error(), tc.wantErr)) {
				t.Fatalf("projectDoctorStatus() error = %v, want %q", gotErr, tc.wantErr)
			}
		})
	}
}

func TestRejectProjectExposeSource(t *testing.T) {
	ctx := projectconfig.Context{Overlay: projectconfig.Overlay{Registry: registry.Registry{Tools: map[string]registry.Tool{
		"acme": {Name: "acme"},
	}}}}
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "project source", args: []string{"acme"}, wantErr: true},
		{name: "case folded", args: []string{"ACME", "child"}, wantErr: true},
		{name: "shared file source", args: []string{"--shared-file", "acme", "state", "/tools/acme"}, wantErr: true},
		{name: "global source", args: []string{"go"}},
		{name: "malformed shared file", args: []string{"--shared-file"}},
		{name: "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectProjectExposeSource(ctx, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("rejectProjectExposeSource() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func captureMainStdout(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = oldStdout }()

	fn()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
