//go:build linux

package wslfs

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const maxMachineIDFileSize = 4 << 10

func currentLayout() (hostenv.WSLLayout, error) {
	runtime, account, uid, err := currentIdentity()
	if err != nil {
		return hostenv.WSLLayout{}, err
	}
	machineID, err := readMachineIDFile("/etc/machine-id")
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("read native WSL machine identity: %w", err)
	}
	layout, err := runtime.NativeWSLLayout(account.HomeDir, uint32(uid), machineID)
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("derive native WSL layout: %w", err)
	}
	return layout, nil
}

// CurrentRegistryPath derives only the fixed user registry location. Unlike
// CurrentLayout it does not need machine identity, so bootstrap help/config can
// remain available while that state input is unavailable or damaged.
func CurrentRegistryPath() (string, error) {
	runtime, account, _, err := currentIdentity()
	if err != nil {
		return "", err
	}
	registryPath, err := runtime.NativeWSLRegistryPath(account.HomeDir)
	if err != nil {
		return "", fmt.Errorf("derive native WSL registry path: %w", err)
	}
	return registryPath, nil
}

func currentIdentity() (hostenv.Runtime, *user.User, uint32, error) {
	runtime, err := hostenv.Current()
	if err != nil {
		return hostenv.Runtime{}, nil, 0, fmt.Errorf("classify native WSL runtime: %w", err)
	}
	if runtime.Kind != hostenv.WSL2Native {
		return hostenv.Runtime{}, nil, 0, fmt.Errorf("native WSL layout requires runtime kind %q, got %q", hostenv.WSL2Native, runtime.Kind)
	}
	account, err := user.Current()
	if err != nil {
		return hostenv.Runtime{}, nil, 0, fmt.Errorf("resolve native WSL user account: %w", err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return hostenv.Runtime{}, nil, 0, fmt.Errorf("native WSL user account has invalid numeric UID %q", account.Uid)
	}
	if uint32(uid) != uint32(os.Getuid()) {
		return hostenv.Runtime{}, nil, 0, fmt.Errorf("native WSL account UID %d does not match process UID %d", uid, os.Getuid())
	}
	return runtime, account, uint32(uid), nil
}

func readMachineIDFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMachineIDFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxMachineIDFileSize {
		return "", fmt.Errorf("%s exceeds %d bytes", path, maxMachineIDFileSize)
	}
	return strings.TrimSpace(string(data)), nil
}
