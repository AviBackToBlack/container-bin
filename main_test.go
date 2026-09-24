package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
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

	loadRegistry = func() (registry.Registry, string, error) {
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
	loadRegistry = func() (registry.Registry, string, error) {
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
