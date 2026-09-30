package wsldocker

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSignalContainerBindsExactSignalToProvenSocket(t *testing.T) {
	for _, signal := range []int{1, 2, maxLinuxSignal} {
		t.Run(fmtSignal(signal), func(t *testing.T) {
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
				if path != DockerSocketPath || request.Method != http.MethodPost || request.Path != "/containers/"+testContainerID+"/kill" {
					t.Fatalf("perform(%q, %+v)", path, request)
				}
				if request.Query.Encode() != "signal="+fmtSignal(signal) || len(request.Body) != 0 || len(request.SuccessStatuses) != 1 || request.SuccessStatuses[0] != http.StatusNoContent {
					t.Fatalf("signal request = %+v", request)
				}
				return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
			}
			if err := signalContainer(context.Background(), testContainerID, signal, deps); err != nil {
				t.Fatal(err)
			}
			if statCalls != 2 {
				t.Fatalf("stat calls = %d", statCalls)
			}
		})
	}
}

func TestSignalContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	tests := map[string]struct {
		ctx         context.Context
		containerID string
		signal      int
	}{
		"nil context":  {containerID: testContainerID, signal: 2},
		"short ID":     {ctx: context.Background(), containerID: "abc", signal: 2},
		"uppercase ID": {ctx: context.Background(), containerID: strings.ToUpper(testContainerID), signal: 2},
		"zero signal":  {ctx: context.Background(), containerID: testContainerID},
		"negative":     {ctx: context.Background(), containerID: testContainerID, signal: -1},
		"too large":    {ctx: context.Background(), containerID: testContainerID, signal: maxLinuxSignal + 1},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid signal") }
			if err := signalContainer(test.ctx, test.containerID, test.signal, deps); err == nil {
				t.Fatal("signalContainer() succeeded")
			}
		})
	}

	if err := signalContainer(context.Background(), testContainerID, 2, operationDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete-dependencies error = %v", err)
	}
}

func fmtSignal(signal int) string {
	return strconv.Itoa(signal)
}
