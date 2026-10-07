//go:build linux

package wsldocker

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestImageInspectReferenceSurvivesUnixHTTPTransport(t *testing.T) {
	reference := "ghcr.io/acme/tool@sha256:" + strings.Repeat("a", 64)
	wantPath := "/images/" + reference + "/json"
	socket := serveUnixHTTP(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != wantPath || request.URL.RawQuery != "" {
			t.Errorf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			http.Error(response, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = response.Write([]byte(`{"Id":"sha256:` + strings.Repeat("b", 64) + `","RepoDigests":["` + reference + `"]}`))
	}))
	result, err := performDockerRequest(context.Background(), socket, Request{
		Method: http.MethodGet,
		Path:   wantPath,
	}, operationTimeout, maxImageInspectOutput, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", result.StatusCode)
	}
}
