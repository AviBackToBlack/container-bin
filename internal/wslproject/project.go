// Package wslproject classifies canonical project roots for the native WSL2
// frontend. It does not map arguments or enable WSL execution.
package wslproject

import (
	"errors"
	"fmt"
	"io/fs"
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

// Project is a canonical, case-sensitive native-WSL project identity. Storage
// describes Root only; callers must classify each resolved descendant path
// before relying on its storage boundary.
type Project struct {
	Root         string
	Storage      Storage
	MountPoint   string
	WindowsDrive string
	mountID      int
	device       uint64
}

// Descendant is one canonical path proven to remain within a Project's exact
// storage and mount identity. A missing output path is accepted only through
// its nearest existing non-symlink directory ancestor.
type Descendant struct {
	Path            string
	Relative        string
	Exists          bool
	NearestExisting string
}

// InspectionInfo is the filesystem identity needed to prove a native-WSL
// path. It is intentionally narrower than os.FileInfo so deterministic
// integration corpora can exercise the production proof algorithm without a
// live WSL mount table.
type InspectionInfo struct {
	Mode   os.FileMode
	Device uint64
}

// Inspection supplies the native-WSL runtime and filesystem observations used
// by Resolver. Production entry points bind these hooks to the live host;
// portable integration tests can instead provide a fixed, internally
// consistent WSL filesystem model.
type Inspection struct {
	CurrentRuntime func() (hostenv.Runtime, error)
	Lstat          func(string) (InspectionInfo, error)
	EvalSymlinks   func(string) (string, error)
	ReadMountInfo  func() ([]byte, error)
}

// Resolver applies the production native-WSL project selection and path proof
// algorithms to one coherent inspection source.
type Resolver struct {
	deps dependencies
}

// NewResolver constructs a proof resolver from a complete inspection source.
func NewResolver(inspection Inspection) (Resolver, error) {
	if inspection.CurrentRuntime == nil || inspection.Lstat == nil || inspection.EvalSymlinks == nil || inspection.ReadMountInfo == nil {
		return Resolver{}, errors.New("native WSL project inspection is incomplete")
	}
	return Resolver{deps: dependencies{
		currentRuntime: inspection.CurrentRuntime,
		lstat: func(path string) (pathInfo, error) {
			info, err := inspection.Lstat(path)
			return pathInfo{Mode: info.Mode, Dev: info.Device}, err
		},
		evalSymlinks:  inspection.EvalSymlinks,
		readMountInfo: inspection.ReadMountInfo,
	}}, nil
}

// Classify proves the storage and canonical identity of one project root.
func (r Resolver) Classify(root string) (Project, error) {
	deps, err := r.inspectionDependencies()
	if err != nil {
		return Project{}, err
	}
	return classify(root, deps)
}

// ClassifyDescendant revalidates a project and proves one path remains inside
// its exact storage and mount identity.
func (r Resolver) ClassifyDescendant(project Project, candidate string) (Descendant, error) {
	deps, err := r.inspectionDependencies()
	if err != nil {
		return Descendant{}, err
	}
	return resolveDescendant(project, candidate, deps)
}

func (r Resolver) inspectionDependencies() (dependencies, error) {
	if r.deps.currentRuntime == nil || r.deps.lstat == nil || r.deps.evalSymlinks == nil || r.deps.readMountInfo == nil {
		return dependencies{}, errors.New("native WSL project inspection is incomplete")
	}
	return r.deps, nil
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
	device       uint64
	point        string
	filesystem   string
	source       string
	superOptions string
}

type missingBoundary struct {
	nearest string
	missing string
	info    pathInfo
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
	if mount.device != info.Dev {
		return Project{}, fmt.Errorf("mount selected for WSL project root %s does not back the root dentry", root)
	}
	if drive, ok := defaultDriveMount(mount.point); ok && isDrvFS(mount, drive) {
		if root == mount.point {
			return Project{}, fmt.Errorf("WSL project root %s cannot be an entire Windows drive", root)
		}
		return Project{Root: root, Storage: WindowsDrive, MountPoint: mount.point, WindowsDrive: drive, mountID: mount.id, device: mount.device}, nil
	}
	if isDrvFS(mount, "") {
		if _, ok := defaultDriveMount(mount.point); !ok {
			return Project{}, fmt.Errorf("WSL project root %s uses DrvFs at unsupported mount point %s", root, mount.point)
		}
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
	return Project{Root: root, Storage: Distribution, MountPoint: mount.point, mountID: mount.id, device: mount.device}, nil
}

func resolveDescendant(project Project, candidate string, d dependencies) (Descendant, error) {
	if err := validateRoot(project.Root); err != nil {
		return Descendant{}, fmt.Errorf("invalid classified WSL project root: %w", err)
	}
	if err := validateRoot(candidate); err != nil {
		return Descendant{}, fmt.Errorf("invalid WSL project path: %w", err)
	}
	if candidate != project.Root && !strings.HasPrefix(candidate, project.Root+"/") {
		return Descendant{}, fmt.Errorf("WSL path %s is outside project root %s", candidate, project.Root)
	}
	current, err := classify(project.Root, d)
	if err != nil {
		return Descendant{}, fmt.Errorf("revalidate WSL project root %s: %w", project.Root, err)
	}
	if !sameProjectIdentity(project, current) {
		return Descendant{}, fmt.Errorf("WSL project root %s changed storage or mount identity", project.Root)
	}

	nearest := candidate
	exists := true
	var info pathInfo
	for {
		info, err = d.lstat(nearest)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Descendant{}, fmt.Errorf("inspect WSL project path %s: %w", nearest, err)
		}
		exists = false
		if nearest == project.Root {
			return Descendant{}, fmt.Errorf("WSL project root %s disappeared during path classification", project.Root)
		}
		nearest = path.Dir(nearest)
		if nearest != project.Root && !strings.HasPrefix(nearest, project.Root+"/") {
			return Descendant{}, fmt.Errorf("nearest existing ancestor escaped WSL project root %s", project.Root)
		}
	}
	if info.Mode&os.ModeSymlink != 0 {
		return Descendant{}, fmt.Errorf("WSL project path %s is a symlink", nearest)
	}
	if !exists && !info.Mode.IsDir() {
		return Descendant{}, fmt.Errorf("nearest existing ancestor %s is not a directory", nearest)
	}
	resolved, err := d.evalSymlinks(nearest)
	if err != nil {
		return Descendant{}, fmt.Errorf("resolve WSL project path %s: %w", nearest, err)
	}
	if resolved != nearest {
		return Descendant{}, fmt.Errorf("WSL project path %s resolves through a symlink to %s", nearest, resolved)
	}
	rawMounts, err := d.readMountInfo()
	if err != nil {
		return Descendant{}, fmt.Errorf("read WSL mount table for path %s: %w", nearest, err)
	}
	mounts, err := parseMountInfo(rawMounts)
	if err != nil {
		return Descendant{}, fmt.Errorf("parse WSL mount table for path %s: %w", nearest, err)
	}
	mount, err := containingMount(nearest, mounts)
	if err != nil {
		return Descendant{}, fmt.Errorf("select mount for WSL project path %s: %w", nearest, err)
	}
	if mount.device != info.Dev {
		return Descendant{}, fmt.Errorf("mount selected for WSL project path %s does not back its nearest existing dentry", nearest)
	}
	if mount.id != current.mountID || mount.device != current.device || mount.point != current.MountPoint {
		return Descendant{}, fmt.Errorf("WSL project path %s crosses mount boundary %s", nearest, mount.point)
	}
	relative := strings.TrimPrefix(candidate, project.Root)
	if relative == "" {
		relative = "."
	} else {
		relative = strings.TrimPrefix(relative, "/")
	}
	return Descendant{Path: candidate, Relative: relative, Exists: exists, NearestExisting: nearest}, nil
}

// proveMissingProject proves that root is absent below an unchanged supported
// native-WSL storage boundary. It checks every existing lexical ancestor with
// Lstat, so a symlink cannot be hidden above the nearest existing directory,
// and repeats the ancestry proof after reading mountinfo to bound replacement
// races. A vanished default /mnt/<drive> mount is never orphan evidence.
func proveMissingProject(root string, d dependencies) error {
	if err := validateRoot(root); err != nil {
		return err
	}
	if d.currentRuntime == nil || d.lstat == nil || d.readMountInfo == nil {
		return errors.New("missing WSL project proof dependencies are incomplete")
	}
	runtime, err := d.currentRuntime()
	if err != nil {
		return fmt.Errorf("classify native WSL runtime: %w", err)
	}
	if runtime.Kind != hostenv.WSL2Native {
		return fmt.Errorf("missing WSL project proof requires runtime kind %q, got %q", hostenv.WSL2Native, runtime.Kind)
	}
	before, err := inspectMissingBoundary(root, d.lstat)
	if err != nil {
		return err
	}
	rawMounts, err := d.readMountInfo()
	if err != nil {
		return fmt.Errorf("read WSL mount table for missing project %s: %w", root, err)
	}
	mounts, err := parseMountInfo(rawMounts)
	if err != nil {
		return fmt.Errorf("parse WSL mount table for missing project %s: %w", root, err)
	}

	if drive, underDrive := defaultDriveRoot(root); underDrive {
		mountPoint := "/mnt/" + drive
		if before.nearest != mountPoint && !strings.HasPrefix(before.nearest, mountPoint+"/") {
			return fmt.Errorf("Windows drive mount %s is unavailable; missing project %s is unsafe to orphan", mountPoint, root)
		}
		mount, err := containingMount(root, mounts)
		if err != nil {
			return fmt.Errorf("select mount for missing WSL project %s: %w", root, err)
		}
		if mount.point != mountPoint || !isDrvFS(mount, drive) || mount.device != before.info.Dev {
			return fmt.Errorf("Windows drive mount %s is not the expected live DrvFs boundary", mountPoint)
		}
	} else {
		distroRoot, err := d.lstat("/")
		if err != nil {
			return fmt.Errorf("inspect native WSL distribution root: %w", err)
		}
		mount, err := containingMount(before.nearest, mounts)
		if err != nil {
			return fmt.Errorf("select mount for missing WSL project %s: %w", root, err)
		}
		if before.info.Dev != distroRoot.Dev || mount.device != before.info.Dev {
			return fmt.Errorf("missing WSL project %s is below an unqualified filesystem boundary", root)
		}
	}

	after, err := inspectMissingBoundary(root, d.lstat)
	if err != nil {
		return err
	}
	if before.nearest != after.nearest || before.missing != after.missing || before.info != after.info {
		return fmt.Errorf("missing WSL project %s ancestry changed during proof", root)
	}
	return nil
}

func inspectMissingBoundary(root string, lstat func(string) (pathInfo, error)) (missingBoundary, error) {
	nearest := missingBoundary{}
	current := ""
	for _, element := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		current += "/" + element
		info, err := lstat(current)
		if err == nil {
			if info.Mode&os.ModeSymlink != 0 || !info.Mode.IsDir() {
				return missingBoundary{}, fmt.Errorf("WSL project ancestor %s must be a non-symlink directory", current)
			}
			nearest = missingBoundary{nearest: current, info: info}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return missingBoundary{}, fmt.Errorf("inspect WSL project ancestor %s: %w", current, err)
		}
		nearest.missing = current
		break
	}
	if nearest.missing == "" {
		return missingBoundary{}, fmt.Errorf("WSL project path %s exists and is not orphaned", root)
	}
	if nearest.nearest == "" {
		rootInfo, err := lstat("/")
		if err != nil {
			return missingBoundary{}, fmt.Errorf("inspect native WSL distribution root: %w", err)
		}
		if rootInfo.Mode&os.ModeSymlink != 0 || !rootInfo.Mode.IsDir() {
			return missingBoundary{}, errors.New("native WSL distribution root must be a non-symlink directory")
		}
		nearest.nearest = "/"
		nearest.info = rootInfo
	}
	return nearest, nil
}

func sameProjectIdentity(first, second Project) bool {
	return first.Root == second.Root && first.Storage == second.Storage && first.MountPoint == second.MountPoint &&
		first.WindowsDrive == second.WindowsDrive && first.mountID == second.mountID && first.device == second.device
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
		if err != nil || parentID <= 0 {
			return nil, fmt.Errorf("line %d has invalid parent mount ID", index+1)
		}
		device, err := parseMountDevice(fields[2])
		if err != nil {
			return nil, fmt.Errorf("line %d has invalid mount device", index+1)
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
			device:       device,
			point:        point,
			filesystem:   fields[separator+1],
			source:       source,
			superOptions: fields[separator+3],
		})
	}
	return mounts, nil
}

func parseMountDevice(field string) (uint64, error) {
	majorText, minorText, ok := strings.Cut(field, ":")
	if !ok || majorText == "" || minorText == "" || strings.Contains(minorText, ":") {
		return 0, errors.New("invalid major:minor device")
	}
	major, err := strconv.ParseUint(majorText, 10, 32)
	if err != nil {
		return 0, err
	}
	minor, err := strconv.ParseUint(minorText, 10, 32)
	if err != nil {
		return 0, err
	}
	return linuxDevice(major, minor), nil
}

// linuxDevice encodes Linux major/minor values in the dev_t layout exposed by
// syscall.Stat_t.Dev.
func linuxDevice(major, minor uint64) uint64 {
	return (minor & 0xff) |
		((major & 0xfff) << 8) |
		((minor &^ 0xff) << 12) |
		((major &^ 0xfff) << 32)
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

func isDrvFS(mount mountInfo, drive string) bool {
	switch mount.filesystem {
	case "9p":
		for _, option := range strings.Split(mount.superOptions, ",") {
			if option == "aname=drvfs" || strings.HasPrefix(option, "aname=drvfs;") {
				return true
			}
		}
		return false
	case "virtiofs":
		if drive == "" {
			return false
		}
		tag := "drvfs" + strings.ToUpper(drive)
		return mount.source == tag+"0" || mount.source == tag+"1"
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
