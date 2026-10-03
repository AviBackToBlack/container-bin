package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const maxContainerInspectOutput = 1 << 20

// ContainerSnapshot is the bounded immutable subset of one exact Docker
// container inspection needed by later ownership and lifecycle checks.
type ContainerSnapshot struct {
	id           string
	labels       map[string]string
	running      bool
	tty          bool
	attachStdin  bool
	attachStdout bool
	attachStderr bool
	openStdin    bool
	stdinOnce    bool
	autoRemove   bool
}

func (s ContainerSnapshot) ID() string                { return s.id }
func (s ContainerSnapshot) Running() bool             { return s.running }
func (s ContainerSnapshot) TTY() bool                 { return s.tty }
func (s ContainerSnapshot) AttachStdin() bool         { return s.attachStdin }
func (s ContainerSnapshot) AttachStdout() bool        { return s.attachStdout }
func (s ContainerSnapshot) AttachStderr() bool        { return s.attachStderr }
func (s ContainerSnapshot) OpenStdin() bool           { return s.openStdin }
func (s ContainerSnapshot) StdinOnce() bool           { return s.stdinOnce }
func (s ContainerSnapshot) AutoRemove() bool          { return s.autoRemove }
func (s ContainerSnapshot) Labels() map[string]string { return cloneContainerLabels(s.labels) }

func inspectContainer(ctx context.Context, containerID string, deps operationDependencies) (ContainerSnapshot, error) {
	if ctx == nil {
		return ContainerSnapshot{}, errors.New("Docker Desktop WSL container inspect requires a context")
	}
	if err := validateContainerID(containerID); err != nil {
		return ContainerSnapshot{}, fmt.Errorf("Docker Desktop WSL container inspect: %w", err)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return ContainerSnapshot{}, errors.New("Docker Desktop WSL container inspect dependencies are incomplete")
	}
	response, err := execute(ctx, Request{
		Method:          http.MethodGet,
		Path:            "/containers/" + containerID + "/json",
		SuccessStatuses: []int{http.StatusOK},
	}, deps)
	if err != nil {
		return ContainerSnapshot{}, err
	}
	return decodeContainerInspectResponse(response.Body, containerID)
}

func decodeContainerInspectResponse(raw []byte, expectedID string) (ContainerSnapshot, error) {
	if len(raw) > maxContainerInspectOutput {
		return ContainerSnapshot{}, fmt.Errorf("Docker container inspect response exceeds %d bytes", maxContainerInspectOutput)
	}
	var response struct {
		ID     string `json:"Id"`
		Config *struct {
			Labels       map[string]string `json:"Labels"`
			TTY          *bool             `json:"Tty"`
			AttachStdin  *bool             `json:"AttachStdin"`
			AttachStdout *bool             `json:"AttachStdout"`
			AttachStderr *bool             `json:"AttachStderr"`
			OpenStdin    *bool             `json:"OpenStdin"`
			StdinOnce    *bool             `json:"StdinOnce"`
		} `json:"Config"`
		State *struct {
			Running *bool `json:"Running"`
		} `json:"State"`
		HostConfig *struct {
			AutoRemove *bool `json:"AutoRemove"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ContainerSnapshot{}, fmt.Errorf("decode Docker container inspect response: %w", err)
	}
	if err := validateContainerID(response.ID); err != nil {
		return ContainerSnapshot{}, fmt.Errorf("Docker container inspect returned invalid ID: %w", err)
	}
	if response.ID != expectedID {
		return ContainerSnapshot{}, fmt.Errorf("Docker container inspect returned ID %q, expected exact requested ID", response.ID)
	}
	if response.Config == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config")
	}
	if response.State == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing State")
	}
	if response.Config.TTY == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config.Tty")
	}
	if response.Config.AttachStdin == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config.AttachStdin")
	}
	if response.Config.AttachStdout == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config.AttachStdout")
	}
	if response.Config.AttachStderr == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config.AttachStderr")
	}
	if response.Config.OpenStdin == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config.OpenStdin")
	}
	if response.Config.StdinOnce == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing Config.StdinOnce")
	}
	if response.State.Running == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing State.Running")
	}
	if response.HostConfig == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing HostConfig")
	}
	if response.HostConfig.AutoRemove == nil {
		return ContainerSnapshot{}, errors.New("Docker container inspect response is missing HostConfig.AutoRemove")
	}
	return ContainerSnapshot{
		id:           response.ID,
		labels:       cloneContainerLabels(response.Config.Labels),
		running:      *response.State.Running,
		tty:          *response.Config.TTY,
		attachStdin:  *response.Config.AttachStdin,
		attachStdout: *response.Config.AttachStdout,
		attachStderr: *response.Config.AttachStderr,
		openStdin:    *response.Config.OpenStdin,
		stdinOnce:    *response.Config.StdinOnce,
		autoRemove:   *response.HostConfig.AutoRemove,
	}, nil
}

func cloneContainerLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}
