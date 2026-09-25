package hostenv

import (
	"strings"
	"testing"
)

const testMachineID = "0123456789abcdef0123456789abcdef"

func TestNativeWSLLayoutIsFixedAndDistributionScoped(t *testing.T) {
	runtime := Runtime{Kind: WSL2Native, Distro: "Ubuntu-24.04"}
	layout, err := runtime.NativeWSLLayout("/home/alice", 1000, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	want := WSLLayout{
		Distro:         "Ubuntu-24.04",
		UID:            1000,
		Home:           "/home/alice",
		BinaryPath:     "/home/alice/.local/lib/container-bin/cb",
		ManagementShim: "/home/alice/.local/bin/cb",
		ShimDir:        "/home/alice/.local/bin",
		ConfigDir:      "/home/alice/.config/container-bin",
		RegistryPath:   "/home/alice/.config/container-bin/container-bin.toml",
		LockPath:       "/home/alice/.config/container-bin/container-bin.lock",
		StateDir:       "/home/alice/.local/state/container-bin",
		StateNamespace: "wsl2-98d6411d95851ca46df32643077388c2",
	}
	if layout != want {
		t.Fatalf("NativeWSLLayout =\n%+v\nwant\n%+v", layout, want)
	}
	if strings.Contains(layout.StateNamespace, runtime.Distro) || strings.Contains(layout.StateNamespace, testMachineID) {
		t.Fatalf("state namespace exposes raw identity: %q", layout.StateNamespace)
	}

	again, err := runtime.NativeWSLLayout("/home/alice", 1000, testMachineID)
	if err != nil || again.StateNamespace != layout.StateNamespace {
		t.Fatalf("state namespace is not stable: (%q, %v)", again.StateNamespace, err)
	}
	otherDistro, err := (Runtime{Kind: WSL2Native, Distro: "Debian"}).NativeWSLLayout("/home/alice", 1000, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	otherMachine, err := runtime.NativeWSLLayout("/home/alice", 1000, "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := runtime.NativeWSLLayout("/home/alice", 1001, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	caseVariant, err := (Runtime{Kind: WSL2Native, Distro: "ubuntu-24.04"}).NativeWSLLayout("/home/alice", 1000, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for name, namespace := range map[string]string{
		"distro":        otherDistro.StateNamespace,
		"machine":       otherMachine.StateNamespace,
		"user":          otherUser.StateNamespace,
		"distro casing": caseVariant.StateNamespace,
	} {
		if namespace == layout.StateNamespace {
			t.Errorf("%s identity collapsed to %q", name, namespace)
		}
	}
}

func TestNativeWSLLayoutRejectsAmbiguousInputs(t *testing.T) {
	cases := []struct {
		name      string
		runtime   Runtime
		home      string
		machineID string
		want      string
	}{
		{name: "wrong runtime", runtime: Runtime{Kind: WindowsNative}, home: "/home/alice", machineID: testMachineID, want: "requires runtime kind"},
		{name: "missing distro", runtime: Runtime{Kind: WSL2Native}, home: "/home/alice", machineID: testMachineID, want: "distribution identity"},
		{name: "padded distro", runtime: Runtime{Kind: WSL2Native, Distro: " Ubuntu"}, home: "/home/alice", machineID: testMachineID, want: "canonical UTF-8"},
		{name: "distro control", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu\n"}, home: "/home/alice", machineID: testMachineID, want: "canonical UTF-8"},
		{name: "relative home", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "home/alice", machineID: testMachineID, want: "canonical absolute"},
		{name: "root home", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/", machineID: testMachineID, want: "non-root"},
		{name: "unclean home", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/../alice", machineID: testMachineID, want: "canonical absolute"},
		{name: "trailing slash", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/alice/", machineID: testMachineID, want: "canonical absolute"},
		{name: "Windows filesystem home", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/mnt/c/Users/alice", machineID: testMachineID, want: "distribution-local"},
		{name: "backslash home", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/alice\\x", machineID: testMachineID, want: "canonical absolute"},
		{name: "short machine ID", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/alice", machineID: "abcd", want: "32 canonical"},
		{name: "uppercase machine ID", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/alice", machineID: strings.ToUpper(testMachineID), want: "32 canonical"},
		{name: "non-hex machine ID", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/alice", machineID: strings.Repeat("g", 32), want: "32 canonical"},
		{name: "zero machine ID", runtime: Runtime{Kind: WSL2Native, Distro: "Ubuntu"}, home: "/home/alice", machineID: strings.Repeat("0", 32), want: "all zeroes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.runtime.NativeWSLLayout(tc.home, 1000, tc.machineID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NativeWSLLayout error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestClassifiedPaddedDistroDoesNotCollapseStateIdentity(t *testing.T) {
	runtime := classify("linux", "6.6.87.2-microsoft-standard-WSL2", " Ubuntu-24.04", "")
	if runtime.Distro != " Ubuntu-24.04" {
		t.Fatalf("classify() distro = %q, want raw identity", runtime.Distro)
	}
	_, err := runtime.NativeWSLLayout("/home/alice", 1000, testMachineID)
	if err == nil || !strings.Contains(err.Error(), "canonical UTF-8") {
		t.Fatalf("NativeWSLLayout error = %v, want padded identity rejection", err)
	}
}
