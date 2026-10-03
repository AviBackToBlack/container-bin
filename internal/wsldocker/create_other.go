//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// CreateContainer is unavailable outside Linux because the fixed Unix socket
// and peer credentials are part of the Docker Desktop WSL trust boundary.
func CreateContainer(context.Context, ContainerCreateSpec) (Container, error) {
	return Container{}, errors.New("Docker Desktop WSL container create requires Linux")
}
