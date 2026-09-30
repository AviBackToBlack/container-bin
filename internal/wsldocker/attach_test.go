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
	closed bool
}

func (s *testDuplex) Close() error { s.closed = true; return nil }

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
					"Content-Type": {attachMediaType},
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

func TestAttachStreamEnforcesInputAndFramingContract(t *testing.T) {
	duplex := &testDuplex{}
	stream := &AttachStream{stream: duplex, stdin: false, tty: true}
	if _, err := stream.Write([]byte("forbidden")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write() error = %v", err)
	}
	if stream.Multiplexed() {
		t.Fatal("TTY stream reported multiplexed framing")
	}
}

func successfulAttachResult(stream io.ReadWriteCloser) attachOperationResult {
	return attachOperationResult{
		StatusCode: http.StatusSwitchingProtocols,
		Header: http.Header{
			"Connection":   {"Upgrade"},
			"Upgrade":      {"tcp"},
			"Content-Type": {attachMediaType},
		},
		PeerUID: 0,
		Stream:  stream,
	}
}

func validAttachDependencies(socket socketInfo, stream io.ReadWriteCloser) attachDependencies {
	return attachDependencies{
		check:      func(context.Context) (Result, error) { return Result{socket: socket}, nil },
		statSocket: func(string) (socketInfo, error) { return socket, nil },
		perform: func(context.Context, string, AttachRequest) (attachOperationResult, error) {
			return successfulAttachResult(stream), nil
		},
	}
}
