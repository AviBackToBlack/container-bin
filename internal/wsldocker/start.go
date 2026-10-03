package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

func startContainer(ctx context.Context, containerID string, deps operationDependencies) error {
	if ctx == nil {
		return errors.New("Docker Desktop WSL container start requires a context")
	}
	if err := validateContainerID(containerID); err != nil {
		return fmt.Errorf("Docker Desktop WSL container start: %w", err)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return errors.New("Docker Desktop WSL container start dependencies are incomplete")
	}
	_, err := execute(ctx, Request{
		Method:          http.MethodPost,
		Path:            "/containers/" + containerID + "/start",
		SuccessStatuses: []int{http.StatusNoContent},
	}, deps)
	return err
}
