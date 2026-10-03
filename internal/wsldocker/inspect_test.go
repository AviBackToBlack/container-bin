package wsldocker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestInspectContainerBindsExactSnapshotToProvenSocket(t *testing.T) {
	statCalls := 0
	deps := validOperationDependencies(validSocketInfo())
	deps.statSocket = func(path string) (socketInfo, error) {
		if path != DockerSocketPath {
			t.Fatalf("stat path = %q", path)
		}
		statCalls++
		return validSocketInfo(), nil
	}
	deps.perform = func(_ context.Context, path string, request Request) (operationResult, error) {
		if path != DockerSocketPath || request.Method != http.MethodGet || request.Path != "/containers/"+testContainerID+"/json" {
			t.Fatalf("perform(%q, %+v)", path, request)
		}
		if len(request.Query) != 0 || len(request.Body) != 0 || len(request.SuccessStatuses) != 1 || request.SuccessStatuses[0] != http.StatusOK {
			t.Fatalf("inspect request = %+v", request)
		}
		return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: []byte(`{
			"Id":"` + testContainerID + `",
			"Config":{"Labels":{"cb.managed":"true","cb.run_id":"run-1"},"Tty":true,"AttachStdin":true,"AttachStdout":true,"AttachStderr":true,"OpenStdin":true,"StdinOnce":true},
			"State":{"Running":true},
			"HostConfig":{"AutoRemove":true}
		}`)}, nil
	}
	snapshot, err := inspectContainer(context.Background(), testContainerID, deps)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID() != testContainerID || !snapshot.Running() || !snapshot.TTY() || !snapshot.AttachStdin() || !snapshot.AttachStdout() || !snapshot.AttachStderr() || !snapshot.OpenStdin() || !snapshot.StdinOnce() || !snapshot.AutoRemove() || statCalls != 2 {
		t.Fatalf("snapshot = %#v, stat calls = %d", snapshot, statCalls)
	}
	labels := snapshot.Labels()
	if labels["cb.managed"] != "true" || labels["cb.run_id"] != "run-1" {
		t.Fatalf("labels = %#v", labels)
	}
	labels["cb.managed"] = "mutated"
	if snapshot.Labels()["cb.managed"] != "true" {
		t.Fatal("Labels returned mutable internal state")
	}
}

func TestInspectContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	for name, test := range map[string]struct {
		ctx         context.Context
		containerID string
	}{
		"nil context": {containerID: testContainerID},
		"short ID":    {ctx: context.Background(), containerID: "abc"},
		"uppercase":   {ctx: context.Background(), containerID: strings.ToUpper(testContainerID)},
	} {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid inspect") }
			if _, err := inspectContainer(test.ctx, test.containerID, deps); err == nil {
				t.Fatal("inspectContainer() succeeded")
			}
		})
	}
	if _, err := inspectContainer(context.Background(), testContainerID, operationDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete-dependencies error = %v", err)
	}
}

func TestDecodeContainerInspectResponseRejectsUnsafeShapes(t *testing.T) {
	validConfig := `"Labels":null,"Tty":false,"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"OpenStdin":false,"StdinOnce":false`
	valid := `{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`
	otherID := strings.Repeat("a", 64)
	tests := map[string]struct {
		raw  []byte
		want string
	}{
		"malformed":             {raw: []byte(`{`), want: "decode"},
		"oversized":             {raw: make([]byte, maxContainerInspectOutput+1), want: "exceeds"},
		"missing ID":            {raw: []byte(`{"Config":{},"State":{}}`), want: "invalid ID"},
		"invalid ID":            {raw: []byte(`{"Id":"abc","Config":{},"State":{}}`), want: "invalid ID"},
		"mismatched ID":         {raw: []byte(`{"Id":"` + otherID + `","Config":{},"State":{}}`), want: "expected exact"},
		"missing Config":        {raw: []byte(`{"Id":"` + testContainerID + `","State":{}}`), want: "missing Config"},
		"null Config":           {raw: []byte(`{"Id":"` + testContainerID + `","Config":null,"State":{}}`), want: "missing Config"},
		"missing State":         {raw: []byte(`{"Id":"` + testContainerID + `","Config":{}}`), want: "missing State"},
		"null State":            {raw: []byte(`{"Id":"` + testContainerID + `","Config":{},"State":null}`), want: "missing State"},
		"missing Tty":           {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"OpenStdin":false,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.Tty"},
		"null Tty":              {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":null,"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"OpenStdin":false,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.Tty"},
		"missing attach stdin":  {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":false,"AttachStdout":false,"AttachStderr":false,"OpenStdin":false,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.AttachStdin"},
		"missing attach stdout": {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":false,"AttachStdin":false,"AttachStderr":false,"OpenStdin":false,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.AttachStdout"},
		"missing attach stderr": {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":false,"AttachStdin":false,"AttachStdout":false,"OpenStdin":false,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.AttachStderr"},
		"missing stdin":         {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":false,"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.OpenStdin"},
		"null stdin":            {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":false,"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"OpenStdin":null,"StdinOnce":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.OpenStdin"},
		"missing stdin once":    {raw: []byte(`{"Id":"` + testContainerID + `","Config":{"Tty":false,"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"OpenStdin":false},"State":{"Running":false},"HostConfig":{"AutoRemove":false}}`), want: "missing Config.StdinOnce"},
		"missing Running":       {raw: []byte(`{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{},"HostConfig":{"AutoRemove":false}}`), want: "missing State.Running"},
		"null Running":          {raw: []byte(`{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{"Running":null},"HostConfig":{"AutoRemove":false}}`), want: "missing State.Running"},
		"missing HostConfig":    {raw: []byte(`{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{"Running":false}}`), want: "missing HostConfig"},
		"null HostConfig":       {raw: []byte(`{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{"Running":false},"HostConfig":null}`), want: "missing HostConfig"},
		"missing AutoRemove":    {raw: []byte(`{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{"Running":false},"HostConfig":{}}`), want: "missing HostConfig.AutoRemove"},
		"null AutoRemove":       {raw: []byte(`{"Id":"` + testContainerID + `","Config":{` + validConfig + `},"State":{"Running":false},"HostConfig":{"AutoRemove":null}}`), want: "missing HostConfig.AutoRemove"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeContainerInspectResponse(test.raw, testContainerID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want containing %q", err, test.want)
			}
		})
	}
	snapshot, err := decodeContainerInspectResponse([]byte(valid), testContainerID)
	if err != nil || snapshot.Labels() == nil || len(snapshot.Labels()) != 0 || snapshot.Running() || snapshot.TTY() || snapshot.AttachStdin() || snapshot.AttachStdout() || snapshot.AttachStderr() || snapshot.OpenStdin() || snapshot.StdinOnce() {
		t.Fatalf("minimal snapshot = %#v, err = %v", snapshot, err)
	}
}
