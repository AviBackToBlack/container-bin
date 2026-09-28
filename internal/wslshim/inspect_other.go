//go:build !linux

package wslshim

import (
	"errors"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

// Inspect is unavailable outside Linux because native symlink ownership is
// part of the WSL tool-shim identity boundary.
func Inspect(hostenv.WSLLayout, []string) (Result, error) {
	return Result{}, errors.New("native WSL tool-shim inspection requires Linux")
}

func InspectNames(hostenv.WSLLayout, []string) (Result, error) {
	return Result{}, errors.New("native WSL tool-shim inspection requires Linux")
}

// Reconcile is unavailable outside Linux because native descriptor-relative
// symlink mutation is part of the WSL trust boundary.
func Reconcile(hostenv.WSLLayout, []string) (Result, error) {
	return Result{}, errors.New("native WSL tool-shim mutation requires Linux")
}

func ReconcileManagement(hostenv.WSLLayout) (Shim, error) {
	return Shim{}, errors.New("native WSL management-shim mutation requires Linux")
}
