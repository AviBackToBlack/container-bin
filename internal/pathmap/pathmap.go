// Package pathmap owns container-bin's Windows path semantics: classifying
// which argv elements are host paths, translating them to container paths,
// locating a tool's project root, and deriving the deterministic volume names
// that root implies.
package pathmap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

type PathMount struct{ Source, Target string }

type pathMapper struct {
	root          string
	cwd           string
	workspaceRoot string
	mounts        []PathMount
	mountBySource map[string]string
}

// NormalizeToolArgs repairs shell-level argv shapes that are unambiguous from
// the tool registry. In particular, PowerShell can hand native processes
// `-opt=` and its value as two argv elements. For path_equals options we join
// those back before path mapping, preserving the target tool's required syntax.
func NormalizeToolArgs(t registry.Tool, args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		joined := false
		for _, opt := range t.PathEquals {
			prefix := opt + "="
			if arg == prefix && i+1 < len(args) {
				out = append(out, prefix+args[i+1])
				i++
				joined = true
				break
			}
		}
		if !joined {
			out = append(out, arg)
		}
	}
	return out
}

func MapToolArgs(t registry.Tool, root, cwd, workspaceRoot string, userArgs []string) ([]string, []PathMount, error) {
	normalized := NormalizeToolArgs(t, userArgs)
	if runtime.GOOS != "windows" {
		return append([]string(nil), normalized...), nil, nil
	}
	pm := &pathMapper{root: root, cwd: cwd, workspaceRoot: workspaceRoot, mountBySource: map[string]string{}}
	mapped := append([]string(nil), normalized...)
	forceNext := false
	for i, arg := range normalized {
		if forceNext {
			v, err := pm.mapArg(arg, true)
			if err != nil {
				return nil, nil, err
			}
			mapped[i] = v
			forceNext = false
			continue
		}

		if containsString(t.PathNext, arg) {
			mapped[i] = arg
			forceNext = true
			continue
		}

		eqMapped := false
		for _, opt := range t.PathEquals {
			prefix := opt + "="
			if strings.HasPrefix(arg, prefix) {
				v, err := pm.mapArg(strings.TrimPrefix(arg, prefix), true)
				if err != nil {
					return nil, nil, err
				}
				mapped[i] = prefix + v
				eqMapped = true
				break
			}
		}
		if eqMapped {
			continue
		}

		lastEnabled := t.PathLast && (len(t.PathLastIfAny) == 0 || anyArgPresent(normalized, t.PathLastIfAny))
		forceLast := lastEnabled && i == len(normalized)-1 && arg != "-" && !strings.HasPrefix(arg, "-")
		v, err := pm.mapArg(arg, forceLast)
		if err != nil {
			return nil, nil, err
		}
		mapped[i] = v
	}
	if forceNext {
		return nil, nil, fmt.Errorf("tool %q: option %q requires a path argument", t.Name, normalized[len(normalized)-1])
	}
	return mapped, pm.mounts, nil
}

func (pm *pathMapper) mapArg(arg string, force bool) (string, error) {
	hostPath, isPath, err := resolveWindowsPathArgMode(pm.cwd, arg, force)
	if err != nil {
		return "", err
	}
	if !isPath {
		return arg, nil
	}
	if rel, ok := pathWithin(pm.root, hostPath); ok {
		if rel == "." {
			return pm.workspaceRoot, nil
		}
		return pm.workspaceRoot + "/" + filepath.ToSlash(rel), nil
	}
	mountRoot, err := externalMountRoot(hostPath)
	if err != nil {
		return "", fmt.Errorf("map argument path %q: %w", arg, err)
	}
	key := strings.ToLower(filepath.Clean(mountRoot))
	target, exists := pm.mountBySource[key]
	if !exists {
		target = fmt.Sprintf("/cb/mounts/%d", len(pm.mounts))
		pm.mountBySource[key] = target
		pm.mounts = append(pm.mounts, PathMount{mountRoot, target})
	}
	rel, err := filepath.Rel(mountRoot, hostPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return target, nil
	}
	return target + "/" + filepath.ToSlash(rel), nil
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func anyArgPresent(args, needles []string) bool {
	for _, arg := range args {
		if containsString(needles, arg) {
			return true
		}
	}
	return false
}

// resolveWindowsPathArg recognizes the forms that can be mapped safely without
// knowing tool-specific argument grammar:
//
//	C:\foo\bar      absolute Windows path
//	.\foo / ..\foo explicit relative Windows path
//	subdir\foo      only when it already exists on the host
//
// Plain strings that merely contain backslashes are left untouched.
func resolveWindowsPathArg(cwd, arg string) (string, bool, error) {
	return resolveWindowsPathArgMode(cwd, arg, false)
}

func resolveWindowsPathArgMode(cwd, arg string, force bool) (string, bool, error) {
	// Never treat a package-pattern wildcard as a path, even when force is true.
	// Windows strips trailing dots from a path component, so C:\proj\... collapses
	// to C:\proj and silently maps to the workspace root. Declining is safe:
	// the container's working directory is the project, so an unmapped ./...
	// resolves correctly inside the container. This also catches D:\proj\...
	// deliberately: failing loudly beats silently running the wrong subset.
	if hasPackagePatternSuffix(arg) {
		return "", false, nil
	}
	if isWindowsAbsPath(arg) {
		p, err := CanonicalPath(arg)
		if err != nil {
			return "", false, fmt.Errorf("canonicalize argument path %q: %w", arg, err)
		}
		return p, true, nil
	}
	if isExplicitWindowsRelPath(arg) || (force && !strings.HasPrefix(arg, "-") && arg != "") {
		p, err := CanonicalPath(filepath.Join(cwd, arg))
		if err != nil {
			return "", false, fmt.Errorf("canonicalize relative argument path %q: %w", arg, err)
		}
		return p, true, nil
	}
	if strings.Contains(arg, `\`) && !strings.ContainsAny(arg, "\r\n\t") {
		candidate := filepath.Join(cwd, arg)
		if _, err := os.Stat(candidate); err == nil {
			p, err := CanonicalPath(candidate)
			if err != nil {
				return "", false, err
			}
			return p, true, nil
		}
	}
	return "", false, nil
}
func isExplicitWindowsRelPath(s string) bool {
	return strings.HasPrefix(s, `.\`) || strings.HasPrefix(s, `..\`) ||
		strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

func hasPackagePatternSuffix(s string) bool {
	if !strings.HasSuffix(s, "...") {
		return false
	}
	prefix := s[:len(s)-3]
	return prefix == "" || strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, `\`)
}

func isWindowsAbsPath(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	isLetter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	return isLetter && s[1] == ':' && (s[2] == '\\' || s[2] == '/')
}

func pathWithin(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func externalMountRoot(hostPath string) (string, error) {
	if st, err := os.Stat(hostPath); err == nil {
		if st.IsDir() {
			return hostPath, nil
		}
		return filepath.Dir(hostPath), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	candidate := filepath.Dir(hostPath)
	for {
		if st, err := os.Stat(candidate); err == nil {
			if !st.IsDir() {
				return "", fmt.Errorf("nearest existing ancestor is not a directory: %s", candidate)
			}
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("no existing ancestor for %s", hostPath)
		}
		candidate = parent
	}
}

func WorkspaceRootFor(t registry.Tool, root string) string {
	if t.Provider != "stateful" {
		return "/workspace"
	}
	base := filepath.Base(filepath.Clean(root))
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "project"
	}
	// Windows project names cannot contain '/', so this remains a single
	// container path segment while preserving the basename semantics tools
	// such as `npm init` observe.
	base = strings.ReplaceAll(base, "/", "_")
	return "/workspace/" + base
}

func StatefulWorkspaceDestination(dst, workspaceRoot string) string {
	if dst == "/workspace" {
		return workspaceRoot
	}
	if strings.HasPrefix(dst, "/workspace/") {
		return workspaceRoot + strings.TrimPrefix(dst, "/workspace")
	}
	return dst
}

func ProjectMarkersFor(t registry.Tool) []string {
	if len(t.ProjectMarkers) > 0 {
		return t.ProjectMarkers
	}
	// Backward compatibility for registries created before v0.6, whose
	// Python sections did not yet declare project_markers explicitly.
	if t.Provider == "python" {
		return []string{"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", ".git"}
	}
	return []string{".git"}
}

func FindProjectRoot(start string, markers []string) (string, bool) {
	dir := start
	for {
		for _, marker := range markers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func VolumeHash(root string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return hex.EncodeToString(sum[:])[:12]
}

func StatefulProjectVolumeID(group, name, root string, project bool) string {
	_ = project // root already falls back to cwd when no marker was found
	return "cb-" + group + "-" + name + "-" + VolumeHash(root)
}

func StatefulSharedVolumeID(group, name string) string { return "cb-" + group + "-" + name }

func CanonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}

func PythonEnvID(root string, project bool) string {
	if !project {
		return "cb-python-313-global"
	}
	return "cb-python-313-" + VolumeHash(root)
}
