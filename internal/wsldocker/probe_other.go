//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// Check is unavailable outside Linux because native WSL2 classification and
// Unix-socket ownership are part of the Docker integration boundary.
func Check(context.Context) (Result, error) {
	return Result{}, errors.New("Docker Desktop WSL integration check requires Linux")
}

// Execute is unavailable outside Linux because the fixed Unix socket and peer
// credentials are part of the Docker Desktop WSL trust boundary.
func Execute(context.Context, Request) (Response, error) {
	return Response{}, errors.New("Docker Desktop WSL operation requires Linux")
}
