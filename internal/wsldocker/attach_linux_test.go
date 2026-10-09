//go:build linux

package wsldocker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPerformDockerAttachUsesExactUpgradeAndReturnsDuplexStream(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/containers/"+testContainerID+"/attach" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
			return
		}
		query := request.URL.Query()
		if query.Get("logs") != "0" || query.Get("stream") != "1" || query.Get("stdin") != "true" || query.Get("stdout") != "true" || query.Get("stderr") != "false" {
			t.Errorf("query = %v", query)
		}
		if !headerHasToken(request.Header, "Connection", "upgrade") || request.Header.Get("Upgrade") != "tcp" || request.Header.Get("Accept") != attachRawMediaType+", "+attachMultiplexedMediaType {
			t.Errorf("headers = %v", request.Header)
		}
		connection, readerWriter, err := http.NewResponseController(response).Hijack()
		if err != nil {
			t.Errorf("hijack = %v", err)
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprintf(readerWriter, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: %s\r\n\r\nhello", attachMultiplexedMediaType)
		_ = readerWriter.Flush()
		input, err := io.ReadAll(readerWriter)
		if err != nil || string(input) != "ping" {
			t.Errorf("stdin = %q, %v", input, err)
			return
		}
		_, _ = readerWriter.WriteString("done")
		_ = readerWriter.Flush()
	}))
	result, err := performDockerAttach(context.Background(), socket, AttachRequest{
		ContainerID: testContainerID, Stdin: true, Stdout: true,
	}, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer result.Stream.Close()
	if result.StatusCode != http.StatusSwitchingProtocols || result.PeerUID != uint32(os.Geteuid()) {
		t.Fatalf("attach result = %+v", result)
	}
	output := make([]byte, 5)
	if _, err := io.ReadFull(result.Stream, output); err != nil || string(output) != "hello" {
		t.Fatalf("initial output = %q, %v", output, err)
	}
	if _, err := result.Stream.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := result.Stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	output = make([]byte, 4)
	if _, err := io.ReadFull(result.Stream, output); err != nil || string(output) != "done" {
		t.Fatalf("duplex output = %q, %v", output, err)
	}
}

func TestPerformDockerAttachCancellationClosesBlockedStream(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		connection, readerWriter, err := http.NewResponseController(response).Hijack()
		if err != nil {
			t.Errorf("hijack = %v", err)
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprintf(readerWriter, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: %s\r\n\r\n", attachMultiplexedMediaType)
		_ = readerWriter.Flush()
		_, _ = io.Copy(io.Discard, readerWriter)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	result, err := performDockerAttach(ctx, socket, AttachRequest{ContainerID: testContainerID, Stdout: true}, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := result.Stream.Read(make([]byte, 1))
		readDone <- err
	}()
	cancel()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("blocked attach read succeeded after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not unblock attach read")
	}
}

func TestPerformDockerAttachBoundsErrorResponse(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(strings.Repeat("x", maxOperationOutput+1)))
	}))
	_, err := performDockerAttach(context.Background(), socket, AttachRequest{ContainerID: testContainerID, Stdout: true}, uint32(os.Geteuid()))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized attach error = %v", err)
	}
}

func TestPerformDockerAttachRejectsUntrustedPeerBeforeRequest(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("attach request reached an untrusted peer")
	}))
	unexpectedUID := uint32(os.Geteuid()) + 1
	_, err := performDockerAttach(context.Background(), socket, AttachRequest{ContainerID: testContainerID, Stdout: true}, unexpectedUID)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("expected %d", unexpectedUID)) {
		t.Fatalf("untrusted-peer error = %v", err)
	}
}

func TestPerformDockerAttachReportsHandshakeTimeout(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	_, err := performDockerAttachWithTimeout(
		context.Background(), socket,
		AttachRequest{ContainerID: testContainerID, Stdout: true},
		uint32(os.Geteuid()), 50*time.Millisecond,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handshake-timeout error = %v", err)
	}
}
