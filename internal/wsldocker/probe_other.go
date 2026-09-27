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
