package wslshim

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

func TestPlanBuildsSortedFixedSymlinkIdentities(t *testing.T) {
	layout := testLayout()
	names := []string{"python313", "node24", "python"}
	planned, err := Plan(layout, names)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"python313", "node24", "python"}) {
		t.Fatalf("Plan mutated names: %#v", names)
	}
	want := []Shim{
		{Name: "node24", Path: "/home/alice/.local/bin/node24", Target: layout.BinaryPath},
		{Name: "python", Path: "/home/alice/.local/bin/python", Target: layout.BinaryPath},
		{Name: "python313", Path: "/home/alice/.local/bin/python313", Target: layout.BinaryPath},
	}
	if !reflect.DeepEqual(planned, want) {
		t.Fatalf("Plan() = %#v, want %#v", planned, want)
	}
}

func TestInspectReportsReadyAndMissingWithoutMutation(t *testing.T) {
	layout := testLayout()
	deps := validDependencies(layout)
	deps.files[pathFor(layout, "node24")] = fileInfo{Mode: os.ModeSymlink | 0o777, UID: layout.UID}
	deps.links[pathFor(layout, "node24")] = layout.BinaryPath
	result, err := inspect(layout, []string{"python313", "node24"}, deps.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	want := []Shim{
		{Name: "node24", Path: pathFor(layout, "node24"), Target: layout.BinaryPath, State: Ready},
		{Name: "python313", Path: pathFor(layout, "python313"), Target: layout.BinaryPath, State: Missing},
	}
	if !reflect.DeepEqual(result.Shims, want) {
		t.Fatalf("Inspect() = %#v, want %#v", result.Shims, want)
	}
}

func TestPlanRejectsRedirectedLayoutAndInvalidNames(t *testing.T) {
	layoutMutations := map[string]func(*hostenv.WSLLayout){
		"relative home":       func(l *hostenv.WSLLayout) { l.Home = "home/alice" },
		"Windows home":        func(l *hostenv.WSLLayout) { l.Home = `/mnt/c/Users/alice` },
		"redirected shim dir": func(l *hostenv.WSLLayout) { l.ShimDir = "/tmp/bin" },
		"redirected binary":   func(l *hostenv.WSLLayout) { l.BinaryPath = "/tmp/cb" },
		"redirected cb shim":  func(l *hostenv.WSLLayout) { l.ManagementShim = "/tmp/cb" },
		"invalid distro":      func(l *hostenv.WSLLayout) { l.Distro = " Ubuntu" },
	}
	for name, mutate := range layoutMutations {
		t.Run(name, func(t *testing.T) {
			layout := testLayout()
			mutate(&layout)
			if _, err := Plan(layout, []string{"node24"}); err == nil {
				t.Fatal("Plan() succeeded")
			}
		})
	}
	for _, names := range [][]string{{"cb"}, {"Node24"}, {"bad/name"}, {"node24", "node24"}} {
		if _, err := Plan(testLayout(), names); err == nil {
			t.Fatalf("Plan(%q) succeeded", names)
		}
	}
}

func TestInspectRejectsForeignOrUnsafeBoundaries(t *testing.T) {
	tests := map[string]func(*fakeDependencies, *hostenv.WSLLayout){
		"wrong runtime": func(d *fakeDependencies, _ *hostenv.WSLLayout) {
			d.runtime = hostenv.Runtime{Kind: hostenv.LinuxNative}
		},
		"wrong distro": func(d *fakeDependencies, _ *hostenv.WSLLayout) {
			d.runtime.Distro = "Debian"
		},
		"wrong UID": func(d *fakeDependencies, _ *hostenv.WSLLayout) { d.uid = 1001 },
		"writable shim dir": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.files[l.ShimDir] = fileInfo{Mode: os.ModeDir | 0o775, UID: l.UID}
		},
		"foreign binary": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.files[l.BinaryPath] = fileInfo{Mode: 0o755, UID: 1001}
		},
		"management regular file": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.files[l.ManagementShim] = fileInfo{Mode: 0o755, UID: l.UID}
		},
		"management wrong target": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.links[l.ManagementShim] = "/tmp/cb"
		},
		"tool regular file": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.files[pathFor(*l, "node24")] = fileInfo{Mode: 0o755, UID: l.UID}
		},
		"tool wrong owner": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.files[pathFor(*l, "node24")] = fileInfo{Mode: os.ModeSymlink | 0o777, UID: 1001}
			d.links[pathFor(*l, "node24")] = l.BinaryPath
		},
		"tool wrong target": func(d *fakeDependencies, l *hostenv.WSLLayout) {
			d.files[pathFor(*l, "node24")] = fileInfo{Mode: os.ModeSymlink | 0o777, UID: l.UID}
			d.links[pathFor(*l, "node24")] = "/tmp/cb"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			layout := testLayout()
			deps := validDependencies(layout)
			mutate(&deps, &layout)
			if _, err := inspect(layout, []string{"node24"}, deps.dependencies()); err == nil {
				t.Fatal("inspect() succeeded")
			}
		})
	}
}

func TestInspectPropagatesFilesystemErrors(t *testing.T) {
	layout := testLayout()
	deps := validDependencies(layout)
	deps.failPath = layout.ShimDir
	if _, err := inspect(layout, nil, deps.dependencies()); err == nil || !strings.Contains(err.Error(), "filesystem unavailable") {
		t.Fatalf("inspect() error = %v", err)
	}
}

type fakeDependencies struct {
	runtime  hostenv.Runtime
	uid      uint32
	files    map[string]fileInfo
	links    map[string]string
	failPath string
}

func validDependencies(layout hostenv.WSLLayout) fakeDependencies {
	return fakeDependencies{
		runtime: hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: layout.Distro},
		uid:     layout.UID,
		files: map[string]fileInfo{
			layout.BinaryPath:     {Mode: 0o755, UID: layout.UID},
			layout.ShimDir:        {Mode: os.ModeDir | 0o700, UID: layout.UID},
			layout.ManagementShim: {Mode: os.ModeSymlink | 0o777, UID: layout.UID},
		},
		links: map[string]string{layout.ManagementShim: layout.BinaryPath},
	}
}

func (d *fakeDependencies) dependencies() dependencies {
	return dependencies{
		currentRuntime: func() (hostenv.Runtime, error) { return d.runtime, nil },
		currentUID:     func() uint32 { return d.uid },
		lstat: func(path string) (fileInfo, error) {
			if path == d.failPath {
				return fileInfo{}, errors.New("filesystem unavailable")
			}
			info, ok := d.files[path]
			if !ok {
				return fileInfo{}, fs.ErrNotExist
			}
			return info, nil
		},
		readlink: func(path string) (string, error) {
			target, ok := d.links[path]
			if !ok {
				return "", fs.ErrNotExist
			}
			return target, nil
		},
	}
}

func testLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{
		Distro:         "Ubuntu-24.04",
		UID:            1000,
		Home:           "/home/alice",
		BinaryPath:     "/home/alice/.local/lib/container-bin/cb",
		ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir:        "/home/alice/.local/bin",
	}
}

func pathFor(layout hostenv.WSLLayout, name string) string {
	return layout.ShimDir + "/" + name
}
