package wsldocker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestWaitContainerBindsExactLongPollToProvenSocket(t *testing.T) {
	socket := validSocketInfo()
	statCalls := 0
	deps := validOperationDependencies(socket)
	deps.statSocket = func(path string) (socketInfo, error) {
		if path != DockerSocketPath {
			t.Fatalf("stat path = %q", path)
		}
		statCalls++
		return socket, nil
	}
	deps.perform = func(_ context.Context, path string, request Request) (operationResult, error) {
		if path != DockerSocketPath || request.Method != http.MethodPost || request.Path != "/containers/"+testContainerID+"/wait" {
			t.Fatalf("perform(%q, %+v)", path, request)
		}
		if request.Query.Encode() != "condition=not-running" || len(request.Body) != 0 || len(request.SuccessStatuses) != 1 || request.SuccessStatuses[0] != http.StatusOK {
			t.Fatalf("wait request = %+v", request)
		}
		return operationResult{StatusCode: http.StatusOK, Raw: []byte(`{"StatusCode":137,"Error":null}`), PeerUID: 0}, nil
	}
	exitCode, err := waitContainer(context.Background(), testContainerID, deps)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 137 || statCalls != 2 {
		t.Fatalf("waitContainer() = %d, stat calls=%d", exitCode, statCalls)
	}
}

func TestWaitContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	for name, containerID := range map[string]string{
		"short":     "abc",
		"uppercase": strings.ToUpper(testContainerID),
		"non hex":   strings.Repeat("g", 64),
	} {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid container ID") }
			if _, err := waitContainer(context.Background(), containerID, deps); err == nil {
				t.Fatal("waitContainer() succeeded")
			}
		})
	}

	deps := validOperationDependencies(validSocketInfo())
	deps.check = func(context.Context) (Result, error) { panic("proof reached with nil context") }
	if _, err := waitContainer(nil, testContainerID, deps); err == nil {
		t.Fatal("waitContainer() accepted nil context")
	}
	if _, err := waitContainer(context.Background(), testContainerID, operationDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete-dependencies error = %v", err)
	}
}

func TestDecodeContainerWaitResponse(t *testing.T) {
	for _, exitCode := range []int{0, 1, 130, 255} {
		raw := []byte(fmt.Sprintf(`{"StatusCode":%d}`, exitCode))
		got, err := decodeContainerWaitResponse(raw)
		if err != nil || got != exitCode {
			t.Fatalf("decodeContainerWaitResponse(%d) = (%d, %v)", exitCode, got, err)
		}
	}

	tests := map[string]struct {
		raw  []byte
		want string
	}{
		"malformed":        {raw: []byte(`{`), want: "decode"},
		"missing status":   {raw: []byte(`{}`), want: "missing StatusCode"},
		"null status":      {raw: []byte(`{"StatusCode":null}`), want: "missing StatusCode"},
		"negative status":  {raw: []byte(`{"StatusCode":-1}`), want: "invalid exit code"},
		"oversized status": {raw: []byte(`{"StatusCode":256}`), want: "invalid exit code"},
		"engine error":     {raw: []byte(`{"StatusCode":125,"Error":{"Message":"wait failed"}}`), want: `wait failed`},
		"empty error":      {raw: []byte(`{"StatusCode":125,"Error":{"Message":""}}`), want: "invalid error message"},
		"unsafe error":     {raw: []byte(`{"StatusCode":125,"Error":{"Message":"forged\nline"}}`), want: "invalid error message"},
		"oversized body":   {raw: make([]byte, maxContainerWaitOutput+1), want: "exceeds"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeContainerWaitResponse(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeContainerWaitResponse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
