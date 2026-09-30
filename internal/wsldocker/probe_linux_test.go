//go:build linux

package wsldocker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeDockerInfoUsesUnixSocketAndPeerCredentials(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/info" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
			http.Error(response, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = response.Write([]byte(validInfo))
	}))
	result, err := probeDockerInfoForPeer(context.Background(), socket, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Raw) != validInfo || result.PeerUID != uint32(os.Geteuid()) {
		t.Fatalf("probeDockerInfo() = (bytes=%d, peer UID=%d)", len(result.Raw), result.PeerUID)
	}
}

func TestProbeDockerInfoBoundsResponse(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("x", maxProbeOutput+1)))
	}))
	if _, err := probeDockerInfoForPeer(context.Background(), socket, uint32(os.Geteuid())); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("probeDockerInfo() error = %v", err)
	}
}

func TestProbeDockerInfoRejectsHTTPFailure(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}))
	if _, err := probeDockerInfoForPeer(context.Background(), socket, uint32(os.Geteuid())); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("probeDockerInfo() error = %v", err)
	}
}

func TestPerformDockerRequestSendsBoundedControlRequest(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if request.Method != http.MethodPost || request.URL.Path != "/containers/create" || request.URL.Query().Get("name") != "example" || string(body) != `{"Image":"alpine"}` {
			t.Errorf("request = %s %s?%s body=%q", request.Method, request.URL.Path, request.URL.RawQuery, body)
			http.Error(response, "unexpected request", http.StatusBadRequest)
			return
		}
		if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", request.Header)
		}
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"Id":"example"}`))
	}))
	result, err := performDockerRequest(context.Background(), socket, Request{
		Method: http.MethodPost,
		Path:   "/containers/create",
		Query:  url.Values{"name": {"example"}},
		Body:   []byte(`{"Image":"alpine"}`),
	}, operationTimeout, maxOperationOutput, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusCreated || string(result.Raw) != `{"Id":"example"}` || result.PeerUID != uint32(os.Geteuid()) {
		t.Fatalf("performDockerRequest() = %+v", result)
	}
}

func TestPerformDockerRequestAllowsCallerBoundLongPoll(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/containers/"+testContainerID+"/wait" || request.URL.Query().Get("condition") != "not-running" {
			t.Errorf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			http.Error(response, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = response.Write([]byte(`{"StatusCode":0}`))
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := performDockerRequest(ctx, socket, Request{
		Method: http.MethodPost,
		Path:   "/containers/" + testContainerID + "/wait",
		Query:  url.Values{"condition": {"not-running"}},
	}, 0, maxContainerWaitOutput, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || string(result.Raw) != `{"StatusCode":0}` {
		t.Fatalf("performDockerRequest() = %+v", result)
	}
}

func TestPerformDockerRequestCancelsBlockedLongPollWithCaller(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	socket := serveUnixHTTP(t, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, err := performDockerRequest(ctx, socket, Request{
			Method: http.MethodPost,
			Path:   "/containers/" + testContainerID + "/wait",
			Query:  url.Values{"condition": {"not-running"}},
		}, 0, maxContainerWaitOutput, uint32(os.Geteuid()))
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("long-poll request did not reach the server")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("performDockerRequest() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked long poll ignored caller cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("server request context was not canceled")
	}
}

func TestPerformDockerRequestRejectsInvalidLifetimeBeforeDial(t *testing.T) {
	request := Request{Method: http.MethodGet, Path: "/info"}
	if _, err := performDockerRequest(nil, "unused", request, 0, maxProbeOutput, 0); err == nil || !strings.Contains(err.Error(), "requires a context") {
		t.Fatalf("nil-context error = %v", err)
	}
	if _, err := performDockerRequest(context.Background(), "unused", request, -time.Second, maxProbeOutput, 0); err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("negative-timeout error = %v", err)
	}
}

func TestPerformDockerRequestBoundsResponse(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("x", maxOperationOutput+1)))
	}))
	if _, err := performDockerRequest(context.Background(), socket, Request{Method: http.MethodGet, Path: "/info"}, operationTimeout, maxOperationOutput, uint32(os.Geteuid())); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("performDockerRequest() error = %v", err)
	}
}

func TestPerformDockerRequestRejectsPeerBeforeSending(t *testing.T) {
	requestReceived := make(chan struct{}, 1)
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestReceived <- struct{}{}
		response.WriteHeader(http.StatusOK)
	}))
	unexpectedUID := uint32(os.Geteuid()) + 1
	if _, err := performDockerRequest(context.Background(), socket, Request{Method: http.MethodPost, Path: "/containers/create", Body: []byte(`{}`)}, operationTimeout, maxOperationOutput, unexpectedUID); err == nil || !strings.Contains(err.Error(), "socket peer") {
		t.Fatalf("performDockerRequest() error = %v", err)
	}
	select {
	case <-requestReceived:
		t.Fatal("HTTP request reached an untrusted socket peer")
	default:
	}
}

func serveUnixHTTP(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})
	return socket
}
