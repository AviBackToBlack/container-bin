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
			mount := spec.Mounts[1]
			if request.Method != http.MethodGet || request.Path != "/volumes/"+mount.Source || !reflect.DeepEqual(request.SuccessStatuses, []int{http.StatusOK}) {
				t.Fatalf("volume proof request = %#v", request)
			}
			return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: exactVolumeInspect(mount)}, nil
		case 2:
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
			if body.Image != spec.Image || body.WorkingDir != spec.WorkingDirectory || !reflect.DeepEqual(body.Command, spec.Command) || !reflect.DeepEqual(body.Environment, spec.Environment) || !reflect.DeepEqual(body.HostConfig.Mounts, engineMounts(spec.Mounts)) {
				t.Fatalf("runtime body = %#v", body)
			}
			wantLabels := containerLabels(Container{namespace: spec.Namespace, runID: testRunID, tool: spec.Tool})
			if !reflect.DeepEqual(body.Labels, wantLabels) {
				t.Fatalf("labels = %#v, want %#v", body.Labels, wantLabels)
			}
			return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: []byte(`{"Id":"` + testContainerID + `","Warnings":[]}`)}, nil
		case 3:
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
	if requests != 3 || container.ID() != testContainerID || container.RunID() != testRunID || container.Namespace() != spec.Namespace || container.Tool() != spec.Tool {
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
			observed := spec
			observed.TTY = !spec.TTY
			return ownedContainerInspect(testContainerID, observed, testRunID, false, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := testContainerCreateSpec()
			spec.Mounts = spec.Mounts[:1]
			deps := testCreateDependencies()
			calls := 0
			deps.operations.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
				calls++
				switch calls {
				case 1:
					return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: response(spec)}, nil
				case 2:
					if name == "warning" {
						return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: ownedContainerInspect(testContainerID, spec, testRunID, false, true)}, nil
					}
					return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: response(spec)}, nil
				case 3:
					if name == "warning" {
						if request.Method != http.MethodDelete || request.Path != "/containers/"+testContainerID {
							t.Fatalf("rollback request = %#v", request)
						}
						return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
					}
					return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: response(spec)}, nil
				case 4:
					if name == "warning" {
						return operationResult{StatusCode: http.StatusNotFound, PeerUID: 0, Raw: []byte(`{"message":"No such container"}`)}, nil
					}
					if request.Method != http.MethodDelete || request.Path != "/containers/"+testContainerID || request.Query.Get("force") != "false" || request.Query.Get("v") != "false" {
						t.Fatalf("rollback request = %#v", request)
					}
					return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
				case 5:
					return operationResult{StatusCode: http.StatusNotFound, PeerUID: 0, Raw: []byte(`{"message":"No such container"}`)}, nil
				default:
					t.Fatalf("unexpected request %d: %#v", calls, request)
					return operationResult{}, nil
				}
			}
			_, err := createContainer(context.Background(), spec, deps)
			if err == nil {
				t.Fatal("createContainer() succeeded")
			}
			wantCalls := 5
			if name == "warning" {
				wantCalls = 4
			}
			if name == "foreign labels" {
				wantCalls = 3
				if !strings.Contains(err.Error(), "rollback") || !strings.Contains(err.Error(), "label") {
					t.Fatalf("foreign rollback error = %v", err)
				}
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
		"tool":                          func(spec *ContainerCreateSpec) { spec.Tool = "Bad" },
		"namespace":                     func(spec *ContainerCreateSpec) { spec.Namespace = "wsl2-short" },
		"image":                         func(spec *ContainerCreateSpec) { spec.Image = " image" },
		"image UTF-8":                   func(spec *ContainerCreateSpec) { spec.Image = string([]byte{0xff}) },
		"working directory":             func(spec *ContainerCreateSpec) { spec.WorkingDirectory = "/" },
		"command NUL":                   func(spec *ContainerCreateSpec) { spec.Command = []string{"bad\x00arg"} },
		"command UTF-8":                 func(spec *ContainerCreateSpec) { spec.Command = []string{string([]byte{0xff})} },
		"environment":                   func(spec *ContainerCreateSpec) { spec.Environment = []string{"1BAD=value"} },
		"environment UTF-8":             func(spec *ContainerCreateSpec) { spec.Environment = []string{"GOOD=" + string([]byte{0xff})} },
		"duplicate env":                 func(spec *ContainerCreateSpec) { spec.Environment = []string{"A=1", "A=2"} },
		"mount type":                    func(spec *ContainerCreateSpec) { spec.Mounts[0].Type = "socket" },
		"mount source":                  func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = "relative" },
		"mount target":                  func(spec *ContainerCreateSpec) { spec.Mounts[0].Target = "/" },
		"Docker socket":                 func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = DockerSocketPath },
		"Docker socket alias":           func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = "/run/docker.sock" },
		"Docker socket directory":       func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = "/var/run" },
		"Docker socket alias directory": func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = "/run" },
		"Docker socket ancestor":        func(spec *ContainerCreateSpec) { spec.Mounts[0].Source = "/var" },
		"foreign volume":                func(spec *ContainerCreateSpec) { spec.Mounts[1].Source = "foreign-volume" },
		"unproven volume":               func(spec *ContainerCreateSpec) { spec.Mounts[1].VolumeLabels = nil },
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
	valid.Mounts = valid.Mounts[:1]
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
			spec := testContainerCreateSpec()
			spec.Mounts = spec.Mounts[:1]
			if _, err := createContainer(context.Background(), spec, deps); err == nil {
				t.Fatal("createContainer() succeeded")
			}
			if calls != 1 {
				t.Fatalf("unsafe response triggered %d requests; no exact cleanup target exists", calls)
			}
		})
	}
}

func TestCreateContainerProvesExactVolumeBeforeCreation(t *testing.T) {
	for name, result := range map[string]operationResult{
		"missing": {StatusCode: http.StatusNotFound, PeerUID: 0, Raw: []byte(`{"message":"No such volume"}`)},
		"foreign": {StatusCode: http.StatusOK, PeerUID: 0, Raw: func() []byte {
			mount := testContainerCreateSpec().Mounts[1]
			mount.VolumeLabels = map[string]string{
				"cb.managed": "true", "cb.kind": "shared", "cb.owner": "node24/foreign", containerNamespaceLabel: testWSLNamespace,
			}
			return exactVolumeInspect(mount)
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			deps := testCreateDependencies()
			calls := 0
			deps.operations.perform = func(_ context.Context, _ string, request Request) (operationResult, error) {
				calls++
				if request.Method != http.MethodGet || !strings.HasPrefix(request.Path, "/volumes/") {
					t.Fatalf("request = %#v", request)
				}
				return result, nil
			}
			if _, err := createContainer(context.Background(), testContainerCreateSpec(), deps); err == nil {
				t.Fatal("createContainer() accepted an unproven volume")
			}
			if calls != 1 {
				t.Fatalf("requests = %d; container creation must not be attempted", calls)
			}
		})
	}
}

func TestCreateContainerRejectsSymlinkedBindRootBeforeDockerProof(t *testing.T) {
	deps := testCreateDependencies()
	deps.resolveBindSource = func(source string) (string, error) {
		if source != "/home/alice/project" {
			t.Fatalf("bind source = %q", source)
		}
		return "/run", nil
	}
	deps.operations.check = func(context.Context) (Result, error) {
		panic("Docker proof reached for unsafe bind source")
	}
	if _, err := createContainer(context.Background(), testContainerCreateSpec(), deps); err == nil || !strings.Contains(err.Error(), "symlinked bind roots are forbidden") {
		t.Fatalf("bind proof error = %v", err)
	}
}

func TestCreateContainerRollbackUsesFreshBoundedContext(t *testing.T) {
	spec := testContainerCreateSpec()
	spec.Mounts = spec.Mounts[:1]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := testCreateDependencies()
	calls := 0
	deps.operations.perform = func(operationContext context.Context, _ string, request Request) (operationResult, error) {
		calls++
		switch calls {
		case 1:
			return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: []byte(`{"Id":"` + testContainerID + `","Warnings":[]}`)}, nil
		case 2:
			cancel()
			return operationResult{}, context.Canceled
		case 3:
			if operationContext.Err() != nil {
				t.Fatalf("rollback reused canceled context: %v", operationContext.Err())
			}
			if _, ok := operationContext.Deadline(); !ok {
				t.Fatal("rollback context has no deadline")
			}
			return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: ownedContainerInspect(testContainerID, spec, testRunID, false, true)}, nil
		case 4:
			if request.Method != http.MethodDelete {
				t.Fatalf("rollback request = %#v", request)
			}
			return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
		case 5:
			return operationResult{StatusCode: http.StatusNotFound, PeerUID: 0, Raw: []byte(`{"message":"No such container"}`)}, nil
		default:
			t.Fatalf("unexpected request %d: %#v", calls, request)
			return operationResult{}, nil
		}
	}
	_, err := createContainer(ctx, spec, deps)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("create cancellation error = %v", err)
	}
	if calls != 5 {
		t.Fatalf("requests = %d", calls)
	}
}

func TestCreateContainerReportsRollbackFailure(t *testing.T) {
	spec := testContainerCreateSpec()
	spec.Mounts = spec.Mounts[:1]
	deps := testCreateDependencies()
	calls := 0
	deps.operations.perform = func(context.Context, string, Request) (operationResult, error) {
		calls++
		if calls == 1 {
			return operationResult{StatusCode: http.StatusCreated, PeerUID: 0, Raw: []byte(`{"Id":"` + testContainerID + `","Warnings":["warning"]}`)}, nil
		}
		if calls == 2 {
			return operationResult{StatusCode: http.StatusOK, PeerUID: 0, Raw: ownedContainerInspect(testContainerID, spec, testRunID, false, true)}, nil
		}
		return operationResult{StatusCode: http.StatusConflict, PeerUID: 0, Raw: []byte(`{"message":"container is running"}`)}, nil
	}
	_, err := createContainer(context.Background(), spec, deps)
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
			{Type: "volume", Source: "cb-" + testWSLNamespace + "-node-modules", Target: "/workspace/project/node_modules", VolumeLabels: testVolumeLabels()},
		},
		TTY: true,
	}
}

func testCreateDependencies() createDependencies {
	return createDependencies{
		operations:        validOperationDependencies(validSocketInfo()),
		newRunID:          func() (string, error) { return testRunID, nil },
		resolveBindSource: func(source string) (string, error) { return source, nil },
	}
}

func ownedContainerInspect(id string, spec ContainerCreateSpec, runID string, running, autoRemove bool) []byte {
	body := struct {
		ID     string `json:"Id"`
		Config struct {
			Labels       map[string]string `json:"Labels"`
			TTY          bool              `json:"Tty"`
			AttachStdin  bool              `json:"AttachStdin"`
			AttachStdout bool              `json:"AttachStdout"`
			AttachStderr bool              `json:"AttachStderr"`
			OpenStdin    bool              `json:"OpenStdin"`
			StdinOnce    bool              `json:"StdinOnce"`
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
	body.Config.AttachStdin = true
	body.Config.AttachStdout = true
	body.Config.AttachStderr = true
	body.Config.OpenStdin = true
	body.Config.StdinOnce = true
	body.State.Running = running
	body.HostConfig.AutoRemove = autoRemove
	raw, _ := json.Marshal(body)
	return raw
}

func testVolumeLabels() map[string]string {
	return map[string]string{
		"cb.managed":            "true",
		"cb.kind":               "shared",
		"cb.owner":              "node24/node-modules",
		containerNamespaceLabel: testWSLNamespace,
	}
}

func exactVolumeInspect(mount ContainerMount) []byte {
	raw, _ := json.Marshal(map[string]any{
		"Name": mount.Source, "Driver": "local", "Scope": "local", "Labels": mount.VolumeLabels,
	})
	return raw
}

func engineMounts(mounts []ContainerMount) []ContainerMount {
	cloned := append([]ContainerMount(nil), mounts...)
	for index := range cloned {
		cloned[index].VolumeLabels = nil
	}
	return cloned
}
