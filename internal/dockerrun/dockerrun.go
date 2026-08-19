// Package dockerrun assembles and executes the `docker run` invocation that
// backs every tool shim: provider-specific volume and environment setup, the
// container working directory, and stdio/exit-code passthrough.
package dockerrun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/dockervol"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

// interactiveTerminal is intentionally conservative: Docker gets a TTY only
// when both stdin and stdout are character devices. Pipes/redirection and
// process-captured output remain plain -i, preserving automation semantics.
func interactiveTerminal() bool {
	in, err := os.Stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	out, err := os.Stdout.Stat()
	return err == nil && out.Mode()&os.ModeCharDevice != 0
}

func RunTool(t registry.Tool, userArgs []string) (int, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return 1, err
	}
	root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
	if !found {
		root = cwd
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return 1, err
	}
	workspaceRoot := pathmap.WorkspaceRootFor(t, root)
	containerWD := workspaceRoot
	if rel != "." {
		containerWD += "/" + filepath.ToSlash(rel)
	}

	mappedUserArgs, pathMounts, err := pathmap.MapToolArgs(t, root, cwd, workspaceRoot, userArgs)
	if err != nil {
		return 1, err
	}
	imageRef, err := lockfile.RuntimeImageForTool(t)
	if err != nil {
		return 1, err
	}
	args := []string{"run", "--rm", "-i"}
	if interactiveTerminal() {
		args = append(args, "-t")
	}
	args = append(args, "--workdir", containerWD)
	rootSpec, err := MountSpec("bind", root, workspaceRoot)
	if err != nil {
		return 1, err
	}
	args = append(args, "--mount", rootSpec)
	for _, m := range pathMounts {
		mount, err := MountSpec("bind", m.Source, m.Target)
		if err != nil {
			return 1, err
		}
		args = append(args, "--mount", mount)
	}
	for _, name := range selectedHostEnv(t) {
		args = append(args, "-e", name)
	}
	for _, kv := range t.EnvSet {
		args = append(args, "-e", kv)
	}

	switch t.Provider {
	case "python":
		envID := pathmap.PythonEnvID(root, found)
		pyLabels := map[string]string{"cb.managed": "true", "cb.owner": "python313/venv"}
		if found {
			pyLabels["cb.kind"] = "project"
			pyLabels["cb.project_path"] = root
			pyLabels["cb.project_hash"] = pathmap.VolumeHash(root)
		} else {
			pyLabels["cb.kind"] = "compat"
		}
		venvSpec, err := MountSpec("volume", envID, "/venv")
		if err != nil {
			return 1, err
		}
		pipCacheSpec, err := MountSpec("volume", "cb-pip-cache", "/root/.cache/pip")
		if err != nil {
			return 1, err
		}
		if err := dockervol.EnsureManaged(envID, pyLabels); err != nil {
			return 1, err
		}
		if err := dockervol.EnsureManaged("cb-pip-cache", map[string]string{"cb.managed": "true", "cb.kind": "shared", "cb.owner": "python313/pip-cache"}); err != nil {
			return 1, err
		}
		args = append(args,
			"--mount", venvSpec,
			"--mount", pipCacheSpec,
			"-e", "VIRTUAL_ENV=/venv",
			"-e", "PATH=/venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin",
		)
		args = append(args, imageRef)
		bootstrap := `if [ ! -x /venv/bin/python ]; then python -m venv /venv || exit $?; fi; if [ "$1" = "__CB_PIP__" ]; then shift; exec /venv/bin/python -m pip "$@"; else exec /venv/bin/python "$@"; fi`
		args = append(args, "sh", "-c", bootstrap, "cb")
		if t.Role == "pip" {
			args = append(args, "__CB_PIP__")
		}
		args = append(args, t.ArgsPrefix...)
		args = append(args, mappedUserArgs...)
	case "stateful":
		for _, spec := range t.ProjectVolumes {
			name, dst, err := registry.ParseVolumeBinding(spec)
			if err != nil {
				return 1, err
			}
			vol := pathmap.StatefulProjectVolumeID(t.StateGroup, name, root, found)
			dst = pathmap.StatefulWorkspaceDestination(dst, workspaceRoot)
			volSpec, err := MountSpec("volume", vol, dst)
			if err != nil {
				return 1, err
			}
			if err := dockervol.EnsureManaged(vol, map[string]string{"cb.managed": "true", "cb.kind": "project", "cb.owner": t.StateGroup + "/" + name, "cb.project_path": root, "cb.project_hash": pathmap.VolumeHash(root)}); err != nil {
				return 1, err
			}
			args = append(args, "--mount", volSpec)
		}
		for _, spec := range t.SharedVolumes {
			name, dst, err := registry.ParseVolumeBinding(spec)
			if err != nil {
				return 1, err
			}
			vol := pathmap.StatefulSharedVolumeID(t.StateGroup, name)
			volSpec, err := MountSpec("volume", vol, dst)
			if err != nil {
				return 1, err
			}
			if err := dockervol.EnsureManaged(vol, map[string]string{"cb.managed": "true", "cb.kind": "shared", "cb.owner": t.StateGroup + "/" + name}); err != nil {
				return 1, err
			}
			args = append(args, "--mount", volSpec)
		}
		args = append(args, imageRef)
		args = append(args, t.Command...)
		args = append(args, t.ArgsPrefix...)
		args = append(args, mappedUserArgs...)
	case "stateless":
		args = append(args, imageRef)
		args = append(args, t.Command...)
		args = append(args, t.ArgsPrefix...)
		args = append(args, mappedUserArgs...)
	default:
		return 1, fmt.Errorf("unsupported provider %q", t.Provider)
	}

	cmd := exec.Command("docker", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, os.Environ()
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

func selectedHostEnv(t registry.Tool) []string {
	if len(t.EnvNames) == 0 && len(t.EnvPrefixes) == 0 {
		return nil
	}
	exact := map[string]bool{}
	for _, n := range t.EnvNames {
		exact[strings.ToUpper(n)] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, kv := range os.Environ() {
		name := strings.SplitN(kv, "=", 2)[0]
		upper := strings.ToUpper(name)
		match := exact[upper]
		if !match {
			for _, p := range t.EnvPrefixes {
				if strings.HasPrefix(upper, strings.ToUpper(p)) {
					match = true
					break
				}
			}
		}
		if match && !seen[upper] {
			seen[upper] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func MountSpec(kind, src, dst string) (string, error) {
	// src is checked before dst deliberately: for the project-root bind
	// mount, dst (workspaceRoot) is always derived from a substring of src
	// (root), so this ordering is what makes a comma-named project fail on
	// the source path rather than the destination -- see docs/windows-paths.md
	// row P15 and TestRootBindMountCommaFailsClosedOnSrc. Swapping the checks
	// would not change whether the mount is rejected, but would change which
	// message the row and test depend on.
	if strings.Contains(src, ",") {
		return "", fmt.Errorf("source path/volume contains a comma and cannot be represented safely in docker --mount syntax (values are comma-separated with no escaping): %s", src)
	}
	if strings.Contains(dst, ",") {
		return "", fmt.Errorf("destination path contains a comma and cannot be represented safely in docker --mount syntax (values are comma-separated with no escaping): %s", dst)
	}
	return fmt.Sprintf("type=%s,src=%s,dst=%s", kind, src, dst), nil
}

func EnsureImageLocalForTool(t registry.Tool) error {
	ref, err := lockfile.RuntimeImageForTool(t)
	if err != nil {
		return err
	}
	if err := exec.Command("docker", "image", "inspect", ref).Run(); err != nil {
		return fmt.Errorf("required image is not local: %s (run `cb lock`/`cb update` first)", ref)
	}
	return nil
}
