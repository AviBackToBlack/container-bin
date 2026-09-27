//go:build !windows

package imagetrust

import (
	"context"
	"errors"
)

type commandRunner struct{}

func (commandRunner) Run(context.Context, string, []string, string) ([]byte, []byte, error) {
	return nil, nil, errors.New("cosign image verification requires native Windows")
}
