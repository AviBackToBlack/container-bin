package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

const maxContainerListOutput = 4 << 20

// ContainerCandidate is untrusted namespace-filtered discovery output. It must
// be passed to ProveRetainedContainer before any lifecycle mutation.
type ContainerCandidate struct {
	id        string
	runID     string
	namespace string
	tool      string
}

func (c ContainerCandidate) ID() string        { return c.id }
func (c ContainerCandidate) RunID() string     { return c.runID }
func (c ContainerCandidate) Namespace() string { return c.namespace }
func (c ContainerCandidate) Tool() string      { return c.tool }

type controlExecuteFunc func(context.Context, Request) (Response, error)
type containerInspectFunc func(context.Context, string) (ContainerSnapshot, error)

// DiscoverRetainedContainers returns discovery-only candidates selected by the
// exact managed-run namespace labels. The Docker list response is never treated
// as ownership proof.
func DiscoverRetainedContainers(ctx context.Context, namespace string) ([]ContainerCandidate, error) {
	return discoverRetainedContainers(ctx, namespace, executeRetainedContainerDiscovery)
}

// ProveRetainedContainer re-inspects one discovery candidate and returns an
// immutable owned handle only when its labels, retained lifecycle and stdio
// contract exactly match a ContainerBin native-WSL run. A candidate that
// disappeared after discovery is reported as exists=false.
func ProveRetainedContainer(ctx context.Context, candidate ContainerCandidate, namespace string) (Container, ContainerSnapshot, bool, error) {
	return proveRetainedContainer(ctx, candidate, namespace, InspectContainer)
}

func discoverRetainedContainers(ctx context.Context, namespace string, execute controlExecuteFunc) ([]ContainerCandidate, error) {
	if ctx == nil {
		return nil, errors.New("native WSL retained-container discovery requires a context")
	}
	if !validWSLNamespace(namespace) {
		return nil, fmt.Errorf("native WSL retained-container discovery requires a valid namespace, got %q", namespace)
	}
	if execute == nil {
		return nil, errors.New("native WSL retained-container discovery executor is unavailable")
	}
	filters, err := json.Marshal(map[string][]string{
		"label": {
			"cb.managed=true",
			"cb.kind=run",
			containerNamespaceLabel + "=" + namespace,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode native WSL retained-container discovery filters: %w", err)
	}
	response, err := execute(ctx, Request{
		Method: http.MethodGet,
		Path:   "/containers/json",
		Query: url.Values{
			"all":     {"true"},
			"filters": {string(filters)},
		},
		SuccessStatuses: []int{http.StatusOK},
	})
	if err != nil {
		return nil, fmt.Errorf("discover native WSL retained containers: %w", err)
	}
	if len(response.Body) > maxContainerListOutput {
		return nil, fmt.Errorf("Docker container list response exceeds %d bytes", maxContainerListOutput)
	}
	var listed []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(response.Body, &listed); err != nil {
		return nil, fmt.Errorf("decode native WSL retained-container list: %w", err)
	}
	candidates := make([]ContainerCandidate, 0, len(listed))
	seen := make(map[string]bool, len(listed))
	for index, listedContainer := range listed {
		candidate, err := decodeContainerCandidate(listedContainer.ID, listedContainer.Labels, namespace)
		if err != nil {
			return nil, fmt.Errorf("validate native WSL retained-container candidate %d: %w", index, err)
		}
		if seen[candidate.id] {
			return nil, fmt.Errorf("Docker container list returned duplicate ID %s", candidate.id)
		}
		seen[candidate.id] = true
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
	return candidates, nil
}

func decodeContainerCandidate(id string, labels map[string]string, namespace string) (ContainerCandidate, error) {
	if err := validateContainerID(id); err != nil {
		return ContainerCandidate{}, fmt.Errorf("invalid container ID: %w", err)
	}
	if labels["cb.managed"] != "true" || labels["cb.kind"] != "run" || labels[containerNamespaceLabel] != namespace {
		return ContainerCandidate{}, errors.New("candidate does not match the exact managed run namespace labels")
	}
	runID := labels[containerRunIDLabel]
	tool := labels[containerToolLabel]
	if !validRunID(runID) || !validRuntimeName(tool) {
		return ContainerCandidate{}, errors.New("candidate run or tool identity is invalid")
	}
	return ContainerCandidate{id: id, runID: runID, namespace: namespace, tool: tool}, nil
}

func proveRetainedContainer(ctx context.Context, candidate ContainerCandidate, namespace string, inspect containerInspectFunc) (Container, ContainerSnapshot, bool, error) {
	if ctx == nil {
		return Container{}, ContainerSnapshot{}, false, errors.New("native WSL retained-container proof requires a context")
	}
	if inspect == nil {
		return Container{}, ContainerSnapshot{}, false, errors.New("native WSL retained-container inspector is unavailable")
	}
	if !validWSLNamespace(namespace) || candidate.namespace != namespace || !validRunID(candidate.runID) || !validRuntimeName(candidate.tool) {
		return Container{}, ContainerSnapshot{}, false, errors.New("native WSL retained-container candidate identity is invalid")
	}
	if err := validateContainerID(candidate.id); err != nil {
		return Container{}, ContainerSnapshot{}, false, fmt.Errorf("native WSL retained-container candidate: %w", err)
	}
	snapshot, err := inspect(ctx, candidate.id)
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound {
			return Container{}, ContainerSnapshot{}, false, nil
		}
		return Container{}, ContainerSnapshot{}, false, fmt.Errorf("inspect native WSL retained-container candidate: %w", err)
	}
	container := Container{
		id: candidate.id, runID: candidate.runID, namespace: namespace,
		tool: candidate.tool, retainUntilCleanup: true,
	}
	if err := requireOwnedContainer(container, snapshot); err != nil {
		return Container{}, ContainerSnapshot{}, false, fmt.Errorf("refuse native WSL retained-container adoption: %w", err)
	}
	if snapshot.AutoRemove() || !snapshot.AttachStdin() || !snapshot.AttachStdout() || !snapshot.AttachStderr() || !snapshot.OpenStdin() || !snapshot.StdinOnce() {
		return Container{}, ContainerSnapshot{}, false, errors.New("refuse native WSL retained-container adoption whose retention or stdio contract changed")
	}
	return container, snapshot, true, nil
}
