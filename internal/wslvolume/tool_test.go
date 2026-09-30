package wslvolume

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
)

func TestPlanStatefulToolVolumesBuildsExactProjectAndSharedBindings(t *testing.T) {
	project := testClassifiedProject()
	proofCalls := 0
	deps := toolDependencies{
		classifyDescendant: func(got wslproject.Project, candidate string) (wslproject.Descendant, error) {
			proofCalls++
			if got.Root != project.Root || candidate != project.Root {
				t.Fatalf("project proof = (%+v, %q)", got, candidate)
			}
			return exactProjectRoot(project), nil
		},
	}
	tool := registry.Tool{
		Provider:       "stateful",
		StateGroup:     "node24",
		ProjectVolumes: []string{"node-modules:/workspace/node_modules", "project-cache:/var/project-cache"},
		SharedVolumes:  []string{"npm-cache:/root/.npm"},
	}
	bindings, err := planStatefulToolVolumes(testScope(t), tool, project, "/workspace/Project", deps)
	if err != nil {
		t.Fatal(err)
	}
	if proofCalls != 1 || len(bindings) != 3 {
		t.Fatalf("proofCalls=%d bindings=%d", proofCalls, len(bindings))
	}
	wantDestinations := []string{"/workspace/Project/node_modules", "/var/project-cache", "/root/.npm"}
	for index, want := range wantDestinations {
		if bindings[index].Destination() != want {
			t.Errorf("binding %d destination = %q, want %q", index, bindings[index].Destination(), want)
		}
	}
	projectLabels := bindings[0].Volume().Labels()
	if projectLabels["cb.kind"] != "project" || projectLabels["cb.project_path"] != project.Root || projectLabels[NamespaceLabel] != testNamespace {
		t.Fatalf("project labels = %#v", projectLabels)
	}
	sharedLabels := bindings[2].Volume().Labels()
	if sharedLabels["cb.kind"] != "shared" || sharedLabels["cb.owner"] != "node24/npm-cache" || sharedLabels[NamespaceLabel] != testNamespace {
		t.Fatalf("shared labels = %#v", sharedLabels)
	}
}

func TestPlanStatefulToolVolumesAllowsSharedOnlyIsolatedProfile(t *testing.T) {
	called := false
	bindings, err := planStatefulToolVolumes(testScope(t), registry.Tool{
		Provider: "stateful", StateGroup: "go124", CwdMode: "isolated",
		SharedVolumes: []string{"gomodcache:/go/pkg/mod"},
	}, wslproject.Project{}, "", toolDependencies{
		classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) {
			called = true
			return wslproject.Descendant{}, errors.New("unexpected project proof")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || len(bindings) != 1 || bindings[0].Destination() != "/go/pkg/mod" {
		t.Fatalf("shared-only plan = %#v, proofCalled=%v", bindings, called)
	}
}

func TestPlanStatefulToolVolumesRejectsInexactProjectProof(t *testing.T) {
	project := testClassifiedProject()
	for name, descendant := range map[string]wslproject.Descendant{
		"missing":        {Path: project.Root, Relative: ".", Exists: false, NearestExisting: "/home/alice"},
		"wrong relative": {Path: project.Root, Relative: "child", Exists: true, NearestExisting: project.Root},
		"wrong path":     {Path: project.Root + "/child", Relative: ".", Exists: true, NearestExisting: project.Root},
		"wrong ancestor": {Path: project.Root, Relative: ".", Exists: true, NearestExisting: "/home/alice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := planStatefulToolVolumes(testScope(t), testStatefulProjectTool(), project, "/workspace/project", toolDependencies{
				classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) {
					return descendant, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), "exact existing project root") {
				t.Fatalf("inexact-proof error = %v", err)
			}
		})
	}
	if _, err := planStatefulToolVolumes(testScope(t), testStatefulProjectTool(), project, "/workspace/project", toolDependencies{
		classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) {
			return wslproject.Descendant{}, errors.New("crosses mount boundary")
		},
	}); err == nil || !strings.Contains(err.Error(), "crosses mount boundary") {
		t.Fatalf("boundary-proof error = %v", err)
	}
}

func TestEnsureStatefulToolVolumesPlansBeforeMutationAndDeduplicatesIdentity(t *testing.T) {
	project := testClassifiedProject()
	tool := registry.Tool{
		Provider:      "stateful",
		StateGroup:    "go124",
		SharedVolumes: []string{"cache:/one", "cache:/two", "bin:/three"},
	}
	var ensured []string
	deps := toolDependencies{
		ensure: func(_ context.Context, volume Volume) error {
			ensured = append(ensured, volume.Name())
			return nil
		},
	}
	bindings, err := ensureStatefulToolVolumes(context.Background(), testScope(t), tool, project, "", deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 3 || len(ensured) != 2 || ensured[0] != bindings[0].Volume().Name() || ensured[1] != bindings[2].Volume().Name() {
		t.Fatalf("bindings=%#v ensured=%q", bindings, ensured)
	}

	ensured = nil
	tool.SharedVolumes = append(tool.SharedVolumes, "bad")
	if _, err := ensureStatefulToolVolumes(context.Background(), testScope(t), tool, project, "", deps); err == nil {
		t.Fatal("invalid complete plan was accepted")
	}
	if len(ensured) != 0 {
		t.Fatalf("ensure ran before complete plan validation: %q", ensured)
	}
}

func TestEnsureStatefulToolVolumesRejectsTargetCollisionBeforeMutation(t *testing.T) {
	ensured := false
	tool := registry.Tool{
		Provider:      "stateful",
		StateGroup:    "go124",
		SharedVolumes: []string{"cache:/state", "bin:/state"},
	}
	_, err := ensureStatefulToolVolumes(context.Background(), testScope(t), tool, wslproject.Project{}, "", toolDependencies{
		ensure: func(context.Context, Volume) error {
			ensured = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "collides") || ensured {
		t.Fatalf("target-collision result = (%v, ensured=%v)", err, ensured)
	}
}

func TestEnsureStatefulToolVolumesStopsOnLifecycleFailure(t *testing.T) {
	tool := registry.Tool{Provider: "stateful", StateGroup: "go124", SharedVolumes: []string{"cache:/one", "bin:/two"}}
	calls := 0
	_, err := ensureStatefulToolVolumes(context.Background(), testScope(t), tool, wslproject.Project{}, "", toolDependencies{
		ensure: func(_ context.Context, volume Volume) error {
			calls++
			if calls == 2 {
				return errors.New("engine unavailable")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "engine unavailable") || calls != 2 {
		t.Fatalf("ensure failure = %v, calls=%d", err, calls)
	}
}

func TestPlanStatefulToolVolumesRejectsInvalidInputs(t *testing.T) {
	project := testClassifiedProject()
	exactDeps := toolDependencies{
		classifyDescendant: func(wslproject.Project, string) (wslproject.Descendant, error) {
			return exactProjectRoot(project), nil
		},
	}
	for name, test := range map[string]struct {
		scope     Scope
		tool      registry.Tool
		workspace string
	}{
		"zero scope":      {tool: testStatefulProjectTool(), workspace: "/workspace/project"},
		"wrong provider":  {scope: testScope(t), tool: registry.Tool{Provider: "stateless", StateGroup: "demo", SharedVolumes: []string{"cache:/cache"}}},
		"missing group":   {scope: testScope(t), tool: registry.Tool{Provider: "stateful", SharedVolumes: []string{"cache:/cache"}}},
		"missing volumes": {scope: testScope(t), tool: registry.Tool{Provider: "stateful", StateGroup: "demo"}},
		"bad workspace":   {scope: testScope(t), tool: testStatefulProjectTool(), workspace: "/"},
		"isolated project": {
			scope: testScope(t),
			tool:  registry.Tool{Provider: "stateful", StateGroup: "demo", CwdMode: "isolated", ProjectVolumes: []string{"cache:/cache"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planStatefulToolVolumes(test.scope, test.tool, project, test.workspace, exactDeps); err == nil {
				t.Fatal("planStatefulToolVolumes() succeeded")
			}
		})
	}
	if _, err := ensureStatefulToolVolumes(nil, testScope(t), registry.Tool{Provider: "stateful", StateGroup: "demo", SharedVolumes: []string{"cache:/cache"}}, project, "", toolDependencies{
		ensure: func(context.Context, Volume) error { return nil },
	}); err == nil {
		t.Fatal("ensureStatefulToolVolumes() accepted nil context")
	}
}

func TestBindingAccessorsDoNotExposeMutableLabels(t *testing.T) {
	volume, err := testScope(t).Shared("go124", "cache")
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{volume: volume, destination: "/cache"}
	labels := binding.Volume().Labels()
	labels["cb.owner"] = "mutated"
	if reflect.DeepEqual(labels, binding.Volume().Labels()) {
		t.Fatal("Binding exposed mutable volume labels")
	}
}

func testClassifiedProject() wslproject.Project {
	return wslproject.Project{Root: "/home/alice/Project", Storage: wslproject.Distribution, MountPoint: "/"}
}

func exactProjectRoot(project wslproject.Project) wslproject.Descendant {
	return wslproject.Descendant{Path: project.Root, Relative: ".", Exists: true, NearestExisting: project.Root}
}

func testStatefulProjectTool() registry.Tool {
	return registry.Tool{Provider: "stateful", StateGroup: "node24", ProjectVolumes: []string{"modules:/workspace/node_modules"}}
}
