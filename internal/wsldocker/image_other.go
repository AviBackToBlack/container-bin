//go:build !linux

package wsldocker

import (
	"context"
	"errors"
)

func PullImage(context.Context, string) error {
	return errors.New("Docker Desktop WSL image pull requires Linux")
}

func InspectImage(context.Context, string) (ImageSnapshot, error) {
	return ImageSnapshot{}, errors.New("Docker Desktop WSL image inspect requires Linux")
}
