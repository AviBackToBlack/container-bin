//go:build !windows

package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func verifyOwnership(path string) error {
	for _, candidate := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(candidate)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", candidate)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot determine owner of %s", candidate)
		}
		if stat.Uid != 0 {
			return fmt.Errorf("%s is not owned by root", candidate)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is writable by group or other", candidate)
		}
	}
	return nil
}
