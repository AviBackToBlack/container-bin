//go:build linux

package wslshim

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
)

// Inspect validates the fixed binary/shim leaf objects and reports whether each
// registry-derived tool shim is already ready or still missing. The caller must
// first validate the same layout with wslfs; Inspect never creates, replaces or
// removes a filesystem object.
func Inspect(layout hostenv.WSLLayout, names []string) (Result, error) {
	return inspect(layout, names, inspectionDependencies())
}

// InspectNames validates registry-derived tool-shim leaves without requiring
// the managed binary or management shim to exist yet. The native installer
// uses it to reject collisions before publishing any managed leaf object.
func InspectNames(layout hostenv.WSLLayout, names []string) (Result, error) {
	plan, err := wslfs.Check(layout)
	if err != nil {
		return Result{}, fmt.Errorf("validate native WSL layout before tool-shim preflight: %w", err)
	}
	for _, missing := range plan.MissingDirectories {
		if missing == layout.ShimDir {
			planned, err := Plan(layout, names)
			if err != nil {
				return Result{}, err
			}
			for index := range planned {
				planned[index].State = Missing
			}
			return Result{Shims: planned}, nil
		}
	}
	return inspectNames(layout, names, inspectionDependencies())
}

// Reconcile creates only missing registry-derived tool shims after validating
// the complete fixed layout. Existing objects are never replaced. The native
// frontend does not expose this primitive until installer qualification lands.
func Reconcile(layout hostenv.WSLLayout, names []string) (Result, error) {
	plan, err := wslfs.Check(layout)
	if err != nil {
		return Result{}, fmt.Errorf("validate native WSL layout before shim mutation: %w", err)
	}
	if len(plan.MissingDirectories) != 0 {
		return Result{}, errors.New("native WSL layout requires preparation before shim mutation")
	}
	return reconcile(layout, names, mutationDependencies{
		dependencies:      inspectionDependencies(),
		openShimDirectory: openPinnedShimDirectory,
	})
}

// ReconcileManagement creates the fixed management symlink only when it is
// missing. Foreign objects or targets are never replaced.
func ReconcileManagement(layout hostenv.WSLLayout) (Shim, error) {
	plan, err := wslfs.Check(layout)
	if err != nil {
		return Shim{}, fmt.Errorf("validate native WSL layout before management-shim mutation: %w", err)
	}
	if len(plan.MissingDirectories) != 0 {
		return Shim{}, errors.New("native WSL layout requires preparation before management-shim mutation")
	}
	return reconcileManagement(layout, mutationDependencies{
		dependencies:      inspectionDependencies(),
		openShimDirectory: openPinnedShimDirectory,
	})
}

func inspectionDependencies() dependencies {
	return dependencies{
		currentRuntime: hostenv.Current,
		currentUID:     func() uint32 { return uint32(os.Getuid()) },
		lstat:          lstat,
		readlink:       os.Readlink,
	}
}

func lstat(path string) (fileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileInfo{}, errors.New("filesystem ownership is unavailable")
	}
	return fileInfo{Mode: info.Mode(), ExactMode: os.FileMode(stat.Mode & 0o7777), UID: stat.Uid}, nil
}
