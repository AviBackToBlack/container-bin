package wslrun

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
	"github.com/AviBackToBlack/container-bin/internal/wslshim"
)

func TestRunFrontendUsesOnlyFixedLayoutAndManagedIdentity(t *testing.T) {
	layout := hostenv.WSLLayout{
		Distro: "Ubuntu-24.04", UID: 1000, Home: "/home/alice",
		BinaryPath: "/home/alice/.local/lib/container-bin/cb", ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir: "/home/alice/.local/bin", ConfigDir: "/home/alice/.config/container-bin",
		RegistryPath: "/home/alice/.config/container-bin/container-bin.toml",
		LockPath:     "/home/alice/.config/container-bin/container-bin.lock",
		StateDir:     "/home/alice/.local/state/container-bin", StateNamespace: testNamespace,
	}
	stream := &fakeAttach{reader: bytes.NewReader(rawFrame(1, "ok")), multiplexed: true, writeDone: make(chan struct{})}
	var stdout, stderr bytes.Buffer
	var calls []string
	runDeps := successfulRunDependencies(t, stream, &stdout, &stderr, &calls)
	deps := frontendDependencies{
		currentLayout:         func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout:           func(got hostenv.WSLLayout) (wslfs.Plan, error) { return wslfs.Plan{Layout: got}, nil },
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			if path != layout.RegistryPath {
				t.Fatalf("registry path = %q", path)
			}
			return registry.Registry{Tools: map[string]registry.Tool{
				"demo": {Name: "demo", Image: "demo:1", Provider: "stateless", Command: []string{"demo"}},
			}}, path, nil
		},
		inspectShims: func(_ hostenv.WSLLayout, names []string) (wslshim.Result, error) {
			return wslshim.Result{Shims: []wslshim.Shim{{Name: names[0], State: wslshim.Ready}}}, nil
		},
		lstat:        func(string) (os.FileInfo, error) { return fakeFileInfo{mode: 0o600}, nil },
		executable:   func() (string, error) { return layout.BinaryPath, nil },
		evalSymlinks: func(value string) (string, error) { return value, nil },
		getwd:        func() (string, error) { return "/project", nil },
		interactive:  func() bool { return false },
		environ:      func() []string { return nil },
		plan:         testPlanDependencies(true),
		run:          runDeps,
	}
	code, err := runFrontend(context.Background(), "demo", []string{"input.txt"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != 23 || stdout.String() != "ok" || stderr.Len() != 0 {
		t.Fatalf("frontend result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunFrontendRejectsUnmanagedExecutableBeforeConfigOrDocker(t *testing.T) {
	layout := hostenv.WSLLayout{
		BinaryPath: "/home/alice/.local/lib/container-bin/cb", RegistryPath: "/home/alice/.config/container-bin/container-bin.toml",
	}
	deps := frontendDependencies{
		currentLayout:         func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout:           func(got hostenv.WSLLayout) (wslfs.Plan, error) { return wslfs.Plan{Layout: got}, nil },
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		loadPolicy: func() (policy.Policy, error) {
			t.Fatal("policy loaded for unmanaged binary")
			return policy.Policy{}, nil
		},
		loadRegistry: func(string, registry.Authenticator) (registry.Registry, string, error) {
			t.Fatal("registry loaded for unmanaged binary")
			return registry.Registry{}, "", nil
		},
		inspectShims: func(hostenv.WSLLayout, []string) (wslshim.Result, error) {
			return wslshim.Result{Shims: []wslshim.Shim{{Name: "demo", State: wslshim.Ready}}}, nil
		},
		lstat:        func(string) (os.FileInfo, error) { return fakeFileInfo{mode: 0o600}, nil },
		executable:   func() (string, error) { return "/tmp/cb", nil },
		evalSymlinks: func(value string) (string, error) { return value, nil },
		getwd:        func() (string, error) { return "/project", nil },
		interactive:  func() bool { return false },
		environ:      func() []string { return nil },
	}
	_, err := runFrontend(context.Background(), "demo", nil, deps)
	if err == nil || !strings.Contains(err.Error(), "requires managed binary") {
		t.Fatalf("unmanaged executable error = %v", err)
	}
}

type fakeFileInfo struct{ mode os.FileMode }

func (f fakeFileInfo) Name() string       { return "file" }
func (f fakeFileInfo) Size() int64        { return 1 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }
