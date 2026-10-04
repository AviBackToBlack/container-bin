//go:build linux

package wsldocker

import (
	"context"
	"time"
)

type boundedControlPerformFunc func(context.Context, string, Request, time.Duration, int64, uint32) (operationResult, error)

func executeRetainedContainerDiscovery(ctx context.Context, request Request) (Response, error) {
	return execute(ctx, request, retainedContainerDiscoveryDependencies(performDockerRequest))
}

func retainedContainerDiscoveryDependencies(perform boundedControlPerformFunc) operationDependencies {
	return operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return perform(ctx, socketPath, request, operationTimeout, maxContainerListOutput, 0)
		},
	}
}
