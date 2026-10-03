package wsldocker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestStartContainerBindsExactRequestToProvenSocket(t *testing.T) {
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
		if path != DockerSocketPath || request.Method != http.MethodPost || request.Path != "/containers/"+testContainerID+"/start" {
			t.Fatalf("perform(%q, %+v)", path, request)
		}
		if len(request.Query) != 0 || len(request.Body) != 0 || len(request.SuccessStatuses) != 1 || request.SuccessStatuses[0] != http.StatusNoContent {
			t.Fatalf("start request = %+v", request)
		}
		return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
	}
	if err := startContainer(context.Background(), testContainerID, deps); err != nil {
		t.Fatal(err)
	}
	if statCalls != 2 {
		t.Fatalf("stat calls = %d, want 2", statCalls)
	}
}

func TestStartContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	for name, containerID := range map[string]string{
		"short":     "abc",
		"uppercase": strings.ToUpper(testContainerID),
		"non hex":   strings.Repeat("g", 64),
	} {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid container ID") }
			if err := startContainer(context.Background(), containerID, deps); err == nil {
				t.Fatal("startContainer() succeeded")
			}
		})
	}

	deps := validOperationDependencies(validSocketInfo())
	deps.check = func(context.Context) (Result, error) { panic("proof reached with nil context") }
	if err := startContainer(nil, testContainerID, deps); err == nil {
		t.Fatal("startContainer() accepted nil context")
	}
	if err := startContainer(context.Background(), testContainerID, operationDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete-dependencies error = %v", err)
	}
}

func TestStartContainerRejectsAlreadyRunning(t *testing.T) {
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(context.Context, string, Request) (operationResult, error) {
		return operationResult{StatusCode: http.StatusNotModified, Raw: []byte(`{"message":"container is already running"}`), PeerUID: 0}, nil
	}
	err := startContainer(context.Background(), testContainerID, deps)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotModified || apiErr.Message != "container is already running" {
		t.Fatalf("startContainer() error = %#v", err)
	}
}
