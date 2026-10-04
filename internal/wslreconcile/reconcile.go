// Package wslreconcile safely reconciles retained native-WSL runtime
// containers whose owning ContainerBin process no longer exists.
package wslreconcile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
)

const reconcileTimeout = 30 * time.Second

type coordinator interface {
	Close() error
}

type lease interface {
	Remove() error
	Close() error
}

type leaseStatus uint8

const (
	leaseMissing leaseStatus = iota
	leaseActive
	leaseOrphaned
)

type candidate struct {
	id     string
	runID  string
	tool   string
	docker wsldocker.ContainerCandidate
}

type retainedContainer struct {
	id      string
	runID   string
	tool    string
	running bool
	docker  wsldocker.Container
}

type dependencies struct {
	acquireCoordinator func(context.Context, hostenv.WSLLayout) (coordinator, error)
	createLease        func(hostenv.WSLLayout, string) (lease, error)
	probeLease         func(hostenv.WSLLayout, string) (leaseStatus, lease, error)
	discoverLeases     func(hostenv.WSLLayout) ([]string, error)
	discover           func(context.Context, string) ([]candidate, error)
	prove              func(context.Context, candidate, string) (retainedContainer, bool, error)
	signal             func(context.Context, retainedContainer, int) error
	wait               func(context.Context, retainedContainer) (int, error)
	remove             func(context.Context, retainedContainer) error
}

// Entry is one exact retained runtime classified by reconciliation.
type Entry struct {
	ContainerID string
	RunID       string
	Tool        string
	Running     bool
	Active      bool
	Removed     bool
}

// LeaseEntry is one managed run lease whose container is proven absent from
// the complete namespace discovery result.
type LeaseEntry struct {
	RunID   string
	Active  bool
	Removed bool
}

// Report is the deterministic result of one namespace reconciliation pass.
type Report struct {
	Namespace string
	Applied   bool
	Entries   []Entry
	Leases    []LeaseEntry
}

// ActiveLeaseOnlyCount returns the number of locked leases with no retained
// container. They are preserved because another process still owns the lock.
func (r Report) ActiveLeaseOnlyCount() int {
	count := 0
	for _, entry := range r.Leases {
		if entry.Active {
			count++
		}
	}
	return count
}

// OrphanedLeaseCount returns the number of unlocked lease paths whose retained
// container is absent.
func (r Report) OrphanedLeaseCount() int {
	return len(r.Leases) - r.ActiveLeaseOnlyCount()
}

// ReapedLeaseCount returns the number of orphaned lease paths removed by this
// pass.
func (r Report) ReapedLeaseCount() int {
	count := 0
	for _, entry := range r.Leases {
		if entry.Removed {
			count++
		}
	}
	return count
}

// ActiveCount returns the number of retained containers with a locked lease.
func (r Report) ActiveCount() int {
	count := 0
	for _, entry := range r.Entries {
		if entry.Active {
			count++
		}
	}
	return count
}

// OrphanCount returns the number of retained containers without an active lease.
func (r Report) OrphanCount() int {
	return len(r.Entries) - r.ActiveCount()
}

// RemovedCount returns the number of orphaned containers removed by this pass.
func (r Report) RemovedCount() int {
	count := 0
	for _, entry := range r.Entries {
		if entry.Removed {
			count++
		}
	}
	return count
}

// RunGuard holds the namespace coordinator until a newly created container
// has a process-held lease. Its Close method deliberately leaves the lease
// path behind when exact container removal was not proven.
type RunGuard struct {
	layout      hostenv.WSLLayout
	deps        dependencies
	coordinator coordinator
	lease       lease
	closed      bool
}

// BeginRun reconciles prior orphans while holding the namespace coordinator,
// then keeps that coordinator until Adopt publishes the new run lease.
func BeginRun(ctx context.Context, layout hostenv.WSLLayout) (*RunGuard, error) {
	return beginRun(ctx, layout, productionDependencies())
}

func beginRun(ctx context.Context, layout hostenv.WSLLayout, deps dependencies) (*RunGuard, error) {
	if ctx == nil {
		return nil, errors.New("native WSL runtime reconciliation requires a context")
	}
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	coordinator, err := deps.acquireCoordinator(ctx, layout)
	if err != nil {
		return nil, fmt.Errorf("acquire native WSL runtime coordinator: %w", err)
	}
	guard := &RunGuard{layout: layout, deps: deps, coordinator: coordinator}
	if _, err := reconcileLocked(ctx, layout, true, deps); err != nil {
		return nil, errors.Join(err, guard.Close(false))
	}
	return guard, nil
}

// Adopt creates and locks the lease for the exact Docker run identity before
// releasing the namespace coordinator.
func (g *RunGuard) Adopt(runID string) error {
	if g == nil || g.closed {
		return errors.New("native WSL runtime guard is not active")
	}
	if g.lease != nil {
		return errors.New("native WSL runtime guard already adopted a run")
	}
	created, err := g.deps.createLease(g.layout, runID)
	if err != nil {
		return fmt.Errorf("create native WSL runtime lease: %w", err)
	}
	g.lease = created
	if err := g.coordinator.Close(); err != nil {
		g.coordinator = nil
		// Keep the locked lease attached to the guard. Deferred container
		// cleanup can then remove it only after proving absence; otherwise Close
		// unlocks it but deliberately leaves the pathname as recovery evidence.
		return fmt.Errorf("release native WSL runtime coordinator: %w", err)
	}
	g.coordinator = nil
	return nil
}

// Close releases this process's lease. The lease pathname is removed only
// after the caller proves the retained container is gone.
func (g *RunGuard) Close(containerGone bool) error {
	if g == nil || g.closed {
		return nil
	}
	g.closed = true
	var errs []error
	if g.lease != nil {
		if containerGone {
			ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
			coordinator, err := g.deps.acquireCoordinator(ctx, g.layout)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Errorf("reacquire native WSL runtime coordinator before lease removal: %w", err))
			} else {
				removeErr := g.lease.Remove()
				leaseCloseErr := g.lease.Close()
				coordinatorCloseErr := coordinator.Close()
				errs = append(errs, removeErr, leaseCloseErr, coordinatorCloseErr)
				g.lease = nil
			}
		}
		if g.lease != nil {
			errs = append(errs, g.lease.Close())
			g.lease = nil
		}
	}
	if g.coordinator != nil {
		errs = append(errs, g.coordinator.Close())
		g.coordinator = nil
	}
	return errors.Join(errs...)
}

func reconcile(ctx context.Context, layout hostenv.WSLLayout, apply bool, deps dependencies) (Report, error) {
	if ctx == nil {
		return Report{}, errors.New("native WSL runtime reconciliation requires a context")
	}
	if err := validateDependencies(deps); err != nil {
		return Report{}, err
	}
	coordinator, err := deps.acquireCoordinator(ctx, layout)
	if err != nil {
		return Report{}, fmt.Errorf("acquire native WSL runtime coordinator: %w", err)
	}
	report, reconcileErr := reconcileLocked(ctx, layout, apply, deps)
	return report, errors.Join(reconcileErr, coordinator.Close())
}

type classified struct {
	container retainedContainer
	active    bool
	lease     lease
}

type classifiedLease struct {
	runID  string
	active bool
	lease  lease
}

func reconcileLocked(ctx context.Context, layout hostenv.WSLLayout, apply bool, deps dependencies) (report Report, err error) {
	report = Report{Namespace: layout.StateNamespace, Applied: apply}
	candidates, err := deps.discover(ctx, layout.StateNamespace)
	if err != nil {
		return report, fmt.Errorf("discover retained native WSL containers: %w", err)
	}
	leaseRunIDs, err := deps.discoverLeases(layout)
	if err != nil {
		return report, fmt.Errorf("discover native WSL runtime leases: %w", err)
	}
	leaseRunIDSet := make(map[string]struct{}, len(leaseRunIDs))
	for _, runID := range leaseRunIDs {
		if err := validateRunID(runID); err != nil {
			return report, fmt.Errorf("discover native WSL runtime lease: %w", err)
		}
		if _, duplicate := leaseRunIDSet[runID]; duplicate {
			return report, fmt.Errorf("discover native WSL runtime leases: duplicate run identity %s", runID)
		}
		leaseRunIDSet[runID] = struct{}{}
	}
	sort.Strings(leaseRunIDs)
	classifiedContainers := make([]classified, 0, len(candidates))
	classifiedLeases := make([]classifiedLease, 0, len(leaseRunIDs))
	defer func() {
		for _, item := range classifiedContainers {
			if item.lease != nil {
				err = errors.Join(err, item.lease.Close())
			}
		}
		for _, item := range classifiedLeases {
			if item.lease != nil {
				err = errors.Join(err, item.lease.Close())
			}
		}
	}()

	// Complete every ownership and lease proof before the first mutation.
	containerRunIDs := make(map[string]struct{}, len(candidates))
	for _, discovered := range candidates {
		container, exists, proveErr := deps.prove(ctx, discovered, layout.StateNamespace)
		if proveErr != nil {
			return report, fmt.Errorf("prove retained native WSL container %s: %w", discovered.id, proveErr)
		}
		if !exists {
			continue
		}
		if _, duplicate := containerRunIDs[container.runID]; duplicate {
			return report, fmt.Errorf("prove retained native WSL containers: duplicate run identity %s", container.runID)
		}
		containerRunIDs[container.runID] = struct{}{}
		status, heldLease, probeErr := deps.probeLease(layout, container.runID)
		if probeErr != nil {
			return report, fmt.Errorf("probe native WSL runtime lease %s: %w", container.runID, probeErr)
		}
		classifiedContainers = append(classifiedContainers, classified{
			container: container,
			lease:     heldLease,
		})
		classified := &classifiedContainers[len(classifiedContainers)-1]
		if status != leaseMissing && status != leaseActive && status != leaseOrphaned {
			return report, errors.New("native WSL runtime lease probe returned an invalid status")
		}
		if status == leaseMissing && heldLease != nil {
			return report, errors.New("missing native WSL runtime lease unexpectedly returned a handle")
		}
		if status == leaseActive && heldLease != nil {
			return report, errors.New("active native WSL runtime lease unexpectedly returned a handle")
		}
		if status == leaseOrphaned && heldLease == nil {
			return report, errors.New("orphaned native WSL runtime lease did not return its lock")
		}
		classified.active = status == leaseActive
	}
	for _, runID := range leaseRunIDs {
		if _, hasContainer := containerRunIDs[runID]; hasContainer {
			continue
		}
		status, heldLease, probeErr := deps.probeLease(layout, runID)
		if probeErr != nil {
			return report, fmt.Errorf("probe detached native WSL runtime lease %s: %w", runID, probeErr)
		}
		classifiedLeases = append(classifiedLeases, classifiedLease{runID: runID, lease: heldLease})
		classified := &classifiedLeases[len(classifiedLeases)-1]
		if status != leaseMissing && status != leaseActive && status != leaseOrphaned {
			return report, errors.New("detached native WSL runtime lease probe returned an invalid status")
		}
		if status == leaseMissing && heldLease != nil {
			return report, errors.New("missing detached native WSL runtime lease unexpectedly returned a handle")
		}
		if status == leaseActive && heldLease != nil {
			return report, errors.New("active detached native WSL runtime lease unexpectedly returned a handle")
		}
		if status == leaseOrphaned && heldLease == nil {
			return report, errors.New("orphaned detached native WSL runtime lease did not return its lock")
		}
		if status == leaseMissing {
			classifiedLeases = classifiedLeases[:len(classifiedLeases)-1]
			continue
		}
		classified.active = status == leaseActive
	}

	sort.Slice(classifiedContainers, func(i, j int) bool {
		return classifiedContainers[i].container.id < classifiedContainers[j].container.id
	})
	for i := range classifiedContainers {
		item := &classifiedContainers[i]
		entry := Entry{
			ContainerID: item.container.id,
			RunID:       item.container.runID,
			Tool:        item.container.tool,
			Running:     item.container.running,
			Active:      item.active,
		}
		if apply && !item.active {
			gone, cleanupErr := reconcileOrphan(ctx, item.container, deps)
			if cleanupErr != nil {
				return report, cleanupErr
			}
			if !gone {
				return report, fmt.Errorf("native WSL orphan container %s cleanup did not prove absence", item.container.id)
			}
			if item.lease != nil {
				if removeErr := item.lease.Remove(); removeErr != nil {
					return report, fmt.Errorf("remove reconciled native WSL runtime lease %s: %w", item.container.runID, removeErr)
				}
			}
			entry.Removed = true
		}
		report.Entries = append(report.Entries, entry)
	}
	for i := range classifiedLeases {
		item := &classifiedLeases[i]
		entry := LeaseEntry{RunID: item.runID, Active: item.active}
		if apply && !item.active {
			if removeErr := item.lease.Remove(); removeErr != nil {
				return report, fmt.Errorf("remove detached native WSL runtime lease %s: %w", item.runID, removeErr)
			}
			entry.Removed = true
		}
		report.Leases = append(report.Leases, entry)
	}
	return report, nil
}

func reconcileOrphan(ctx context.Context, container retainedContainer, deps dependencies) (bool, error) {
	operationContext, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	if container.running {
		if err := deps.signal(operationContext, container, 9); err != nil {
			if isAPIStatus(err, http.StatusNotFound) {
				return true, nil
			}
			if !isAPIStatus(err, http.StatusConflict) {
				return false, fmt.Errorf("stop orphaned native WSL container %s: %w", container.id, err)
			}
		}
		if _, err := deps.wait(operationContext, container); err != nil && !isAPIStatus(err, http.StatusNotFound) {
			return false, fmt.Errorf("wait for orphaned native WSL container %s: %w", container.id, err)
		}
	}
	if err := deps.remove(operationContext, container); err != nil {
		return false, fmt.Errorf("remove orphaned native WSL container %s: %w", container.id, err)
	}
	return true, nil
}

func validateDependencies(deps dependencies) error {
	if deps.acquireCoordinator == nil || deps.createLease == nil || deps.probeLease == nil ||
		deps.discoverLeases == nil || deps.discover == nil || deps.prove == nil || deps.signal == nil || deps.wait == nil || deps.remove == nil {
		return errors.New("native WSL runtime reconciliation dependencies are incomplete")
	}
	return nil
}

func validateRunID(runID string) error {
	if len(runID) != 32 {
		return errors.New("native WSL runtime lease requires a 32-character run identity")
	}
	for _, char := range runID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return errors.New("native WSL runtime lease requires a lowercase hexadecimal run identity")
		}
	}
	return nil
}

func isAPIStatus(err error, status int) bool {
	var apiError *wsldocker.APIError
	return errors.As(err, &apiError) && apiError.StatusCode == status
}
