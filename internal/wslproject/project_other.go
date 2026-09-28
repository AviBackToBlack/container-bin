//go:build !linux

package wslproject

import "errors"

// Classify is unavailable outside Linux because mount identity and symlink
// semantics are part of the native WSL project boundary.
func Classify(string) (Project, error) {
	return Project{}, errors.New("native WSL project classification requires Linux")
}
