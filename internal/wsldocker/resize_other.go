//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// ResizeContainer is unavailable outside Linux because the fixed Unix socket
// and peer credentials are part of the Docker Desktop WSL trust boundary.
func ResizeContainer(context.Context, string, uint16, uint16) error {
	return errors.New("Docker Desktop WSL container resize requires Linux")
}
