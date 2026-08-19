// Package atomicio provides crash-safe file replacement and recovery of the
// .bak file that replacement leaves behind when it is interrupted.
package atomicio

import (
	"fmt"
	"os"
	"path/filepath"
)

func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".container-bin-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	// Windows cannot rename over an existing file reliably, so keep a tiny backup window.
	bak := path + ".bak"
	_ = os.Remove(bak)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, bak); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Rename(bak, path)
		return err
	}
	_ = os.Remove(bak)
	return nil
}

func RecoverFromBackup(path string, validate func(backupPath string) error) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	bak := path + ".bak"
	if _, err := os.Stat(bak); err == nil {
		if err := validate(bak); err != nil {
			return false, fmt.Errorf("%s is missing and its backup %s is unusable: %w; remove the backup to fall back to defaults, or repair it and retry", path, bak, err)
		}
		if err := os.Rename(bak, path); err != nil {
			return false, err
		}
		fmt.Fprintf(os.Stderr, "container-bin: recovered %s from %s after an interrupted write\n", path, bak)
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}
