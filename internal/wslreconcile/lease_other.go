//go:build !linux

package wslreconcile

import (
	"context"
	"errors"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

var errLinuxLeaseRequired = errors.New("native WSL runtime leases require Linux")

func acquireFileCoordinator(context.Context, hostenv.WSLLayout) (coordinator, error) {
	return nil, errLinuxLeaseRequired
}

func createFileLease(hostenv.WSLLayout, string) (lease, error) {
	return nil, errLinuxLeaseRequired
}

func probeFileLease(hostenv.WSLLayout, string) (leaseStatus, lease, error) {
	return leaseMissing, nil, errLinuxLeaseRequired
}

func discoverFileLeases(hostenv.WSLLayout) ([]string, error) {
	return nil, errLinuxLeaseRequired
}
