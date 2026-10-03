//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// RemoveContainer is unavailable outside Linux because the fixed Unix socket
// and peer credentials are part of the Docker Desktop WSL trust boundary.
func RemoveContainer(context.Context, Container) error {
	return errors.New("Docker Desktop WSL container remove requires Linux")
}
