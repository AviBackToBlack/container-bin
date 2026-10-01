//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// InspectContainer is unavailable outside Linux because the fixed Unix socket
// and peer credentials are part of the Docker Desktop WSL trust boundary.
func InspectContainer(context.Context, string) (ContainerSnapshot, error) {
	return ContainerSnapshot{}, errors.New("Docker Desktop WSL container inspect requires Linux")
}
