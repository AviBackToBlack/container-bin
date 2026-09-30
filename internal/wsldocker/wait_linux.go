//go:build linux

package wsldocker

import "context"

// WaitContainer waits for one exact container to stop and returns its validated
// process exit code. The caller's context is the only duration bound.
func WaitContainer(ctx context.Context, containerID string) (int, error) {
	return waitContainer(ctx, containerID, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, 0, maxContainerWaitOutput, 0)
		},
	})
}
