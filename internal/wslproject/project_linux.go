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
	"github.com/AviBackToBlack/container-bin/internal/registry"
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

// ClassifyDescendant revalidates a previously classified project and proves
// that one canonical path has not crossed a symlink or nested-mount boundary.
// Missing output paths are classified through their nearest existing ancestor.
func ClassifyDescendant(project Project, candidate string) (Descendant, error) {
	return resolveDescendant(project, candidate, dependencies{
		currentRuntime: hostenv.Current,
		lstat:          statPath,
		evalSymlinks:   filepath.EvalSymlinks,
		readMountInfo:  readMountInfo,
	})
}

// ProveMissingProject proves that an absent recorded project path still lies
// below the same supported distribution or default Windows-drive boundary.
func ProveMissingProject(root string) error {
	return proveMissingProject(root, dependencies{
		currentRuntime: hostenv.Current,
		lstat:          statPath,
		readMountInfo:  readMountInfo,
	})
}

// SelectForTool applies a profile's marker policy and classifies the selected
// native-WSL project root. The boolean reports whether a marker or trusted
// overlay root was found; otherwise the proven working directory is the root.
func SelectForTool(start string, tool registry.Tool) (Project, bool, error) {
	return selectForTool(start, tool, selectionDependencies{
		classify:           Classify,
		classifyDescendant: ClassifyDescendant,
		lstat:              statPath,
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
