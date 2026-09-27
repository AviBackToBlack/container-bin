//go:build linux

package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	probeTimeout   = 10 * time.Second
	maxProbeOutput = 64 << 10
)

// Check proves that the current native WSL2 distribution reaches Docker
// Desktop's WSL-integrated Linux engine through the fixed local Unix socket.
func Check(ctx context.Context) (Result, error) {
	return check(ctx, dependencies{
		currentRuntime: hostenv.Current,
		lookupEnv:      os.LookupEnv,
		statSocket:     statDockerSocket,
		probeInfo:      probeDockerInfo,
	})
}

func statDockerSocket(path string) (socketInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return socketInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return socketInfo{}, errors.New("Docker socket ownership could not be determined")
	}
	return socketInfo{Mode: info.Mode(), UID: stat.Uid, Dev: uint64(stat.Dev), Ino: stat.Ino}, nil
}

func probeDockerInfo(ctx context.Context, socketPath string) (probeResult, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var peerUID uint32
	transport := &http.Transport{
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			if err != nil {
				return nil, err
			}
			uid, err := unixPeerUID(conn)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			peerUID = uid
			return conn, nil
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://docker/info", nil)
	if err != nil {
		return probeResult{}, fmt.Errorf("build Docker Desktop engine identity request: %w", err)
	}
	request.Close = true
	response, err := client.Do(request)
	if err != nil {
		if probeCtx.Err() != nil {
			return probeResult{}, fmt.Errorf("query Docker Desktop engine through %s: %w", DockerHost, probeCtx.Err())
		}
		return probeResult{}, fmt.Errorf("query Docker Desktop engine through %s: %w", DockerHost, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProbeOutput+1))
	if err != nil {
		return probeResult{}, fmt.Errorf("read Docker Desktop engine identity: %w", err)
	}
	if len(raw) > maxProbeOutput {
		return probeResult{}, fmt.Errorf("Docker Desktop engine probe output exceeds %d bytes", maxProbeOutput)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return probeResult{}, fmt.Errorf("Docker Desktop engine identity request returned HTTP %d", response.StatusCode)
	}
	return probeResult{Raw: raw, PeerUID: peerUID}, nil
}

func unixPeerUID(conn net.Conn) (uint32, error) {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("Docker Desktop endpoint did not create a Unix connection")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("inspect Docker Desktop socket peer: %w", err)
	}
	var (
		credentials *syscall.Ucred
		controlErr  error
	)
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("inspect Docker Desktop socket peer: %w", err)
	}
	if controlErr != nil {
		return 0, fmt.Errorf("inspect Docker Desktop socket peer: %w", controlErr)
	}
	if credentials == nil {
		return 0, errors.New("Docker Desktop socket peer credentials are unavailable")
	}
	return credentials.Uid, nil
}
