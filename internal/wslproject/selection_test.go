package wslproject

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

func TestSelectForToolAppliesNearestAndOutermostMarkerPolicy(t *testing.T) {
	start := "/home/alice/workspace/crates/app/src"
	modes := map[string]os.FileMode{
		"/home/alice/workspace/Cargo.toml":            0o644,
		"/home/alice/workspace/crates/app/Cargo.toml": 0o644,
	}
	tool := registry.Tool{ProjectMarkers: []string{"Cargo.toml"}}

	project, found, err := selectForTool(start, tool, selectionTestDependencies(start, modes))
	if err != nil {
		t.Fatal(err)
	}
	if !found || project.Root != "/home/alice/workspace/crates/app" {
		t.Fatalf("nearest selection = (%+v, %v)", project, found)
	}

	tool.ProjectRootMode = "outermost"
	project, found, err = selectForTool(start, tool, selectionTestDependencies(start, modes))
	if err != nil {
		t.Fatal(err)
	}
	if !found || project.Root != "/home/alice/workspace" {
		t.Fatalf("outermost selection = (%+v, %v)", project, found)
	}
}

func TestSelectForToolUsesSharedCompatibilityMarkersAndWorkingDirectoryFallback(t *testing.T) {
	start := "/home/alice/project/src"
	pythonModes := map[string]os.FileMode{"/home/alice/project/pyproject.toml": 0o644}
	project, found, err := selectForTool(start, registry.Tool{Provider: "python"}, selectionTestDependencies(start, pythonModes))
	if err != nil {
		t.Fatal(err)
	}
	if !found || project.Root != "/home/alice/project" {
		t.Fatalf("legacy Python selection = (%+v, %v)", project, found)
	}

	project, found, err = selectForTool(start, registry.Tool{}, selectionTestDependencies(start, nil))
	if err != nil {
		t.Fatal(err)
	}
	if found || project.Root != start {
		t.Fatalf("working-directory fallback = (%+v, %v)", project, found)
	}
}

func TestSelectForToolUsesOnlyExactTrustedRoot(t *testing.T) {
	start := "/home/alice/project/src"
	tool := registry.Tool{TrustedProjectRoot: "/home/alice/project"}
	lstatCalled := false
	deps := selectionTestDependencies(start, nil)
	deps.lstat = func(string) (pathInfo, error) {
		lstatCalled = true
		return pathInfo{Mode: os.ModeDir | 0o755, Dev: 1}, nil
	}
	project, found, err := selectForTool(start, tool, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !lstatCalled || !found || project.Root != tool.TrustedProjectRoot {
		t.Fatalf("trusted selection = (%+v, %v), lstatCalled=%v", project, found, lstatCalled)
	}

	tool.TrustedProjectRoot = "/home/alice/other"
	if _, _, err := selectForTool(start, tool, deps); err == nil || !strings.Contains(err.Error(), "outside trusted") {
		t.Fatalf("outside-trusted error = %v", err)
	}
}

func TestSelectForToolRequiresWorkingDirectoryBoundaryProof(t *testing.T) {
	start := "/home/alice/project/mounted/src"
	deps := selectionTestDependencies(start, map[string]os.FileMode{"/home/alice/project/.git": os.ModeDir | 0o755})
	deps.classifyDescendant = func(Project, string) (Descendant, error) {
		return Descendant{}, errors.New("crosses mount boundary")
	}
	if _, _, err := selectForTool(start, registry.Tool{}, deps); err == nil || !strings.Contains(err.Error(), "crosses mount boundary") {
		t.Fatalf("boundary-proof error = %v", err)
	}
}

func TestSelectForToolRejectsUntrustedMarkerShapesAndObjects(t *testing.T) {
	start := "/home/alice/project"
	tests := map[string]struct {
		tool  registry.Tool
		modes map[string]os.FileMode
		deps  func(*selectionDependencies)
		want  string
	}{
		"parent marker": {
			tool: registry.Tool{ProjectMarkers: []string{"../.git"}},
			want: "one non-empty Linux path element",
		},
		"symlink marker": {
			tool:  registry.Tool{ProjectMarkers: []string{".git"}},
			modes: map[string]os.FileMode{start + "/.git": os.ModeSymlink | 0o777},
			want:  "is a symlink",
		},
		"special marker": {
			tool:  registry.Tool{ProjectMarkers: []string{"marker"}},
			modes: map[string]os.FileMode{start + "/marker": os.ModeNamedPipe | 0o600},
			want:  "not a regular file or directory",
		},
		"inspection failure": {
			tool: registry.Tool{ProjectMarkers: []string{".git"}},
			deps: func(deps *selectionDependencies) {
				deps.lstat = func(candidate string) (pathInfo, error) {
					if candidate == start {
						return pathInfo{Mode: os.ModeDir | 0o755, Dev: 1}, nil
					}
					return pathInfo{}, fs.ErrPermission
				}
			},
			want: "permission denied",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			deps := selectionTestDependencies(start, test.modes)
			if test.deps != nil {
				test.deps(&deps)
			}
			if _, _, err := selectForTool(start, test.tool, deps); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("selectForTool() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSelectForToolRejectsInvalidModesAndInputs(t *testing.T) {
	for name, test := range map[string]struct {
		start string
		tool  registry.Tool
	}{
		"root working directory":     {start: "/"},
		"relative working directory": {start: "project"},
		"isolated tool":              {start: "/home/alice/project", tool: registry.Tool{CwdMode: "isolated"}},
		"invalid cwd mode":           {start: "/home/alice/project", tool: registry.Tool{CwdMode: "guess"}},
		"invalid root mode":          {start: "/home/alice/project", tool: registry.Tool{ProjectRootMode: "middle"}},
	} {
		t.Run(name, func(t *testing.T) {
			deps := selectionTestDependencies(test.start, nil)
			if _, _, err := selectForTool(test.start, test.tool, deps); err == nil {
				t.Fatal("selectForTool() succeeded")
			}
		})
	}
}

func TestSelectForToolRejectsMissingSymlinkAndFileWorkingDirectories(t *testing.T) {
	start := "/home/alice/project"
	for name, mode := range map[string]os.FileMode{
		"missing": 0,
		"symlink": os.ModeSymlink | 0o777,
		"file":    0o644,
	} {
		t.Run(name, func(t *testing.T) {
			deps := selectionTestDependencies(start, nil)
			deps.lstat = func(candidate string) (pathInfo, error) {
				if candidate == start && name == "missing" {
					return pathInfo{}, fs.ErrNotExist
				}
				if candidate == start {
					return pathInfo{Mode: mode, Dev: 1}, nil
				}
				return pathInfo{}, fs.ErrNotExist
			}
			if _, _, err := selectForTool(start, registry.Tool{}, deps); err == nil {
				t.Fatal("selectForTool() accepted an invalid working directory")
			}
		})
	}
}

func selectionTestDependencies(start string, modes map[string]os.FileMode) selectionDependencies {
	return selectionDependencies{
		classify: func(root string) (Project, error) {
			return Project{Root: root, Storage: Distribution, MountPoint: "/", mountID: 24, device: 1}, nil
		},
		classifyDescendant: func(project Project, candidate string) (Descendant, error) {
			if !pathWithin(project.Root, candidate) {
				return Descendant{}, errors.New("outside project root")
			}
			relative := strings.TrimPrefix(candidate, project.Root)
			if relative == "" {
				relative = "."
			} else {
				relative = strings.TrimPrefix(relative, "/")
			}
			return Descendant{Path: candidate, Relative: relative, Exists: true, NearestExisting: candidate}, nil
		},
		lstat: func(name string) (pathInfo, error) {
			if name == start {
				return pathInfo{Mode: os.ModeDir | 0o755, Dev: 1}, nil
			}
			mode, ok := modes[name]
			if !ok {
				return pathInfo{}, fs.ErrNotExist
			}
			return pathInfo{Mode: mode, Dev: 1}, nil
		},
	}
}
