//go:build !windows

package selfupdate

import (
	"context"
	"errors"
)

func waitForParentExit(context.Context, int) error {
	return errors.New("self-update helper waiting requires native Windows")
}

func launchHelperCleanup(string, int) error {
	return errors.New("self-update helper cleanup requires native Windows")
}
