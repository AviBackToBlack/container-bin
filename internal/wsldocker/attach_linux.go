//go:build linux

package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const attachHandshakeTimeout = 30 * time.Second

// OpenAttach repeats the Docker Desktop endpoint proof and opens one bounded
// container-attach upgrade over the same fixed root-owned Unix socket.
func OpenAttach(ctx context.Context, request AttachRequest) (*AttachStream, error) {
	return openAttach(ctx, request, attachDependencies{
		check:      Check,
		statSocket: statDockerSocket,
		perform: func(ctx context.Context, socketPath string, request AttachRequest) (attachOperationResult, error) {
			return performDockerAttach(ctx, socketPath, request, 0)
		},
	})
}

func performDockerAttach(ctx context.Context, socketPath string, request AttachRequest, expectedPeerUID uint32) (attachOperationResult, error) {
	requestCtx, cancelRequest := context.WithCancel(ctx)
	handshakeTimer := time.AfterFunc(attachHandshakeTimeout, cancelRequest)
	peerUID := ^uint32(0)
	transport := &http.Transport{
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 << 10,
		ResponseHeaderTimeout:  attachHandshakeTimeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: attachHandshakeTimeout}).DialContext(ctx, "unix", socketPath)
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
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	query := url.Values{
		"logs":   {"0"},
		"stream": {"1"},
		"stdin":  {strconv.FormatBool(request.Stdin)},
		"stdout": {strconv.FormatBool(request.Stdout)},
		"stderr": {strconv.FormatBool(request.Stderr)},
	}
	endpoint := url.URL{Scheme: "http", Host: "docker", Path: "/containers/" + request.ContainerID + "/attach", RawQuery: query.Encode()}
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		handshakeTimer.Stop()
		cancelRequest()
		transport.CloseIdleConnections()
		return attachOperationResult{}, fmt.Errorf("build Docker Desktop attach request: %w", err)
	}
	httpRequest.Header.Set("Accept", attachMediaType)
	httpRequest.Header.Set("Connection", "Upgrade")
	httpRequest.Header.Set("Upgrade", "tcp")
	response, err := client.Do(httpRequest)
	if err != nil {
		handshakeTimer.Stop()
		cancelRequest()
		transport.CloseIdleConnections()
		if ctx.Err() != nil {
			return attachOperationResult{}, fmt.Errorf("attach Docker Desktop container through %s: %w", DockerHost, ctx.Err())
		}
		if requestCtx.Err() != nil {
			return attachOperationResult{}, fmt.Errorf("attach Docker Desktop container through %s: %w", DockerHost, requestCtx.Err())
		}
		return attachOperationResult{}, fmt.Errorf("attach Docker Desktop container through %s: %w", DockerHost, err)
	}
	result := attachOperationResult{StatusCode: response.StatusCode, Header: response.Header.Clone(), PeerUID: peerUID}
	if response.StatusCode != http.StatusSwitchingProtocols {
		defer handshakeTimer.Stop()
		defer cancelRequest()
		defer transport.CloseIdleConnections()
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, maxOperationOutput+1))
		if err != nil {
			return attachOperationResult{}, fmt.Errorf("read Docker Desktop attach error response: %w", err)
		}
		if len(raw) > maxOperationOutput {
			return attachOperationResult{}, fmt.Errorf("Docker Desktop attach error response exceeds %d bytes", maxOperationOutput)
		}
		result.ErrorBody = raw
		return result, nil
	}
	if !handshakeTimer.Stop() {
		cancelRequest()
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		return attachOperationResult{}, fmt.Errorf("attach Docker Desktop container through %s: %w", DockerHost, context.DeadlineExceeded)
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		cancelRequest()
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		return attachOperationResult{}, errors.New("Docker Desktop attach upgrade did not return a duplex stream")
	}
	result.Stream = &attachTransportStream{ReadWriteCloser: stream, transport: transport, cancel: cancelRequest}
	return result, nil
}

type attachTransportStream struct {
	io.ReadWriteCloser
	transport *http.Transport
	cancel    context.CancelFunc
}

func (s *attachTransportStream) Close() error {
	s.cancel()
	err := s.ReadWriteCloser.Close()
	s.transport.CloseIdleConnections()
	return err
}
