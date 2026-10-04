package wslstate

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

const testNamespace = "wsl2-0123456789abcdef0123456789abcdef"

func TestStateClassifiesOnlyProvenNamespacedVolumes(t *testing.T) {
	deps, scope := testDependencies(t)
	project, err := scope.Project(wslvolume.PythonStateGroup, "venv", "/work/current")
	if err != nil {
		t.Fatal(err)
	}
	cache, _ := scope.Shared(wslvolume.PythonStateGroup, "pip-cache")
	staleShared, _ := scope.Shared("node24", "cache")
	orphan, _ := scope.Project("node24", "modules", "/work/gone")
	unsafe, _ := scope.Project("go124", "cache", "/work/unsafe")
	deps.discover = func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error) {
		return []wslvolume.Volume{unsafe, staleShared, cache, project, orphan}, nil
	}
	deps.lstat = func(path string) (os.FileInfo, error) {
		switch path {
		case testLayout().RegistryPath:
			return fakeInfo{mode: 0o600}, nil
		case "/work/gone":
			return nil, fs.ErrNotExist
		case "/work/unsafe":
			return fakeInfo{mode: os.ModeSymlink}, nil
		default:
			return fakeInfo{mode: os.ModeDir | 0o700}, nil
		}
	}

	var out bytes.Buffer
	if err := run(context.Background(), []string{"state"}, &out, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"distribution: Ubuntu-24.04", "namespace:    " + testNamespace,
		"CURRENT", project.Name(), "SHARED", cache.Name(), "MANAGED", staleShared.Name(),
		"ORPHAN", orphan.Name(), "/work/gone", "UNSAFE", unsafe.Name(), "/work/unsafe",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("state output missing %q:\n%s", want, out.String())
		}
	}
}

func TestGCDryRunSelectsCurrentProjectAndNeverSharedState(t *testing.T) {
	deps, scope := testDependencies(t)
	project, _ := scope.Project(wslvolume.PythonStateGroup, "venv", "/work/current")
	cache, _ := scope.Shared(wslvolume.PythonStateGroup, "pip-cache")
	deps.discover = func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error) {
		return []wslvolume.Volume{cache, project}, nil
	}
	removed := false
	deps.remove = func(context.Context, wslvolume.Volume) error { removed = true; return nil }

	for _, filter := range []string{"python", wslvolume.PythonStateGroup} {
		t.Run(filter, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(context.Background(), []string{"gc", filter}, &out, deps); err != nil {
				t.Fatal(err)
			}
			if removed || !strings.Contains(out.String(), project.Name()) || strings.Contains(out.String(), cache.Name()) {
				t.Fatalf("dry-run removed=%t output=%q", removed, out.String())
			}
		})
	}
}

func TestGCApplyRemovesOnlyMissingProvenProjectVolumes(t *testing.T) {
	deps, scope := testDependencies(t)
	missing, _ := scope.Project("node24", "modules", "/work/gone")
	unsafe, _ := scope.Project("node24", "cache", "/work/unsafe")
	other, _ := scope.Project("go124", "cache", "/work/other-gone")
	shared, _ := scope.Shared("node24", "cache")
	deps.discover = func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error) {
		return []wslvolume.Volume{unsafe, shared, other, missing}, nil
	}
	deps.lstat = func(path string) (os.FileInfo, error) {
		if path == testLayout().RegistryPath {
			return fakeInfo{mode: 0o600}, nil
		}
		if path == "/work/unsafe" {
			return fakeInfo{mode: os.ModeSymlink}, nil
		}
		if path == "/work" || path == "/" {
			return fakeInfo{mode: os.ModeDir | 0o755}, nil
		}
		return nil, fs.ErrNotExist
	}
	var removed []string
	deps.remove = func(_ context.Context, volume wslvolume.Volume) error {
		removed = append(removed, volume.Name())
		return nil
	}
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"node-v24": {Name: "node-v24", Provider: "stateful", StateGroup: "node24", ProjectVolumes: []string{"modules:/workspace/node_modules"}},
	}}
	deps.loadRegistry = func(path string, _ registry.Authenticator) (registry.Registry, string, error) { return reg, path, nil }

	var out bytes.Buffer
	if err := run(context.Background(), []string{"gc", "node24", "--orphans", "--apply"}, &out, deps); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != missing.Name() {
		t.Fatalf("removed = %#v, want only %s", removed, missing.Name())
	}
	if !strings.Contains(out.String(), "Removed "+missing.Name()) {
		t.Fatalf("apply output = %q", out.String())
	}
}

func TestGCProofFailurePrecedesMutationAndOutput(t *testing.T) {
	deps, _ := testDependencies(t)
	deps.discover = func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error) {
		return nil, errors.New("ownership proof failed")
	}
	deps.remove = func(context.Context, wslvolume.Volume) error {
		t.Fatal("removal ran after proof failure")
		return nil
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"gc", "--orphans", "--apply"}, &out, deps)
	if err == nil || !strings.Contains(err.Error(), "ownership proof failed") || out.Len() != 0 {
		t.Fatalf("proof failure err=%v output=%q", err, out.String())
	}
}

func TestStateRequiresInstalledRegularRegistry(t *testing.T) {
	deps, _ := testDependencies(t)
	deps.lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	if err := run(context.Background(), []string{"state"}, &bytes.Buffer{}, deps); err == nil || !strings.Contains(err.Error(), "registry is not installed") {
		t.Fatalf("missing registry error = %v", err)
	}
	deps.lstat = func(string) (os.FileInfo, error) { return fakeInfo{mode: os.ModeSymlink}, nil }
	if err := run(context.Background(), []string{"state"}, &bytes.Buffer{}, deps); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("unsafe registry error = %v", err)
	}
}

func TestInvalidGCSyntaxFailsBeforeFrontendIO(t *testing.T) {
	deps := dependencies{
		currentLayout: func() (hostenv.WSLLayout, error) {
			t.Fatal("layout derived for invalid syntax")
			return hostenv.WSLLayout{}, nil
		},
	}
	err := run(context.Background(), []string{"gc", "one", "two"}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "usage: cb gc") {
		t.Fatalf("invalid syntax error = %v", err)
	}
}

func TestResolveFilterMapsDefaultAliasToStateGroup(t *testing.T) {
	reg := registry.Registry{
		Tools: map[string]registry.Tool{
			"node24": {
				Name: "node24", Provider: "stateful", StateGroup: "node24",
				DefaultFamily: "node", DefaultVersion: "24", DefaultAlias: "node",
			},
		},
		Defaults: map[string]string{"node": "24"},
	}
	resolved, group := resolveFilter(reg, "node")
	if resolved != "node24" || group != "node24" {
		t.Fatalf("resolveFilter(node) = (%q, %q)", resolved, group)
	}
}

func TestMissingProjectWithUnsafeBoundaryProofIsNeverOrphaned(t *testing.T) {
	status, err := inspectProjectPath("/work/link/gone", func(string) (os.FileInfo, error) {
		return nil, fs.ErrNotExist
	}, func(string) error {
		return errors.New("symlinked ancestor")
	})
	if err != nil || status != "UNSAFE" {
		t.Fatalf("unsafe ancestor classification = (%q, %v)", status, err)
	}
}

func TestGCRechecksOrphanImmediatelyBeforeRemoval(t *testing.T) {
	deps, scope := testDependencies(t)
	orphan, _ := scope.Project("node24", "modules", "/work/restored")
	deps.discover = func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error) {
		return []wslvolume.Volume{orphan}, nil
	}
	checks := 0
	deps.lstat = func(path string) (os.FileInfo, error) {
		if path == testLayout().RegistryPath {
			return fakeInfo{mode: 0o600}, nil
		}
		checks++
		if checks == 1 {
			return nil, fs.ErrNotExist
		}
		return fakeInfo{mode: os.ModeDir | 0o755}, nil
	}
	deps.remove = func(context.Context, wslvolume.Volume) error {
		t.Fatal("restored project volume was removed")
		return nil
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"gc", "--orphans", "--apply"}, &out, deps)
	if err == nil || !strings.Contains(err.Error(), "refusing stale orphan plan") || out.Len() != 0 {
		t.Fatalf("stale-plan result err=%v output=%q", err, out.String())
	}
}

func testDependencies(t *testing.T) (dependencies, wslvolume.Scope) {
	t.Helper()
	layout := testLayout()
	scope, err := wslvolume.New(layout)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.Registry{Tools: map[string]registry.Tool{
		"python": {Name: "python", Provider: "python"},
	}}
	return dependencies{
		currentLayout:         func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout:           func(got hostenv.WSLLayout) (wslfs.Plan, error) { return wslfs.Plan{Layout: got}, nil },
		checkRegistryRecovery: func(hostenv.WSLLayout) error { return nil },
		loadPolicy:            func() (policy.Policy, error) { return policy.Policy{}, nil },
		loadRegistry:          func(path string, _ registry.Authenticator) (registry.Registry, string, error) { return reg, path, nil },
		lstat:                 func(string) (os.FileInfo, error) { return fakeInfo{mode: 0o600}, nil },
		getwd:                 func() (string, error) { return "/work/current", nil },
		selectProject: func(string, registry.Tool) (wslproject.Project, bool, error) {
			return wslproject.Project{Root: "/work/current"}, true, nil
		},
		planStateful: func(wslvolume.Scope, registry.Tool, wslproject.Project, string) ([]wslvolume.Volume, error) {
			return nil, nil
		},
		planPython: func(scope wslvolume.Scope, project wslproject.Project, found bool) (wslvolume.PythonState, error) {
			venv, err := scope.Project(wslvolume.PythonStateGroup, "venv", project.Root)
			if !found {
				venv, err = scope.Shared(wslvolume.PythonStateGroup, "compat-venv")
			}
			if err != nil {
				return wslvolume.PythonState{}, err
			}
			cache, err := scope.Shared(wslvolume.PythonStateGroup, "pip-cache")
			return wslvolume.PythonState{Venv: venv, PipCache: cache}, err
		},
		proveMissing: func(string) error { return nil },
		discover:     func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error) { return nil, nil },
		remove:       func(context.Context, wslvolume.Volume) error { return nil },
	}, scope
}

func testLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{
		Distro: "Ubuntu-24.04", UID: 1000, Home: "/home/alice",
		BinaryPath: "/home/alice/.local/lib/container-bin/cb", ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir: "/home/alice/.local/bin", ConfigDir: "/home/alice/.config/container-bin",
		RegistryPath: "/home/alice/.config/container-bin/container-bin.toml",
		LockPath:     "/home/alice/.config/container-bin/container-bin.lock", StateDir: "/home/alice/.local/state/container-bin",
		StateNamespace: testNamespace,
	}
}

type fakeInfo struct{ mode os.FileMode }

func (f fakeInfo) Name() string       { return "fake" }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }
