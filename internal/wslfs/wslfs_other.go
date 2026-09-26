//go:build !linux

package wslfs

import (
	"errors"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

// Prepare is unavailable outside native Linux because Unix ownership and mode
// checks are part of the WSL trust boundary.
func Prepare(hostenv.WSLLayout) error {
	return errors.New("native WSL filesystem preparation requires Linux")
}
