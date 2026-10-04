//go:build linux

package wsldocker

import (
	"context"
	"testing"
	"time"
)

func TestRetainedContainerDiscoveryUsesItsDeclaredOutputBound(t *testing.T) {
	var gotLimit int64
	deps := retainedContainerDiscoveryDependencies(func(_ context.Context, _ string, _ Request, _ time.Duration, maxOutput int64, _ uint32) (operationResult, error) {
		gotLimit = maxOutput
		return operationResult{}, nil
	})
	if _, err := deps.perform(context.Background(), DockerSocketPath, Request{}); err != nil {
		t.Fatal(err)
	}
	if gotLimit != maxContainerListOutput || gotLimit <= maxOperationOutput {
		t.Fatalf("retained-container discovery limit = %d, want %d and greater than shared %d", gotLimit, maxContainerListOutput, maxOperationOutput)
	}
}
