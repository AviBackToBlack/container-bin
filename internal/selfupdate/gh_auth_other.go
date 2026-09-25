//go:build !windows

package selfupdate

import (
	"context"
	"errors"
)

func authenticateGitHubCLI(context.Context, string) (string, error) {
	return "", errors.New("GitHub CLI authentication requires native Windows")
}

func verifierBaseEnvironment() ([]string, error) {
	return nil, nil
}
