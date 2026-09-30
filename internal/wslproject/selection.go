package wslproject

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

type selectionDependencies struct {
	classify           func(string) (Project, error)
	classifyDescendant func(Project, string) (Descendant, error)
	lstat              func(string) (pathInfo, error)
}

func selectForTool(start string, tool registry.Tool, d selectionDependencies) (Project, bool, error) {
	if d.classify == nil || d.classifyDescendant == nil || d.lstat == nil {
		return Project{}, false, errors.New("native WSL project selection dependencies are incomplete")
	}
	if err := validateRoot(start); err != nil {
		return Project{}, false, fmt.Errorf("invalid native WSL working directory: %w", err)
	}
	switch tool.CwdMode {
	case "", "project":
	case "isolated":
		return Project{}, false, errors.New("native WSL project selection is not valid for an isolated tool")
	default:
		return Project{}, false, fmt.Errorf("unsupported cwd_mode %q", tool.CwdMode)
	}
	switch tool.ProjectRootMode {
	case "", "nearest", "outermost":
	default:
		return Project{}, false, fmt.Errorf("unsupported project_root_mode %q", tool.ProjectRootMode)
	}
	startInfo, err := d.lstat(start)
	if err != nil {
		return Project{}, false, fmt.Errorf("inspect native WSL working directory %s: %w", start, err)
	}
	if startInfo.Mode&os.ModeSymlink != 0 || !startInfo.Mode.IsDir() {
		return Project{}, false, fmt.Errorf("native WSL working directory %s must be a non-symlink directory", start)
	}

	root := ""
	found := false
	if tool.TrustedProjectRoot != "" {
		if err := validateRoot(tool.TrustedProjectRoot); err != nil {
			return Project{}, false, fmt.Errorf("invalid trusted native WSL project root: %w", err)
		}
		if !pathWithin(tool.TrustedProjectRoot, start) {
			return Project{}, false, fmt.Errorf("working directory %s is outside trusted native WSL project root %s", start, tool.TrustedProjectRoot)
		}
		root, found = tool.TrustedProjectRoot, true
	} else {
		markers := registry.ProjectMarkersFor(tool)
		for _, marker := range markers {
			if err := registry.ValidateProjectMarker(marker); err != nil {
				return Project{}, false, fmt.Errorf("invalid native WSL project marker %q: %w", marker, err)
			}
		}
		var err error
		root, found, err = findProjectRoot(start, markers, tool.ProjectRootMode == "outermost", d.lstat)
		if err != nil {
			return Project{}, false, err
		}
	}
	if !found {
		root = start
	}

	project, err := d.classify(root)
	if err != nil {
		return Project{}, false, fmt.Errorf("classify selected native WSL project root %s: %w", root, err)
	}
	if _, err := d.classifyDescendant(project, start); err != nil {
		return Project{}, false, fmt.Errorf("prove native WSL working directory %s under selected project root %s: %w", start, root, err)
	}
	return project, found, nil
}

func findProjectRoot(start string, markers []string, outermost bool, lstat func(string) (pathInfo, error)) (string, bool, error) {
	selected := ""
	for dir := start; dir != "/"; dir = path.Dir(dir) {
		matched := false
		for _, marker := range markers {
			markerPath := path.Join(dir, marker)
			info, err := lstat(markerPath)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return "", false, fmt.Errorf("inspect native WSL project marker %s: %w", markerPath, err)
			}
			if info.Mode&os.ModeSymlink != 0 {
				return "", false, fmt.Errorf("native WSL project marker %s is a symlink", markerPath)
			}
			if !info.Mode.IsDir() && !info.Mode.IsRegular() {
				return "", false, fmt.Errorf("native WSL project marker %s is not a regular file or directory", markerPath)
			}
			matched = true
		}
		if matched {
			selected = dir
			if !outermost {
				return selected, true, nil
			}
		}
	}
	return selected, selected != "", nil
}

func pathWithin(root, candidate string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}
