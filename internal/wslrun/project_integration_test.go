package wslrun

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslpathmap"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

const (
	corpusImage    = "example.com/acme/demo:2"
	corpusResolved = "example.com/acme/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestBuildToolPlanComposesProjectBoundaryLockAndVolumeIdentity(t *testing.T) {
	tests := []struct {
		name       string
		root       string
		cwd        string
		storage    wslproject.Storage
		mountPoint string
	}{
		{name: "distribution filesystem", root: "/home/alice/work/App", cwd: "/home/alice/work/App/src", storage: wslproject.Distribution, mountPoint: "/"},
		{name: "default Windows drive", root: "/mnt/c/Work/App", cwd: "/mnt/c/Work/App/src", storage: wslproject.WindowsDrive, mountPoint: "/mnt/c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := newCorpusFilesystem(tc.root, tc.cwd, tc.storage)
			resolver := newCorpusResolver(t, model)
			layout := corpusLayout(t)
			tool := corpusTool()
			deps := corpusPlanDependencies(resolver)

			plan, err := buildToolPlan(tool,
				[]string{"--input", "input.txt", "--output=generated/out.json"},
				policy.Policy{}, layout, tc.cwd, false, nil, deps)
			if err != nil {
				t.Fatal(err)
			}
			if plan.spec.Image != corpusResolved {
				t.Fatalf("resolved image = %q, want %q", plan.spec.Image, corpusResolved)
			}
			if plan.spec.WorkingDirectory != "/workspace/project/src" {
				t.Fatalf("container cwd = %q", plan.spec.WorkingDirectory)
			}
			wantCommand := []string{"demo", "--input", "/workspace/project/src/input.txt", "--output=/workspace/project/src/generated/out.json"}
			if !reflect.DeepEqual(plan.spec.Command, wantCommand) {
				t.Fatalf("command = %#v, want %#v", plan.spec.Command, wantCommand)
			}
			if len(plan.spec.Mounts) != 3 || plan.spec.Mounts[0].Type != "bind" || plan.spec.Mounts[0].Source != tc.root || plan.spec.Mounts[0].Target != projectWorkspace {
				t.Fatalf("project and managed mounts = %#v", plan.spec.Mounts)
			}
			if len(plan.volumes) != 2 {
				t.Fatalf("managed volumes = %#v", plan.volumes)
			}
			projectHash, err := wslvolume.ProjectHash(tc.root)
			if err != nil {
				t.Fatal(err)
			}
			projectLabels := plan.volumes[0].Labels()
			if projectLabels["cb.kind"] != "project" || projectLabels["cb.project_path"] != tc.root || projectLabels["cb.project_hash"] != projectHash {
				t.Fatalf("project volume labels = %#v", projectLabels)
			}
			if plan.spec.Mounts[1].Source != plan.volumes[0].Name() || plan.spec.Mounts[1].Target != "/workspace/project/.cache" {
				t.Fatalf("project volume mount = %#v", plan.spec.Mounts[1])
			}
			sharedLabels := plan.volumes[1].Labels()
			if sharedLabels["cb.kind"] != "shared" || sharedLabels["cb.owner"] != "demo/cache" || plan.spec.Mounts[2].Target != "/root/.cache/demo" {
				t.Fatalf("shared volume = mount %#v labels %#v", plan.spec.Mounts[2], sharedLabels)
			}

			project, found, err := resolver.SelectForTool(tc.cwd, tool)
			if err != nil || !found || project.Root != tc.root || project.Storage != tc.storage || project.MountPoint != tc.mountPoint {
				t.Fatalf("selected project = %+v, found=%v, err=%v", project, found, err)
			}
			caseVariant := strings.Replace(tc.root, "/App", "/app", 1)
			variantHash, err := wslvolume.ProjectHash(caseVariant)
			if err != nil {
				t.Fatal(err)
			}
			if variantHash == projectHash {
				t.Fatalf("case-distinct project roots shared hash %q", projectHash)
			}
		})
	}
}

func TestBuildToolPlanRejectsMixedAndCrossBoundaryPathsBeforeVolumePlanning(t *testing.T) {
	tests := []struct {
		name      string
		storage   wslproject.Storage
		argument  string
		configure func(*corpusFilesystem)
		want      string
	}{
		{name: "distribution to Windows drive", storage: wslproject.Distribution, argument: "/mnt/c/Work/App/input.txt", want: "outside project root"},
		{name: "Windows drive to distribution", storage: wslproject.WindowsDrive, argument: "/home/alice/work/App/input.txt", want: "outside project root"},
		{name: "forced Windows spelling", storage: wslproject.Distribution, argument: `C:\Work\App\input.txt`, want: "canonical absolute non-root Linux path"},
		{name: "case mismatch", storage: wslproject.Distribution, argument: "/home/alice/work/app/input.txt", want: "outside project root"},
		{
			name: "symlink descendant", storage: wslproject.Distribution, argument: "linked/input.txt", want: "is a symlink",
			configure: func(model *corpusFilesystem) {
				model.entries[model.cwd+"/linked"] = wslproject.InspectionInfo{Mode: os.ModeSymlink, Device: model.projectDevice}
			},
		},
		{
			name: "nested mount", storage: wslproject.Distribution, argument: "nested/input.txt", want: "crosses mount boundary",
			configure: func(model *corpusFilesystem) {
				model.entries[model.cwd+"/nested"] = directoryInfo(corpusNestedDevice)
				model.entries[model.cwd+"/nested/input.txt"] = fileInfo(corpusNestedDevice)
				model.mountInfo += "26 24 0:46 / " + model.cwd + "/nested rw - tmpfs tmpfs rw\n"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, cwd := "/home/alice/work/App", "/home/alice/work/App/src"
			if tc.storage == wslproject.WindowsDrive {
				root, cwd = "/mnt/c/Work/App", "/mnt/c/Work/App/src"
			}
			model := newCorpusFilesystem(root, cwd, tc.storage)
			if tc.configure != nil {
				tc.configure(model)
			}
			resolver := newCorpusResolver(t, model)
			deps := corpusPlanDependencies(resolver)
			deps.planVolumes = func(wslvolume.Scope, registry.Tool, wslproject.Project, string) ([]wslvolume.Binding, error) {
				t.Fatal("volume planning ran after argument boundary rejection")
				return nil, nil
			}
			_, err := buildToolPlan(corpusTool(), []string{"--input", tc.argument}, policy.Policy{}, corpusLayout(t), cwd, false, nil, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("boundary error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

type corpusFilesystem struct {
	root          string
	cwd           string
	projectDevice uint64
	entries       map[string]wslproject.InspectionInfo
	mountInfo     string
}

const (
	corpusDistributionDevice uint64 = 2049 // Linux dev_t encoding for 8:1.
	corpusWindowsDevice      uint64 = 45   // Linux dev_t encoding for 0:45.
	corpusNestedDevice       uint64 = 46   // Linux dev_t encoding for 0:46.
)

func newCorpusFilesystem(root, cwd string, storage wslproject.Storage) *corpusFilesystem {
	device := corpusDistributionDevice
	mountInfo := "24 1 8:1 / / rw - ext4 /dev/sdb rw\n"
	if storage == wslproject.WindowsDrive {
		device = corpusWindowsDevice
		mountInfo += "25 24 0:45 / /mnt/c rw - 9p C: rw,aname=drvfs;path=C:\n"
	}
	entries := map[string]wslproject.InspectionInfo{
		"/":                directoryInfo(corpusDistributionDevice),
		root:               directoryInfo(device),
		cwd:                directoryInfo(device),
		root + "/.git":     directoryInfo(device),
		cwd + "/input.txt": fileInfo(device),
		cwd + "/generated": directoryInfo(device),
	}
	return &corpusFilesystem{root: root, cwd: cwd, projectDevice: device, entries: entries, mountInfo: mountInfo}
}

func newCorpusResolver(t *testing.T, model *corpusFilesystem) wslproject.Resolver {
	t.Helper()
	resolver, err := wslproject.NewResolver(wslproject.Inspection{
		CurrentRuntime: func() (hostenv.Runtime, error) {
			return hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}, nil
		},
		Lstat: func(path string) (wslproject.InspectionInfo, error) {
			info, ok := model.entries[path]
			if !ok {
				return wslproject.InspectionInfo{}, fs.ErrNotExist
			}
			return info, nil
		},
		EvalSymlinks:  func(path string) (string, error) { return path, nil },
		ReadMountInfo: func() ([]byte, error) { return []byte(model.mountInfo), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func corpusPlanDependencies(resolver wslproject.Resolver) planDependencies {
	return planDependencies{
		resolveImage:  lockfile.RuntimeImageForToolAt,
		selectProject: resolver.SelectForTool,
		mapArgs: func(tool registry.Tool, project wslproject.Project, cwd, workspace string, args []string) ([]string, string, error) {
			return wslpathmap.MapToolArgsWithClassifier(tool, project, cwd, workspace, args, resolver.ClassifyDescendant)
		},
		planVolumes: func(scope wslvolume.Scope, tool registry.Tool, project wslproject.Project, workspace string) ([]wslvolume.Binding, error) {
			return wslvolume.PlanStatefulToolVolumesWithClassifier(scope, tool, project, workspace, resolver.ClassifyDescendant)
		},
		planPython: func(wslvolume.Scope, wslproject.Project, bool) (wslvolume.PythonState, error) {
			return wslvolume.PythonState{}, errors.New("unexpected Python volume planning")
		},
	}
}

func corpusLayout(t *testing.T) hostenv.WSLLayout {
	t.Helper()
	lockPath := filepath.Join(t.TempDir(), "container-bin.lock")
	digest := strings.TrimPrefix(corpusResolved, "example.com/acme/demo@")
	if err := lockfile.WriteMode(lockPath, &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{
		corpusImage: {Configured: corpusImage, Resolved: corpusResolved, Digest: digest},
	}}, 0o600); err != nil {
		t.Fatal(err)
	}
	return hostenv.WSLLayout{StateNamespace: testNamespace, LockPath: lockPath}
}

func corpusTool() registry.Tool {
	return registry.Tool{
		Name: "demo", Image: corpusImage, Provider: "stateful", Command: []string{"demo"},
		PathNext: []string{"--input"}, PathEquals: []string{"--output"}, ProjectMarkers: []string{".git"},
		StateGroup: "demo", ProjectVolumes: []string{"build-cache:/workspace/.cache"}, SharedVolumes: []string{"cache:/root/.cache/demo"},
	}
}

func directoryInfo(device uint64) wslproject.InspectionInfo {
	return wslproject.InspectionInfo{Mode: os.ModeDir | 0o755, Device: device}
}

func fileInfo(device uint64) wslproject.InspectionInfo {
	return wslproject.InspectionInfo{Mode: 0o644, Device: device}
}
