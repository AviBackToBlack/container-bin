package wslrun

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

const testNamespace = "wsl2-0123456789abcdef0123456789abcdef"

func TestBuildToolPlanWiresProjectMappingEnvironmentAndCommand(t *testing.T) {
	deps := testPlanDependencies(true)
	tool := registry.Tool{
		Name: "demo", Image: "demo:1", Provider: "stateless", Command: []string{"demo"}, ArgsPrefix: []string{"--fixed"},
		EnvNames: []string{"KEEP"}, EnvPrefixes: []string{"APP_"}, EnvSet: []string{"APP_MODE=fixed"},
	}
	plan, err := buildToolPlan(tool, []string{"input.txt"}, policy.Policy{}, testLayout(), "/project/sub", false,
		[]string{"DROP=no", "APP_MODE=host", "APP_TOKEN=secret", "KEEP=yes"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if plan.spec.Image != "demo@sha256:locked" || plan.spec.WorkingDirectory != "/workspace/project/sub" {
		t.Fatalf("plan identity = image %q cwd %q", plan.spec.Image, plan.spec.WorkingDirectory)
	}
	if !plan.spec.RetainUntilCleanup {
		t.Fatal("runtime plan did not retain the container for exit-status collection")
	}
	if got, want := plan.spec.Command, []string{"demo", "--fixed", "/workspace/project/input.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
	if got, want := plan.spec.Environment, []string{"APP_MODE=fixed", "APP_TOKEN=secret", "KEEP=yes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
	if len(plan.spec.Mounts) != 1 || plan.spec.Mounts[0].Source != "/project" || plan.spec.Mounts[0].Target != projectWorkspace {
		t.Fatalf("project mount = %#v", plan.spec.Mounts)
	}
	if len(plan.volumes) != 0 {
		t.Fatalf("stateless plan has volumes %#v", plan.volumes)
	}
}

func TestBuildToolPlanWiresStatefulSharedVolumesInIsolatedMode(t *testing.T) {
	deps := testPlanDependencies(false)
	deps.planVolumes = wslvolume.PlanStatefulToolVolumes
	tool := registry.Tool{
		Name: "demo", Image: "demo:1", Provider: "stateful", CwdMode: "isolated",
		StateGroup: "demo", SharedVolumes: []string{"cache:/cb/cache"}, Command: []string{"demo"},
	}
	plan, err := buildToolPlan(tool, []string{"arg"}, policy.Policy{}, testLayout(), "/ignored", false, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if plan.spec.WorkingDirectory != isolatedWorkspace || len(plan.spec.Mounts) != 1 || plan.spec.Mounts[0].Type != "volume" {
		t.Fatalf("isolated stateful plan = %+v", plan.spec)
	}
	if len(plan.volumes) != 1 || plan.spec.Mounts[0].Source != plan.volumes[0].Name() || plan.spec.Mounts[0].Target != "/cb/cache" {
		t.Fatalf("isolated volume identity was not preserved: mounts=%#v volumes=%#v", plan.spec.Mounts, plan.volumes)
	}
}

func TestBuildToolPlanPreservesPythonProjectAndCompatibilityState(t *testing.T) {
	for _, tc := range []struct {
		name         string
		found        bool
		wantKind     string
		wantNamePart string
	}{
		{name: "project", found: true, wantKind: "project", wantNamePart: "9-python313-4-venv-"},
		{name: "compatibility", found: false, wantKind: "shared", wantNamePart: "9-python313-11-compat-venv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := testPlanDependencies(tc.found)
			tool := registry.Tool{Name: "pip", Image: "python:3.13-slim", Provider: "python", Role: "pip"}
			plan, err := buildToolPlan(tool, []string{"install", "demo"}, policy.Policy{}, testLayout(), "/project", false, nil, deps)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.volumes) != 2 || len(plan.spec.Mounts) != 3 {
				t.Fatalf("Python plan volumes=%d mounts=%d", len(plan.volumes), len(plan.spec.Mounts))
			}
			venv := plan.volumes[0]
			if !strings.Contains(venv.Name(), tc.wantNamePart) || venv.Labels()["cb.kind"] != tc.wantKind {
				t.Fatalf("venv identity = %q labels=%v", venv.Name(), venv.Labels())
			}
			if got := strings.Join(plan.spec.Command, "|"); !strings.Contains(got, "__CB_PIP__|install|demo") {
				t.Fatalf("pip command = %q", got)
			}
			if !containsAssignment(plan.spec.Environment, "VIRTUAL_ENV=/venv") || !containsAssignment(plan.spec.Environment, "PATH=/venv/bin:") {
				t.Fatalf("Python environment = %#v", plan.spec.Environment)
			}
		})
	}
}

func TestBuildToolPlanRejectsWindowsHostMountsBeforeResolution(t *testing.T) {
	deps := testPlanDependencies(true)
	deps.resolveImage = func(registry.Tool, policy.Policy, string) (string, error) {
		t.Fatal("image resolution ran after unsupported host_mounts")
		return "", nil
	}
	_, err := buildToolPlan(registry.Tool{
		Name: "demo", Image: "demo:1", Provider: "stateless", HostMounts: []string{`C:\\data:/data:ro`},
	}, nil, policy.Policy{}, testLayout(), "/project", false, nil, deps)
	if err == nil || !strings.Contains(err.Error(), "cannot use Windows host_mounts") {
		t.Fatalf("host_mounts error = %v", err)
	}
}

func testLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{StateNamespace: testNamespace, LockPath: "/home/alice/.config/container-bin/container-bin.lock"}
}

func testPlanDependencies(found bool) planDependencies {
	project := wslproject.Project{Root: "/project", Storage: wslproject.Distribution, MountPoint: "/"}
	return planDependencies{
		resolveImage:  func(registry.Tool, policy.Policy, string) (string, error) { return "demo@sha256:locked", nil },
		selectProject: func(string, registry.Tool) (wslproject.Project, bool, error) { return project, found, nil },
		classifyDescendant: func(_ wslproject.Project, candidate string) (wslproject.Descendant, error) {
			relative := "."
			if candidate != "/project" {
				relative = strings.TrimPrefix(candidate, "/project/")
			}
			return wslproject.Descendant{Path: candidate, Relative: relative, Exists: true, NearestExisting: candidate}, nil
		},
		mapArgs: func(_ registry.Tool, _ wslproject.Project, _ string, workspace string, args []string) ([]string, string, error) {
			mapped := append([]string(nil), args...)
			for index, argument := range mapped {
				if argument == "input.txt" {
					mapped[index] = workspace + "/input.txt"
				}
			}
			return mapped, workspace + "/sub", nil
		},
		planVolumes: func(wslvolume.Scope, registry.Tool, wslproject.Project, string) ([]wslvolume.Binding, error) {
			return nil, nil
		},
	}
}

func containsAssignment(assignments []string, prefix string) bool {
	for _, assignment := range assignments {
		if strings.HasPrefix(assignment, prefix) {
			return true
		}
	}
	return false
}
