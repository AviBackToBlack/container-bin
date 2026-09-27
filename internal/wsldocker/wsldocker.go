// Package wsldocker proves that a native WSL2 process is connected to Docker
// Desktop's supported WSL integration rather than an in-distribution or remote
// Docker Engine. It does not enable the WSL frontend by itself.
package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	DockerSocketPath = "/var/run/docker.sock"
	DockerHost       = "unix:///var/run/docker.sock"
)

var dockerRedirectVariables = []string{
	"DOCKER_API_VERSION",
	"DOCKER_CERT_PATH",
	"DOCKER_CONTEXT",
	"DOCKER_HOST",
	"DOCKER_TLS",
	"DOCKER_TLS_VERIFY",
}

// Result is the exact Docker Desktop endpoint and server identity accepted by
// the WSL integration check. Later frontend wiring must retain DockerHost for
// every Docker invocation rather than falling back to ambient contexts.
type Result struct {
	DockerPath     string
	Host           string
	ServerVersion  string
	KernelVersion  string
	DesktopAddress string
}

type socketInfo struct {
	Mode os.FileMode
	UID  uint32
}

type dependencies struct {
	currentRuntime func() (hostenv.Runtime, error)
	lookupEnv      func(string) (string, bool)
	lookPath       func(string) (string, error)
	statSocket     func(string) (socketInfo, error)
	runInfo        func(context.Context, string, string) ([]byte, error)
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
	dockerPath, err := d.lookPath("docker")
	if err != nil {
		return Result{}, fmt.Errorf("find native Linux Docker CLI: %w", err)
	}
	if !strings.HasPrefix(dockerPath, "/") || strings.ContainsRune(dockerPath, '\x00') {
		return Result{}, fmt.Errorf("native Linux Docker CLI resolved to non-absolute path %q", dockerPath)
	}
	socket, err := d.statSocket(DockerSocketPath)
	if err != nil {
		return Result{}, fmt.Errorf("inspect Docker Desktop WSL socket %s: %w", DockerSocketPath, err)
	}
	if socket.Mode&os.ModeSocket == 0 {
		return Result{}, fmt.Errorf("Docker Desktop WSL endpoint %s is not a Unix socket", DockerSocketPath)
	}
	if socket.UID != 0 {
		return Result{}, fmt.Errorf("Docker Desktop WSL endpoint %s is owned by UID %d, expected root", DockerSocketPath, socket.UID)
	}
	if socket.Mode.Perm()&0o002 != 0 {
		return Result{}, fmt.Errorf("Docker Desktop WSL endpoint %s is world-writable (mode %04o)", DockerSocketPath, socket.Mode.Perm())
	}
	raw, err := d.runInfo(ctx, dockerPath, DockerHost)
	if err != nil {
		return Result{}, err
	}
	result, err := validateInfo(raw)
	if err != nil {
		return Result{}, err
	}
	result.DockerPath = dockerPath
	result.Host = DockerHost
	return result, nil
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
