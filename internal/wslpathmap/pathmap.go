// Package wslpathmap maps native WSL project paths into a container workspace.
// It consumes wslproject's mount-boundary proof and does not enable execution.
package wslpathmap

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

type dependencies struct {
	classify func(wslproject.Project, string) (wslproject.Descendant, error)
}

// DescendantClassifier proves one candidate against an already classified
// project. The production mapper uses wslproject.ClassifyDescendant.
type DescendantClassifier func(wslproject.Project, string) (wslproject.Descendant, error)

// MapToolArgs maps only paths proven to remain inside project and returns the
// matching container working directory. Absolute or explicitly path-shaped
// arguments outside that boundary fail closed; this primitive never invents
// an external bind mount or Windows-path equivalence.
func MapToolArgs(tool registry.Tool, project wslproject.Project, cwd, workspaceRoot string, args []string) ([]string, string, error) {
	return MapToolArgsWithClassifier(tool, project, cwd, workspaceRoot, args, wslproject.ClassifyDescendant)
}

// MapToolArgsWithClassifier composes argument mapping with a caller-owned
// coherent project resolver. It exists for cross-package integration corpora;
// normal runtime code should use MapToolArgs.
func MapToolArgsWithClassifier(tool registry.Tool, project wslproject.Project, cwd, workspaceRoot string, args []string, classify DescendantClassifier) ([]string, string, error) {
	return mapToolArgs(tool, project, cwd, workspaceRoot, args, dependencies{classify: classify})
}

func mapToolArgs(tool registry.Tool, project wslproject.Project, cwd, workspaceRoot string, args []string, deps dependencies) ([]string, string, error) {
	if tool.Name == "" {
		return nil, "", errors.New("native WSL argument mapping requires a named tool")
	}
	if deps.classify == nil {
		return nil, "", errors.New("native WSL argument mapping dependencies are incomplete")
	}
	if err := validateContainerPath(workspaceRoot); err != nil {
		return nil, "", fmt.Errorf("invalid native WSL workspace root: %w", err)
	}
	cwdDescendant, err := deps.classify(project, cwd)
	if err != nil {
		return nil, "", fmt.Errorf("validate native WSL working directory: %w", err)
	}
	containerWorkingDirectory := workspaceRoot
	if cwdDescendant.Relative != "." {
		containerWorkingDirectory = path.Join(workspaceRoot, cwdDescendant.Relative)
	}

	mapped := append([]string(nil), args...)
	forceNext := false
	forcedOptions := true
	for index, arg := range args {
		if forceNext {
			value, err := mapArg(project, cwd, workspaceRoot, arg, true, deps)
			if err != nil {
				return nil, "", err
			}
			mapped[index] = value
			forceNext = false
			continue
		}
		if arg == "--" {
			forcedOptions = false
			continue
		}
		if forcedOptions && contains(tool.PathNext, arg) {
			forceNext = true
			continue
		}

		equalsMapped := false
		for _, option := range tool.PathEquals {
			prefix := option + "="
			if forcedOptions && strings.HasPrefix(arg, prefix) {
				value, err := mapArg(project, cwd, workspaceRoot, strings.TrimPrefix(arg, prefix), true, deps)
				if err != nil {
					return nil, "", err
				}
				mapped[index] = prefix + value
				equalsMapped = true
				break
			}
		}
		if equalsMapped {
			continue
		}

		lastEnabled := tool.PathLast && (len(tool.PathLastIfAny) == 0 || anyPresent(args, tool.PathLastIfAny))
		forceLast := forcedOptions && lastEnabled && index == len(args)-1 && arg != "-" && !strings.HasPrefix(arg, "-")
		value, err := mapArg(project, cwd, workspaceRoot, arg, forceLast, deps)
		if err != nil {
			return nil, "", err
		}
		mapped[index] = value
	}
	if forceNext {
		return nil, "", fmt.Errorf("tool %q: option %q requires a path argument", tool.Name, args[len(args)-1])
	}
	return mapped, containerWorkingDirectory, nil
}

func mapArg(project wslproject.Project, cwd, workspaceRoot, arg string, force bool, deps dependencies) (string, error) {
	if isWindowsAbsolutePath(arg) {
		return "", fmt.Errorf("map native WSL argument path %q: Windows path spelling is not supported", arg)
	}
	candidate, isPath := resolvePathArg(cwd, arg, force)
	if !isPath {
		return arg, nil
	}
	descendant, err := deps.classify(project, candidate)
	if err != nil {
		return "", fmt.Errorf("map native WSL argument path %q: %w", arg, err)
	}
	if descendant.Relative == "." {
		return workspaceRoot, nil
	}
	return path.Join(workspaceRoot, descendant.Relative), nil
}

func isWindowsAbsolutePath(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	if len(value) < 3 {
		return false
	}
	drive := value[0]
	isLetter := drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z'
	return isLetter && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func resolvePathArg(cwd, arg string, force bool) (string, bool) {
	if hasPackagePatternSuffix(arg) && !path.IsAbs(arg) {
		return "", false
	}
	if path.IsAbs(arg) {
		return path.Clean(arg), true
	}
	explicit := strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../")
	if explicit || (force && arg != "" && !strings.HasPrefix(arg, "-")) {
		return path.Clean(path.Join(cwd, arg)), true
	}
	return "", false
}

func validateContainerPath(value string) error {
	if value == "" || !utf8.ValidString(value) || !path.IsAbs(value) || path.Clean(value) != value || value == "/" || strings.ContainsRune(value, '\\') {
		return errors.New("workspace root must be a canonical absolute non-root Linux path")
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return errors.New("workspace root contains a control character")
		}
	}
	return nil
}

func hasPackagePatternSuffix(value string) bool {
	if !strings.HasSuffix(value, "...") {
		return false
	}
	prefix := value[:len(value)-3]
	return prefix == "" || strings.HasSuffix(prefix, "/")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func anyPresent(args, values []string) bool {
	for _, arg := range args {
		if contains(values, arg) {
			return true
		}
	}
	return false
}
