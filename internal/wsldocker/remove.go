package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

func removeContainer(ctx context.Context, container Container, deps operationDependencies) error {
	if ctx == nil {
		return errors.New("Docker Desktop WSL container remove requires a context")
	}
	if err := validateContainerHandle(container); err != nil {
		return fmt.Errorf("Docker Desktop WSL container remove: %w", err)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return errors.New("Docker Desktop WSL container remove dependencies are incomplete")
	}
	snapshot, exists, err := inspectContainerIfExists(ctx, container.id, deps)
	if err != nil {
		return fmt.Errorf("inspect Docker container before removal: %w", err)
	}
	if !exists {
		return nil
	}
	if err := requireOwnedContainer(container, snapshot); err != nil {
		return fmt.Errorf("refuse Docker container removal: %w", err)
	}
	if !snapshot.AutoRemove() {
		return errors.New("refuse Docker container removal without auto-remove ownership configuration")
	}
	if snapshot.Running() {
		return errors.New("refuse non-force removal of a running Docker container")
	}
	if err := deleteContainer(ctx, container.id, deps); err != nil {
		return err
	}
	_, exists, err = inspectContainerIfExists(ctx, container.id, deps)
	if err != nil {
		return fmt.Errorf("verify Docker container removal: %w", err)
	}
	if exists {
		return errors.New("Docker container still exists after non-force removal")
	}
	return nil
}

func deleteContainer(ctx context.Context, containerID string, deps operationDependencies) error {
	_, err := execute(ctx, Request{
		Method: http.MethodDelete,
		Path:   "/containers/" + containerID,
		Query: url.Values{
			"force": {"false"},
			"v":     {"false"},
		},
		SuccessStatuses: []int{http.StatusNoContent},
	}, deps)
	if err != nil {
		return fmt.Errorf("remove stopped Docker container without force: %w", err)
	}
	return nil
}

func inspectContainerIfExists(ctx context.Context, containerID string, deps operationDependencies) (ContainerSnapshot, bool, error) {
	snapshot, err := inspectContainer(ctx, containerID, deps)
	if err == nil {
		return snapshot, true, nil
	}
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound {
		return ContainerSnapshot{}, false, nil
	}
	return ContainerSnapshot{}, false, err
}

func validateContainerHandle(container Container) error {
	if err := validateContainerID(container.id); err != nil {
		return err
	}
	if !validRunID(container.runID) || !validWSLNamespace(container.namespace) || !validRuntimeName(container.tool) {
		return errors.New("container ownership identity is invalid")
	}
	return nil
}
