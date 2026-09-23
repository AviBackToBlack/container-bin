// Package hostenv classifies the host process boundary before ContainerBin
// reads configuration or performs Docker/filesystem work.
package hostenv

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const maxKernelReleaseSize = 4 << 10

type Kind string

const (
	WindowsNative     Kind = "windows-native"
	WindowsWSLInterop Kind = "windows-wsl-interop"
	WSL2Native        Kind = "wsl2-native"
	WSL1Native        Kind = "wsl1-native"
	WSLUnrecognized   Kind = "wsl-microsoft-unrecognized"
	LinuxNative       Kind = "linux-native"
	Unsupported       Kind = "unsupported"
)

type Runtime struct {
	Kind           Kind
	GOOS           string
	KernelRelease  string
	Distro         string
	InteropMarkers []string
}

// Current returns a conservative classification of the current process. WSL2
// must identify itself through the Microsoft WSL2 kernel release; environment
// variables alone never upgrade an ordinary Linux process into WSL support.
func Current() (Runtime, error) {
	goos := runtime.GOOS
	kernelRelease := ""
	if goos == "linux" {
		var err error
		kernelRelease, err = readBoundedFile("/proc/sys/kernel/osrelease")
		if err != nil {
			return Runtime{}, fmt.Errorf("read Linux kernel release: %w", err)
		}
	}
	return classify(goos, kernelRelease, os.Getenv("WSL_DISTRO_NAME"), os.Getenv("WSL_INTEROP")), nil
}

// RequireFrontend enforces the currently shipped host boundary. Native
// Windows is supported. WSL2 is recognized explicitly but remains gated until
// its config, shim, state-namespace and Docker integration slices have landed.
func RequireFrontend() error {
	return requireFrontend(Current())
}

func requireFrontend(info Runtime, probeErr error) error {
	if probeErr != nil {
		return fmt.Errorf("cannot prove a supported host runtime: %w", probeErr)
	}
	switch info.Kind {
	case WindowsNative:
		return nil
	case WindowsWSLInterop:
		markers := strings.Join(info.InteropMarkers, ", ")
		if markers == "" {
			markers = "WSL_INTEROP or WSL_DISTRO_NAME"
		}
		return fmt.Errorf("Windows ContainerBin process inherited WSL interoperability marker(s): %s; this invocation is unsupported; run cb from a native Windows process, or use the native WSL frontend after it is released", markers)
	case WSL2Native:
		if info.Distro == "" {
			return errors.New("native WSL2 was detected but WSL_DISTRO_NAME is unavailable, so distribution identity cannot be proven")
		}
		return fmt.Errorf("native WSL2 distribution %q was detected, but the WSL frontend is not enabled in this release", info.Distro)
	case WSL1Native:
		return errors.New("WSL1 is unsupported; the planned native frontend requires WSL2 and Docker Desktop WSL integration")
	case WSLUnrecognized:
		return fmt.Errorf("Microsoft WSL kernel %q lacks an explicit WSL2 marker, so its generation cannot be proven; this host is unsupported", info.KernelRelease)
	case LinuxNative:
		return errors.New("standalone Linux hosts are unsupported; Linux execution is limited to the planned native WSL2 frontend")
	default:
		return fmt.Errorf("host operating system %q is unsupported", info.GOOS)
	}
}

func classify(goos, kernelRelease, distro, interop string) Runtime {
	info := Runtime{
		GOOS:          strings.TrimSpace(goos),
		KernelRelease: strings.TrimSpace(kernelRelease),
		Distro:        strings.TrimSpace(distro),
	}
	interop = strings.TrimSpace(interop)
	switch info.GOOS {
	case "windows":
		if interop != "" {
			info.InteropMarkers = append(info.InteropMarkers, "WSL_INTEROP")
		}
		if info.Distro != "" {
			info.InteropMarkers = append(info.InteropMarkers, "WSL_DISTRO_NAME")
		}
		if len(info.InteropMarkers) != 0 {
			info.Kind = WindowsWSLInterop
		} else {
			info.Kind = WindowsNative
		}
	case "linux":
		kernel := strings.ToLower(info.KernelRelease)
		switch {
		case strings.Contains(kernel, "microsoft") && strings.Contains(kernel, "wsl2"):
			info.Kind = WSL2Native
		case strings.Contains(kernel, "microsoft-standard"):
			info.Kind = WSLUnrecognized
		case strings.Contains(kernel, "microsoft"):
			info.Kind = WSL1Native
		default:
			info.Kind = LinuxNative
		}
	default:
		info.Kind = Unsupported
	}
	return info
}

func readBoundedFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxKernelReleaseSize+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxKernelReleaseSize {
		return "", fmt.Errorf("%s exceeds %d bytes", path, maxKernelReleaseSize)
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return value, nil
}
