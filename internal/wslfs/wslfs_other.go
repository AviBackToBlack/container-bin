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

// Check is unavailable outside native Linux because Unix ownership and mode
// checks are part of the WSL trust boundary.
func Check(hostenv.WSLLayout) (Plan, error) {
	return Plan{}, errors.New("native WSL filesystem validation requires Linux")
}

func CheckRegistryRecovery(hostenv.WSLLayout) error {
	return errors.New("native WSL registry recovery validation requires Linux")
}

func CheckLockRecovery(hostenv.WSLLayout) error {
	return errors.New("native WSL lockfile recovery validation requires Linux")
}

func currentLayout() (hostenv.WSLLayout, error) {
	return hostenv.WSLLayout{}, errors.New("native WSL layout discovery requires Linux")
}

func CurrentRegistryPath() (string, error) {
	return "", errors.New("native WSL registry path discovery requires Linux")
}
