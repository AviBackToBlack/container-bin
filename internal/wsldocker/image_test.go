package wsldocker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestPullImageUsesBoundedProofBoundEngineRequest(t *testing.T) {
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(_ context.Context, socket string, request Request) (operationResult, error) {
		if socket != DockerSocketPath || request.Method != http.MethodPost || request.Path != "/images/create" || request.Query.Get("fromImage") != "python:3.13-slim" {
			t.Fatalf("pull request = socket %q, %#v", socket, request)
		}
		return operationResult{StatusCode: http.StatusOK, Raw: []byte("{\"status\":\"Pull complete\"}\n"), PeerUID: 0}, nil
	}
	if err := pullImage(context.Background(), "python:3.13-slim", deps); err != nil {
		t.Fatal(err)
	}
}

func TestPullImageRejectsStreamedDaemonErrors(t *testing.T) {
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(context.Context, string, Request) (operationResult, error) {
		return operationResult{
			StatusCode: http.StatusOK,
			Raw:        []byte("{\"errorDetail\":{\"message\":\"pull access denied\"},\"error\":\"pull access denied\"}\n"),
			PeerUID:    0,
		}, nil
	}
	err := pullImage(context.Background(), "private.example/acme/tool:1", deps)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("pullImage() error = %v", err)
	}
}

func TestDecodeImagePullResponseRejectsMalformedEmptyAndUnsafeStreams(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":     nil,
		"malformed": []byte("not json"),
		"unsafe":    []byte("{\"error\":\"spoof\\nnext line\"}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := decodeImagePullResponse(raw, "python:3.13"); err == nil {
				t.Fatal("decodeImagePullResponse succeeded")
			}
		})
	}
}

func TestInspectImageReturnsDefensiveSnapshot(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	digest := "python@sha256:" + strings.Repeat("b", 64)
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
		if request.Method != http.MethodGet || request.Path != "/images/python:3.13-slim/json" {
			t.Fatalf("inspect request = %#v", request)
		}
		body := `{"Id":"` + id + `","RepoDigests":["` + digest + `"]}`
		return operationResult{StatusCode: http.StatusOK, Raw: []byte(body), PeerUID: 0}, nil
	}
	snapshot, err := inspectImage(context.Background(), "python:3.13-slim", deps)
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.RepoDigests()
	got[0] = "changed"
	if snapshot.ID() != id || snapshot.RepoDigests()[0] != digest {
		t.Fatalf("snapshot = ID %q RepoDigests %v", snapshot.ID(), snapshot.RepoDigests())
	}
}

func TestDecodeImageInspectResponseRejectsMalformedIdentity(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for name, raw := range map[string]string{
		"json":              `{`,
		"id":                `{"Id":"short","RepoDigests":[]}`,
		"repo digest":       `{"Id":"` + id + `","RepoDigests":["foreign:latest"]}`,
		"tagged repository": `{"Id":"` + id + `","RepoDigests":["python:latest@sha256:` + strings.Repeat("b", 64) + `"]}`,
		"digest algorithm":  `{"Id":"` + id + `","RepoDigests":["python@sha512:` + strings.Repeat("b", 64) + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeImageInspectResponse([]byte(raw)); err == nil {
				t.Fatal("decodeImageInspectResponse succeeded")
			}
		})
	}
}

func TestImageOperationsRejectInvalidInputsBeforeEngineProof(t *testing.T) {
	for _, reference := range []string{"", " ../escape", "bad\\name", "https://registry.example/tool"} {
		deps := validOperationDependencies(validSocketInfo())
		deps.check = func(context.Context) (Result, error) { panic("engine proof reached") }
		if err := pullImage(context.Background(), reference, deps); err == nil {
			t.Errorf("pullImage accepted %q", reference)
		}
		if _, err := inspectImage(context.Background(), reference, deps); err == nil {
			t.Errorf("inspectImage accepted %q", reference)
		}
	}
	deps := validOperationDependencies(validSocketInfo())
	deps.check = func(context.Context) (Result, error) { panic("engine proof reached") }
	if err := pullImage(nil, "python:3.13", deps); err == nil {
		t.Fatal("pullImage accepted nil context")
	}
	if _, err := inspectImage(nil, "python:3.13", deps); err == nil {
		t.Fatal("inspectImage accepted nil context")
	}
}

func TestImageInspectAcceptsLocalIDWhilePullRejectsIt(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
		if request.Path != "/images/"+id+"/json" {
			t.Fatalf("inspect path = %q", request.Path)
		}
		return operationResult{StatusCode: http.StatusOK, Raw: []byte(`{"Id":"` + id + `","RepoDigests":[]}`), PeerUID: 0}, nil
	}
	if _, err := inspectImage(context.Background(), id, deps); err != nil {
		t.Fatal(err)
	}
	deps.check = func(context.Context) (Result, error) { panic("pull reached engine proof for local ID") }
	if err := pullImage(context.Background(), id, deps); err == nil || !strings.Contains(err.Error(), "registry reference") {
		t.Fatalf("pullImage(local ID) error = %v", err)
	}
}
