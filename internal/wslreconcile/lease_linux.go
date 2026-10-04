//go:build linux

package wslreconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	lockRetryDelay = 25 * time.Millisecond
)

type fileCoordinator struct {
	file *os.File
}

type fileLease struct {
	file    *os.File
	dir     *os.File
	name    string
	dev     uint64
	ino     uint64
	removed bool
}

func acquireFileCoordinator(ctx context.Context, layout hostenv.WSLLayout) (coordinator, error) {
	dir, _, err := openStateDirectory(layout)
	if err != nil {
		return nil, err
	}
	// flock works on the already-open distribution-local directory. Using the
	// directory itself keeps --check genuinely read-only while still serializing
	// discovery against create-to-lease publication.
	if err := lockContext(ctx, dir); err != nil {
		return nil, errors.Join(err, dir.Close())
	}
	return &fileCoordinator{file: dir}, nil
}

func createFileLease(layout hostenv.WSLLayout, runID string) (lease, error) {
	name, err := leaseName(runID)
	if err != nil {
		return nil, err
	}
	dir, state, err := openStateDirectory(layout)
	if err != nil {
		return nil, err
	}
	file, stat, err := openManagedLockFile(dir, state, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL, 0o600)
	if err != nil {
		dir.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		cleanupErr := syscall.Unlinkat(int(dir.Fd()), name)
		return nil, errors.Join(fmt.Errorf("lock newly created native WSL runtime lease: %w", err), cleanupErr, file.Close(), dir.Close())
	}
	return &fileLease{file: file, dir: dir, name: name, dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func probeFileLease(layout hostenv.WSLLayout, runID string) (leaseStatus, lease, error) {
	name, err := leaseName(runID)
	if err != nil {
		return leaseMissing, nil, err
	}
	dir, state, err := openStateDirectory(layout)
	if err != nil {
		return leaseMissing, nil, err
	}
	file, stat, err := openManagedLockFile(dir, state, name, syscall.O_RDWR, 0)
	if errors.Is(err, syscall.ENOENT) {
		dir.Close()
		return leaseMissing, nil, nil
	}
	if err != nil {
		dir.Close()
		return leaseMissing, nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := errors.Join(file.Close(), dir.Close())
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return leaseActive, nil, closeErr
		}
		return leaseMissing, nil, errors.Join(fmt.Errorf("probe native WSL runtime lease lock: %w", err), closeErr)
	}
	return leaseOrphaned, &fileLease{file: file, dir: dir, name: name, dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func openStateDirectory(layout hostenv.WSLLayout) (*os.File, *syscall.Stat_t, error) {
	if layout.StateDir == "" || !strings.HasPrefix(layout.StateDir, "/") || layout.UID != uint32(os.Geteuid()) {
		return nil, nil, errors.New("native WSL runtime state identity is invalid for the current user")
	}
	fd, err := syscall.Open(layout.StateDir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open native WSL runtime state directory %s: %w", layout.StateDir, err)
	}
	dir := os.NewFile(uintptr(fd), layout.StateDir)
	stat := new(syscall.Stat_t)
	if err := syscall.Fstat(fd, stat); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect native WSL runtime state directory: %w", err), dir.Close())
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != layout.UID || stat.Mode&0o777 != 0o700 {
		return nil, nil, errors.Join(errors.New("native WSL runtime state directory must be an owner-only 0700 directory"), dir.Close())
	}
	return dir, stat, nil
}

func openManagedLockFile(dir *os.File, state *syscall.Stat_t, name string, flags int, mode uint32) (*os.File, *syscall.Stat_t, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
	if err != nil {
		return nil, nil, fmt.Errorf("open native WSL runtime lock %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	cleanupCreated := func() error {
		if flags&syscall.O_EXCL == 0 {
			return nil
		}
		err := syscall.Unlinkat(int(dir.Fd()), name)
		if errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return err
	}
	stat := new(syscall.Stat_t)
	if err := syscall.Fstat(fd, stat); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect native WSL runtime lock %s: %w", name, err), cleanupCreated(), file.Close())
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != state.Uid || stat.Mode&0o777 != 0o600 || uint64(stat.Dev) != uint64(state.Dev) || stat.Nlink != 1 {
		return nil, nil, errors.Join(fmt.Errorf("native WSL runtime lock %s must be a same-device owner-only 0600 regular file with one link", name), cleanupCreated(), file.Close())
	}
	return file, stat, nil
}

func lockContext(ctx context.Context, file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock native WSL runtime coordinator: %w", err)
		}
		timer := time.NewTimer(lockRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("wait for native WSL runtime coordinator: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func leaseName(runID string) (string, error) {
	if len(runID) != 32 {
		return "", errors.New("native WSL runtime lease requires a 32-character run identity")
	}
	for _, char := range runID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", errors.New("native WSL runtime lease requires a lowercase hexadecimal run identity")
		}
	}
	return "run-" + runID + ".lease", nil
}

func (c *fileCoordinator) Close() error {
	if c == nil || c.file == nil {
		return nil
	}
	err := errors.Join(
		syscall.Flock(int(c.file.Fd()), syscall.LOCK_UN),
		c.file.Close(),
	)
	c.file = nil
	return err
}

func (l *fileLease) Remove() error {
	// The caller must hold the namespace coordinator until both Remove and
	// Close complete, preventing a cooperating reconciler from observing a
	// replacement pathname while this inode remains locked.
	if l == nil || l.removed {
		return nil
	}
	fd, err := syscall.Openat(int(l.dir.Fd()), l.name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		l.removed = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("reopen native WSL runtime lease %s before removal: %w", l.name, err)
	}
	current := os.NewFile(uintptr(fd), l.name)
	stat := new(syscall.Stat_t)
	if err := syscall.Fstat(fd, stat); err != nil {
		return errors.Join(fmt.Errorf("inspect native WSL runtime lease %s before removal: %w", l.name, err), current.Close())
	}
	if uint64(stat.Dev) != l.dev || stat.Ino != l.ino {
		return errors.Join(fmt.Errorf("refuse to remove replaced native WSL runtime lease %s", l.name), current.Close())
	}
	if err := current.Close(); err != nil {
		return err
	}
	if err := syscall.Unlinkat(int(l.dir.Fd()), l.name); err != nil && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("remove native WSL runtime lease %s: %w", l.name, err)
	}
	l.removed = true
	return nil
}

func (l *fileLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := errors.Join(
		syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN),
		l.file.Close(),
		l.dir.Close(),
	)
	l.file = nil
	l.dir = nil
	return err
}
