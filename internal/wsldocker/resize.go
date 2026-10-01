package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

func resizeContainer(ctx context.Context, containerID string, height, width uint16, deps operationDependencies) error {
	if ctx == nil {
		return errors.New("Docker Desktop WSL container resize requires a context")
	}
	if err := validateContainerID(containerID); err != nil {
		return fmt.Errorf("Docker Desktop WSL container resize: %w", err)
	}
	if height == 0 {
		return errors.New("Docker Desktop WSL container resize requires a positive height")
	}
	if width == 0 {
		return errors.New("Docker Desktop WSL container resize requires a positive width")
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return errors.New("Docker Desktop WSL container resize dependencies are incomplete")
	}
	_, err := execute(ctx, Request{
		Method: http.MethodPost,
		Path:   "/containers/" + containerID + "/resize",
		Query: url.Values{
			"h": {strconv.FormatUint(uint64(height), 10)},
			"w": {strconv.FormatUint(uint64(width), 10)},
		},
		SuccessStatuses: []int{http.StatusOK},
	}, deps)
	return err
}
