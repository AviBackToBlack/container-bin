package wsldocker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const testContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type testDuplex struct {
	bytes.Buffer
	closed      bool
	writeClosed bool
}

func (s *testDuplex) Close() error { s.closed = true; return nil }
func (s *testDuplex) CloseWrite() error {
	s.writeClosed = true
	return nil
}

func TestOpenAttachBindsDuplexStreamToProvenSocket(t *testing.T) {
	socket := validSocketInfo()
	duplex := &testDuplex{}
	request := AttachRequest{ContainerID: testContainerID, Stdin: true, Stdout: true}
	statCalls := 0
	stream, err := openAttach(context.Background(), request, attachDependencies{
		check: func(context.Context) (Result, error) { return Result{socket: socket}, nil },
		statSocket: func(path string) (socketInfo, error) {
			if path != DockerSocketPath {
				t.Fatalf("stat path = %q", path)
			}
			statCalls++
			return socket, nil
		},
		perform: func(_ context.Context, path string, got AttachRequest) (attachOperationResult, error) {
			if path != DockerSocketPath || got != request {
				t.Fatalf("perform(%q, %+v)", path, got)
			}
			return attachOperationResult{
				StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{
					"Connection":   {"keep-alive, Upgrade"},
					"Upgrade":      {"tcp"},
					"Content-Type": {attachMultiplexedMediaType},
				},
				PeerUID: 0,
				Stream:  duplex,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if statCalls != 2 || !stream.Multiplexed() {
		t.Fatalf("stat calls=%d multiplexed=%v", statCalls, stream.Multiplexed())
	}
	if _, err := stream.Write([]byte("stdin")); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseWrite(); err != nil || !duplex.writeClosed {
		t.Fatalf("CloseWrite() = %v, writeClosed=%v", err, duplex.writeClosed)
	}
	if _, err := stream.Write([]byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write() after CloseWrite error = %v", err)
	}
	if err := stream.Close(); err != nil || !duplex.closed {
		t.Fatalf("Close() = %v, closed=%v", err, duplex.closed)
	}
}

func TestOpenAttachRejectsInvalidRequestBeforeProof(t *testing.T) {
	for name, request := range map[string]AttachRequest{
		"short ID":   {ContainerID: "abc", Stdout: true},
		"uppercase":  {ContainerID: strings.ToUpper(testContainerID), Stdout: true},
		"no streams": {ContainerID: testContainerID},
	} {
		t.Run(name, func(t *testing.T) {
			deps := validAttachDependencies(validSocketInfo(), &testDuplex{})
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid attach") }
			if _, err := openAttach(context.Background(), request, deps); err == nil {
				t.Fatal("openAttach() succeeded")
			}
		})
	}
	if _, err := openAttach(nil, AttachRequest{ContainerID: testContainerID, Stdout: true}, validAttachDependencies(validSocketInfo(), &testDuplex{})); err == nil {
		t.Fatal("openAttach() accepted nil context")
	}
}

func TestOpenAttachClosesStreamOnBoundaryOrProtocolFailure(t *testing.T) {
	tests := map[string]func(*attachDependencies, *attachOperationResult){
		"untrusted peer": func(_ *attachDependencies, result *attachOperationResult) { result.PeerUID = 1000 },
		"socket changed": func(deps *attachDependencies, _ *attachOperationResult) {
			calls := 0
			deps.statSocket = func(string) (socketInfo, error) {
				calls++
				socket := validSocketInfo()
				if calls == 2 {
					socket.Ino++
				}
				return socket, nil
			}
		},
		"wrong status":  func(_ *attachDependencies, result *attachOperationResult) { result.StatusCode = http.StatusOK },
		"wrong upgrade": func(_ *attachDependencies, result *attachOperationResult) { result.Header.Del("Upgrade") },
		"wrong media type": func(_ *attachDependencies, result *attachOperationResult) {
			result.Header.Set("Content-Type", "application/json")
		},
		"missing stream": func(_ *attachDependencies, result *attachOperationResult) { result.Stream = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			duplex := &testDuplex{}
			result := successfulAttachResult(duplex)
			deps := validAttachDependencies(validSocketInfo(), duplex)
			mutate(&deps, &result)
			deps.perform = func(context.Context, string, AttachRequest) (attachOperationResult, error) { return result, nil }
			if _, err := openAttach(context.Background(), AttachRequest{ContainerID: testContainerID, Stdout: true}, deps); err == nil {
				t.Fatal("openAttach() succeeded")
			}
			if result.Stream != nil && !duplex.closed {
				t.Fatal("failed attach did not close upgraded stream")
			}
		})
	}
}

func TestOpenAttachReturnsBoundedTypedAPIError(t *testing.T) {
	deps := validAttachDependencies(validSocketInfo(), &testDuplex{})
	deps.perform = func(context.Context, string, AttachRequest) (attachOperationResult, error) {
		return attachOperationResult{
			StatusCode: http.StatusConflict,
			ErrorBody:  []byte(`{"message":"container is not running"}`),
			PeerUID:    0,
		}, nil
	}
	_, err := openAttach(context.Background(), AttachRequest{ContainerID: testContainerID, Stdout: true}, deps)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Path != "/containers/"+testContainerID+"/attach" || apiErr.Message != "container is not running" {
		t.Fatalf("typed attach error = %#v (%v)", apiErr, err)
	}
}

func TestOpenAttachRejectsContextCanceledDuringUpgrade(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	duplex := &testDuplex{}
	deps := validAttachDependencies(validSocketInfo(), duplex)
	statCalls := 0
	deps.statSocket = func(string) (socketInfo, error) {
		statCalls++
		if statCalls == 2 {
			cancel()
		}
		return validSocketInfo(), nil
	}
	_, err := openAttach(ctx, AttachRequest{ContainerID: testContainerID, Stdout: true}, deps)
	if err == nil || !strings.Contains(err.Error(), "context ended during upgrade") || !duplex.closed {
		t.Fatalf("canceled upgrade = %v, closed=%v", err, duplex.closed)
	}
}

func TestValidateAttachHeadersBindsMediaTypeToTTYFraming(t *testing.T) {
	base := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}}
	for _, test := range []struct {
		name      string
		mediaType string
		tty       bool
		wantError bool
	}{
		{name: "legacy non-TTY raw stream", mediaType: attachRawMediaType},
		{name: "current non-TTY multiplexed stream", mediaType: attachMultiplexedMediaType},
		{name: "TTY raw stream", mediaType: attachRawMediaType, tty: true},
		{name: "TTY rejects multiplexed stream", mediaType: attachMultiplexedMediaType, tty: true, wantError: true},
		{name: "reject parameters", mediaType: attachRawMediaType + "; charset=utf-8", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := base.Clone()
			header.Set("Content-Type", test.mediaType)
			err := validateAttachHeaders(header, test.tty)
			if (err != nil) != test.wantError {
				t.Fatalf("validateAttachHeaders() error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestAttachStreamEnforcesInputAndFramingContract(t *testing.T) {
	duplex := &testDuplex{}
	stream := &AttachStream{stream: duplex, stdin: false, tty: true}
	if _, err := stream.Write([]byte("forbidden")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write() error = %v", err)
	}
	if stream.Multiplexed() {
		t.Fatal("TTY stream reported multiplexed framing")
	}
	if err := stream.CloseWrite(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("CloseWrite() error = %v", err)
	}
}

func successfulAttachResult(stream attachDuplex) attachOperationResult {
	return attachOperationResult{
		StatusCode: http.StatusSwitchingProtocols,
		Header: http.Header{
			"Connection":   {"Upgrade"},
			"Upgrade":      {"tcp"},
			"Content-Type": {attachRawMediaType},
		},
		PeerUID: 0,
		Stream:  stream,
	}
}

func validAttachDependencies(socket socketInfo, stream attachDuplex) attachDependencies {
	return attachDependencies{
		check:      func(context.Context) (Result, error) { return Result{socket: socket}, nil },
		statSocket: func(string) (socketInfo, error) { return socket, nil },
		perform: func(context.Context, string, AttachRequest) (attachOperationResult, error) {
			return successfulAttachResult(stream), nil
		},
	}
}
