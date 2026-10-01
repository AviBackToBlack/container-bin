//go:build linux

package wsldocker

import "context"

// StartContainer starts one exact stopped container through the proof-bound
// Docker Desktop WSL socket. Already-running containers fail closed.
func StartContainer(ctx context.Context, containerID string) error {
	return startContainer(ctx, containerID, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxOperationOutput, 0)
		},
	})
}
