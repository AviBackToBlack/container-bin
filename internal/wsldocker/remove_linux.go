//go:build linux

package wsldocker

import "context"

// RemoveContainer removes one stopped container only after re-proving its exact
// ContainerBin WSL ownership labels and retention mode. Already auto-removed
// containers succeed.
func RemoveContainer(ctx context.Context, container Container) error {
	return removeContainer(ctx, container, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxOperationOutput, 0)
		},
	})
}
