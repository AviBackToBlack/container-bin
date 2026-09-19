package hostenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name, goos, kernel, distro, interop string
		want                                Kind
	}{
		{name: "windows", goos: "windows", want: WindowsNative},
		{name: "windows distro marker", goos: "windows", distro: "Ubuntu", want: WindowsWSLInterop},
		{name: "windows interop marker", goos: "windows", interop: "/run/WSL/1_interop", want: WindowsWSLInterop},
		{name: "wsl2", goos: "linux", kernel: "6.6.87.2-microsoft-standard-WSL2", distro: "Ubuntu", want: WSL2Native},
		{name: "wsl2 case insensitive", goos: "linux", kernel: "5.15.167.4-MICROSOFT-standard-wsl2", want: WSL2Native},
		{name: "wsl1", goos: "linux", kernel: "4.4.0-19041-Microsoft", want: WSL1Native},
		{name: "standalone linux ignores env alone", goos: "linux", kernel: "6.12.0-generic", distro: "Ubuntu", interop: "/run/WSL/1_interop", want: LinuxNative},
		{name: "darwin", goos: "darwin", want: Unsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.goos, tc.kernel, tc.distro, tc.interop)
			if got.Kind != tc.want {
				t.Fatalf("classify() kind = %q, want %q (%+v)", got.Kind, tc.want, got)
			}
		})
	}
}

func TestRequireFrontend(t *testing.T) {
	tests := []struct {
		name     string
		info     Runtime
		probeErr error
		want     string
	}{
		{name: "windows native", info: Runtime{Kind: WindowsNative}},
		{name: "windows interop", info: Runtime{Kind: WindowsWSLInterop}, want: "Windows cb.exe launched through WSL"},
		{name: "wsl2 missing distro", info: Runtime{Kind: WSL2Native}, want: "distribution identity cannot be proven"},
		{name: "wsl2 gated", info: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, want: "WSL frontend is not enabled"},
		{name: "wsl1", info: Runtime{Kind: WSL1Native}, want: "WSL1 is unsupported"},
		{name: "linux", info: Runtime{Kind: LinuxNative}, want: "standalone Linux hosts are unsupported"},
		{name: "other", info: Runtime{Kind: Unsupported, GOOS: "darwin"}, want: `operating system "darwin" is unsupported`},
		{name: "probe", probeErr: errors.New("no proc"), want: "cannot prove a supported host runtime"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := requireFrontend(tc.info, tc.probeErr)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("requireFrontend() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("requireFrontend() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadBoundedFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("6.6.0-microsoft-standard-WSL2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readBoundedFile(good)
	if err != nil || got != "6.6.0-microsoft-standard-WSL2" {
		t.Fatalf("readBoundedFile() = (%q, %v)", got, err)
	}

	for name, content := range map[string]string{
		"empty":    " \r\n",
		"oversize": strings.Repeat("x", maxKernelReleaseSize+1),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBoundedFile(path); err == nil {
			t.Errorf("readBoundedFile(%s) succeeded", name)
		}
	}
}
