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
