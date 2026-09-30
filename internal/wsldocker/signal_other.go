//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// SignalContainer is unavailable outside Linux because the fixed Unix socket
// and peer credentials are part of the Docker Desktop WSL trust boundary.
func SignalContainer(context.Context, string, int) error {
	return errors.New("Docker Desktop WSL container signal requires Linux")
}
