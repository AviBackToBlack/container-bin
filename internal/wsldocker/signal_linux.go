//go:build linux

package wsldocker

import "context"

// SignalContainer sends one explicit Linux signal number to one exact running
// container. Signal numbers outside the Linux 1..64 domain fail closed.
func SignalContainer(ctx context.Context, containerID string, signal int) error {
	return signalContainer(ctx, containerID, signal, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxOperationOutput, 0)
		},
	})
}
