//go:build linux

package wslshim

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const temporaryShimAttempts = 8

type pinnedShimDirectory struct {
	file *os.File
	uid  uint32
}

func openPinnedShimDirectory(layout hostenv.WSLLayout) (shimDirectory, error) {
	file, err := openDirectoryNoSymlinks(layout.ShimDir)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	var directory syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &directory); err != nil {
		return nil, fmt.Errorf("inspect pinned shim directory: %w", err)
	}
	var root syscall.Stat_t
	if err := syscall.Stat(string(filepath.Separator), &root); err != nil {
		return nil, fmt.Errorf("inspect distribution root filesystem: %w", err)
	}
	mode := os.FileMode(directory.Mode & 0o7777)
	if directory.Mode&syscall.S_IFMT != syscall.S_IFDIR || directory.Uid != layout.UID || mode&0o700 != 0o700 || mode&0o022 != 0 || mode&0o7000 != 0 {
		return nil, fmt.Errorf("pinned native WSL shim directory must be a UID %d owner-accessible directory not writable by group or other", layout.UID)
	}
	if directory.Dev != root.Dev {
		return nil, fmt.Errorf("pinned native WSL shim directory is on filesystem device %d, not distribution root device %d", directory.Dev, root.Dev)
	}
	valid = true
	return &pinnedShimDirectory{file: file, uid: layout.UID}, nil
}

func openDirectoryNoSymlinks(directory string) (*os.File, error) {
	clean := filepath.Clean(directory)
	if !filepath.IsAbs(clean) || clean != directory {
		return nil, fmt.Errorf("directory path %q is not canonical and absolute", directory)
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, fmt.Errorf("directory path %q has an invalid component", directory)
		}
		nextPath := filepath.Join(procDirectoryPath(current), component)
		nextFD, openErr := syscall.Open(nextPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		closeErr := current.Close()
		if openErr != nil {
			return nil, fmt.Errorf("open directory component %q without following symlinks: %w", component, openErr)
		}
		if closeErr != nil {
			_ = syscall.Close(nextFD)
			return nil, fmt.Errorf("close parent directory while opening %q: %w", directory, closeErr)
		}
		current = os.NewFile(uintptr(nextFD), component)
	}
	return current, nil
}

func (d *pinnedShimDirectory) ensure(shim Shim, kind string) error {
	if filepath.Base(shim.Path) != shim.Name || shim.Name == "." || shim.Name == ".." {
		return fmt.Errorf("shim identity %q is not a direct child", shim.Path)
	}
	destination := filepath.Join(procDirectoryPath(d.file), shim.Name)
	if err := validatePinnedSymlink(destination, shim.Target, d.uid, kind); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	temporary, err := d.createTemporarySymlink(shim.Target)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Link(temporary, destination); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("publish shim without replacing an existing object: %w", err)
		}
		if err := validatePinnedSymlink(destination, shim.Target, d.uid, kind); err != nil {
			return fmt.Errorf("shim appeared concurrently with an invalid identity: %w", err)
		}
		return nil
	}
	if err := validatePinnedSymlink(destination, shim.Target, d.uid, kind); err != nil {
		return fmt.Errorf("validate newly published shim: %w", err)
	}
	return nil
}

func (d *pinnedShimDirectory) createTemporarySymlink(target string) (string, error) {
	for attempt := 0; attempt < temporaryShimAttempts; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("generate temporary shim name: %w", err)
		}
		temporary := filepath.Join(procDirectoryPath(d.file), ".cb-"+hex.EncodeToString(random)+".tmp")
		if err := os.Symlink(target, temporary); err == nil {
			return temporary, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create temporary shim: %w", err)
		}
	}
	return "", errors.New("could not allocate a unique temporary shim name")
}

func (d *pinnedShimDirectory) close() error {
	return d.file.Close()
}

func procDirectoryPath(file *os.File) string {
	return filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(file.Fd()), 10))
}

func validatePinnedSymlink(link, target string, uid uint32, kind string) error {
	info, err := lstat(link)
	if err != nil {
		return err
	}
	return validateSymlink(link, target, uid, info, kind, dependencies{readlink: os.Readlink})
}
