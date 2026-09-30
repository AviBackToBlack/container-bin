//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// WaitContainer is unavailable outside Linux because the fixed Unix socket and
// peer credentials are part of the Docker Desktop WSL trust boundary.
func WaitContainer(context.Context, string) (int, error) {
	return 0, errors.New("Docker Desktop WSL container wait requires Linux")
}
