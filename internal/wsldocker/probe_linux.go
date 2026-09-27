//go:build linux

package wsldocker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

const (
	probeTimeout   = 10 * time.Second
	maxProbeOutput = 64 << 10
)

// Check proves that the current native WSL2 distribution reaches Docker
// Desktop's WSL-integrated Linux engine through the fixed local Unix socket.
func Check(ctx context.Context) (Result, error) {
	return check(ctx, dependencies{
		currentRuntime: hostenv.Current,
		lookupEnv:      os.LookupEnv,
		lookPath:       exec.LookPath,
		statSocket:     statDockerSocket,
		runInfo:        runDockerInfo,
	})
}

func statDockerSocket(path string) (socketInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return socketInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return socketInfo{}, errors.New("Docker socket ownership could not be determined")
	}
	return socketInfo{Mode: info.Mode(), UID: stat.Uid}, nil
}

func runDockerInfo(ctx context.Context, dockerPath, host string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, dockerPath,
		"--host", host,
		"info", "--format", "{{json .}}",
	)
	var stdout, stderr cappedBuffer
	stdout.max = maxProbeOutput
	stderr.max = maxProbeOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if probeCtx.Err() != nil {
		return nil, fmt.Errorf("query Docker Desktop engine through %s: %w", host, probeCtx.Err())
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("Docker Desktop engine probe output exceeds %d bytes", maxProbeOutput)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("query Docker Desktop engine through %s: %w", host, err)
		}
		return nil, fmt.Errorf("query Docker Desktop engine through %s: %w: %s", host, err, detail)
	}
	return stdout.Bytes(), nil
}

type cappedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := b.max - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return written, nil
	}
	if len(data) > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return written, nil
}
