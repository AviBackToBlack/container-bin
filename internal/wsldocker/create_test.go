package wsldocker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const (
	testWSLNamespace = "wsl2-0123456789abcdef0123456789abcdef"
	testRunID        = "abcdef0123456789abcdef0123456789"
)

func TestCreateContainerBuildsExactRequestAndReprovesOwnership(t *testing.T) {
	spec := testContainerCreateSpec()
	deps := testCreateDependencies()
	requests := 0
	deps.operations.perform = func(_ context.Context, socketPath string, request Request) (operationResult, error) {
		if socketPath != DockerSocketPath {
			t.Fatalf("socket path = %q", socketPath)
		}
		requests++
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.Path != "/containers/create" || len(request.Query) != 0 || !reflect.DeepEqual(request.SuccessStatuses, []int{http.StatusCreated}) {
				t.Fatalf("create request = %#v", request)
			}
			var body containerCreateBody
			if err := json.Unmarshal(request.Body, &body); err != nil {
				t.Fatal(err)
			}
			if !body.AttachStdin || !body.AttachStdout || !body.AttachStderr || !body.OpenStdin || !body.StdinOnce || !body.TTY || !body.HostConfig.AutoRemove {
				t.Fatalf("stdio/cleanup body = %#v", body)
			}
			if body.Image != spec.Image || body.WorkingDir != spec.WorkingDirectory || !reflect.DeepEqual(body.Command, spec.Command) || !reflect.DeepEqual(body.Environment, spec.Environment) || !reflect.DeepEqual(body.HostConfig.Mounts, spec.Mounts) {
				t.Fatalf("runtime body = %#v", body)
			}
			wantLabels := containerLabels(Container{namespace: spec.Namespace, runID: testRunID, tool: spec.Tool})
			if !reflect.DeepEqual(body.Labels, wantLabels) {
				t.Fatalf("labels = %#v, want %#v", body.Labels, wantLabels)
			}
			return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: []byte(`{"Id":"` + testContainerID + `","Warnings":[]}`)}, nil
		case 2:
			if request.Method != http.MethodGet || request.Path != "/containers/"+testContainerID+"/json" {
				t.Fatalf("inspect request = %#v", request)
			}
			return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: ownedContainerInspect(testContainerID, spec, testRunID, false, true)}, nil
		default:
			t.Fatalf("unexpected request %d: %#v", requests, request)
			return operationResult{}, nil
		}
	}
	container, err := createContainer(context.Background(), spec, deps)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || container.ID() != testContainerID || container.RunID() != testRunID || container.Namespace() != spec.Namespace || container.Tool() != spec.Tool {
		t.Fatalf("container = %#v, requests=%d", container, requests)
	}
}

func TestCreateContainerRollsBackWarningAndPostCreateMismatch(t *testing.T) {
	for name, response := range map[string]func(ContainerCreateSpec) []byte{
		"warning": func(ContainerCreateSpec) []byte {
			return []byte(`{"Id":"` + testContainerID + `","Warnings":["platform mismatch"]}`)
		},
		"foreign labels": func(spec ContainerCreateSpec) []byte {
			return ownedContainerInspect(testContainerID, spec, strings.Repeat("0", 32), false, true)
		},
		"wrong terminal": func(spec ContainerCreateSpec) []byte {
			return ownedContainerInspect(testContainerID, spec, testRunID, false, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := testContainerCreateSpec()
			deps := testCreateDependencies()
			calls := 0
			deps.operations.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
				calls++
				if calls == 1 {
					return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: []byte(`{"Id":"` + testContainerID + `","Warnings":[]}`)}, nil
				}
				if name != "warning" && calls == 2 {
					return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: response(spec)}, nil
				}
				if request.Method != http.MethodDelete || request.Path != "/containers/"+testContainerID || request.Query.Get("force") != "false" || request.Query.Get("v") != "false" {
					t.Fatalf("rollback request = %#v", request)
				}
				return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
			}
			if name == "warning" {
				deps.operations.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
					calls++
					if calls == 1 {
						return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: response(spec)}, nil
					}
					if request.Method != http.MethodDelete {
						t.Fatalf("rollback request = %#v", request)
					}
					return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
				}
			}
			if _, err := createContainer(context.Background(), spec, deps); err == nil {
				t.Fatal("createContainer() succeeded")
			}
			wantCalls := 3
			if name == "warning" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("requests = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestCreateContainerRejectsInvalidInputsBeforeProof(t *testing.T) {
	valid := testContainerCreateSpec()
	tests := map[string]func(*ContainerCreateSpec){
		"tool":              func(spec *ContainerCreateSpec) { spec.Tool = "Bad" },
		"namespace":         func(spec *ContainerCreateSpec) { spec.Namespace = "wsl2-short" },
		"image":             func(spec *ContainerCreateSpec) { spec.Image = " image" },
		"image UTF-8":       func(spec *ContainerCreateSpec) { spec.Image = string([]byte{0xff}) },
		"working directory": func(spec *ContainerCreateSpec) { spec.WorkingDirectory = "/" },
		"command NUL":       func(spec *ContainerCreateSpec) { spec.Command = []string{"bad\x00arg"} },
		"command UTF-8":     func(spec *ContainerCreateSpec) { spec.Command = []string{string([]byte{0xff})} },
		"environment":       func(spec *ContainerCreateSpec) { spec.Environment = []string{"1BAD=value"} },
		"environment UTF-8": func(spec *ContainerCreateSpec) { spec.Environment = []string{"GOOD=" + string([]byte{0xff})} },
		"duplicate env":     func(spec *ContainerCreateSpec) { spec.Environment = []string{"A=1", "A=2"} },
		"mount type":        func(spec *ContainerCreateSpec) { spec.Mounts[0].Type = "socket" },
		"mount source":      func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = "relative" },
		"mount target":      func(spec *ContainerCreateSpec) { spec.Mounts[0].Target = "/" },
		"Docker socket":     func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = DockerSocketPath },
		"foreign volume":    func(spec *ContainerCreateSpec) { spec.Mounts[1].Source = "foreign-volume" },
		"second bind": func(spec *ContainerCreateSpec) {
			spec.Mounts = append(spec.Mounts, ContainerMount{Type: "bind", Source: "/home/alice/other", Target: "/other"})
		},
		"duplicate target": func(spec *ContainerCreateSpec) { spec.Mounts = append(spec.Mounts, spec.Mounts[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := valid
			spec.Command = append([]string(nil), valid.Command...)
			spec.Environment = append([]string(nil), valid.Environment...)
			spec.Mounts = append([]ContainerMount(nil), valid.Mounts...)
			mutate(&spec)
			deps := testCreateDependencies()
			deps.operations.check = func(context.Context) (Result, error) { panic("proof reached for invalid create") }
			if _, err := createContainer(context.Background(), spec, deps); err == nil {
				t.Fatal("createContainer() succeeded")
			}
		})
	}
	if _, err := createContainer(nil, valid, testCreateDependencies()); err == nil {
		t.Fatal("createContainer() accepted nil context")
	}
	if _, err := createContainer(context.Background(), valid, createDependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("incomplete dependencies error = %v", err)
	}
	deps := testCreateDependencies()
	deps.newRunID = func() (string, error) { return "bad", nil }
	if _, err := createContainer(context.Background(), valid, deps); err == nil || !strings.Contains(err.Error(), "identity is invalid") {
		t.Fatalf("invalid run identity error = %v", err)
	}
	deps.newRunID = func() (string, error) { return "", errors.New("entropy unavailable") }
	if _, err := createContainer(context.Background(), valid, deps); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("run identity failure = %v", err)
	}
}

func TestCreateContainerRejectsUnsafeResponsesWithoutGuessingCleanup(t *testing.T) {
	for name, raw := range map[string][]byte{
		"malformed":  []byte(`{`),
		"oversized":  make([]byte, maxContainerCreateOutput+1),
		"missing ID": []byte(`{"Warnings":[]}`),
		"short ID":   []byte(`{"Id":"abc","Warnings":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			deps := testCreateDependencies()
			calls := 0
			deps.operations.perform = func(context.Context, string, Request) (operationResult, error) {
				calls++
				return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: raw}, nil
			}
			if _, err := createContainer(context.Background(), testContainerCreateSpec(), deps); err == nil {
				t.Fatal("createContainer() succeeded")
			}
			if calls != 1 {
				t.Fatalf("unsafe response triggered %d requests; no exact cleanup target exists", calls)
			}
		})
	}
}

func TestCreateContainerReportsRollbackFailure(t *testing.T) {
	deps := testCreateDependencies()
	calls := 0
	deps.operations.perform = func(context.Context, string, Request) (operationResult, error) {
		calls++
		if calls == 1 {
			return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: []byte(`{"Id":"` + testContainerID + `","Warnings":["warning"]}`)}, nil
		}
		return operationResult{StatusCode: http.StatusConflict, PeerUID: 0, Raw: []byte(`{"message":"container is running"}`)}, nil
	}
	_, err := createContainer(context.Background(), testContainerCreateSpec(), deps)
	if err == nil || !strings.Contains(err.Error(), "warning") || !strings.Contains(err.Error(), "rollback") || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("rollback failure = %v", err)
	}
}

func testContainerCreateSpec() ContainerCreateSpec {
	return ContainerCreateSpec{
		Tool:             "node24",
		Namespace:        testWSLNamespace,
		Image:            "node@sha256:" + strings.Repeat("a", 64),
		Command:          []string{"node", "--version"},
		Environment:      []string{"NODE_ENV=test", "EMPTY="},
		WorkingDirectory: "/workspace/project",
		Mounts: []ContainerMount{
			{Type: "bind", Source: "/home/alice/project", Target: "/workspace/project"},
			{Type: "volume", Source: "cb-" + testWSLNamespace + "-node-modules", Target: "/workspace/project/node_modules"},
		},
		TTY: true,
	}
}

func testCreateDependencies() createDependencies {
	return createDependencies{
		operations: validOperationDependencies(validSocketInfo()),
		newRunID:   func() (string, error) { return testRunID, nil },
	}
}

func ownedContainerInspect(id string, spec ContainerCreateSpec, runID string, running, autoRemove bool) []byte {
	body := struct {
		ID     string `json:"Id"`
		Config struct {
			Labels    map[string]string `json:"Labels"`
			TTY       bool              `json:"Tty"`
			OpenStdin bool              `json:"OpenStdin"`
		} `json:"Config"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		HostConfig struct {
			AutoRemove bool `json:"AutoRemove"`
		} `json:"HostConfig"`
	}{ID: id}
	body.Config.Labels = containerLabels(Container{id: id, namespace: spec.Namespace, runID: runID, tool: spec.Tool})
	body.Config.TTY = spec.TTY
	body.Config.OpenStdin = true
	body.State.Running = running
	body.HostConfig.AutoRemove = autoRemove
	raw, _ := json.Marshal(body)
	return raw
}
