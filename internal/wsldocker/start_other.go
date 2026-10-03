//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// StartContainer is unavailable outside Linux because the fixed Unix socket
// and peer credentials are part of the Docker Desktop WSL trust boundary.
func StartContainer(context.Context, string) error {
	return errors.New("Docker Desktop WSL container start requires Linux")
}
