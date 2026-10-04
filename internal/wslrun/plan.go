// Package wslrun composes the native WSL2 tool frontend from the proof-bound
// project, volume and Docker Engine primitives. It never consults Docker CLI
// configuration or translates Windows paths.
package wslrun

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
	"github.com/AviBackToBlack/container-bin/internal/wslpathmap"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

const (
	projectWorkspace  = "/workspace/project"
	isolatedWorkspace = "/root"
	pythonStateGroup  = "python313"
	pythonBootstrap   = `if [ ! -x /venv/bin/python ]; then python -m venv /venv || exit $?; fi; if [ "$1" = "__CB_PIP__" ]; then shift; exec /venv/bin/python -m pip "$@"; else exec /venv/bin/python "$@"; fi`
)

type toolPlan struct {
	layout  hostenv.WSLLayout
	spec    wsldocker.ContainerCreateSpec
	volumes []wslvolume.Volume
}

type planDependencies struct {
	resolveImage       func(registry.Tool, policy.Policy, string) (string, error)
	selectProject      func(string, registry.Tool) (wslproject.Project, bool, error)
	classifyDescendant func(wslproject.Project, string) (wslproject.Descendant, error)
	mapArgs            func(registry.Tool, wslproject.Project, string, string, []string) ([]string, string, error)
	planVolumes        func(wslvolume.Scope, registry.Tool, wslproject.Project, string) ([]wslvolume.Binding, error)
}

func buildToolPlan(tool registry.Tool, userArgs []string, machinePolicy policy.Policy, layout hostenv.WSLLayout, cwd string, tty bool, environ []string, deps planDependencies) (toolPlan, error) {
	if deps.resolveImage == nil || deps.selectProject == nil || deps.classifyDescendant == nil || deps.mapArgs == nil || deps.planVolumes == nil {
		return toolPlan{}, errors.New("native WSL runtime planning dependencies are incomplete")
	}
	if len(tool.HostMounts) != 0 {
		return toolPlan{}, errors.New("native WSL profiles cannot use Windows host_mounts; use project paths or managed volumes")
	}
	scope, err := wslvolume.New(layout)
	if err != nil {
		return toolPlan{}, err
	}
	image, err := deps.resolveImage(tool, machinePolicy, layout.LockPath)
	if err != nil {
		return toolPlan{}, err
	}
	environment, err := selectedEnvironment(tool, environ)
	if err != nil {
		return toolPlan{}, err
	}

	plan := toolPlan{layout: layout, spec: wsldocker.ContainerCreateSpec{
		Tool: tool.Name, Namespace: scope.Namespace(), Image: image,
		TTY: tty, Environment: environment, RetainUntilCleanup: true,
	}}
	var (
		project wslproject.Project
		found   bool
		mapped  []string
	)
	if tool.CwdMode == "isolated" {
		plan.spec.WorkingDirectory = isolatedWorkspace
		mapped = append([]string(nil), userArgs...)
	} else {
		project, found, err = deps.selectProject(cwd, tool)
		if err != nil {
			return toolPlan{}, err
		}
		mapped, plan.spec.WorkingDirectory, err = deps.mapArgs(tool, project, cwd, projectWorkspace, userArgs)
		if err != nil {
			return toolPlan{}, err
		}
		plan.spec.Mounts = append(plan.spec.Mounts, wsldocker.ContainerMount{
			Type: "bind", Source: project.Root, Target: projectWorkspace,
		})
	}

	appendVolume := func(volume wslvolume.Volume, destination string) error {
		destination = path.Clean(destination)
		for _, mount := range plan.spec.Mounts {
			if mount.Target == destination {
				return fmt.Errorf("native WSL mount target %s is declared more than once", destination)
			}
		}
		plan.volumes = append(plan.volumes, volume)
		plan.spec.Mounts = append(plan.spec.Mounts, wsldocker.ContainerMount{
			Type: "volume", Source: volume.Name(), Target: destination, VolumeLabels: volume.Labels(),
		})
		return nil
	}

	switch tool.Provider {
	case "stateless":
		plan.spec.Command = appendCommand(tool.Command, tool.ArgsPrefix, mapped)
	case "stateful":
		bindings, err := deps.planVolumes(scope, tool, project, projectWorkspace)
		if err != nil {
			return toolPlan{}, err
		}
		for _, binding := range bindings {
			if err := appendVolume(binding.Volume(), binding.Destination()); err != nil {
				return toolPlan{}, err
			}
		}
		plan.spec.Command = appendCommand(tool.Command, tool.ArgsPrefix, mapped)
	case "python":
		if tool.CwdMode == "isolated" {
			return toolPlan{}, errors.New("native WSL Python provider cannot use isolated cwd mode")
		}
		var venv wslvolume.Volume
		if found {
			root, proofErr := deps.classifyDescendant(project, project.Root)
			if proofErr != nil {
				return toolPlan{}, fmt.Errorf("prove native WSL Python project root: %w", proofErr)
			}
			if !root.Exists || root.Path != project.Root || root.Relative != "." || root.NearestExisting != project.Root {
				return toolPlan{}, errors.New("native WSL Python project proof did not identify the exact existing root")
			}
			venv, err = scope.Project(pythonStateGroup, "venv", project.Root)
		} else {
			venv, err = scope.Shared(pythonStateGroup, "compat-venv")
		}
		if err != nil {
			return toolPlan{}, err
		}
		pipCache, err := scope.Shared(pythonStateGroup, "pip-cache")
		if err != nil {
			return toolPlan{}, err
		}
		if err := appendVolume(venv, "/venv"); err != nil {
			return toolPlan{}, err
		}
		if err := appendVolume(pipCache, "/root/.cache/pip"); err != nil {
			return toolPlan{}, err
		}
		plan.spec.Environment, err = mergeLiteralEnvironment(plan.spec.Environment,
			[]string{"VIRTUAL_ENV=/venv", "PATH=/venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"})
		if err != nil {
			return toolPlan{}, err
		}
		plan.spec.Command = []string{"sh", "-c", pythonBootstrap, "cb"}
		if tool.Role == "pip" {
			plan.spec.Command = append(plan.spec.Command, "__CB_PIP__")
		}
		plan.spec.Command = append(plan.spec.Command, tool.ArgsPrefix...)
		plan.spec.Command = append(plan.spec.Command, mapped...)
	default:
		return toolPlan{}, fmt.Errorf("unsupported native WSL provider %q", tool.Provider)
	}
	if err := wsldocker.ValidateContainerCreateSpec(plan.spec); err != nil {
		return toolPlan{}, fmt.Errorf("validate native WSL container plan: %w", err)
	}
	return plan, nil
}

func appendCommand(parts ...[]string) []string {
	var result []string
	for _, part := range parts {
		result = append(result, part...)
	}
	return result
}

func selectedEnvironment(tool registry.Tool, environ []string) ([]string, error) {
	literal := make(map[string]string, len(tool.EnvSet))
	for _, assignment := range tool.EnvSet {
		name, _, ok := strings.Cut(assignment, "=")
		if !ok || !validEnvironmentName(name) || !utf8.ValidString(assignment) || strings.ContainsRune(assignment, '\x00') {
			return nil, fmt.Errorf("invalid literal environment assignment %q", assignment)
		}
		if _, duplicate := literal[name]; duplicate {
			return nil, fmt.Errorf("duplicate literal environment assignment for %s", name)
		}
		literal[name] = assignment
	}
	exact := make(map[string]bool, len(tool.EnvNames))
	for _, name := range tool.EnvNames {
		exact[name] = true
	}
	selected := make(map[string]string)
	for _, assignment := range environ {
		name, _, ok := strings.Cut(assignment, "=")
		if !ok || !validEnvironmentName(name) || !utf8.ValidString(assignment) || strings.ContainsRune(assignment, '\x00') {
			continue
		}
		match := exact[name]
		if !match {
			for _, prefix := range tool.EnvPrefixes {
				if strings.HasPrefix(name, prefix) {
					match = true
					break
				}
			}
		}
		if match {
			if _, duplicate := selected[name]; duplicate {
				return nil, fmt.Errorf("host environment contains duplicate selected variable %s", name)
			}
			selected[name] = assignment
		}
	}
	for name, assignment := range literal {
		selected[name] = assignment
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, selected[name])
	}
	return result, nil
}

func mergeLiteralEnvironment(current, additions []string) ([]string, error) {
	byName := make(map[string]string, len(current)+len(additions))
	for _, assignment := range append(append([]string(nil), current...), additions...) {
		name, _, ok := strings.Cut(assignment, "=")
		if !ok || !validEnvironmentName(name) {
			return nil, fmt.Errorf("invalid environment assignment %q", assignment)
		}
		byName[name] = assignment
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, byName[name])
	}
	return result, nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func productionPlanDependencies() planDependencies {
	return planDependencies{
		resolveImage:       lockfile.RuntimeImageForToolAt,
		selectProject:      wslproject.SelectForTool,
		classifyDescendant: wslproject.ClassifyDescendant,
		mapArgs:            wslpathmap.MapToolArgs,
		planVolumes:        wslvolume.PlanStatefulToolVolumes,
	}
}
