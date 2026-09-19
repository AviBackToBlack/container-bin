package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

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

func TestBootstrapCommandsDoNotLoadRegistry(t *testing.T) {
	oldArgs := os.Args
	oldLoadRegistry := loadRegistry
	defer func() {
		os.Args = oldArgs
		loadRegistry = oldLoadRegistry
	}()

	loadRegistry = func() (registry.Registry, string, error) {
		panic("bootstrap command attempted to load the registry")
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

func TestSelfUpdateCheckDoesNotLoadRegistry(t *testing.T) {
	oldArgs := os.Args
	oldLoadRegistry := loadRegistry
	oldRunSelfUpdateCheck := runSelfUpdateCheck
	defer func() {
		os.Args = oldArgs
		loadRegistry = oldLoadRegistry
		runSelfUpdateCheck = oldRunSelfUpdateCheck
	}()

	loadRegistry = func() (registry.Registry, string, error) {
		panic("self-update check attempted to load the registry")
	}
	runSelfUpdateCheck = func(_ context.Context, current string, args []string, out io.Writer) error {
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
