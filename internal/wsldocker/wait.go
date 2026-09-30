package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

const maxContainerWaitOutput = 64 << 10

type containerWaitResponse struct {
	StatusCode *int64 `json:"StatusCode"`
	Error      *struct {
		Message string `json:"Message"`
	} `json:"Error"`
}

func waitContainer(ctx context.Context, containerID string, deps operationDependencies) (int, error) {
	if ctx == nil {
		return 0, errors.New("Docker Desktop WSL container wait requires a context")
	}
	if err := validateContainerID(containerID); err != nil {
		return 0, fmt.Errorf("Docker Desktop WSL container wait: %w", err)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return 0, errors.New("Docker Desktop WSL container wait dependencies are incomplete")
	}
	response, err := execute(ctx, Request{
		Method:          http.MethodPost,
		Path:            "/containers/" + containerID + "/wait",
		Query:           url.Values{"condition": {"not-running"}},
		SuccessStatuses: []int{http.StatusOK},
	}, deps)
	if err != nil {
		return 0, err
	}
	return decodeContainerWaitResponse(response.Body)
}

func decodeContainerWaitResponse(raw []byte) (int, error) {
	if len(raw) > maxContainerWaitOutput {
		return 0, fmt.Errorf("Docker container wait response exceeds %d bytes", maxContainerWaitOutput)
	}
	var response containerWaitResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return 0, fmt.Errorf("decode Docker container wait response: %w", err)
	}
	if response.Error != nil {
		if !validDockerMessage(response.Error.Message) {
			return 0, errors.New("Docker container wait returned an invalid error message")
		}
		return 0, fmt.Errorf("Docker container wait failed: %q", response.Error.Message)
	}
	if response.StatusCode == nil {
		return 0, errors.New("Docker container wait response is missing StatusCode")
	}
	if *response.StatusCode < 0 || *response.StatusCode > 255 {
		return 0, fmt.Errorf("Docker container wait returned invalid exit code %d", *response.StatusCode)
	}
	return int(*response.StatusCode), nil
}
