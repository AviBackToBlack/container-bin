//go:build linux

package wslfs

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

func currentLayout() (hostenv.WSLLayout, error) {
	runtime, err := hostenv.Current()
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("classify native WSL runtime: %w", err)
	}
	if runtime.Kind != hostenv.WSL2Native {
		return hostenv.WSLLayout{}, fmt.Errorf("native WSL layout requires runtime kind %q, got %q", hostenv.WSL2Native, runtime.Kind)
	}
	account, err := user.Current()
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("resolve native WSL user account: %w", err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("native WSL user account has invalid numeric UID %q", account.Uid)
	}
	if uint32(uid) != uint32(os.Getuid()) {
		return hostenv.WSLLayout{}, fmt.Errorf("native WSL account UID %d does not match process UID %d", uid, os.Getuid())
	}
	machineID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("read native WSL machine identity: %w", err)
	}
	layout, err := runtime.NativeWSLLayout(account.HomeDir, uint32(uid), strings.TrimSpace(string(machineID)))
	if err != nil {
		return hostenv.WSLLayout{}, fmt.Errorf("derive native WSL layout: %w", err)
	}
	return layout, nil
}
