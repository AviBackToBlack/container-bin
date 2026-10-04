//go:build linux

package wslreconcile

import (
	"context"

	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
)

func productionDependencies() dependencies {
	return dependencies{
		acquireCoordinator: acquireFileCoordinator,
		createLease:        createFileLease,
		probeLease:         probeFileLease,
		discoverLeases:     discoverFileLeases,
		discover: func(ctx context.Context, namespace string) ([]candidate, error) {
			discovered, err := wsldocker.DiscoverRetainedContainers(ctx, namespace)
			if err != nil {
				return nil, err
			}
			candidates := make([]candidate, 0, len(discovered))
			for _, item := range discovered {
				candidates = append(candidates, candidate{
					id: item.ID(), runID: item.RunID(), tool: item.Tool(), docker: item,
				})
			}
			return candidates, nil
		},
		prove: func(ctx context.Context, item candidate, namespace string) (retainedContainer, bool, error) {
			container, snapshot, exists, err := wsldocker.ProveRetainedContainer(ctx, item.docker, namespace)
			if err != nil || !exists {
				return retainedContainer{}, exists, err
			}
			return retainedContainer{
				id: container.ID(), runID: container.RunID(), tool: container.Tool(),
				running: snapshot.Running(), docker: container,
			}, true, nil
		},
		signal: func(ctx context.Context, container retainedContainer, signal int) error {
			return wsldocker.SignalContainer(ctx, container.id, signal)
		},
		wait: func(ctx context.Context, container retainedContainer) (int, error) {
			return wsldocker.WaitContainer(ctx, container.id)
		},
		remove: func(ctx context.Context, container retainedContainer) error {
			return wsldocker.RemoveContainer(ctx, container.docker)
		},
	}
}
