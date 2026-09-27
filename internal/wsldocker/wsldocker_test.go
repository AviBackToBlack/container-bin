package wsldocker

import (
	"context"
	"errors"
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
