//go:build linux

package wsldocker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
