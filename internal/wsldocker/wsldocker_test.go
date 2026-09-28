package wsldocker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const validInfo = `{
  "ServerVersion":"29.1.0",
  "KernelVersion":"6.6.87.2-microsoft-standard-WSL2",
  "OperatingSystem":"Docker Desktop",
  "OSType":"linux",
  "Name":"docker-desktop",
  "Labels":["com.docker.desktop.address=unix:///var/run/docker-cli.sock"]
}`

func TestCheckAcceptsExactDockerDesktopWSLIntegration(t *testing.T) {
	deps := validDependencies()
	result, err := check(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Host != DockerHost || result.ServerVersion != "29.1.0" || result.DesktopAddress != "unix:///var/run/docker-cli.sock" {
		t.Fatalf("Check() = %+v", result)
	}
}

func TestValidateInfoAcceptsDocumentedDesktopAddressForms(t *testing.T) {
	for _, tc := range []struct {
		json string
		want string
	}{
		{json: "unix:///var/run/docker-cli.sock", want: "unix:///var/run/docker-cli.sock"},
		{json: `npipe://\\\\.\\pipe\\docker_cli`, want: `npipe://\\.\pipe\docker_cli`},
	} {
		t.Run(tc.want, func(t *testing.T) {
			raw := strings.Replace(validInfo, "unix:///var/run/docker-cli.sock", tc.json, 1)
			result, err := validateInfo([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if result.DesktopAddress != tc.want {
				t.Fatalf("DesktopAddress = %q, want %q", result.DesktopAddress, tc.want)
			}
		})
	}
}

func TestCheckRejectsNilContextBeforeProbing(t *testing.T) {
	deps := validDependencies()
	deps.currentRuntime = func() (hostenv.Runtime, error) { panic("runtime called with nil context") }
	if _, err := check(nil, deps); err == nil || !strings.Contains(err.Error(), "requires a context") {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestCheckRejectsAmbiguousBoundaries(t *testing.T) {
	tests := map[string]func(*dependencies){
		"non WSL runtime": func(d *dependencies) {
			d.currentRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.LinuxNative}, nil }
		},
		"missing distro identity": func(d *dependencies) {
			d.currentRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{Kind: hostenv.WSL2Native}, nil }
		},
		"noncanonical distro identity": func(d *dependencies) {
			d.currentRuntime = func() (hostenv.Runtime, error) {
				return hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: " Ubuntu-24.04"}, nil
			}
		},
		"runtime error": func(d *dependencies) {
			d.currentRuntime = func() (hostenv.Runtime, error) { return hostenv.Runtime{}, errors.New("kernel unavailable") }
		},
		"regular endpoint": func(d *dependencies) {
			d.statSocket = func(string) (socketInfo, error) { return socketInfo{Mode: 0o660, UID: 0, Dev: 1, Ino: 2}, nil }
		},
		"user owned endpoint": func(d *dependencies) {
			d.statSocket = func(string) (socketInfo, error) {
				return socketInfo{Mode: os.ModeSocket | 0o660, UID: 1000, Dev: 1, Ino: 2}, nil
			}
		},
		"world writable endpoint": func(d *dependencies) {
			d.statSocket = func(string) (socketInfo, error) {
				return socketInfo{Mode: os.ModeSocket | 0o666, UID: 0, Dev: 1, Ino: 2}, nil
			}
		},
		"probe error": func(d *dependencies) {
			d.probeInfo = func(context.Context, string) (probeResult, error) { return probeResult{}, errors.New("unreachable") }
		},
		"untrusted socket peer": func(d *dependencies) {
			d.probeInfo = func(context.Context, string) (probeResult, error) {
				return probeResult{Raw: []byte(validInfo), PeerUID: 1000}, nil
			}
		},
		"socket replaced during probe": func(d *dependencies) {
			calls := 0
			d.statSocket = func(string) (socketInfo, error) {
				calls++
				return socketInfo{Mode: os.ModeSocket | 0o660, UID: 0, Dev: 1, Ino: uint64(calls)}, nil
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			deps := validDependencies()
			mutate(&deps)
			if _, err := check(context.Background(), deps); err == nil {
				t.Fatal("Check() succeeded")
			}
		})
	}
}

func TestCheckRejectsDockerEndpointEnvironmentOverrides(t *testing.T) {
	for _, variable := range dockerRedirectVariables {
		t.Run(variable, func(t *testing.T) {
			deps := validDependencies()
			deps.lookupEnv = func(name string) (string, bool) {
				if name == variable {
					return "configured", true
				}
				return "", false
			}
			if _, err := check(context.Background(), deps); err == nil || !strings.Contains(err.Error(), variable) {
				t.Fatalf("Check() error = %v", err)
			}
		})
	}
}

func TestValidateInfoRejectsNonDesktopAndAmbiguousEvidence(t *testing.T) {
	tests := map[string]string{
		"malformed":            `{`,
		"local engine":         strings.Replace(validInfo, `"Docker Desktop"`, `"Ubuntu 24.04"`, 1),
		"Windows containers":   strings.Replace(validInfo, `"OSType":"linux"`, `"OSType":"windows"`, 1),
		"wrong engine":         strings.Replace(validInfo, `"Name":"docker-desktop"`, `"Name":"local"`, 1),
		"non WSL kernel":       strings.Replace(validInfo, `6.6.87.2-microsoft-standard-WSL2`, `6.8.0-generic`, 1),
		"missing label":        strings.Replace(validInfo, `"com.docker.desktop.address=unix:///var/run/docker-cli.sock"`, `"other=value"`, 1),
		"unsupported label":    strings.Replace(validInfo, `unix:///var/run/docker-cli.sock`, `tcp://127.0.0.1:2375`, 1),
		"duplicate label":      strings.Replace(validInfo, `"com.docker.desktop.address=unix:///var/run/docker-cli.sock"`, `"com.docker.desktop.address=unix:///one","com.docker.desktop.address=npipe://two"`, 1),
		"noncanonical version": strings.Replace(validInfo, `"29.1.0"`, `" 29.1.0"`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := validateInfo([]byte(raw)); err == nil {
				t.Fatal("validateInfo() succeeded")
			}
		})
	}
}

func TestExecuteBindsOperationToProvenSocket(t *testing.T) {
	socket := validSocketInfo()
	request := Request{
		Method:          http.MethodPost,
		Path:            "/containers/example/start",
		Query:           url.Values{"detachKeys": {"ctrl-p,ctrl-q"}},
		Body:            []byte(`{"example":true}`),
		SuccessStatuses: []int{http.StatusNoContent},
	}
	statCalls := 0
	deps := operationDependencies{
		check: func(context.Context) (Result, error) {
			return Result{socket: socket}, nil
		},
		statSocket: func(path string) (socketInfo, error) {
			if path != DockerSocketPath {
				t.Fatalf("stat path = %q", path)
			}
			statCalls++
			return socket, nil
		},
		perform: func(_ context.Context, path string, got Request) (operationResult, error) {
			if path != DockerSocketPath || got.Method != request.Method || got.Path != request.Path || got.Query.Encode() != request.Query.Encode() || string(got.Body) != string(request.Body) {
				t.Fatalf("perform(%q, %+v)", path, got)
			}
			return operationResult{StatusCode: http.StatusNoContent, Raw: []byte("ok"), PeerUID: 0}, nil
		},
	}
	response, err := execute(context.Background(), request, deps)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent || string(response.Body) != "ok" || statCalls != 2 {
		t.Fatalf("execute() = (%+v, stat calls=%d)", response, statCalls)
	}
}

func TestExecuteCopiesMutableRequestStateBeforeProof(t *testing.T) {
	socket := validSocketInfo()
	request := Request{
		Method:          http.MethodPost,
		Path:            "/containers/create",
		Query:           url.Values{"name": {"safe"}},
		Body:            []byte(`{"Image":"example"}`),
		SuccessStatuses: []int{http.StatusCreated},
	}
	deps := validOperationDependencies(socket)
	deps.check = func(context.Context) (Result, error) {
		request.Query.Set("name", "changed")
		request.Body[0] = 'X'
		request.SuccessStatuses[0] = http.StatusOK
		return Result{socket: socket}, nil
	}
	deps.perform = func(_ context.Context, _ string, got Request) (operationResult, error) {
		if got.Query.Get("name") != "safe" || string(got.Body) != `{"Image":"example"}` || got.SuccessStatuses[0] != http.StatusCreated {
			t.Fatalf("mutable request state escaped copy: %+v", got)
		}
		return operationResult{StatusCode: http.StatusCreated, PeerUID: 0}, nil
	}
	if _, err := execute(context.Background(), request, deps); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRejectsInvalidRequestsBeforeProof(t *testing.T) {
	tests := map[string]Request{
		"method":         {Method: http.MethodPut, Path: "/info", SuccessStatuses: []int{http.StatusOK}},
		"relative path":  {Method: http.MethodGet, Path: "info", SuccessStatuses: []int{http.StatusOK}},
		"unclean path":   {Method: http.MethodGet, Path: "/containers/../info", SuccessStatuses: []int{http.StatusOK}},
		"path query":     {Method: http.MethodGet, Path: "/info?all=1", SuccessStatuses: []int{http.StatusOK}},
		"oversized path": {Method: http.MethodGet, Path: "/" + strings.Repeat("x", maxRequestPath), SuccessStatuses: []int{http.StatusOK}},
		"get body":       {Method: http.MethodGet, Path: "/info", Body: []byte("x"), SuccessStatuses: []int{http.StatusOK}},
		"malformed JSON": {Method: http.MethodPost, Path: "/containers/create", Body: []byte("{"), SuccessStatuses: []int{http.StatusCreated}},
		"oversized body": {Method: http.MethodPost, Path: "/build", Body: make([]byte, (1<<20)+1), SuccessStatuses: []int{http.StatusOK}},
		"query":          {Method: http.MethodGet, Path: "/info", Query: url.Values{"x": {strings.Repeat("x", (64<<10)+1)}}, SuccessStatuses: []int{http.StatusOK}},
		"no status":      {Method: http.MethodGet, Path: "/info"},
		"bad status":     {Method: http.MethodGet, Path: "/info", SuccessStatuses: []int{http.StatusBadRequest}},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			deps.check = func(context.Context) (Result, error) { panic("proof reached for invalid request") }
			if _, err := execute(context.Background(), request, deps); err == nil {
				t.Fatal("execute() succeeded")
			}
		})
	}
}

func TestExecuteAllowsExplicitIdempotentNotModifiedStatus(t *testing.T) {
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(context.Context, string, Request) (operationResult, error) {
		return operationResult{StatusCode: http.StatusNotModified, PeerUID: 0}, nil
	}
	request := Request{Method: http.MethodPost, Path: "/containers/example/start", SuccessStatuses: []int{http.StatusNotModified}}
	response, err := execute(context.Background(), request, deps)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("execute() status = %d", response.StatusCode)
	}
}

func TestExecuteRejectsNilContextBeforeProof(t *testing.T) {
	deps := validOperationDependencies(validSocketInfo())
	deps.check = func(context.Context) (Result, error) { panic("proof reached with nil context") }
	request := Request{Method: http.MethodGet, Path: "/info", SuccessStatuses: []int{http.StatusOK}}
	if _, err := execute(nil, request, deps); err == nil || !strings.Contains(err.Error(), "requires a context") {
		t.Fatalf("execute() error = %v", err)
	}
}

func TestExecuteRejectsBoundaryChangesAndUnexpectedResults(t *testing.T) {
	tests := map[string]func(*operationDependencies){
		"proof failure": func(d *operationDependencies) {
			d.check = func(context.Context) (Result, error) { return Result{}, errors.New("not Desktop") }
		},
		"socket replaced after proof": func(d *operationDependencies) {
			d.statSocket = func(string) (socketInfo, error) {
				replaced := validSocketInfo()
				replaced.Ino++
				return replaced, nil
			}
		},
		"non socket after proof": func(d *operationDependencies) {
			d.statSocket = func(string) (socketInfo, error) { return socketInfo{Mode: 0o660, UID: 0}, nil }
		},
		"untrusted operation peer": func(d *operationDependencies) {
			d.perform = func(context.Context, string, Request) (operationResult, error) {
				return operationResult{StatusCode: http.StatusOK, PeerUID: 1000}, nil
			}
		},
		"socket replaced during operation": func(d *operationDependencies) {
			calls := 0
			d.statSocket = func(string) (socketInfo, error) {
				calls++
				socket := validSocketInfo()
				if calls == 2 {
					socket.Ino++
				}
				return socket, nil
			}
		},
		"unexpected status": func(d *operationDependencies) {
			d.perform = func(context.Context, string, Request) (operationResult, error) {
				return operationResult{StatusCode: http.StatusNoContent, PeerUID: 0}, nil
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			deps := validOperationDependencies(validSocketInfo())
			mutate(&deps)
			request := Request{Method: http.MethodGet, Path: "/info", SuccessStatuses: []int{http.StatusOK}}
			if _, err := execute(context.Background(), request, deps); err == nil {
				t.Fatal("execute() succeeded")
			}
		})
	}
}

func TestExecuteReportsBoundedDockerErrorMessage(t *testing.T) {
	deps := validOperationDependencies(validSocketInfo())
	deps.perform = func(context.Context, string, Request) (operationResult, error) {
		return operationResult{
			StatusCode: http.StatusConflict,
			Raw:        []byte(`{"message":"volume is in use - [container abc]"}`),
			PeerUID:    0,
		}, nil
	}
	request := Request{Method: http.MethodDelete, Path: "/volumes/example", SuccessStatuses: []int{http.StatusNoContent}}
	_, err := execute(context.Background(), request, deps)
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), `"volume is in use - [container abc]"`) {
		t.Fatalf("execute() error = %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Method != http.MethodDelete || apiErr.Path != "/volumes/example" || apiErr.StatusCode != http.StatusConflict || apiErr.Message != "volume is in use - [container abc]" {
		t.Fatalf("execute() typed error = %#v", apiErr)
	}
}

func TestExecuteDoesNotEchoUnsafeDockerErrorBody(t *testing.T) {
	for _, body := range [][]byte{
		[]byte("not JSON"),
		[]byte(`{"message":"spoof\nnext line"}`),
		[]byte(`{"message":" leading whitespace"}`),
		[]byte(`{"message":"` + strings.Repeat("x", 4097) + `"}`),
	} {
		deps := validOperationDependencies(validSocketInfo())
		deps.perform = func(context.Context, string, Request) (operationResult, error) {
			return operationResult{StatusCode: http.StatusInternalServerError, Raw: body, PeerUID: 0}, nil
		}
		request := Request{Method: http.MethodGet, Path: "/info", SuccessStatuses: []int{http.StatusOK}}
		_, err := execute(context.Background(), request, deps)
		if err == nil || err.Error() != "Docker Desktop Engine API GET /info returned HTTP 500" {
			t.Fatalf("execute() error = %v for body %q", err, body)
		}
	}
}

func validDependencies() dependencies {
	return dependencies{
		currentRuntime: func() (hostenv.Runtime, error) {
			return hostenv.Runtime{Kind: hostenv.WSL2Native, Distro: "Ubuntu-24.04"}, nil
		},
		lookupEnv: func(string) (string, bool) { return "", false },
		statSocket: func(path string) (socketInfo, error) {
			if path != DockerSocketPath {
				return socketInfo{}, errors.New("unexpected Docker socket path")
			}
			return socketInfo{Mode: os.ModeSocket | 0o660, UID: 0, Dev: 1, Ino: 2}, nil
		},
		probeInfo: func(_ context.Context, path string) (probeResult, error) {
			if path != DockerSocketPath {
				return probeResult{}, errors.New("unexpected Docker probe target")
			}
			return probeResult{Raw: []byte(validInfo), PeerUID: 0}, nil
		},
	}
}

func validSocketInfo() socketInfo {
	return socketInfo{Mode: os.ModeSocket | 0o660, UID: 0, Dev: 1, Ino: 2}
}

func validOperationDependencies(socket socketInfo) operationDependencies {
	return operationDependencies{
		check: func(context.Context) (Result, error) { return Result{socket: socket}, nil },
		statSocket: func(string) (socketInfo, error) {
			return socket, nil
		},
		perform: func(context.Context, string, Request) (operationResult, error) {
			return operationResult{StatusCode: http.StatusOK, PeerUID: 0}, nil
		},
	}
}
