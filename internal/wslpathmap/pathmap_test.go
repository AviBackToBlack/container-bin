package wslpathmap

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

func TestMapToolArgsUsesRegistryPathSemantics(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	deps := testDependencies(project.Root)
	tool := registry.Tool{Name: "example", PathNext: []string{"-i"}, PathEquals: []string{"-chdir"}, PathLast: true}
	args := []string{"-i", "./input.txt", "-chdir=/home/alice/project/module", "result"}
	want := []string{"-i", "/workspace/demo/src/input.txt", "-chdir=/workspace/demo/module", "/workspace/demo/src/result"}
	got, workingDirectory, err := mapToolArgs(tool, project, project.Root+"/src", "/workspace/demo", args, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapped args = %q, want %q", got, want)
	}
	if workingDirectory != "/workspace/demo/src" {
		t.Fatalf("working directory = %q, want /workspace/demo/src", workingDirectory)
	}
	if !reflect.DeepEqual(args, []string{"-i", "./input.txt", "-chdir=/home/alice/project/module", "result"}) {
		t.Fatalf("input args were mutated: %q", args)
	}
}

func TestMapToolArgsRejectsPathsOutsideOrAcrossProjectBoundary(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	deps := testDependencies(project.Root)
	tool := registry.Tool{Name: "example", PathNext: []string{"-i"}}
	for name, arg := range map[string]string{
		"absolute outside": "/etc/passwd",
		"relative escape":  "../outside",
		"embedded escape":  "child/../../outside",
		"nested mount":     "/home/alice/project/mounted/file",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", []string{"-i", arg}, deps); err == nil {
				t.Fatal("path mapping accepted an unproven path")
			}
		})
	}
}

func TestMapToolArgsPreservesNonPathsAndPackagePatterns(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	deps := testDependencies(project.Root)
	tool := registry.Tool{Name: "go", PathNext: []string{"-i"}}
	args := []string{"install", "test", "./...", "pkg/...", "http://host/a/../b", "-i", "./...", "value"}
	got, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", args, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("mapped args = %q, want unchanged %q", got, args)
	}
}

func TestMapToolArgsRejectsWindowsAbsoluteSpellingInAnyPosition(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	tool := registry.Tool{Name: "example"}
	for name, arg := range map[string]string{
		"drive backslash": `C:\Work\App\input.txt`,
		"drive slash":     `c:/Work/App/input.txt`,
		"UNC":             `\\server\share\input.txt`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", []string{arg}, testDependencies(project.Root)); err == nil || !strings.Contains(err.Error(), "Windows path spelling is not supported") {
				t.Fatalf("Windows path spelling error = %v", err)
			}
		})
	}
}

func TestMapToolArgsPreservesWindowsLikeNonPaths(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	args := []string{`C:relative`, `name:value`, `\d+`}
	got, _, err := mapToolArgs(registry.Tool{Name: "example"}, project, project.Root, "/workspace/demo", args, testDependencies(project.Root))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("mapped args = %q, want unchanged %q", got, args)
	}
}

func TestMapToolArgsMapsAbsolutePackagePattern(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	got, _, err := mapToolArgs(
		registry.Tool{Name: "go"},
		project,
		project.Root,
		"/workspace/demo",
		[]string{"test", "/home/alice/project/pkg/..."},
		testDependencies(project.Root),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"test", "/workspace/demo/pkg/..."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mapped args = %q, want %q", got, want)
	}
}

func TestMapToolArgsHonorsDoubleDashAndReportsDanglingOption(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	deps := testDependencies(project.Root)
	tool := registry.Tool{Name: "example", PathNext: []string{"-i"}, PathLast: true}
	got, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", []string{"--", "file.txt"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--", "file.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mapped args = %q, want %q", got, want)
	}
	if _, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", []string{"-i"}, deps); err == nil || !strings.Contains(err.Error(), "requires a path argument") {
		t.Fatalf("dangling path option error = %v", err)
	}
}

func TestMapToolArgsOptionStateEdges(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	tool := registry.Tool{Name: "example", PathNext: []string{"-i"}, PathEquals: []string{"-chdir"}}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "double dash suppresses forced option",
			args: []string{"--", "-i", "./x"},
			want: []string{"--", "-i", "/workspace/demo/x"},
		},
		{
			name: "relative path equals",
			args: []string{"-chdir=./sub"},
			want: []string{"-chdir=/workspace/demo/sub"},
		},
		{
			name: "project root",
			args: []string{project.Root},
			want: []string{"/workspace/demo"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", test.args, testDependencies(project.Root))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mapped args = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMapToolArgsPathLastIfAnyAndMissingOutput(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	tool := registry.Tool{Name: "example", PathLast: true, PathLastIfAny: []string{"--write"}}
	deps := testDependencies(project.Root)

	got, _, err := mapToolArgs(tool, project, project.Root, "/workspace/demo", []string{"output"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inactive path_last_if_any args = %q, want %q", got, want)
	}

	missingClassified := false
	deps.classify = func(_ wslproject.Project, candidate string) (wslproject.Descendant, error) {
		if candidate == project.Root {
			return wslproject.Descendant{Path: candidate, Relative: ".", Exists: true, NearestExisting: candidate}, nil
		}
		if candidate == project.Root+"/new-output" {
			missingClassified = true
			return wslproject.Descendant{Path: candidate, Relative: "new-output", Exists: false, NearestExisting: project.Root}, nil
		}
		return wslproject.Descendant{}, errors.New("unexpected candidate")
	}
	got, _, err = mapToolArgs(tool, project, project.Root, "/workspace/demo", []string{"--write", "new-output"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !missingClassified {
		t.Fatal("missing output did not pass through descendant proof")
	}
	if want := []string{"--write", "/workspace/demo/new-output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active path_last_if_any args = %q, want %q", got, want)
	}
}

func TestMapToolArgsValidatesBoundaryInputs(t *testing.T) {
	project := wslproject.Project{Root: "/home/alice/project", Storage: wslproject.Distribution}
	deps := testDependencies(project.Root)
	if _, _, err := mapToolArgs(registry.Tool{}, project, project.Root, "/workspace/demo", nil, deps); err == nil {
		t.Fatal("unnamed tool was accepted")
	}
	if _, _, err := mapToolArgs(registry.Tool{Name: "tool"}, project, project.Root, "/", nil, deps); err == nil {
		t.Fatal("unsafe workspace root was accepted")
	}
	if _, _, err := mapToolArgs(registry.Tool{Name: "tool"}, project, "/home/alice/other", "/workspace/demo", nil, deps); err == nil {
		t.Fatal("working directory outside the project was accepted")
	}
}

func testDependencies(root string) dependencies {
	return dependencies{
		classify: func(_ wslproject.Project, candidate string) (wslproject.Descendant, error) {
			if candidate == root+"/mounted/file" {
				return wslproject.Descendant{}, errors.New("crosses mount boundary")
			}
			if candidate != root && !strings.HasPrefix(candidate, root+"/") {
				return wslproject.Descendant{}, errors.New("outside project root")
			}
			relative := strings.TrimPrefix(candidate, root+"/")
			if candidate == root {
				relative = "."
			}
			return wslproject.Descendant{Path: candidate, Relative: relative, Exists: true, NearestExisting: candidate}, nil
		},
	}
}
