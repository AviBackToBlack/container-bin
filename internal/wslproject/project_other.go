//go:build !linux

package wslproject

import (
	"errors"

	"github.com/AviBackToBlack/container-bin/internal/registry"
)

// Classify is unavailable outside Linux because mount identity and symlink
// semantics are part of the native WSL project boundary.
func Classify(string) (Project, error) {
	return Project{}, errors.New("native WSL project classification requires Linux")
}

// ClassifyDescendant is unavailable outside Linux because mount identity and
// symlink semantics are part of the native WSL project boundary.
func ClassifyDescendant(Project, string) (Descendant, error) {
	return Descendant{}, errors.New("native WSL project path classification requires Linux")
}

// ProveMissingProject is unavailable outside Linux because mount identity and
// symlink semantics are part of the missing-project proof.
func ProveMissingProject(string) error {
	return errors.New("native WSL missing-project proof requires Linux")
}

// SelectForTool is unavailable outside Linux because project selection must
// immediately prove the selected root and working directory mount identity.
func SelectForTool(string, registry.Tool) (Project, bool, error) {
	return Project{}, false, errors.New("native WSL project selection requires Linux")
}
