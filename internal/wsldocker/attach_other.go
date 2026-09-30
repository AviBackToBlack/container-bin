//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

// OpenAttach is unavailable outside Linux because the fixed Unix socket and
// peer credentials are part of the Docker Desktop WSL trust boundary.
func OpenAttach(context.Context, AttachRequest) (*AttachStream, error) {
	return nil, errors.New("Docker Desktop WSL attach requires Linux")
}
