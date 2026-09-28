//go:build linux

package wslproject

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const maxMountInfoBytes = 1 << 20

// Classify proves the storage and canonical identity of one native-WSL project
// root. The caller must pass an already-selected project root, not an arbitrary
// path to search upward from.
func Classify(root string) (Project, error) {
	return classify(root, dependencies{
		currentRuntime: hostenv.Current,
		lstat:          statPath,
		evalSymlinks:   filepath.EvalSymlinks,
		readMountInfo:  readMountInfo,
	})
}

func statPath(path string) (pathInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return pathInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return pathInfo{}, errors.New("filesystem device identity is unavailable")
	}
	return pathInfo{Mode: info.Mode(), Dev: uint64(stat.Dev)}, nil
}

func readMountInfo() ([]byte, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxMountInfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxMountInfoBytes {
		return nil, fmt.Errorf("/proc/self/mountinfo exceeds %d bytes", maxMountInfoBytes)
	}
	return raw, nil
}
