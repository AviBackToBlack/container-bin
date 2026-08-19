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

// isolatedRoot is a sentinel project root for cwd_mode = "isolated".
// It is intentionally not a real Windows path: the drive letter "?" is not a
// valid Windows volume, so pathmap.pathWithin can never match any host path
// that pathmap.CanonicalPath could produce. MapToolArgs therefore treats every
// path argument as outside the project and routes it through the existing
// external /cb/mounts/N path, exactly as the design requires.
const IsolatedRoot = "?:\\no-project"

type runContext struct {
	cwd           string
	root          string
	workspaceRoot string
	containerWD   string
	found         bool
}

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

	ctx, err := resolveRunContext(t, cwd)
	if err != nil {
		return 1, err
	}

	imageRef, err := lockfile.RuntimeImageForTool(t)
	if err != nil {
		return 1, err
	}

	args, err := buildDockerArgs(t, userArgs, ctx, imageRef, interactiveTerminal())
	if err != nil {
		return 1, err
	}

	if err := ensureDockerVolumes(t, ctx); err != nil {
		return 1, err
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

func resolveRunContext(t registry.Tool, cwd string) (runContext, error) {
	if t.CwdMode == "isolated" {
		return runContext{
			cwd:           cwd,
			root:          IsolatedRoot,
			workspaceRoot: "/root",
			containerWD:   "/root",
			found:         false,
		}, nil
	}

	root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
	if !found {
		root = cwd
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return runContext{}, err
	}
	workspaceRoot := pathmap.WorkspaceRootFor(t, root)
	containerWD := workspaceRoot
	if rel != "." {
		containerWD += "/" + filepath.ToSlash(rel)
	}
	return runContext{
		cwd:           cwd,
		root:          root,
		workspaceRoot: workspaceRoot,
		containerWD:   containerWD,
		found:         found,
	}, nil
}

func buildDockerArgs(t registry.Tool, userArgs []string, ctx runContext, imageRef string, tty bool) ([]string, error) {
	mappedUserArgs, pathMounts, err := pathmap.MapToolArgs(t, ctx.root, ctx.cwd, ctx.workspaceRoot, userArgs)
	if err != nil {
		return nil, err
	}

	args := []string{"run", "--rm", "-i"}
	if tty {
		args = append(args, "-t")
	}
	args = append(args, "--workdir", ctx.containerWD)

	// In project mode the host CWD (or discovered project root) is bind-mounted
	// into the container workspace. In isolated mode no such bind mount exists.
	if t.CwdMode != "isolated" {
		rootSpec, err := MountSpec("bind", ctx.root, ctx.workspaceRoot)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", rootSpec)
	}

	for _, m := range pathMounts {
		mount, err := MountSpec("bind", m.Source, m.Target)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", mount)
	}

	hostMountArgs, err := buildHostMountArgs(t.HostMounts)
	if err != nil {
		return nil, err
	}
	args = append(args, hostMountArgs...)

	for _, name := range selectedHostEnv(t) {
		args = append(args, "-e", name)
	}
	for _, kv := range t.EnvSet {
		args = append(args, "-e", kv)
	}

	switch t.Provider {
	case "python":
		envID := pathmap.PythonEnvID(ctx.root, ctx.found)
		venvSpec, err := MountSpec("volume", envID, "/venv")
		if err != nil {
			return nil, err
		}
		pipCacheSpec, err := MountSpec("volume", "cb-pip-cache", "/root/.cache/pip")
		if err != nil {
			return nil, err
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
				return nil, err
			}
			vol := pathmap.StatefulProjectVolumeID(t.StateGroup, name, ctx.root, ctx.found)
			dst = pathmap.StatefulWorkspaceDestination(dst, ctx.workspaceRoot)
			volSpec, err := MountSpec("volume", vol, dst)
			if err != nil {
				return nil, err
			}
			args = append(args, "--mount", volSpec)
		}
		for _, spec := range t.SharedVolumes {
			name, dst, err := registry.ParseVolumeBinding(spec)
			if err != nil {
				return nil, err
			}
			vol := pathmap.StatefulSharedVolumeID(t.StateGroup, name)
			volSpec, err := MountSpec("volume", vol, dst)
			if err != nil {
				return nil, err
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
		return nil, fmt.Errorf("unsupported provider %q", t.Provider)
	}

	return args, nil
}

func ensureDockerVolumes(t registry.Tool, ctx runContext) error {
	switch t.Provider {
	case "python":
		envID := pathmap.PythonEnvID(ctx.root, ctx.found)
		pyLabels := map[string]string{"cb.managed": "true", "cb.owner": "python313/venv"}
		if ctx.found {
			pyLabels["cb.kind"] = "project"
			pyLabels["cb.project_path"] = ctx.root
			pyLabels["cb.project_hash"] = pathmap.VolumeHash(ctx.root)
		} else {
			pyLabels["cb.kind"] = "compat"
		}
		if err := dockervol.EnsureManaged(envID, pyLabels); err != nil {
			return err
		}
		if err := dockervol.EnsureManaged("cb-pip-cache", map[string]string{"cb.managed": "true", "cb.kind": "shared", "cb.owner": "python313/pip-cache"}); err != nil {
			return err
		}
	case "stateful":
		for _, spec := range t.ProjectVolumes {
			name, _, err := registry.ParseVolumeBinding(spec)
			if err != nil {
				return err
			}
			vol := pathmap.StatefulProjectVolumeID(t.StateGroup, name, ctx.root, ctx.found)
			if err := dockervol.EnsureManaged(vol, map[string]string{"cb.managed": "true", "cb.kind": "project", "cb.owner": t.StateGroup + "/" + name, "cb.project_path": ctx.root, "cb.project_hash": pathmap.VolumeHash(ctx.root)}); err != nil {
				return err
			}
		}
		for _, spec := range t.SharedVolumes {
			name, _, err := registry.ParseVolumeBinding(spec)
			if err != nil {
				return err
			}
			vol := pathmap.StatefulSharedVolumeID(t.StateGroup, name)
			if err := dockervol.EnsureManaged(vol, map[string]string{"cb.managed": "true", "cb.kind": "shared", "cb.owner": t.StateGroup + "/" + name}); err != nil {
				return err
			}
		}
	}
	return nil
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

func mountSpecChecked(kind, src, dst string) error {
	// src is checked before dst deliberately: for the project-root bind
	// mount, dst (workspaceRoot) is always derived from a substring of src
	// (root), so this ordering is what makes a comma-named project fail on
	// the source path rather than the destination -- see docs/windows-paths.md
	// row P15 and TestRootBindMountCommaFailsClosedOnSrc. Swapping the checks
	// would not change whether the mount is rejected, but would change which
	// message the row and test depend on.
	if strings.Contains(src, ",") {
		return fmt.Errorf("source path/volume contains a comma and cannot be represented safely in docker --mount syntax (values are comma-separated with no escaping): %s", src)
	}
	if strings.Contains(dst, ",") {
		return fmt.Errorf("destination path contains a comma and cannot be represented safely in docker --mount syntax (values are comma-separated with no escaping): %s", dst)
	}
	return nil
}

func MountSpec(kind, src, dst string) (string, error) {
	if err := mountSpecChecked(kind, src, dst); err != nil {
		return "", err
	}
	return fmt.Sprintf("type=%s,src=%s,dst=%s", kind, src, dst), nil
}

// MountSpecMode is MountSpec plus an explicit ro/rw mode. mode must be "ro" or
// "rw" -- registry.ParseHostMount already guarantees this for every
// host_mounts entry, so an unrecognized value here is a programmer error, not
// user input; fail closed rather than silently defaulting.
func MountSpecMode(kind, src, dst, mode string) (string, error) {
	if err := mountSpecChecked(kind, src, dst); err != nil {
		return "", err
	}
	switch mode {
	case "ro":
		return fmt.Sprintf("type=%s,src=%s,dst=%s,readonly", kind, src, dst), nil
	case "rw":
		return fmt.Sprintf("type=%s,src=%s,dst=%s", kind, src, dst), nil
	default:
		return "", fmt.Errorf("unsupported mount mode %q", mode)
	}
}

// ExpandHostMountSource resolves the one host variable container-bin
// understands in a host_mounts source. A literal Windows absolute path is
// returned unchanged. registry.ParseHostMount has already rejected any other
// %...% token, so this never needs to guess about anything it hasn’t seen.
func ExpandHostMountSource(source string) (string, error) {
	if strings.HasPrefix(source, "%USERPROFILE%") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve %%USERPROFILE%%: %w", err)
		}
		return home + strings.TrimPrefix(source, "%USERPROFILE%"), nil
	}
	return source, nil
}

func buildHostMountArgs(hostMounts []string) ([]string, error) {
	var args []string
	for _, spec := range hostMounts {
		source, target, mode, err := registry.ParseHostMount(spec)
		if err != nil {
			return nil, err // unreachable in practice; the registry was already validated at load
		}
		expanded, err := ExpandHostMountSource(source)
		if err != nil {
			return nil, err
		}
		canon, err := pathmap.CanonicalPath(expanded)
		if err != nil {
			return nil, fmt.Errorf("host_mounts source %q: %w", source, err)
		}
		if strings.HasPrefix(canon, `\\`) {
			return nil, fmt.Errorf("host_mounts source %q resolves to a UNC path, which Docker Desktop cannot share", source)
		}
		if _, statErr := os.Stat(canon); statErr != nil {
			return nil, fmt.Errorf("host_mounts source %q does not exist: %s", source, canon)
		}
		mount, err := MountSpecMode("bind", canon, target, mode)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", mount)
	}
	return args, nil
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
