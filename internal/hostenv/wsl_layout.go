package hostenv

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const wslNamespaceDomain = "container-bin/wsl2-state/v1\x00"

// WSLLayout is the fixed user-local filesystem and Docker-state contract for
// one native WSL2 distribution. It is computed without consulting XDG or PATH
// environment variables so a launcher or project cannot redirect trust state.
// The frontend remains gated until the later filesystem, Docker integration,
// path and end-to-end qualification slices land.
type WSLLayout struct {
	Distro         string
	UID            uint32
	Home           string
	BinaryPath     string
	ManagementShim string
	ShimDir        string
	ConfigDir      string
	RegistryPath   string
	LockPath       string
	StateDir       string
	StateNamespace string
}

// NativeWSLLayout returns the WSL2 layout for a classified runtime. The
// machine ID must be the canonical /etc/machine-id value; combining it with the
// exact WSL distribution name and numeric Linux UID prevents distributions or
// users sharing Docker Desktop's daemon from silently sharing ContainerBin
// volumes.
func (r Runtime) NativeWSLLayout(home string, uid uint32, machineID string) (WSLLayout, error) {
	if r.Kind != WSL2Native {
		return WSLLayout{}, fmt.Errorf("native WSL layout requires runtime kind %q, got %q", WSL2Native, r.Kind)
	}
	if err := validateDistroIdentity(r.Distro); err != nil {
		return WSLLayout{}, err
	}
	home, err := validateWSLHome(home)
	if err != nil {
		return WSLLayout{}, err
	}
	machineID, err = validateMachineID(machineID)
	if err != nil {
		return WSLLayout{}, err
	}

	configDir := path.Join(home, ".config/container-bin")
	stateDir := path.Join(home, ".local/state/container-bin")
	shimDir := path.Join(home, ".local/bin")
	binaryPath := path.Join(home, ".local/lib/container-bin/cb")
	identity := wslNamespaceDomain + r.Distro + "\x00" + machineID + "\x00" + strconv.FormatUint(uint64(uid), 10)
	sum := sha256.Sum256([]byte(identity))
	return WSLLayout{
		Distro:         r.Distro,
		UID:            uid,
		Home:           home,
		BinaryPath:     binaryPath,
		ManagementShim: path.Join(shimDir, "cb"),
		ShimDir:        shimDir,
		ConfigDir:      configDir,
		RegistryPath:   path.Join(configDir, "container-bin.toml"),
		LockPath:       path.Join(configDir, "container-bin.lock"),
		StateDir:       stateDir,
		StateNamespace: "wsl2-" + hex.EncodeToString(sum[:16]),
	}, nil
}

func validateDistroIdentity(distro string) error {
	if distro == "" {
		return errors.New("native WSL layout requires a distribution identity")
	}
	if strings.TrimSpace(distro) != distro || !utf8.ValidString(distro) {
		return errors.New("WSL distribution identity is not canonical UTF-8")
	}
	for _, r := range distro {
		if unicode.IsControl(r) {
			return errors.New("WSL distribution identity contains a control character")
		}
	}
	return nil
}

func validateWSLHome(home string) (string, error) {
	if home == "" || !utf8.ValidString(home) || strings.ContainsRune(home, '\x00') || strings.ContainsRune(home, '\\') {
		return "", errors.New("native WSL home must be a canonical absolute Linux path")
	}
	for _, r := range home {
		if unicode.IsControl(r) {
			return "", errors.New("native WSL home contains a control character")
		}
	}
	clean := path.Clean(home)
	if !path.IsAbs(home) || clean != home || clean == "/" {
		return "", errors.New("native WSL home must be a canonical absolute non-root Linux path")
	}
	if clean == "/mnt" || strings.HasPrefix(clean, "/mnt/") {
		return "", errors.New("native WSL home must be distribution-local, not under /mnt")
	}
	return clean, nil
}

func validateMachineID(machineID string) (string, error) {
	if len(machineID) != 32 || machineID != strings.ToLower(machineID) {
		return "", errors.New("WSL machine ID must be 32 canonical lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(machineID)
	if err != nil {
		return "", errors.New("WSL machine ID must be 32 canonical lowercase hexadecimal characters")
	}
	allZero := true
	for _, b := range decoded {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "", errors.New("WSL machine ID must not be all zeroes")
	}
	return machineID, nil
}
