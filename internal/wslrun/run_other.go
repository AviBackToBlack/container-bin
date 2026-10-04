//go:build !linux

package wslrun

import (
	"context"
	"errors"
)

func Run(context.Context, string, []string) (int, error) {
	return 0, errors.New("native WSL tool execution requires Linux")
}
