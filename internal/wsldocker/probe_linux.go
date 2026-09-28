//go:build linux

package wsldocker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	probeTimeout       = 10 * time.Second
	operationTimeout   = 30 * time.Second
	maxProbeOutput     = 64 << 10
	maxOperationOutput = 1 << 20
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

// Execute performs one bounded control-plane request against the fixed Docker
// Desktop WSL socket. It repeats the full endpoint proof and binds the request
// to the same socket device/inode with a root peer before returning any result.
func Execute(ctx context.Context, request Request) (Response, error) {
	return execute(ctx, request, operationDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request Request) (operationResult, error) {
			return performDockerRequest(ctx, socketPath, request, operationTimeout, maxOperationOutput, 0)
		},
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
	return probeDockerInfoForPeer(ctx, socketPath, 0)
}

func probeDockerInfoForPeer(ctx context.Context, socketPath string, expectedPeerUID uint32) (probeResult, error) {
	result, err := performDockerRequest(ctx, socketPath, Request{
		Method:          http.MethodGet,
		Path:            "/info",
		SuccessStatuses: []int{http.StatusOK},
	}, probeTimeout, maxProbeOutput, expectedPeerUID)
	if err != nil {
		return probeResult{}, err
	}
	if result.StatusCode != http.StatusOK {
		return probeResult{}, fmt.Errorf("Docker Desktop engine identity request returned HTTP %d", result.StatusCode)
	}
	return probeResult{Raw: result.Raw, PeerUID: result.PeerUID}, nil
}

func performDockerRequest(ctx context.Context, socketPath string, request Request, timeout time.Duration, maxOutput int64, expectedPeerUID uint32) (operationResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	peerUID := ^uint32(0)
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
			if uid != expectedPeerUID {
				_ = conn.Close()
				return nil, fmt.Errorf("Docker Desktop WSL socket peer is UID %d, expected %d", uid, expectedPeerUID)
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
	endpoint := url.URL{Scheme: "http", Host: "docker", Path: request.Path, RawQuery: request.Query.Encode()}
	var body io.Reader
	if len(request.Body) != 0 {
		body = bytes.NewReader(request.Body)
	}
	httpRequest, err := http.NewRequestWithContext(requestCtx, request.Method, endpoint.String(), body)
	if err != nil {
		return operationResult{}, fmt.Errorf("build Docker Desktop Engine API request: %w", err)
	}
	httpRequest.Close = true
	httpRequest.Header.Set("Accept", "application/json")
	if len(request.Body) != 0 {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if requestCtx.Err() != nil {
			return operationResult{}, fmt.Errorf("query Docker Desktop engine through %s: %w", DockerHost, requestCtx.Err())
		}
		return operationResult{}, fmt.Errorf("query Docker Desktop engine through %s: %w", DockerHost, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOutput+1))
	if err != nil {
		return operationResult{}, fmt.Errorf("read Docker Desktop Engine API response: %w", err)
	}
	if int64(len(raw)) > maxOutput {
		return operationResult{}, fmt.Errorf("Docker Desktop Engine API response exceeds %d bytes", maxOutput)
	}
	return operationResult{StatusCode: response.StatusCode, Raw: raw, PeerUID: peerUID}, nil
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
