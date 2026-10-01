package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const maxLinuxSignal = 64

func signalContainer(ctx context.Context, containerID string, signal int, deps operationDependencies) error {
	if ctx == nil {
		return errors.New("Docker Desktop WSL container signal requires a context")
	}
	if err := validateContainerID(containerID); err != nil {
		return fmt.Errorf("Docker Desktop WSL container signal: %w", err)
	}
	if signal < 1 || signal > maxLinuxSignal {
		return fmt.Errorf("Docker Desktop WSL container signal must be between 1 and %d", maxLinuxSignal)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return errors.New("Docker Desktop WSL container signal dependencies are incomplete")
	}
	_, err := execute(ctx, Request{
		Method: http.MethodPost,
		Path:   "/containers/" + containerID + "/kill",
		Query:  url.Values{"signal": {strconv.Itoa(signal)}},
		SuccessStatuses: []int{
			http.StatusNoContent,
		},
	}, deps)
	return err
}
