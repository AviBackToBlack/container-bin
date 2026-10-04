//go:build !linux

package wsldocker

import "context"

func executeRetainedContainerDiscovery(ctx context.Context, request Request) (Response, error) {
	return Execute(ctx, request)
}
