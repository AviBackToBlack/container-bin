//go:build !windows

package selfupdate

import "errors"

func authenticateGitHubCLI(string) (string, error) {
	return "", errors.New("GitHub CLI authentication requires native Windows")
}

func verifierBaseEnvironment() ([]string, error) {
	return nil, nil
}
