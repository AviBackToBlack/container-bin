package wslproject

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const rootMount = "24 1 8:1 / / rw,relatime - ext4 /dev/sdb rw\n"

func TestClassifyDistributionProjectPreservesExactCase(t *testing.T) {
	deps := validDependencies("/home/alice/Project", rootMount)
	project, err := classify("/home/alice/Project", deps)
	if err != nil {
		t.Fatal(err)
	}
	if project.Root != "/home/alice/Project" || project.Storage != Distribution || project.MountPoint != "/" || project.WindowsDrive != "" {
		t.Fatalf("classify() = %+v", project)
	}
}

func TestClassifyDefaultWindowsDriveMounts(t *testing.T) {
	for name, mount := range map[string]string{
		"9p":       rootMount + "25 24 0:45 / /mnt/c rw,noatime - 9p drvfsa rw,aname=drvfs;path=C:\\134;uid=1000\n",
		"virtiofs": rootMount + "25 24 0:45 / /mnt/c rw,noatime - virtiofs drvfsC0 rw\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := "/mnt/c/Users/Alice/Project"
			project, err := classify(root, validDependencies(root, mount))
			if err != nil {
				t.Fatal(err)
			}
			if project.Root != root || project.Storage != WindowsDrive || project.MountPoint != "/mnt/c" || project.WindowsDrive != "c" {
				t.Fatalf("classify() = %+v", project)
			}
		})
	}
}

func TestResolveDescendantAcceptsExistingAndMissingPathsOnProjectMount(t *testing.T) {
	root := "/home/alice/project"
	existing := root + "/pkg/file.go"
	missing := root + "/dist/output.bin"
	deps := validDependencies(root, rootMount)
	baseLstat := deps.lstat
	deps.lstat = func(name string) (pathInfo, error) {
		switch name {
		case existing:
			return pathInfo{Mode: 0o644, Dev: linuxDevice(8, 1)}, nil
		case missing, root + "/dist":
			return pathInfo{}, fs.ErrNotExist
		default:
			return baseLstat(name)
		}
	}
	deps.evalSymlinks = func(name string) (string, error) { return name, nil }
	project, err := classify(root, deps)
	if err != nil {
		t.Fatal(err)
	}

	got, err := resolveDescendant(project, existing, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != existing || got.Relative != "pkg/file.go" || !got.Exists || got.NearestExisting != existing {
		t.Fatalf("existing descendant = %+v", got)
	}
	got, err = resolveDescendant(project, missing, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != missing || got.Relative != "dist/output.bin" || got.Exists || got.NearestExisting != root {
		t.Fatalf("missing descendant = %+v", got)
	}
	got, err = resolveDescendant(project, root, deps)
	if err != nil || got.Relative != "." || !got.Exists {
		t.Fatalf("project root descendant = (%+v, %v)", got, err)
	}
}

func TestResolveDescendantPreservesWindowsDriveProjectIdentity(t *testing.T) {
	root := "/mnt/c/Users/Alice/Project"
	candidate := root + "/src/main.go"
	mounts := rootMount + "25 24 0:45 / /mnt/c rw - 9p drvfsa rw,aname=drvfs;path=C:\\134;uid=1000\n"
	deps := validDependencies(root, mounts)
	baseLstat := deps.lstat
	deps.lstat = func(name string) (pathInfo, error) {
		if name == candidate {
			return pathInfo{Mode: 0o644, Dev: linuxDevice(0, 45)}, nil
		}
		return baseLstat(name)
	}
	deps.evalSymlinks = func(name string) (string, error) { return name, nil }
	project, err := classify(root, deps)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveDescendant(project, candidate, deps)
	if err != nil {
		t.Fatal(err)
	}
	if project.Storage != WindowsDrive || project.WindowsDrive != "c" || got.Relative != "src/main.go" {
		t.Fatalf("Windows-drive descendant = project %+v, path %+v", project, got)
	}
}

func TestResolveDescendantRejectsSymlinkAndNestedMountEscapes(t *testing.T) {
	root := "/home/alice/project"
	candidate := root + "/vendor/pkg"
	tests := map[string]func(*dependencies){
		"final symlink": func(d *dependencies) {
			base := d.lstat
			d.lstat = func(name string) (pathInfo, error) {
				if name == candidate {
					return pathInfo{Mode: os.ModeSymlink | 0o777, Dev: linuxDevice(8, 1)}, nil
				}
				return base(name)
			}
		},
		"parent symlink": func(d *dependencies) {
			base := d.lstat
			d.lstat = func(name string) (pathInfo, error) {
				if name == candidate {
					return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, nil
				}
				return base(name)
			}
			d.evalSymlinks = func(name string) (string, error) {
				if name == candidate {
					return "/srv/vendor/pkg", nil
				}
				return name, nil
			}
		},
		"nested mount": func(d *dependencies) {
			d.readMountInfo = func() ([]byte, error) {
				return []byte(rootMount + "25 24 8:1 / /home/alice/project/vendor rw - ext4 /dev/sdb rw\n"), nil
			}
			base := d.lstat
			d.lstat = func(name string) (pathInfo, error) {
				if name == candidate {
					return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, nil
				}
				return base(name)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			baseDeps := validDependencies(root, rootMount)
			baseDeps.evalSymlinks = func(name string) (string, error) { return name, nil }
			project, err := classify(root, baseDeps)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&baseDeps)
			if _, err := resolveDescendant(project, candidate, baseDeps); err == nil {
				t.Fatal("resolveDescendant() accepted boundary escape")
			}
		})
	}
}

func TestResolveDescendantRejectsInvalidInputsAndChangedProjectIdentity(t *testing.T) {
	root := "/home/alice/project"
	deps := validDependencies(root, rootMount)
	project, err := classify(root, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"/home/alice/other", root + "/../other", `C:\project`, "relative"} {
		if _, err := resolveDescendant(project, candidate, deps); err == nil {
			t.Errorf("resolveDescendant(%q) succeeded", candidate)
		}
	}
	changed := project
	changed.mountID++
	if _, err := resolveDescendant(changed, root, deps); err == nil || !strings.Contains(err.Error(), "changed storage or mount identity") {
		t.Fatalf("changed-identity error = %v", err)
	}

	missingUnderFile := root + "/file/child"
	baseLstat := deps.lstat
	deps.lstat = func(name string) (pathInfo, error) {
		switch name {
		case missingUnderFile:
			return pathInfo{}, fs.ErrNotExist
		case root + "/file":
			return pathInfo{Mode: 0o644, Dev: linuxDevice(8, 1)}, nil
		default:
			return baseLstat(name)
		}
	}
	deps.evalSymlinks = func(name string) (string, error) { return name, nil }
	if _, err := resolveDescendant(project, missingUnderFile, deps); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("missing-under-file error = %v", err)
	}
}

func TestProveMissingProjectRequiresLiveSupportedStorageBoundary(t *testing.T) {
	driveMounts := rootMount + "25 24 0:45 / /mnt/c rw - 9p drvfsa rw,aname=drvfs;path=C:\\134;uid=1000\n"
	tests := map[string]struct {
		root   string
		mounts string
		infos  map[string]pathInfo
		want   string
	}{
		"distribution": {
			root: "/home/alice/gone", mounts: rootMount,
			infos: map[string]pathInfo{
				"/": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/home": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/home/alice": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)},
			},
		},
		"live default drive": {
			root: "/mnt/c/Work/gone", mounts: driveMounts,
			infos: map[string]pathInfo{
				"/": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/mnt": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/mnt/c": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(0, 45)}, "/mnt/c/Work": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(0, 45)},
			},
		},
		"vanished default drive": {
			root: "/mnt/c/Work/gone", mounts: rootMount,
			infos: map[string]pathInfo{
				"/": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/mnt": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)},
			},
			want: "mount /mnt/c is unavailable",
		},
		"symlinked ancestor": {
			root: "/work/link/gone", mounts: rootMount,
			infos: map[string]pathInfo{
				"/": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/work": {Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, "/work/link": {Mode: os.ModeSymlink | 0o777, Dev: linuxDevice(8, 1)},
			},
			want: "non-symlink directory",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := dependencies{
				currentRuntime: func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.WSL2Native}, nil },
				lstat: func(candidate string) (pathInfo, error) {
					if info, ok := tc.infos[candidate]; ok {
						return info, nil
					}
					return pathInfo{}, fs.ErrNotExist
				},
				readMountInfo: func() ([]byte, error) { return []byte(tc.mounts), nil },
			}
			err := proveMissingProject(tc.root, deps)
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("proveMissingProject() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProveMissingProjectRejectsRecreatedPathDuringProof(t *testing.T) {
	root := "/home/alice/gone"
	rootChecks := 0
	deps := dependencies{
		currentRuntime: func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.WSL2Native}, nil },
		lstat: func(candidate string) (pathInfo, error) {
			if candidate == root {
				rootChecks++
				if rootChecks == 1 {
					return pathInfo{}, fs.ErrNotExist
				}
			}
			return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, nil
		},
		readMountInfo: func() ([]byte, error) { return []byte(rootMount), nil },
	}
	if err := proveMissingProject(root, deps); err == nil || !strings.Contains(err.Error(), "exists and is not orphaned") {
		t.Fatalf("recreated path error = %v", err)
	}
}

func TestClassifyRejectsMountHiddenAtAncestor(t *testing.T) {
	root := "/mnt/c/Users/Alice/Project"
	mounts := rootMount +
		"25 24 0:45 / /mnt/c rw - 9p drvfsa rw,aname=drvfs\n" +
		"26 24 0:46 / /mnt rw - tmpfs none rw\n"
	deps := validDependencies(root, mounts)
	deps.lstat = func(name string) (pathInfo, error) {
		if name == root {
			return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(0, 46)}, nil
		}
		return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, nil
	}
	if _, err := classify(root, deps); err == nil || !strings.Contains(err.Error(), "does not back the root dentry") {
		t.Fatalf("classify() error = %v", err)
	}
}

func TestClassifyRejectsMismatchedDrvFSDevice(t *testing.T) {
	root := "/mnt/c/Users/Alice/Project"
	deps := validDependencies(root, rootMount+"25 24 0:45 / /mnt/c rw - 9p drvfsa rw,aname=drvfs\n")
	deps.lstat = func(name string) (pathInfo, error) {
		return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(0, 46)}, nil
	}
	if _, err := classify(root, deps); err == nil || !strings.Contains(err.Error(), "does not back the root dentry") {
		t.Fatalf("classify() error = %v", err)
	}
}

func TestClassifyUsesDeepestContainingMount(t *testing.T) {
	root := "/home/alice/My Project/repo"
	mounts := rootMount + "25 24 8:1 /home/alice/My\\040Project /home/alice/My\\040Project rw - ext4 /dev/sdb rw\n"
	project, err := classify(root, validDependencies(root, mounts))
	if err != nil {
		t.Fatal(err)
	}
	if project.MountPoint != "/home/alice/My Project" {
		t.Fatalf("MountPoint = %q", project.MountPoint)
	}
}

func TestClassifyUsesMountAncestryForSamePointStack(t *testing.T) {
	for name, topID := range map[string]int{"higher top ID": 26, "lower reused top ID": 12} {
		t.Run(name, func(t *testing.T) {
			root := "/mnt/c/Users/Alice/Project"
			mounts := rootMount +
				"25 24 0:45 / /mnt/c rw - 9p drvfsa rw,aname=drvfs\n" +
				fmt.Sprintf("%d 25 0:46 / /mnt/c rw - tmpfs none rw\n", topID)
			if _, err := classify(root, validDependencies(root, mounts)); err == nil || !strings.Contains(err.Error(), "not proven DrvFs") {
				t.Fatalf("classify() error = %v", err)
			}
		})
	}
}

func TestClassifyRejectsAmbiguousBoundaries(t *testing.T) {
	tests := map[string]struct {
		root   string
		mutate func(*dependencies)
	}{
		"non WSL runtime": {
			root: "/home/alice/project",
			mutate: func(d *dependencies) {
				d.currentRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.LinuxNative}, nil }
			},
		},
		"Windows spelling":  {root: `C:\Users\Alice\Project`},
		"relative":          {root: "home/alice/project"},
		"noncanonical":      {root: "/home/alice/../bob"},
		"distribution root": {root: "/"},
		"final symlink": {
			root: "/home/alice/project",
			mutate: func(d *dependencies) {
				d.lstat = func(string) (pathInfo, error) { return pathInfo{Mode: os.ModeSymlink | 0o777, Dev: 1}, nil }
			},
		},
		"parent symlink": {
			root: "/home/alice/project",
			mutate: func(d *dependencies) {
				d.evalSymlinks = func(string) (string, error) { return "/srv/project", nil }
			},
		},
		"regular file": {
			root: "/home/alice/project",
			mutate: func(d *dependencies) {
				d.lstat = func(name string) (pathInfo, error) {
					if name == "/" {
						return pathInfo{Mode: os.ModeDir | 0o755, Dev: 1}, nil
					}
					return pathInfo{Mode: 0o644, Dev: 1}, nil
				}
			},
		},
		"separate native filesystem": {
			root: "/srv/project",
			mutate: func(d *dependencies) {
				d.lstat = func(name string) (pathInfo, error) {
					if name == "/" {
						return pathInfo{Mode: os.ModeDir | 0o755, Dev: 1}, nil
					}
					return pathInfo{Mode: os.ModeDir | 0o755, Dev: 2}, nil
				}
			},
		},
		"unproven mnt drive": {
			root: "/mnt/c/Users/Alice/Project",
		},
		"uppercase mnt drive lookalike": {
			root: "/mnt/C/Users/Alice/Project",
		},
		"lookalike 9p option": {
			root: "/mnt/c/Users/Alice/Project",
			mutate: func(d *dependencies) {
				d.readMountInfo = func() ([]byte, error) {
					return []byte(rootMount + "25 24 0:45 / /mnt/c rw - 9p drvfsa rw,xaname=drvfs-copy\n"), nil
				}
			},
		},
		"lookalike virtiofs tag": {
			root: "/mnt/c/Users/Alice/Project",
			mutate: func(d *dependencies) {
				d.readMountInfo = func() ([]byte, error) {
					return []byte(rootMount + "25 24 0:45 / /mnt/c rw - virtiofs drvfs-evil rw\n"), nil
				}
			},
		},
		"entire Windows drive": {
			root: "/mnt/c",
			mutate: func(d *dependencies) {
				d.readMountInfo = func() ([]byte, error) {
					return []byte(rootMount + "25 24 0:45 / /mnt/c rw - 9p drvfsa rw,aname=drvfs\n"), nil
				}
			},
		},
		"custom DrvFs root": {
			root: "/windows/c/Project",
			mutate: func(d *dependencies) {
				d.readMountInfo = func() ([]byte, error) {
					return []byte(rootMount + "25 24 0:45 / /windows/c rw - 9p drvfsa rw,aname=drvfs\n"), nil
				}
			},
		},
		"empty mount table": {
			root: "/home/alice/project",
			mutate: func(d *dependencies) {
				d.readMountInfo = func() ([]byte, error) { return nil, nil }
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := validDependencies(tc.root, rootMount)
			if tc.mutate != nil {
				tc.mutate(&deps)
			}
			if _, err := classify(tc.root, deps); err == nil {
				t.Fatal("classify() succeeded")
			}
		})
	}
}

func TestParseMountInfoRejectsMalformedOrUnknownEscapes(t *testing.T) {
	for _, raw := range []string{
		"bad\n",
		rootMount + "24 1 8:2 / /other rw - ext4 /dev/sdc rw\n",
		"24 1 bad / / rw - ext4 /dev/sdb rw\n",
		"24 1 8:1 / /bad\\999 rw - ext4 /dev/sdb rw\n",
		"24 1 8:1 / relative rw - ext4 /dev/sdb rw\n",
	} {
		if _, err := parseMountInfo([]byte(raw)); err == nil {
			t.Fatalf("parseMountInfo(%q) succeeded", raw)
		}
	}
}

func TestParseMountInfoAcceptsSelfParentedRoot(t *testing.T) {
	mounts, err := parseMountInfo([]byte("24 24 8:1 / / rw - ext4 /dev/sdb rw\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].parentID != mounts[0].id {
		t.Fatalf("parseMountInfo() = %+v", mounts)
	}
}

func TestClassifyPropagatesRuntimeAndFilesystemErrors(t *testing.T) {
	deps := validDependencies("/home/alice/project", rootMount)
	deps.currentRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{}, errors.New("kernel unavailable") }
	if _, err := classify("/home/alice/project", deps); err == nil || !strings.Contains(err.Error(), "kernel unavailable") {
		t.Fatalf("runtime error = %v", err)
	}
	deps = validDependencies("/home/alice/project", rootMount)
	deps.readMountInfo = func() ([]byte, error) { return nil, errors.New("mountinfo unavailable") }
	if _, err := classify("/home/alice/project", deps); err == nil || !strings.Contains(err.Error(), "mountinfo unavailable") {
		t.Fatalf("mount error = %v", err)
	}
}

func validDependencies(root, mounts string) dependencies {
	device := linuxDevice(8, 1)
	if parsed, err := parseMountInfo([]byte(mounts)); err == nil {
		if mount, err := containingMount(root, parsed); err == nil {
			device = mount.device
		}
	}
	return dependencies{
		currentRuntime: func() (hostenv.Runtime, error) {
			return hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}, nil
		},
		lstat: func(name string) (pathInfo, error) {
			if name != root && name != "/" {
				return pathInfo{}, errors.New("unexpected path")
			}
			if name == "/" {
				return pathInfo{Mode: os.ModeDir | 0o755, Dev: linuxDevice(8, 1)}, nil
			}
			return pathInfo{Mode: os.ModeDir | 0o755, Dev: device}, nil
		},
		evalSymlinks:  func(string) (string, error) { return root, nil },
		readMountInfo: func() ([]byte, error) { return []byte(mounts), nil },
	}
}
