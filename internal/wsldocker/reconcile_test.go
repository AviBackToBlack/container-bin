package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestDiscoverRetainedContainersUsesExactNamespaceFilters(t *testing.T) {
	firstID := "a" + testContainerID[1:]
	secondID := "b" + testContainerID[1:]
	listed := []map[string]any{
		{"Id": secondID, "Labels": containerLabels(Container{namespace: testWSLNamespace, runID: testRunID, tool: "python313"})},
		{"Id": firstID, "Labels": containerLabels(Container{namespace: testWSLNamespace, runID: "abcdefabcdefabcdefabcdefabcdefab", tool: "node24"})},
	}
	raw, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := discoverRetainedContainers(context.Background(), testWSLNamespace, func(_ context.Context, request Request) (Response, error) {
		if request.Method != http.MethodGet || request.Path != "/containers/json" || request.Query.Get("all") != "true" {
			t.Fatalf("discovery request = %#v", request)
		}
		var filters map[string][]string
		if err := json.Unmarshal([]byte(request.Query.Get("filters")), &filters); err != nil {
			t.Fatalf("decode filters: %v", err)
		}
		want := []string{"cb.managed=true", "cb.kind=run", containerNamespaceLabel + "=" + testWSLNamespace}
		if !reflect.DeepEqual(filters["label"], want) {
			t.Fatalf("label filters = %#v, want %#v", filters["label"], want)
		}
		return Response{StatusCode: http.StatusOK, Body: raw}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].ID() != firstID || candidates[1].ID() != secondID {
		t.Fatalf("sorted candidates = %#v", candidates)
	}
}

func TestDiscoverRetainedContainersRejectsUntrustedShapes(t *testing.T) {
	validLabels := containerLabels(Container{namespace: testWSLNamespace, runID: testRunID, tool: "node24"})
	tests := []struct {
		name   string
		id     string
		labels map[string]string
	}{
		{name: "short ID", id: "short", labels: validLabels},
		{name: "foreign namespace", id: testContainerID, labels: mutateContainerLabels(validLabels, func(labels map[string]string) {
			labels[containerNamespaceLabel] = "wsl2-abcdefabcdefabcdefabcdefabcdefab"
		})},
		{name: "missing managed", id: testContainerID, labels: mutateContainerLabels(validLabels, func(labels map[string]string) { delete(labels, "cb.managed") })},
		{name: "bad run ID", id: testContainerID, labels: mutateContainerLabels(validLabels, func(labels map[string]string) { labels[containerRunIDLabel] = "bad" })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal([]map[string]any{{"Id": test.id, "Labels": test.labels}})
			_, err := discoverRetainedContainers(context.Background(), testWSLNamespace, func(context.Context, Request) (Response, error) {
				return Response{StatusCode: http.StatusOK, Body: raw}, nil
			})
			if err == nil {
				t.Fatal("unsafe discovery candidate was accepted")
			}
		})
	}
}

func TestProveRetainedContainerRequiresExactRetainedRuntime(t *testing.T) {
	candidate := ContainerCandidate{id: testContainerID, runID: testRunID, namespace: testWSLNamespace, tool: "node24"}
	wantContainer := Container{id: testContainerID, runID: testRunID, namespace: testWSLNamespace, tool: "node24", retainUntilCleanup: true}
	snapshot := ContainerSnapshot{
		id: testContainerID, labels: containerLabels(wantContainer), running: true,
		attachStdin: true, attachStdout: true, attachStderr: true, openStdin: true, stdinOnce: true,
	}
	container, gotSnapshot, exists, err := proveRetainedContainer(context.Background(), candidate, testWSLNamespace, func(context.Context, string) (ContainerSnapshot, error) {
		return snapshot, nil
	})
	if err != nil || !exists || container != wantContainer || !gotSnapshot.Running() {
		t.Fatalf("proof = (%#v, %#v, %t, %v)", container, gotSnapshot, exists, err)
	}

	changed := snapshot
	changed.autoRemove = true
	if _, _, _, err := proveRetainedContainer(context.Background(), candidate, testWSLNamespace, func(context.Context, string) (ContainerSnapshot, error) {
		return changed, nil
	}); err == nil {
		t.Fatal("auto-remove candidate was adopted")
	}
	changed = snapshot
	changed.labels = mutateContainerLabels(snapshot.labels, func(labels map[string]string) { labels[containerToolLabel] = "foreign" })
	if _, _, _, err := proveRetainedContainer(context.Background(), candidate, testWSLNamespace, func(context.Context, string) (ContainerSnapshot, error) {
		return changed, nil
	}); err == nil {
		t.Fatal("mislabeled candidate was adopted")
	}
}

func TestProveRetainedContainerAcceptsDiscoveryRemovalRace(t *testing.T) {
	candidate := ContainerCandidate{id: testContainerID, runID: testRunID, namespace: testWSLNamespace, tool: "node24"}
	_, _, exists, err := proveRetainedContainer(context.Background(), candidate, testWSLNamespace, func(context.Context, string) (ContainerSnapshot, error) {
		return ContainerSnapshot{}, &APIError{Method: http.MethodGet, Path: "/containers/" + testContainerID + "/json", StatusCode: http.StatusNotFound}
	})
	if err != nil || exists {
		t.Fatalf("disappeared candidate = exists %t, error %v", exists, err)
	}
}

func mutateContainerLabels(labels map[string]string, mutate func(map[string]string)) map[string]string {
	cloned := cloneContainerLabels(labels)
	mutate(cloned)
	return cloned
}

func TestDiscoverRetainedContainersPropagatesExecutorFailure(t *testing.T) {
	want := errors.New("engine unavailable")
	_, err := discoverRetainedContainers(context.Background(), testWSLNamespace, func(context.Context, Request) (Response, error) {
		return Response{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("discovery error = %v, want executor failure", err)
	}
}
