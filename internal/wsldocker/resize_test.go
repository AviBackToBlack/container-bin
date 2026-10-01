package wsldocker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestResizeContainerBindsExactDimensionsToProvenSocket(t *testing.T) {
	socket := validSocketInfo()
	statCalls := 0
	deps := validOperationDependencies(socket)
	deps.statSocket = func(path string) (socketInfo, error) {
		if path != DockerSocketPath {
			t.Fatalf("stat path = %q", path)
		}
		statCalls++
		return socket, nil
	}
	deps.perform = func(_ context.Context, path string, request Request) (operationResult, error) {
		if path != DockerSocketPath || request.Method != http.MethodPost || request.Path != "/containers/"+testContainerID+"/resize" {
			t.Fatalf("perform(%q, %+v)", path, request)
		}
		if request.Query.Encode() != "h=24&w=80" || len(request.Body) != 0 || len(request.SuccessStatuses) != 1 || request.SuccessStatuses[0] != http.StatusOK {
			t.Fatalf("resize request = %+v", request)
		}
		return operationResult{StatusCode: http.StatusOK, PeerUID: 0}, nil
	}
	if err := resizeContainer(context.Background(), testContainerID, 24, 80, deps); err != nil {
		t.Fatal(err)
	}
	if statCalls != 2 {
		t.Fatalf("stat calls = %d", statCalls)
	}
}

func TestResizeContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	tests := map[string]struct {
		ctx         context.Context
		containerID string
		height      uint16
		width       uint16
	}{
		"nil context":  {containerID: testContainerID, height: 24, width: 80},
		"short ID":     {ctx: context.Background(), containerID: "abc", height: 24, width: 80},
		"uppercase ID": {ctx: context.Background(), containerID: strings.ToUpper(testContainerID), height: 24, width: 80},
		"non-hex ID":   {ctx: context.Background(), containerID: strings.Repeat("g", 64), height: 24, width: 80},
		"zero height":  {ctx: context.Background(), containerID: testContainerID, width: 80},
		"zero width":   {ctx: context.Background(), containerID: testContainerID, height: 24},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid resize") }
			if err := resizeContainer(test.ctx, test.containerID, test.height, test.width, deps); err == nil {
				t.Fatal("resizeContainer() succeeded")
			}
		})
	}

	if err := resizeContainer(context.Background(), testContainerID, 24, 80, operationDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete-dependencies error = %v", err)
	}
}
