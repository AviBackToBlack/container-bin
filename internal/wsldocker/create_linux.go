//go:build linux

package wsldocker

import (
	"context"
	"net/http"
	"path/filepath"
)

// CreateContainer creates and re-inspects one exact stopped container through
// the proven Docker Desktop WSL socket. It does not start the container.
func CreateContainer(ctx context.Context, spec ContainerCreateSpec) (Container, error) {
	return createContainer(ctx, spec, createDependencies{
		operations: operationDependencies{
			check:      Check,
			statSocket: statDockerSocket,
			perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
				maxOutput := int64(maxOperationOutput)
				if request.Method == http.MethodPost && request.Path == "/containers/create" {
					maxOutput = maxContainerCreateOutput
				}
				return performDockerRequest(ctx, socketPath, request, operationTimeout, maxOutput, 0)
			},
		},
		newRunID:          newContainerRunID,
		resolveBindSource: filepath.EvalSymlinks,
	})
}
