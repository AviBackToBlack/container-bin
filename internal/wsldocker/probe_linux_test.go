//go:build linux

package wsldocker

import (
	"context"
	"net"
	"net/http"
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
	result, err := probeDockerInfo(context.Background(), socket)
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
	if _, err := probeDockerInfo(context.Background(), socket); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("probeDockerInfo() error = %v", err)
	}
}

func TestProbeDockerInfoRejectsHTTPFailure(t *testing.T) {
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}))
	if _, err := probeDockerInfo(context.Background(), socket); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("probeDockerInfo() error = %v", err)
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
