package wslvolume

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

// Binding is one exact namespaced volume identity and its container target.
// Its fields stay immutable so planning output cannot weaken ownership proof.
type Binding struct {
	volume      Volume
	destination string
}

func (b Binding) Volume() Volume      { return b.volume }
func (b Binding) Destination() string { return b.destination }

type toolDependencies struct {
	classifyDescendant func(wslproject.Project, string) (wslproject.Descendant, error)
	ensure             func(context.Context, Volume) error
}

// PlanStatefulToolVolumes constructs every project and shared volume binding
// for one stateful profile before any Docker mutation. Project volumes consume
// a fresh proof of the exact classified project root.
func PlanStatefulToolVolumes(scope Scope, tool registry.Tool, project wslproject.Project, workspaceRoot string) ([]Binding, error) {
	return planStatefulToolVolumes(scope, tool, project, workspaceRoot, toolDependencies{
		classifyDescendant: wslproject.ClassifyDescendant,
	})
}

// EnsureStatefulToolVolumes plans the complete binding set first, then ensures
// each distinct exact identity through the proof-bound Docker lifecycle.
func EnsureStatefulToolVolumes(ctx context.Context, scope Scope, tool registry.Tool, project wslproject.Project, workspaceRoot string) ([]Binding, error) {
	return ensureStatefulToolVolumes(ctx, scope, tool, project, workspaceRoot, toolDependencies{
		classifyDescendant: wslproject.ClassifyDescendant,
		ensure:             Ensure,
	})
}

func planStatefulToolVolumes(scope Scope, tool registry.Tool, project wslproject.Project, workspaceRoot string, deps toolDependencies) ([]Binding, error) {
	if !validNamespace(scope.namespace) || scope.prefix != "cb-"+scope.namespace+"-" {
		return nil, errors.New("native WSL tool-volume planning requires a valid scope")
	}
	if tool.Provider != "stateful" {
		return nil, fmt.Errorf("native WSL tool-volume planning requires provider %q, got %q", "stateful", tool.Provider)
	}
	if tool.StateGroup == "" {
		return nil, errors.New("native WSL stateful tool requires a state group")
	}
	if len(tool.ProjectVolumes) == 0 && len(tool.SharedVolumes) == 0 {
		return nil, errors.New("native WSL stateful tool requires at least one volume")
	}

	if len(tool.ProjectVolumes) > 0 {
		if tool.CwdMode == "isolated" {
			return nil, errors.New("native WSL isolated tool cannot declare project volumes")
		}
		if deps.classifyDescendant == nil {
			return nil, errors.New("native WSL project-volume proof is unavailable")
		}
		if err := validateProjectRoot(workspaceRoot); err != nil {
			return nil, fmt.Errorf("invalid native WSL container workspace root: %w", err)
		}
		root, err := deps.classifyDescendant(project, project.Root)
		if err != nil {
			return nil, fmt.Errorf("prove native WSL project root for tool volumes: %w", err)
		}
		if !root.Exists || root.Path != project.Root || root.Relative != "." || root.NearestExisting != project.Root {
			return nil, errors.New("native WSL project-volume proof did not identify the exact existing project root")
		}
	}

	bindings := make([]Binding, 0, len(tool.ProjectVolumes)+len(tool.SharedVolumes))
	destinations := make(map[string]string, cap(bindings))
	appendBinding := func(volume Volume, destination string) error {
		if prior, ok := destinations[destination]; ok {
			return fmt.Errorf("native WSL tool volume %s target %s collides with volume %s", volume.Name(), destination, prior)
		}
		destinations[destination] = volume.Name()
		bindings = append(bindings, Binding{volume: volume, destination: destination})
		return nil
	}
	for _, spec := range tool.ProjectVolumes {
		logical, destination, err := registry.ParseVolumeBinding(spec)
		if err != nil {
			return nil, fmt.Errorf("native WSL project volume %q: %w", spec, err)
		}
		volume, err := scope.Project(tool.StateGroup, logical, project.Root)
		if err != nil {
			return nil, fmt.Errorf("construct native WSL project volume %q: %w", logical, err)
		}
		if err := appendBinding(volume, workspaceDestination(destination, workspaceRoot)); err != nil {
			return nil, err
		}
	}
	for _, spec := range tool.SharedVolumes {
		logical, destination, err := registry.ParseVolumeBinding(spec)
		if err != nil {
			return nil, fmt.Errorf("native WSL shared volume %q: %w", spec, err)
		}
		volume, err := scope.Shared(tool.StateGroup, logical)
		if err != nil {
			return nil, fmt.Errorf("construct native WSL shared volume %q: %w", logical, err)
		}
		if err := appendBinding(volume, path.Clean(destination)); err != nil {
			return nil, err
		}
	}
	return bindings, nil
}

func ensureStatefulToolVolumes(ctx context.Context, scope Scope, tool registry.Tool, project wslproject.Project, workspaceRoot string, deps toolDependencies) ([]Binding, error) {
	if ctx == nil {
		return nil, errors.New("native WSL tool-volume ensure requires a context")
	}
	if deps.ensure == nil {
		return nil, errors.New("native WSL tool-volume ensure is unavailable")
	}
	bindings, err := planStatefulToolVolumes(scope, tool, project, workspaceRoot, deps)
	if err != nil {
		return nil, err
	}
	ensured := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		name := binding.volume.Name()
		if ensured[name] {
			continue
		}
		if err := deps.ensure(ctx, binding.volume); err != nil {
			return nil, fmt.Errorf("ensure native WSL tool volume %s: %w", name, err)
		}
		ensured[name] = true
	}
	return bindings, nil
}

func workspaceDestination(destination, workspaceRoot string) string {
	destination = path.Clean(destination)
	if destination == "/workspace" {
		return workspaceRoot
	}
	if strings.HasPrefix(destination, "/workspace/") {
		return workspaceRoot + strings.TrimPrefix(destination, "/workspace")
	}
	return destination
}
