//go:build linux

package wsldocker

import "context"

// ResizeContainer resizes the TTY for one exact container. Dimensions must be
// positive and fit the terminal's unsigned 16-bit row/column representation.
func ResizeContainer(ctx context.Context, containerID string, height, width uint16) error {
	return resizeContainer(ctx, containerID, height, width, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxOperationOutput, 0)
		},
	})
}
