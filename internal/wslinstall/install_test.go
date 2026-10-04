package wslinstall

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
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

func TestUsageIncludesCleanupCommand(t *testing.T) {
	err := (command{}).run([]string{"unknown"}, io.Discard, "dev")
	if err == nil || !strings.Contains(err.Error(), "cb wsl cleanup (--check | --apply)") {
		t.Fatalf("usage error = %v", err)
	}
}

func TestCheckIsReadOnlyAndReportsRequiredActions(t *testing.T) {
	layout := installTestLayout()
	reg := registry.Default()
	mutatingLoadCalled := false
	c := command{
		prepareCommand: func([]string, io.Writer) error { return nil },
		currentLayout:  func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout: func(hostenv.WSLLayout) (wslfs.Plan, error) {
			return wslfs.Plan{Layout: layout, MissingDirectories: []string{layout.ConfigDir}}, nil
		},
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry: func(string, registry.Authenticator) (registry.Registry, string, error) {
			mutatingLoadCalled = true
			return registry.Registry{}, "", errors.New("unexpected mutating load")
		},
		loadRegistryReadOnly: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		executable:  func() (string, error) { return "/home/alice/cb-bootstrap", nil },
		lstat:       func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		binaryState: func(hostenv.WSLLayout, string) (State, error) { return Create, nil },
		inspectNames: func(_ hostenv.WSLLayout, names []string) (wslshim.Result, error) {
			planned, err := wslshim.Plan(layout, names)
			for index := range planned {
				planned[index].State = wslshim.Missing
			}
			return wslshim.Result{Shims: planned}, err
		},
	}
	var out bytes.Buffer
	if err := c.run([]string{"install", "--check"}, &out, "v-test"); err != nil {
		t.Fatal(err)
	}
	if mutatingLoadCalled {
		t.Fatal("read-only check used mutating registry load")
	}
	for _, want := range []string{"read-only; no files changed", "registry:      create", "binary:        create", "management:    create", "APPLY REQUIRED", "frontend:      INSTALL REQUIRED; RUNTIME WIRED; ACTIVATION GATED"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "frontend:      INSTALLED") {
		t.Fatalf("incomplete installation reported installed:\n%s", out.String())
	}
}

func TestCheckReportsRegistryRecoveryWithoutMutation(t *testing.T) {
	layout := installTestLayout()
	reg := registry.Default()
	mutatingLoadCalled := false
	c := command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout: func(hostenv.WSLLayout) (wslfs.Plan, error) {
			return wslfs.Plan{Layout: layout}, nil
		},
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry: func(string, registry.Authenticator) (registry.Registry, string, error) {
			mutatingLoadCalled = true
			return registry.Registry{}, "", errors.New("unexpected mutating load")
		},
		loadRegistryReadOnly: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		executable: func() (string, error) { return "/home/alice/cb-bootstrap", nil },
		lstat: func(path string) (os.FileInfo, error) {
			switch path {
			case layout.RegistryPath:
				return nil, fs.ErrNotExist
			case layout.RegistryPath + ".bak":
				return fakeFileInfo{}, nil
			case layout.ManagementShim:
				return nil, fs.ErrNotExist
			default:
				t.Fatalf("unexpected lstat path %q", path)
				return nil, fs.ErrInvalid
			}
		},
		binaryState: func(hostenv.WSLLayout, string) (State, error) { return Create, nil },
		inspectNames: func(_ hostenv.WSLLayout, names []string) (wslshim.Result, error) {
			planned, err := wslshim.Plan(layout, names)
			for index := range planned {
				planned[index].State = wslshim.Missing
			}
			return wslshim.Result{Shims: planned}, err
		},
	}
	var out bytes.Buffer
	if err := c.run([]string{"install", "--check"}, &out, "v-test"); err != nil {
		t.Fatal(err)
	}
	if mutatingLoadCalled {
		t.Fatal("read-only recovery check used mutating registry load")
	}
	if !strings.Contains(out.String(), "registry:      recover") {
		t.Fatalf("output did not report recovery:\n%s", out.String())
	}
}

func TestCheckRejectsUnsafeRecoveryStateBeforeRegistryLoad(t *testing.T) {
	layout := installTestLayout()
	want := errors.New("registry backup has mode 0644")
	c := command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout: func(hostenv.WSLLayout) (wslfs.Plan, error) {
			return wslfs.Plan{Layout: layout}, nil
		},
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return want },
		loadPolicy: func() (policy.Policy, error) {
			panic("policy loaded before recovery identity was validated")
		},
		loadRegistryReadOnly: func(string, registry.Authenticator) (registry.Registry, string, error) {
			panic("registry loaded before recovery identity was validated")
		},
		executable:  func() (string, error) { return "/home/alice/bootstrap-cb", nil },
		lstat:       func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist },
		binaryState: func(hostenv.WSLLayout, string) (State, error) { return Create, nil },
		inspectNames: func(hostenv.WSLLayout, []string) (wslshim.Result, error) {
			return wslshim.Result{}, nil
		},
	}
	err := c.run([]string{"install", "--check"}, &bytes.Buffer{}, "dev")
	if !errors.Is(err, want) {
		t.Fatalf("install --check error = %v, want wrapped recovery error", err)
	}
}

func TestApplyComposesRegistryBinaryAndShimLifecycle(t *testing.T) {
	layout := installTestLayout()
	reg := registry.Default()
	registryExists, binaryReady, managementReady, shimsReady := false, false, false, false
	prepared, locked := false, false
	c := command{
		prepareCommand: func([]string, io.Writer) error { return nil },
		currentLayout:  func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout: func(hostenv.WSLLayout) (wslfs.Plan, error) {
			return wslfs.Plan{Layout: layout}, nil
		},
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		prepareLayout:         func(hostenv.WSLLayout) error { prepared = true; return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		loadRegistryReadOnly: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		ensureRegistry: func(path string, mode os.FileMode) error {
			if path != layout.RegistryPath || mode != 0o600 {
				t.Fatalf("ensureRegistry(%q, %04o)", path, mode)
			}
			registryExists = true
			return nil
		},
		appendDefaults: func(path, version string, mode os.FileMode) error {
			if !registryExists || path != layout.RegistryPath || version != "v-test" || mode != 0o600 {
				t.Fatalf("appendDefaults(%q, %q, %04o)", path, version, mode)
			}
			return nil
		},
		executable: func() (string, error) { return "/home/alice/cb-bootstrap", nil },
		lstat: func(path string) (os.FileInfo, error) {
			switch path {
			case layout.RegistryPath:
				if registryExists {
					return fakeFileInfo{}, nil
				}
			case layout.ManagementShim:
				if managementReady {
					return fakeFileInfo{}, nil
				}
			}
			return nil, os.ErrNotExist
		},
		binaryState: func(hostenv.WSLLayout, string) (State, error) {
			if binaryReady {
				return Ready, nil
			}
			return Create, nil
		},
		installBinary: func(hostenv.WSLLayout, string) error { binaryReady = true; return nil },
		inspectNames: func(_ hostenv.WSLLayout, names []string) (wslshim.Result, error) {
			planned, err := wslshim.Plan(layout, names)
			for index := range planned {
				if shimsReady {
					planned[index].State = wslshim.Ready
				} else {
					planned[index].State = wslshim.Missing
				}
			}
			return wslshim.Result{Shims: planned}, err
		},
		reconcileManagement: func(hostenv.WSLLayout) (wslshim.Shim, error) {
			if !binaryReady {
				t.Fatal("management shim reconciled before binary")
			}
			managementReady = true
			return wslshim.Shim{State: wslshim.Ready}, nil
		},
		reconcileNames: func(_ hostenv.WSLLayout, names []string) (wslshim.Result, error) {
			if !managementReady {
				t.Fatal("tool shims reconciled before management shim")
			}
			shimsReady = true
			return wslshim.Result{}, nil
		},
		withLock: func(path string, fn func() error) error {
			if !prepared || path != layout.RegistryPath {
				t.Fatalf("lock acquired before prepare or for wrong path: prepared=%t path=%q", prepared, path)
			}
			locked = true
			return fn()
		},
	}
	var out bytes.Buffer
	if err := c.run([]string{"install", "--apply"}, &out, "v-test"); err != nil {
		t.Fatal(err)
	}
	if !prepared || !locked || !registryExists || !binaryReady || !managementReady || !shimsReady {
		t.Fatalf("incomplete lifecycle: prepared=%t locked=%t registry=%t binary=%t management=%t shims=%t", prepared, locked, registryExists, binaryReady, managementReady, shimsReady)
	}
	for _, want := range []string{"applied and revalidated", "INSTALLATION READY", "frontend:      INSTALLED; RUNTIME WIRED; ACTIVATION GATED"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "frontend:      INSTALL REQUIRED") {
		t.Fatalf("ready installation still reported apply required:\n%s", out.String())
	}
}

func TestApplyRejectsBootstrapBeforeRegistryMutation(t *testing.T) {
	layout := installTestLayout()
	registryTouched := false
	c := command{
		currentLayout:         func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout:           func(hostenv.WSLLayout) (wslfs.Plan, error) { return wslfs.Plan{Layout: layout}, nil },
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		prepareLayout:         func(hostenv.WSLLayout) error { return nil },
		executable:            func() (string, error) { return "/home/alice/unsafe", nil },
		binaryState:           func(hostenv.WSLLayout, string) (State, error) { return "", errors.New("unsafe bootstrap") },
		loadPolicy:            func() (policy.Policy, error) { registryTouched = true; return policy.Policy{}, nil },
		loadRegistry: func(string, registry.Authenticator) (registry.Registry, string, error) {
			registryTouched = true
			return registry.Registry{}, "", nil
		},
		loadRegistryReadOnly: func(string, registry.Authenticator) (registry.Registry, string, error) {
			return registry.Registry{}, "", nil
		},
		ensureRegistry:      func(string, os.FileMode) error { registryTouched = true; return nil },
		appendDefaults:      func(string, string, os.FileMode) error { registryTouched = true; return nil },
		lstat:               func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		installBinary:       func(hostenv.WSLLayout, string) error { return nil },
		inspectNames:        func(hostenv.WSLLayout, []string) (wslshim.Result, error) { return wslshim.Result{}, nil },
		reconcileManagement: func(hostenv.WSLLayout) (wslshim.Shim, error) { return wslshim.Shim{}, nil },
		reconcileNames:      func(hostenv.WSLLayout, []string) (wslshim.Result, error) { return wslshim.Result{}, nil },
		withLock:            func(_ string, fn func() error) error { return fn() },
	}
	err := c.run([]string{"install", "--apply"}, io.Discard, "v-test")
	if err == nil || !strings.Contains(err.Error(), "unsafe bootstrap") {
		t.Fatalf("apply error = %v", err)
	}
	if registryTouched {
		t.Fatal("registry or policy lifecycle ran before bootstrap validation")
	}
}

func TestApplyRejectsToolCollisionBeforeBinaryMutation(t *testing.T) {
	layout := installTestLayout()
	installed := false
	reg := registry.Default()
	c := command{
		currentLayout:         func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout:           func(hostenv.WSLLayout) (wslfs.Plan, error) { return wslfs.Plan{Layout: layout}, nil },
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		prepareLayout:         func(hostenv.WSLLayout) error { return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		loadRegistryReadOnly: func(path string, _ registry.Authenticator) (registry.Registry, string, error) {
			return reg, path, nil
		},
		ensureRegistry: func(string, os.FileMode) error { return nil },
		appendDefaults: func(string, string, os.FileMode) error { return nil },
		lstat:          func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		executable:     func() (string, error) { return "/home/alice/cb", nil },
		binaryState:    func(hostenv.WSLLayout, string) (State, error) { return Ready, nil },
		inspectNames: func(hostenv.WSLLayout, []string) (wslshim.Result, error) {
			return wslshim.Result{}, errors.New("foreign shim collision")
		},
		installBinary:       func(hostenv.WSLLayout, string) error { installed = true; return nil },
		reconcileManagement: func(hostenv.WSLLayout) (wslshim.Shim, error) { return wslshim.Shim{}, nil },
		reconcileNames:      func(hostenv.WSLLayout, []string) (wslshim.Result, error) { return wslshim.Result{}, nil },
		withLock:            func(_ string, fn func() error) error { return fn() },
	}
	err := c.run([]string{"install", "--apply"}, io.Discard, "v-test")
	if err == nil || !strings.Contains(err.Error(), "foreign shim collision") {
		t.Fatalf("apply error = %v", err)
	}
	if installed {
		t.Fatal("managed binary changed before tool-shim collision was rejected")
	}
}

func TestPrepareStillDelegatesWithoutInstallDependencies(t *testing.T) {
	called := false
	c := command{prepareCommand: func(args []string, _ io.Writer) error {
		called = true
		if strings.Join(args, " ") != "prepare --check" {
			t.Fatalf("args = %q", args)
		}
		return nil
	}}
	if err := c.run([]string{"prepare", "--check"}, io.Discard, "v-test"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("prepare command was not delegated")
	}
}

func TestPlannedToolNamesIncludesProspectiveUnsignedDefaults(t *testing.T) {
	reg := registry.Registry{SchemaVersion: registry.MaxSchemaVersion, Tools: map[string]registry.Tool{
		"custom": {Name: "custom", Image: "example.test/custom:1", Provider: "stateless"},
	}, Defaults: map[string]string{}}
	names := plannedToolNames(reg, false)
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	for _, want := range append([]string{"custom"}, registry.Default().ToolNames()...) {
		if !seen[want] {
			t.Fatalf("unsigned prospective plan missing %q: %q", want, names)
		}
	}
	signed := plannedToolNames(reg, true)
	if len(signed) != 1 || signed[0] != "custom" {
		t.Fatalf("signed registry plan = %q, want [custom]", signed)
	}
}

func installTestLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{
		Distro:         "Ubuntu-24.04",
		UID:            1000,
		Home:           "/home/alice",
		BinaryPath:     "/home/alice/.local/lib/container-bin/cb",
		ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir:        "/home/alice/.local/bin",
		ConfigDir:      "/home/alice/.config/container-bin",
		RegistryPath:   "/home/alice/.config/container-bin/container-bin.toml",
		LockPath:       "/home/alice/.config/container-bin/container-bin.lock",
		StateDir:       "/home/alice/.local/state/container-bin",
		StateNamespace: "wsl2-0123456789abcdef0123456789abcdef",
	}
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 1 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o600 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }
