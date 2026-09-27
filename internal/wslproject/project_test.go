package wslproject

import (
	"errors"
	"fmt"
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
		"24 24 8:1 / / rw - ext4 /dev/sdb rw\n",
		rootMount + "24 1 8:2 / /other rw - ext4 /dev/sdc rw\n",
		"24 1 8:1 / /bad\\999 rw - ext4 /dev/sdb rw\n",
		"24 1 8:1 / relative rw - ext4 /dev/sdb rw\n",
	} {
		if _, err := parseMountInfo([]byte(raw)); err == nil {
			t.Fatalf("parseMountInfo(%q) succeeded", raw)
		}
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
	return dependencies{
		currentRuntime: func() (hostenv.Runtime, error) {
			return hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}, nil
		},
		lstat: func(name string) (pathInfo, error) {
			if name != root && name != "/" {
				return pathInfo{}, errors.New("unexpected path")
			}
			return pathInfo{Mode: os.ModeDir | 0o755, Dev: 1}, nil
		},
		evalSymlinks:  func(string) (string, error) { return root, nil },
		readMountInfo: func() ([]byte, error) { return []byte(mounts), nil },
	}
}
