//go:build linux

package wslrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
	"github.com/AviBackToBlack/container-bin/internal/wslshim"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

// Run executes one registry-derived native WSL tool shim through the fixed
// layout and proof-bound Docker Desktop Engine transport.
func Run(ctx context.Context, invoked string, args []string) (int, error) {
	return runFrontend(ctx, invoked, args, productionFrontendDependencies())
}

func productionFrontendDependencies() frontendDependencies {
	return frontendDependencies{
		currentLayout:         wslfs.CurrentLayout,
		checkLayout:           wslfs.Check,
		checkRegistryRecovery: wslfs.CheckRegistryRecovery,
		loadPolicy:            policy.Load,
		loadRegistry:          registry.LoadAt,
		inspectShims:          wslshim.Inspect,
		lstat:                 os.Lstat,
		executable:            os.Executable,
		absPath:               filepath.Abs,
		evalSymlinks:          filepath.EvalSymlinks,
		getwd:                 os.Getwd,
		interactive:           interactiveHostTerminal,
		environ:               os.Environ,
		plan:                  productionPlanDependencies(),
		run: runDependencies{
			ensureVolume: wslvolume.Ensure,
			create: func(ctx context.Context, spec wsldocker.ContainerCreateSpec) (containerHandle, error) {
				container, err := wsldocker.CreateContainer(ctx, spec)
				return containerHandle{id: container.ID(), native: container}, err
			},
			attach: func(ctx context.Context, request wsldocker.AttachRequest) (attachStream, error) {
				return wsldocker.OpenAttach(ctx, request)
			},
			start:  wsldocker.StartContainer,
			wait:   wsldocker.WaitContainer,
			resize: wsldocker.ResizeContainer,
			signal: wsldocker.SignalContainer,
			remove: func(ctx context.Context, handle containerHandle) error {
				container, ok := handle.native.(wsldocker.Container)
				if !ok || container.ID() != handle.id {
					return errors.New("native WSL container cleanup identity is invalid")
				}
				return wsldocker.RemoveContainer(ctx, container)
			},
			prepareTerminal: prepareHostTerminal,
			startEvents:     startHostEvents,
			stdin:           os.Stdin,
			stdout:          os.Stdout,
			stderr:          os.Stderr,
		},
	}
}
