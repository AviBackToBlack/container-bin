package wslvolume

import (
	"errors"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

func TestPlanPythonVolumesUsesProjectOrNamespaceCompatibilityIdentity(t *testing.T) {
	project := testClassifiedProject()
	projectState, err := planPythonVolumes(testScope(t), project, true, pythonDependencies{
		classifyDescendant: func(got wslproject.Project, candidate string) (wslproject.Descendant, error) {
			if got.Root != project.Root || candidate != project.Root {
				t.Fatalf("project proof = (%+v, %q)", got, candidate)
			}
			return exactProjectRoot(project), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if labels := projectState.Venv.Labels(); labels["cb.kind"] != "project" || labels["cb.project_path"] != project.Root || labels["cb.owner"] != PythonStateGroup+"/venv" {
		t.Fatalf("project venv labels = %#v", labels)
	}
	if labels := projectState.PipCache.Labels(); labels["cb.kind"] != "shared" || labels["cb.owner"] != PythonStateGroup+"/pip-cache" {
		t.Fatalf("project pip-cache labels = %#v", labels)
	}

	called := false
	compat, err := planPythonVolumes(testScope(t), project, false, pythonDependencies{
		classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) {
			called = true
			return wslproject.Descendant{}, errors.New("unexpected proof")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("compatibility planning attempted project proof")
	}
	if labels := compat.Venv.Labels(); labels["cb.kind"] != "shared" || labels["cb.owner"] != PythonStateGroup+"/compat-venv" {
		t.Fatalf("compatibility venv labels = %#v", labels)
	}
}

func TestPlanPythonVolumesRejectsInvalidProjectProofBeforeReturningState(t *testing.T) {
	project := testClassifiedProject()
	for name, descendant := range map[string]wslproject.Descendant{
		"missing":        {Path: project.Root, Relative: ".", NearestExisting: "/home/alice"},
		"wrong path":     {Path: project.Root + "/child", Relative: ".", Exists: true, NearestExisting: project.Root},
		"wrong relative": {Path: project.Root, Relative: "child", Exists: true, NearestExisting: project.Root},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := planPythonVolumes(testScope(t), project, true, pythonDependencies{
				classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) { return descendant, nil },
			})
			if err == nil || !strings.Contains(err.Error(), "exact existing root") {
				t.Fatalf("inexact proof error = %v", err)
			}
		})
	}
	if _, err := planPythonVolumes(testScope(t), project, true, pythonDependencies{
		classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) {
			return wslproject.Descendant{}, errors.New("mount changed")
		},
	}); err == nil || !strings.Contains(err.Error(), "mount changed") {
		t.Fatalf("proof failure = %v", err)
	}
	if _, err := planPythonVolumes(Scope{}, project, false, pythonDependencies{}); err == nil {
		t.Fatal("zero scope was accepted")
	}
}
