//go:build linux

package wsldocker

import "context"

// InspectContainer returns the bounded immutable lifecycle subset for one exact
// container reached through the proven Docker Desktop WSL socket.
func InspectContainer(ctx context.Context, containerID string) (ContainerSnapshot, error) {
	return inspectContainer(ctx, containerID, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxContainerInspectOutput, 0)
		},
	})
}
