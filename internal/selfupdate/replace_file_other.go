//go:build !windows

package selfupdate

import "os"

func replaceExistingFile(source, destination string) error {
	return os.Rename(source, destination)
}
