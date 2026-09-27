// Package wslproject classifies canonical project roots for the native WSL2
// frontend. It does not map arguments or enable WSL execution.
package wslproject

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

type Storage string

const (
	Distribution Storage = "distribution"
	WindowsDrive Storage = "windows-drive"
)

// Project is a canonical, case-sensitive native-WSL project identity.
type Project struct {
	Root         string
	Storage      Storage
	MountPoint   string
	WindowsDrive string
}

type pathInfo struct {
	Mode os.FileMode
	Dev  uint64
}

type dependencies struct {
	currentRuntime func() (hostenv.Runtime, error)
	lstat          func(string) (pathInfo, error)
	evalSymlinks   func(string) (string, error)
	readMountInfo  func() ([]byte, error)
}

type mountInfo struct {
	id           int
	parentID     int
	point        string
	filesystem   string
	source       string
	superOptions string
}

func classify(root string, d dependencies) (Project, error) {
	if err := validateRoot(root); err != nil {
		return Project{}, err
	}
	runtime, err := d.currentRuntime()
	if err != nil {
		return Project{}, fmt.Errorf("classify native WSL runtime: %w", err)
	}
	if runtime.Kind != hostenv.WSL2Native {
		return Project{}, fmt.Errorf("WSL project classification requires runtime kind %q, got %q", hostenv.WSL2Native, runtime.Kind)
	}
	info, err := d.lstat(root)
	if err != nil {
		return Project{}, fmt.Errorf("inspect WSL project root %s: %w", root, err)
	}
	if info.Mode&os.ModeSymlink != 0 || !info.Mode.IsDir() {
		return Project{}, fmt.Errorf("WSL project root %s must be a non-symlink directory", root)
	}
	resolved, err := d.evalSymlinks(root)
	if err != nil {
		return Project{}, fmt.Errorf("resolve WSL project root %s: %w", root, err)
	}
	if resolved != root {
		return Project{}, fmt.Errorf("WSL project root %s resolves through a symlink to %s", root, resolved)
	}
	rawMounts, err := d.readMountInfo()
	if err != nil {
		return Project{}, fmt.Errorf("read WSL mount table: %w", err)
	}
	mounts, err := parseMountInfo(rawMounts)
	if err != nil {
		return Project{}, fmt.Errorf("parse WSL mount table: %w", err)
	}
	mount, err := containingMount(root, mounts)
	if err != nil {
		return Project{}, fmt.Errorf("select mount for WSL project root %s: %w", root, err)
	}
	if isDrvFS(mount) {
		drive, ok := defaultDriveMount(mount.point)
		if !ok {
			return Project{}, fmt.Errorf("WSL project root %s uses DrvFs at unsupported mount point %s", root, mount.point)
		}
		if root == mount.point {
			return Project{}, fmt.Errorf("WSL project root %s cannot be an entire Windows drive", root)
		}
		return Project{Root: root, Storage: WindowsDrive, MountPoint: mount.point, WindowsDrive: drive}, nil
	}
	if _, ok := defaultDriveRoot(root); ok {
		return Project{}, fmt.Errorf("WSL project root %s is under /mnt/<drive> but the mount is not proven DrvFs", root)
	}
	distroRoot, err := d.lstat("/")
	if err != nil {
		return Project{}, fmt.Errorf("inspect native WSL distribution root: %w", err)
	}
	if info.Dev != distroRoot.Dev {
		return Project{}, fmt.Errorf("WSL project root %s is on an unqualified filesystem device", root)
	}
	return Project{Root: root, Storage: Distribution, MountPoint: mount.point}, nil
}

func validateRoot(root string) error {
	if root == "" || !utf8.ValidString(root) || !path.IsAbs(root) || path.Clean(root) != root || root == "/" || strings.ContainsRune(root, '\\') {
		return errors.New("WSL project root must be a canonical absolute non-root Linux path")
	}
	for _, r := range root {
		if unicode.IsControl(r) {
			return errors.New("WSL project root contains a control character")
		}
	}
	return nil
}

func parseMountInfo(raw []byte) ([]mountInfo, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, errors.New("mount table is empty")
	}
	mounts := make([]mountInfo, 0, len(lines))
	seenIDs := make(map[int]bool, len(lines))
	for index, line := range lines {
		fields := strings.Fields(line)
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) < separator+4 {
			return nil, fmt.Errorf("line %d has invalid field layout", index+1)
		}
		mountID, err := strconv.Atoi(fields[0])
		if err != nil || mountID <= 0 || seenIDs[mountID] {
			return nil, fmt.Errorf("line %d has invalid mount ID", index+1)
		}
		seenIDs[mountID] = true
		parentID, err := strconv.Atoi(fields[1])
		if err != nil || parentID <= 0 || parentID == mountID {
			return nil, fmt.Errorf("line %d has invalid parent mount ID", index+1)
		}
		point, err := unescapeMountField(fields[4])
		if err != nil || !path.IsAbs(point) || path.Clean(point) != point {
			return nil, fmt.Errorf("line %d has invalid mount point", index+1)
		}
		source, err := unescapeMountField(fields[separator+2])
		if err != nil {
			return nil, fmt.Errorf("line %d has invalid mount source", index+1)
		}
		mounts = append(mounts, mountInfo{
			id:           mountID,
			parentID:     parentID,
			point:        point,
			filesystem:   fields[separator+1],
			source:       source,
			superOptions: fields[separator+3],
		})
	}
	return mounts, nil
}

func unescapeMountField(field string) (string, error) {
	var decoded strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] != '\\' {
			decoded.WriteByte(field[i])
			continue
		}
		if i+3 >= len(field) {
			return "", errors.New("truncated mount escape")
		}
		escape := field[i+1 : i+4]
		switch escape {
		case "040":
			decoded.WriteByte(' ')
		case "011":
			decoded.WriteByte('\t')
		case "012":
			decoded.WriteByte('\n')
		case "134":
			decoded.WriteByte('\\')
		default:
			return "", fmt.Errorf("unsupported mount escape %q", escape)
		}
		i += 3
	}
	return decoded.String(), nil
}

func containingMount(root string, mounts []mountInfo) (mountInfo, error) {
	deepest := ""
	for _, mount := range mounts {
		contains := mount.point == "/" || root == mount.point || strings.HasPrefix(root, mount.point+"/")
		if !contains {
			continue
		}
		if len(mount.point) > len(deepest) {
			deepest = mount.point
		}
	}
	if deepest == "" {
		return mountInfo{}, errors.New("no containing mount")
	}
	byID := make(map[int]mountInfo, len(mounts))
	var candidates []mountInfo
	for _, mount := range mounts {
		byID[mount.id] = mount
		if mount.point == deepest {
			candidates = append(candidates, mount)
		}
	}
	var visible []mountInfo
	for _, candidate := range candidates {
		hidden := false
		for _, other := range candidates {
			if other.id != candidate.id && mountDescendsFrom(other, candidate.id, byID) {
				hidden = true
				break
			}
		}
		if !hidden {
			visible = append(visible, candidate)
		}
	}
	if len(visible) != 1 {
		return mountInfo{}, fmt.Errorf("mount point %s has %d visible candidates", deepest, len(visible))
	}
	return visible[0], nil
}

func mountDescendsFrom(mount mountInfo, ancestorID int, byID map[int]mountInfo) bool {
	visited := make(map[int]bool)
	for parentID := mount.parentID; parentID != 0 && !visited[parentID]; {
		if parentID == ancestorID {
			return true
		}
		visited[parentID] = true
		parent, ok := byID[parentID]
		if !ok {
			return false
		}
		parentID = parent.parentID
	}
	return false
}

func isDrvFS(mount mountInfo) bool {
	switch mount.filesystem {
	case "9p":
		for _, option := range strings.Split(mount.superOptions, ",") {
			if option == "aname=drvfs" || strings.HasPrefix(option, "aname=drvfs;") {
				return true
			}
		}
		return false
	case "virtiofs":
		return strings.HasPrefix(mount.source, "drvfs")
	default:
		return false
	}
}

func defaultDriveMount(point string) (string, bool) {
	if len(point) != len("/mnt/c") || !strings.HasPrefix(point, "/mnt/") || point[5] < 'a' || point[5] > 'z' {
		return "", false
	}
	return string(point[5]), true
}

func defaultDriveRoot(root string) (string, bool) {
	if len(root) < len("/mnt/c") || !strings.HasPrefix(root, "/mnt/") {
		return "", false
	}
	drive := root[5]
	if drive >= 'A' && drive <= 'Z' {
		drive += 'a' - 'A'
	}
	if drive < 'a' || drive > 'z' {
		return "", false
	}
	if len(root) != len("/mnt/c") && root[6] != '/' {
		return "", false
	}
	return string(drive), true
}
