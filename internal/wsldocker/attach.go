package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	attachRawMediaType         = "application/vnd.docker.raw-stream"
	attachMultiplexedMediaType = "application/vnd.docker.multiplexed-stream"
)

// AttachRequest describes the one streaming Docker operation ContainerBin
// permits: attaching live stdio to an exact container created by the caller.
// Logs and detach-key behavior are deliberately unavailable.
type AttachRequest struct {
	ContainerID string
	Stdin       bool
	Stdout      bool
	Stderr      bool
	TTY         bool
}

// AttachStream is the context-bound upgraded Docker connection. Non-TTY output
// is Docker's multiplexed raw-stream framing; TTY output is an unframed stream.
type AttachStream struct {
	stream      attachDuplex
	stdin       bool
	tty         bool
	stdinClosed atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

type attachDuplex interface {
	io.ReadWriteCloser
	CloseWrite() error
}

func (s *AttachStream) Read(p []byte) (int, error) {
	if s == nil || s.stream == nil {
		return 0, io.ErrClosedPipe
	}
	return s.stream.Read(p)
}

func (s *AttachStream) Write(p []byte) (int, error) {
	if s == nil || s.stream == nil || !s.stdin || s.stdinClosed.Load() {
		return 0, io.ErrClosedPipe
	}
	return s.stream.Write(p)
}

// CloseWrite sends EOF to container stdin while keeping stdout/stderr open.
// It is idempotent and unavailable when stdin was not requested.
func (s *AttachStream) CloseWrite() error {
	if s == nil || s.stream == nil || !s.stdin {
		return io.ErrClosedPipe
	}
	if !s.stdinClosed.CompareAndSwap(false, true) {
		return nil
	}
	return s.stream.CloseWrite()
}

func (s *AttachStream) Close() error {
	if s == nil || s.stream == nil {
		return nil
	}
	s.stdinClosed.Store(true)
	s.closeOnce.Do(func() { s.closeErr = s.stream.Close() })
	return s.closeErr
}

func (s *AttachStream) Multiplexed() bool { return s != nil && !s.tty }

type attachOperationResult struct {
	StatusCode int
	Header     http.Header
	ErrorBody  []byte
	PeerUID    uint32
	Stream     attachDuplex
}

type attachDependencies struct {
	check      func(context.Context) (Result, error)
	statSocket func(string) (socketInfo, error)
	perform    func(context.Context, string, AttachRequest) (attachOperationResult, error)
}

func openAttach(ctx context.Context, request AttachRequest, deps attachDependencies) (*AttachStream, error) {
	if ctx == nil {
		return nil, errors.New("Docker Desktop WSL attach requires a context")
	}
	if err := validateAttachRequest(request); err != nil {
		return nil, err
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return nil, errors.New("Docker Desktop WSL attach dependencies are incomplete")
	}
	checked, err := deps.check(ctx)
	if err != nil {
		return nil, fmt.Errorf("prove Docker Desktop WSL integration before attach: %w", err)
	}
	before, err := deps.statSocket(DockerSocketPath)
	if err != nil {
		return nil, fmt.Errorf("inspect Docker Desktop WSL socket before attach: %w", err)
	}
	if err := validateSocket(before); err != nil {
		return nil, err
	}
	if !sameSocket(checked.socket, before) {
		return nil, errors.New("Docker Desktop WSL socket changed after the engine identity proof")
	}

	operation, err := deps.perform(ctx, DockerSocketPath, request)
	if err != nil {
		closeAttachResult(operation)
		return nil, err
	}
	fail := func(err error) (*AttachStream, error) {
		closeAttachResult(operation)
		return nil, err
	}
	if operation.PeerUID != 0 {
		return fail(fmt.Errorf("Docker Desktop WSL attach socket peer is UID %d, expected root", operation.PeerUID))
	}
	after, err := deps.statSocket(DockerSocketPath)
	if err != nil {
		return fail(fmt.Errorf("inspect Docker Desktop WSL socket after attach: %w", err))
	}
	if err := validateSocket(after); err != nil {
		return fail(err)
	}
	if !sameSocket(before, after) {
		return fail(errors.New("Docker Desktop WSL socket changed during attach"))
	}
	if err := ctx.Err(); err != nil {
		return fail(fmt.Errorf("Docker Desktop WSL attach context ended during upgrade: %w", err))
	}
	attachPath := "/containers/" + request.ContainerID + "/attach"
	if operation.StatusCode != http.StatusSwitchingProtocols {
		return fail(&APIError{
			Method: http.MethodPost, Path: attachPath, StatusCode: operation.StatusCode,
			Message: dockerErrorMessage(operation.ErrorBody),
		})
	}
	if err := validateAttachHeaders(operation.Header, request.TTY); err != nil {
		return fail(err)
	}
	if operation.Stream == nil {
		return fail(errors.New("Docker Desktop WSL attach returned no upgraded stream"))
	}
	return &AttachStream{stream: operation.Stream, stdin: request.Stdin, tty: request.TTY}, nil
}

func validateAttachRequest(request AttachRequest) error {
	if err := validateContainerID(request.ContainerID); err != nil {
		return fmt.Errorf("Docker Desktop WSL attach: %w", err)
	}
	if !request.Stdin && !request.Stdout && !request.Stderr {
		return errors.New("Docker Desktop WSL attach requires at least one stdio stream")
	}
	return nil
}

func validateAttachHeaders(header http.Header, tty bool) error {
	if !headerHasToken(header, "Connection", "upgrade") || !strings.EqualFold(header.Get("Upgrade"), "tcp") {
		return errors.New("Docker Desktop WSL attach response did not provide the exact TCP upgrade")
	}
	mediaType, parameters, err := mime.ParseMediaType(header.Get("Content-Type"))
	validMediaType := mediaType == attachRawMediaType || (!tty && mediaType == attachMultiplexedMediaType)
	if err != nil || !validMediaType || len(parameters) != 0 {
		return fmt.Errorf("Docker Desktop WSL attach returned unsupported content type %q", header.Get("Content-Type"))
	}
	return nil
}

func headerHasToken(header http.Header, name, target string) bool {
	for _, value := range header.Values(name) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), target) {
				return true
			}
		}
	}
	return false
}

func closeAttachResult(result attachOperationResult) {
	if result.Stream != nil {
		_ = result.Stream.Close()
	}
}
