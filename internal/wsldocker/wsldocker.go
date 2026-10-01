// Package wsldocker proves that a native WSL2 process is connected to Docker
// Desktop's supported WSL integration rather than an in-distribution or remote
// Docker Engine, and provides proof-bound bounded control requests plus
// separately constrained container-attach, inspect, wait, TTY-resize and
// signal transports. It does not enable the WSL frontend by itself.
package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"unicode"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	DockerSocketPath = "/var/run/docker.sock"
	DockerHost       = "unix:///var/run/docker.sock"
	maxRequestPath   = 4096
)

var dockerRedirectVariables = []string{
	"DOCKER_API_VERSION",
	"DOCKER_CERT_PATH",
	"DOCKER_CONFIG",
	"DOCKER_CONTEXT",
	"DOCKER_HOST",
	"DOCKER_TLS",
	"DOCKER_TLS_VERIFY",
}

// Result is the exact Docker Desktop endpoint and server identity accepted by
// the WSL integration check. It is not a durable authorization: callers must
// use Execute for each operation rather than retaining this result or falling
// back to ambient contexts.
type Result struct {
	Host           string
	ServerVersion  string
	KernelVersion  string
	DesktopAddress string
	socket         socketInfo
}

// Request is one bounded Docker Engine control-plane request. OpenAttach and
// WaitContainer own separate, narrower streaming and long-poll contracts.
type Request struct {
	Method          string
	Path            string
	Query           url.Values
	Body            []byte
	SuccessStatuses []int
}

// Response is the bounded status and body returned by an accepted control-plane
// request. Execute returns no Response for an unexpected status or boundary
// failure.
type Response struct {
	StatusCode int
	Body       []byte
}

// APIError reports a bounded Docker Engine error response without requiring
// lifecycle callers to parse an error string.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Docker Desktop Engine API %s %s returned HTTP %d: %q", e.Method, e.Path, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("Docker Desktop Engine API %s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
}

type socketInfo struct {
	Mode os.FileMode
	UID  uint32
	Dev  uint64
	Ino  uint64
}

type probeResult struct {
	Raw     []byte
	PeerUID uint32
}

type dependencies struct {
	currentRuntime func() (hostenv.Runtime, error)
	lookupEnv      func(string) (string, bool)
	statSocket     func(string) (socketInfo, error)
	probeInfo      func(context.Context, string) (probeResult, error)
}

type operationResult struct {
	StatusCode int
	Raw        []byte
	PeerUID    uint32
}

type operationDependencies struct {
	check      func(context.Context) (Result, error)
	statSocket func(string) (socketInfo, error)
	perform    func(context.Context, string, Request) (operationResult, error)
}

func check(ctx context.Context, d dependencies) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("Docker Desktop WSL integration check requires a context")
	}
	runtime, err := d.currentRuntime()
	if err != nil {
		return Result{}, fmt.Errorf("classify native WSL runtime: %w", err)
	}
	if runtime.Kind != hostenv.WSL2Native {
		return Result{}, fmt.Errorf("Docker Desktop WSL integration requires runtime kind %q, got %q", hostenv.WSL2Native, runtime.Kind)
	}
	if runtime.Distro == "" || strings.TrimSpace(runtime.Distro) != runtime.Distro {
		return Result{}, errors.New("Docker Desktop WSL integration requires canonical WSL_DISTRO_NAME")
	}
	for _, r := range runtime.Distro {
		if unicode.IsControl(r) {
			return Result{}, errors.New("Docker Desktop WSL integration requires canonical WSL_DISTRO_NAME")
		}
	}
	for _, name := range dockerRedirectVariables {
		if value, ok := d.lookupEnv(name); ok && value != "" {
			return Result{}, fmt.Errorf("%s is set; refusing ambiguous Docker endpoint configuration", name)
		}
	}
	before, err := d.statSocket(DockerSocketPath)
	if err != nil {
		return Result{}, fmt.Errorf("inspect Docker Desktop WSL socket %s: %w", DockerSocketPath, err)
	}
	if err := validateSocket(before); err != nil {
		return Result{}, err
	}
	probe, err := d.probeInfo(ctx, DockerSocketPath)
	if err != nil {
		return Result{}, err
	}
	if probe.PeerUID != 0 {
		return Result{}, fmt.Errorf("Docker Desktop WSL socket peer is UID %d, expected root", probe.PeerUID)
	}
	after, err := d.statSocket(DockerSocketPath)
	if err != nil {
		return Result{}, fmt.Errorf("reinspect Docker Desktop WSL socket %s: %w", DockerSocketPath, err)
	}
	if err := validateSocket(after); err != nil {
		return Result{}, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino {
		return Result{}, errors.New("Docker Desktop WSL socket changed during the engine identity probe")
	}
	result, err := validateInfo(probe.Raw)
	if err != nil {
		return Result{}, err
	}
	result.Host = DockerHost
	result.socket = after
	return result, nil
}

func execute(ctx context.Context, request Request, d operationDependencies) (Response, error) {
	if ctx == nil {
		return Response{}, errors.New("Docker Desktop WSL operation requires a context")
	}
	if err := validateRequest(request); err != nil {
		return Response{}, err
	}
	request = cloneRequest(request)
	checked, err := d.check(ctx)
	if err != nil {
		return Response{}, fmt.Errorf("prove Docker Desktop WSL integration before operation: %w", err)
	}
	before, err := d.statSocket(DockerSocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("inspect Docker Desktop WSL socket before operation: %w", err)
	}
	if err := validateSocket(before); err != nil {
		return Response{}, err
	}
	if !sameSocket(checked.socket, before) {
		return Response{}, errors.New("Docker Desktop WSL socket changed after the engine identity proof")
	}
	operation, err := d.perform(ctx, DockerSocketPath, request)
	if err != nil {
		return Response{}, err
	}
	if operation.PeerUID != 0 {
		return Response{}, fmt.Errorf("Docker Desktop WSL operation socket peer is UID %d, expected root", operation.PeerUID)
	}
	after, err := d.statSocket(DockerSocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("inspect Docker Desktop WSL socket after operation: %w", err)
	}
	if err := validateSocket(after); err != nil {
		return Response{}, err
	}
	if !sameSocket(before, after) {
		return Response{}, errors.New("Docker Desktop WSL socket changed during the engine operation")
	}
	if !acceptedStatus(operation.StatusCode, request.SuccessStatuses) {
		return Response{}, &APIError{
			Method:     request.Method,
			Path:       request.Path,
			StatusCode: operation.StatusCode,
			Message:    dockerErrorMessage(operation.Raw),
		}
	}
	return Response{StatusCode: operation.StatusCode, Body: operation.Raw}, nil
}

func dockerErrorMessage(raw []byte) string {
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || !validDockerMessage(response.Message) {
		return ""
	}
	return response.Message
}

func validDockerMessage(message string) bool {
	if message == "" || len(message) > 4096 || strings.TrimSpace(message) != message {
		return false
	}
	for _, r := range message {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func cloneRequest(request Request) Request {
	cloned := request
	cloned.Query = make(url.Values, len(request.Query))
	for key, values := range request.Query {
		cloned.Query[key] = append([]string(nil), values...)
	}
	cloned.Body = append([]byte(nil), request.Body...)
	cloned.SuccessStatuses = append([]int(nil), request.SuccessStatuses...)
	return cloned
}

func validateRequest(request Request) error {
	switch request.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete:
	default:
		return fmt.Errorf("unsupported Docker Desktop Engine API method %q", request.Method)
	}
	if request.Path == "" || !strings.HasPrefix(request.Path, "/") {
		return fmt.Errorf("invalid Docker Desktop Engine API path %q", request.Path)
	}
	if len(request.Path) > maxRequestPath {
		return fmt.Errorf("Docker Desktop Engine API path exceeds %d bytes", maxRequestPath)
	}
	if path.Clean(request.Path) != request.Path || strings.ContainsAny(request.Path, "\\?#") {
		return fmt.Errorf("invalid Docker Desktop Engine API path %q", request.Path)
	}
	for _, r := range request.Path {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid Docker Desktop Engine API path %q", request.Path)
		}
	}
	if len(request.Body) > 1<<20 {
		return errors.New("Docker Desktop Engine API request body exceeds 1048576 bytes")
	}
	if request.Method != http.MethodPost && len(request.Body) != 0 {
		return fmt.Errorf("Docker Desktop Engine API %s request cannot carry a body", request.Method)
	}
	if len(request.Body) != 0 && !json.Valid(request.Body) {
		return errors.New("Docker Desktop Engine API request body is not valid JSON")
	}
	if len(request.Query.Encode()) > 64<<10 {
		return errors.New("Docker Desktop Engine API query exceeds 65536 bytes")
	}
	if len(request.SuccessStatuses) == 0 {
		return errors.New("Docker Desktop Engine API request requires an explicit success status")
	}
	for _, status := range request.SuccessStatuses {
		if (status < 200 || status >= 300) && status != http.StatusNotModified {
			return fmt.Errorf("invalid Docker Desktop Engine API success status %d", status)
		}
	}
	return nil
}

func acceptedStatus(status int, accepted []int) bool {
	for _, candidate := range accepted {
		if status == candidate {
			return true
		}
	}
	return false
}

func sameSocket(first, second socketInfo) bool {
	return first.Mode == second.Mode && first.UID == second.UID && first.Dev == second.Dev && first.Ino == second.Ino
}

func validateSocket(socket socketInfo) error {
	if socket.Mode&os.ModeSocket == 0 {
		return fmt.Errorf("Docker Desktop WSL endpoint %s is not a Unix socket", DockerSocketPath)
	}
	if socket.UID != 0 {
		return fmt.Errorf("Docker Desktop WSL endpoint %s is owned by UID %d, expected root", DockerSocketPath, socket.UID)
	}
	if socket.Mode.Perm()&0o002 != 0 {
		return fmt.Errorf("Docker Desktop WSL endpoint %s is world-writable (mode %04o)", DockerSocketPath, socket.Mode.Perm())
	}
	return nil
}

type engineInfo struct {
	ServerVersion   string   `json:"ServerVersion"`
	KernelVersion   string   `json:"KernelVersion"`
	OperatingSystem string   `json:"OperatingSystem"`
	OSType          string   `json:"OSType"`
	Name            string   `json:"Name"`
	Labels          []string `json:"Labels"`
}

func validateInfo(raw []byte) (Result, error) {
	var info engineInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return Result{}, fmt.Errorf("decode Docker Desktop engine identity: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"server version", info.ServerVersion},
		{"kernel version", info.KernelVersion},
		{"operating system", info.OperatingSystem},
		{"OS type", info.OSType},
		{"engine name", info.Name},
	} {
		if err := validateField(field.name, field.value); err != nil {
			return Result{}, err
		}
	}
	if info.OperatingSystem != "Docker Desktop" {
		return Result{}, fmt.Errorf("Docker engine operating system is %q, expected %q", info.OperatingSystem, "Docker Desktop")
	}
	if info.OSType != "linux" {
		return Result{}, fmt.Errorf("Docker Desktop engine OS type is %q, expected %q", info.OSType, "linux")
	}
	if info.Name != "docker-desktop" {
		return Result{}, fmt.Errorf("Docker engine name is %q, expected %q", info.Name, "docker-desktop")
	}
	kernel := strings.ToLower(info.KernelVersion)
	if !strings.Contains(kernel, "microsoft") || !strings.Contains(kernel, "wsl2") {
		return Result{}, fmt.Errorf("Docker Desktop engine kernel %q is not an explicit Microsoft WSL2 kernel", info.KernelVersion)
	}

	const addressKey = "com.docker.desktop.address"
	address := ""
	for _, label := range info.Labels {
		key, value, ok := strings.Cut(label, "=")
		if !ok || key != addressKey {
			continue
		}
		if address != "" {
			return Result{}, fmt.Errorf("Docker Desktop engine reports duplicate %s labels", addressKey)
		}
		if err := validateField("Docker Desktop address label", value); err != nil {
			return Result{}, err
		}
		if !strings.HasPrefix(value, "unix://") && !strings.HasPrefix(value, "npipe://") {
			return Result{}, fmt.Errorf("Docker Desktop address label uses unsupported endpoint %q", value)
		}
		address = value
	}
	if address == "" {
		return Result{}, fmt.Errorf("Docker engine is missing the %s label", addressKey)
	}
	return Result{
		ServerVersion:  info.ServerVersion,
		KernelVersion:  info.KernelVersion,
		DesktopAddress: address,
	}, nil
}

func validateField(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("Docker engine %s is empty or not canonical", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("Docker engine %s contains a control character", name)
		}
	}
	return nil
}
