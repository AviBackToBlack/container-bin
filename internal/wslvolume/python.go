package wslvolume

import (
	"errors"
	"fmt"

	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

const PythonStateGroup = "python313"

// PythonState is the complete native-WSL volume set used by the legacy Python
// provider. The compatibility environment and pip cache are namespace-shared;
// a marker-selected project receives an exact project-scoped environment.
type PythonState struct {
	Venv     Volume
	PipCache Volume
}

type pythonDependencies struct {
	classifyDescendant func(wslproject.Project, string) (wslproject.Descendant, error)
}

// PlanPythonVolumes constructs the same exact identities consumed by ordinary
// runtime execution without contacting Docker.
func PlanPythonVolumes(scope Scope, project wslproject.Project, found bool) (PythonState, error) {
	return planPythonVolumes(scope, project, found, pythonDependencies{
		classifyDescendant: wslproject.ClassifyDescendant,
	})
}

func planPythonVolumes(scope Scope, project wslproject.Project, found bool, deps pythonDependencies) (PythonState, error) {
	if !validNamespace(scope.namespace) || scope.prefix != "cb-"+scope.namespace+"-" {
		return PythonState{}, errors.New("native WSL Python-volume planning requires a valid scope")
	}
	var (
		venv Volume
		err  error
	)
	if found {
		if deps.classifyDescendant == nil {
			return PythonState{}, errors.New("native WSL Python project-volume proof is unavailable")
		}
		root, proofErr := deps.classifyDescendant(project, project.Root)
		if proofErr != nil {
			return PythonState{}, fmt.Errorf("prove native WSL Python project root: %w", proofErr)
		}
		if !root.Exists || root.Path != project.Root || root.Relative != "." || root.NearestExisting != project.Root {
			return PythonState{}, errors.New("native WSL Python project proof did not identify the exact existing root")
		}
		venv, err = scope.Project(PythonStateGroup, "venv", project.Root)
	} else {
		venv, err = scope.Shared(PythonStateGroup, "compat-venv")
	}
	if err != nil {
		return PythonState{}, err
	}
	pipCache, err := scope.Shared(PythonStateGroup, "pip-cache")
	if err != nil {
		return PythonState{}, err
	}
	return PythonState{Venv: venv, PipCache: pipCache}, nil
}
