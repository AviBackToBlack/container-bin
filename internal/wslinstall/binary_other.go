//go:build !linux

package wslinstall

import (
	"errors"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

func BinaryState(hostenv.WSLLayout, string) (State, error) {
	return "", errors.New("native WSL binary inspection requires Linux")
}

func InstallBinary(hostenv.WSLLayout, string) error {
	return errors.New("native WSL binary installation requires Linux")
}
