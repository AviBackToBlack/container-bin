//go:build linux

package wsldocker

import (
	"context"
	"time"
)

const imagePullTimeout = 15 * time.Minute

// PullImage pulls one public registry image through Docker Desktop's proven
// WSL Engine endpoint. No ambient Docker CLI context or credential file is
// consulted.
func PullImage(ctx context.Context, reference string) error {
	return pullImage(ctx, reference, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, imagePullTimeout, maxImagePullOutput, 0)
		},
	})
}

// InspectImage returns the bounded immutable identity for one exact image
// reference through Docker Desktop's proven WSL Engine endpoint.
func InspectImage(ctx context.Context, reference string) (ImageSnapshot, error) {
	return inspectImage(ctx, reference, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxImageInspectOutput, 0)
		},
	})
}
