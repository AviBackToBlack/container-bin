package wsldocker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRemoveContainerProvesOwnershipAndVerifiesAbsence(t *testing.T) {
	spec := testContainerCreateSpec()
	container := Container{id: testContainerID, namespace: spec.Namespace, runID: testRunID, tool: spec.Tool}
	deps := validOperationDependencies(validSocketInfo())
	calls := 0
	deps.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
		calls++
		switch calls {
		case 1:
			return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: ownedContainerInspect(container.id, spec, testRunID, false, true)}, nil
		case 2:
			if request.Method != http.MethodDelete || request.Path != "/containers/"+container.id || request.Query.Get("force") != "false" || request.Query.Get("v") != "false" || len(request.SuccessStatuses) != 1 || request.SuccessStatuses[0] != http.StatusNoContent {
				t.Fatalf("delete request = %#v", request)
			}
			return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
		case 3:
			return operationResult{StatusCode: http.StatusNotFound, PeerUID: 0, Raw: []byte(`{"message":"No such container"}`)}, nil
		default:
			t.Fatalf("unexpected request %d: %#v", calls, request)
			return operationResult{}, nil
		}
	}
	if err := removeContainer(context.Background(), container, deps); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("requests = %d", calls)
	}
}

func TestRemoveContainerAcceptsAlreadyAutoRemovedContainer(t *testing.T) {
	container := Container{id: testContainerID, namespace: testWSLNamespace, runID: testRunID, tool: "node24"}
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(context.Context, string, Request) (operationResult, error) {
		return operationResult{StatusCode: http.StatusNotFound, PeerUID: 0, Raw: []byte(`{"message":"No such container"}`)}, nil
	}
	if err := removeContainer(context.Background(), container, deps); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveContainerRejectsForeignRunningOrNonAutoRemoveContainer(t *testing.T) {
	for name, inspect := range map[string][]byte{
		"foreign":        ownedContainerInspect(testContainerID, testContainerCreateSpec(), strings.Repeat("0", 32), false, true),
		"running":        ownedContainerInspect(testContainerID, testContainerCreateSpec(), testRunID, true, true),
		"no auto-remove": ownedContainerInspect(testContainerID, testContainerCreateSpec(), testRunID, false, false),
	} {
		t.Run(name, func(t *testing.T) {
			container := Container{id: testContainerID, namespace: testWSLNamespace, runID: testRunID, tool: "node24"}
			deps := validOperationDependencies(validSocketInfo())
			calls := 0
			deps.perform = func(context.Context, string, Request) (operationResult, error) {
				calls++
				return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: inspect}, nil
			}
			if err := removeContainer(context.Background(), container, deps); err == nil {
				t.Fatal("removeContainer() succeeded")
			}
			if calls != 1 {
				t.Fatalf("requests = %d", calls)
			}
		})
	}
}

func TestRemoveContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	valid := Container{id: testContainerID, namespace: testWSLNamespace, runID: testRunID, tool: "node24"}
	for name, container := range map[string]Container{
		"ID":        {id: "short", namespace: valid.namespace, runID: valid.runID, tool: valid.tool},
		"namespace": {id: valid.id, namespace: "bad", runID: valid.runID, tool: valid.tool},
		"run ID":    {id: valid.id, namespace: valid.namespace, runID: "bad", tool: valid.tool},
		"tool":      {id: valid.id, namespace: valid.namespace, runID: valid.runID, tool: "Bad"},
	} {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid removal") }
			if err := removeContainer(context.Background(), container, deps); err == nil {
				t.Fatal("removeContainer() succeeded")
			}
		})
	}
	if err := removeContainer(nil, valid, validOperationDependencies(validSocketInfo())); err == nil {
		t.Fatal("removeContainer() accepted nil context")
	}
	if err := removeContainer(context.Background(), valid, operationDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete dependencies error = %v", err)
	}
}

func TestRemoveContainerFailsWhenObjectStillExistsAfterDelete(t *testing.T) {
	spec := testContainerCreateSpec()
	container := Container{id: testContainerID, namespace: spec.Namespace, runID: testRunID, tool: spec.Tool}
	deps := validOperationDependencies(validSocketInfo())
	calls := 0
	deps.perform = func(context.Context, string, Request) (operationResult, error) {
		calls++
		if calls == 2 {
			return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
		}
		return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: ownedContainerInspect(container.id, spec, testRunID, false, true)}, nil
	}
	if err := removeContainer(context.Background(), container, deps); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("remove verification error = %v", err)
	}
}
