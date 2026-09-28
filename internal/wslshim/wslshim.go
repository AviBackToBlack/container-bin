// Package wslshim defines, inspects and reconciles registry-derived native WSL
// tool-shim identities. Frontend lifecycle wiring remains a separate boundary.
package wslshim

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

type State string

const (
	Ready   State = "ready"
	Missing State = "missing"
)

// Shim is the exact symlink identity for one registry-derived command.
type Shim struct {
	Name   string
	Path   string
	Target string
	State  State
}

type Result struct {
	Shims []Shim
}

type fileInfo struct {
	Mode      os.FileMode
	ExactMode os.FileMode
	UID       uint32
}

type dependencies struct {
	currentRuntime func() (hostenv.Runtime, error)
	currentUID     func() uint32
	lstat          func(string) (fileInfo, error)
	readlink       func(string) (string, error)
}

type shimDirectory interface {
	ensure(Shim) error
	close() error
}

type mutationDependencies struct {
	dependencies
	openShimDirectory func(hostenv.WSLLayout) (shimDirectory, error)
}

// Plan returns sorted deterministic identities without accessing the filesystem.
func Plan(layout hostenv.WSLLayout, names []string) ([]Shim, error) {
	if err := validateLayout(layout); err != nil {
		return nil, err
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	shims := make([]Shim, 0, len(names))
	previous := ""
	for _, name := range names {
		if !registry.ValidToolName(name) || registry.ReservedToolName(name) {
			return nil, fmt.Errorf("invalid or reserved native WSL tool-shim name %q", name)
		}
		if name == previous {
			return nil, fmt.Errorf("duplicate native WSL tool-shim name %q", name)
		}
		previous = name
		shims = append(shims, Shim{
			Name:   name,
			Path:   path.Join(layout.ShimDir, name),
			Target: layout.BinaryPath,
		})
	}
	return shims, nil
}

func inspect(layout hostenv.WSLLayout, names []string, d dependencies) (Result, error) {
	planned, err := Plan(layout, names)
	if err != nil {
		return Result{}, err
	}
	runtime, err := d.currentRuntime()
	if err != nil {
		return Result{}, fmt.Errorf("classify native WSL runtime: %w", err)
	}
	if runtime.Kind != hostenv.WSL2Native || runtime.Distro != layout.Distro {
		return Result{}, fmt.Errorf("native WSL tool-shim layout belongs to distro %q, current runtime is %q kind %q", layout.Distro, runtime.Distro, runtime.Kind)
	}
	if uid := d.currentUID(); uid != layout.UID {
		return Result{}, fmt.Errorf("native WSL tool-shim layout belongs to UID %d, current UID is %d", layout.UID, uid)
	}
	if err := inspectBinary(layout, d); err != nil {
		return Result{}, err
	}
	if err := inspectShimDir(layout, d); err != nil {
		return Result{}, err
	}
	if err := inspectSymlink(layout.ManagementShim, layout.BinaryPath, layout.UID, "management shim", d); err != nil {
		return Result{}, err
	}
	for index := range planned {
		info, err := d.lstat(planned[index].Path)
		if errors.Is(err, fs.ErrNotExist) {
			planned[index].State = Missing
			continue
		}
		if err != nil {
			return Result{}, fmt.Errorf("inspect native WSL tool shim %s: %w", planned[index].Path, err)
		}
		if err := validateSymlink(planned[index].Path, planned[index].Target, layout.UID, info, "tool shim", d); err != nil {
			return Result{}, err
		}
		planned[index].State = Ready
	}
	return Result{Shims: planned}, nil
}

func reconcile(layout hostenv.WSLLayout, names []string, d mutationDependencies) (Result, error) {
	before, err := inspect(layout, names, d.dependencies)
	if err != nil {
		return Result{}, err
	}
	missing := make([]Shim, 0, len(before.Shims))
	for _, shim := range before.Shims {
		if shim.State == Missing {
			missing = append(missing, shim)
		}
	}
	if len(missing) == 0 {
		return before, nil
	}
	if d.openShimDirectory == nil {
		return Result{}, errors.New("native WSL tool-shim mutation is unavailable")
	}
	directory, err := d.openShimDirectory(layout)
	if err != nil {
		return Result{}, fmt.Errorf("open native WSL shim directory for mutation: %w", err)
	}
	for _, shim := range missing {
		if err := directory.ensure(shim); err != nil {
			closeErr := directory.close()
			return Result{}, errors.Join(fmt.Errorf("install native WSL tool shim %s: %w", shim.Path, err), closeErr)
		}
	}
	if err := directory.close(); err != nil {
		return Result{}, fmt.Errorf("close native WSL shim directory after mutation: %w", err)
	}
	after, err := inspect(layout, names, d.dependencies)
	if err != nil {
		return Result{}, fmt.Errorf("revalidate native WSL tool shims after mutation: %w", err)
	}
	for _, shim := range after.Shims {
		if shim.State != Ready {
			return Result{}, fmt.Errorf("native WSL tool shim %s remained %s after mutation", shim.Path, shim.State)
		}
	}
	return after, nil
}

func inspectBinary(layout hostenv.WSLLayout, d dependencies) error {
	info, err := d.lstat(layout.BinaryPath)
	if err != nil {
		return fmt.Errorf("inspect native WSL managed binary %s: %w", layout.BinaryPath, err)
	}
	if !info.Mode.IsRegular() || info.UID != layout.UID || info.ExactMode != 0o755 {
		return fmt.Errorf("native WSL managed binary %s must be a UID %d regular file with mode 0755", layout.BinaryPath, layout.UID)
	}
	return nil
}

func inspectShimDir(layout hostenv.WSLLayout, d dependencies) error {
	info, err := d.lstat(layout.ShimDir)
	if err != nil {
		return fmt.Errorf("inspect native WSL shim directory %s: %w", layout.ShimDir, err)
	}
	mode := info.ExactMode
	if !info.Mode.IsDir() || info.UID != layout.UID || mode&0o700 != 0o700 || mode&0o022 != 0 || mode&0o7000 != 0 {
		return fmt.Errorf("native WSL shim directory %s must be a UID %d owner-accessible directory not writable by group or other", layout.ShimDir, layout.UID)
	}
	return nil
}

func inspectSymlink(link, target string, uid uint32, kind string, d dependencies) error {
	info, err := d.lstat(link)
	if err != nil {
		return fmt.Errorf("inspect native WSL %s %s: %w", kind, link, err)
	}
	return validateSymlink(link, target, uid, info, kind, d)
}

func validateSymlink(link, target string, uid uint32, info fileInfo, kind string, d dependencies) error {
	if info.Mode&os.ModeSymlink == 0 || info.UID != uid {
		return fmt.Errorf("native WSL %s %s must be a UID %d symlink", kind, link, uid)
	}
	actual, err := d.readlink(link)
	if err != nil {
		return fmt.Errorf("read native WSL %s %s: %w", kind, link, err)
	}
	if actual != target {
		return fmt.Errorf("native WSL %s %s targets %q, expected %q", kind, link, actual, target)
	}
	return nil
}

func validateLayout(layout hostenv.WSLLayout) error {
	if layout.Distro == "" || strings.TrimSpace(layout.Distro) != layout.Distro || !utf8.ValidString(layout.Distro) {
		return errors.New("native WSL tool-shim layout has invalid distribution identity")
	}
	for _, r := range layout.Distro {
		if unicode.IsControl(r) {
			return errors.New("native WSL tool-shim layout has invalid distribution identity")
		}
	}
	home := layout.Home
	if home == "" || !utf8.ValidString(home) || !path.IsAbs(home) || path.Clean(home) != home || home == "/" || home == "/mnt" || strings.HasPrefix(home, "/mnt/") || strings.ContainsRune(home, '\\') {
		return errors.New("native WSL tool-shim layout has invalid home path")
	}
	for _, r := range home {
		if unicode.IsControl(r) {
			return errors.New("native WSL tool-shim layout has invalid home path")
		}
	}
	wantShimDir := path.Join(home, ".local/bin")
	wantBinary := path.Join(home, ".local/lib/container-bin/cb")
	if layout.ShimDir != wantShimDir || layout.BinaryPath != wantBinary || layout.ManagementShim != path.Join(wantShimDir, "cb") {
		return errors.New("native WSL tool-shim layout does not use the fixed managed paths")
	}
	return nil
}
