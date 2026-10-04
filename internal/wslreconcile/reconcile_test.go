package wslreconcile

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
)

type fakeCoordinator struct {
	closed *int
	err    error
}

func (f *fakeCoordinator) Close() error {
	*f.closed++
	return f.err
}

type orderedCoordinator struct{ events *[]string }

func (c orderedCoordinator) Close() error {
	*c.events = append(*c.events, "coordinator-close")
	return nil
}

type orderedLease struct{ events *[]string }

func (l orderedLease) Remove() error {
	*l.events = append(*l.events, "lease-remove")
	return nil
}

func (l orderedLease) Close() error {
	*l.events = append(*l.events, "lease-close")
	return nil
}

type fakeLease struct {
	removed *int
	closed  *int
}

func (f *fakeLease) Remove() error {
	*f.removed++
	return nil
}

func (f *fakeLease) Close() error {
	*f.closed++
	return nil
}

func testLayout() hostenv.WSLLayout {
	return hostenv.WSLLayout{StateNamespace: "wsl2-0123456789abcdef0123456789abcdef", StateDir: "/home/test/.local/state/container-bin"}
}

func testCandidate(index string, running bool) (candidate, retainedContainer) {
	id := strings.Repeat(index, 64)
	runID := strings.Repeat(index, 32)
	return candidate{id: id, runID: runID, tool: "go"}, retainedContainer{id: id, runID: runID, tool: "go", running: running}
}

func testDependencies(items []candidate, containers map[string]retainedContainer, statuses map[string]leaseStatus, event *[]string) dependencies {
	return dependencies{
		acquireCoordinator: func(context.Context, hostenv.WSLLayout) (coordinator, error) {
			*event = append(*event, "coordinator")
			return &fakeCoordinator{closed: new(int)}, nil
		},
		createLease: func(_ hostenv.WSLLayout, runID string) (lease, error) {
			*event = append(*event, "lease:"+runID)
			return &fakeLease{removed: new(int), closed: new(int)}, nil
		},
		probeLease: func(_ hostenv.WSLLayout, runID string) (leaseStatus, lease, error) {
			status := statuses[runID]
			*event = append(*event, "probe:"+runID)
			if status == leaseOrphaned {
				return status, &fakeLease{removed: new(int), closed: new(int)}, nil
			}
			return status, nil, nil
		},
		discover: func(context.Context, string) ([]candidate, error) { return items, nil },
		prove: func(_ context.Context, item candidate, _ string) (retainedContainer, bool, error) {
			container, ok := containers[item.id]
			return container, ok, nil
		},
		signal: func(_ context.Context, item retainedContainer, signal int) error {
			*event = append(*event, "signal:"+item.runID)
			if signal != 9 {
				return errors.New("unexpected signal")
			}
			return nil
		},
		wait: func(_ context.Context, item retainedContainer) (int, error) {
			*event = append(*event, "wait:"+item.runID)
			return 137, nil
		},
		remove: func(_ context.Context, item retainedContainer) error {
			*event = append(*event, "remove:"+item.runID)
			return nil
		},
	}
}

func TestReconcilePreservesActiveAndRemovesOrphans(t *testing.T) {
	activeCandidate, active := testCandidate("a", true)
	runningCandidate, running := testCandidate("b", true)
	stoppedCandidate, stopped := testCandidate("c", false)
	items := []candidate{stoppedCandidate, activeCandidate, runningCandidate}
	containers := map[string]retainedContainer{active.id: active, running.id: running, stopped.id: stopped}
	statuses := map[string]leaseStatus{active.runID: leaseActive, running.runID: leaseMissing, stopped.runID: leaseOrphaned}
	var events []string

	report, err := reconcile(context.Background(), testLayout(), true, testDependencies(items, containers, statuses, &events))
	if err != nil {
		t.Fatal(err)
	}
	if report.ActiveCount() != 1 || report.OrphanCount() != 2 || report.RemovedCount() != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	joined := strings.Join(events, ",")
	if strings.Contains(joined, "signal:"+active.runID) || strings.Contains(joined, "remove:"+active.runID) {
		t.Fatalf("active runtime was mutated: %s", joined)
	}
	for _, want := range []string{"signal:" + running.runID, "wait:" + running.runID, "remove:" + running.runID, "remove:" + stopped.runID} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing event %q in %s", want, joined)
		}
	}
}

func TestReconcileCheckIsReadOnly(t *testing.T) {
	item, container := testCandidate("d", true)
	var events []string
	report, err := reconcile(context.Background(), testLayout(), false, testDependencies(
		[]candidate{item}, map[string]retainedContainer{item.id: container}, map[string]leaseStatus{item.runID: leaseMissing}, &events,
	))
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedCount() != 0 || strings.Contains(strings.Join(events, ","), "signal:") || strings.Contains(strings.Join(events, ","), "remove:") {
		t.Fatalf("check mode mutated state: report=%+v events=%v", report, events)
	}
}

func TestReconcileCompletesAllProofsBeforeMutation(t *testing.T) {
	firstCandidate, first := testCandidate("e", false)
	secondCandidate, _ := testCandidate("f", false)
	var events []string
	deps := testDependencies([]candidate{firstCandidate, secondCandidate}, map[string]retainedContainer{first.id: first}, map[string]leaseStatus{first.runID: leaseMissing}, &events)
	deps.prove = func(_ context.Context, item candidate, _ string) (retainedContainer, bool, error) {
		if item.id == secondCandidate.id {
			return retainedContainer{}, false, errors.New("ambiguous ownership")
		}
		return first, true, nil
	}
	if _, err := reconcile(context.Background(), testLayout(), true, deps); err == nil || !strings.Contains(err.Error(), "ambiguous ownership") {
		t.Fatalf("expected proof error, got %v", err)
	}
	if strings.Contains(strings.Join(events, ","), "remove:") {
		t.Fatalf("mutation occurred before complete preflight: %v", events)
	}
}

func TestReconcileToleratesRunningCompletionRaces(t *testing.T) {
	item, container := testCandidate("1", true)
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var events []string
			deps := testDependencies([]candidate{item}, map[string]retainedContainer{item.id: container}, map[string]leaseStatus{item.runID: leaseMissing}, &events)
			deps.signal = func(context.Context, retainedContainer, int) error {
				return &wsldocker.APIError{StatusCode: status}
			}
			report, err := reconcile(context.Background(), testLayout(), true, deps)
			if err != nil {
				t.Fatal(err)
			}
			if report.RemovedCount() != 1 {
				t.Fatalf("orphan was not reconciled: %+v", report)
			}
		})
	}
}

func TestBeginRunHoldsCoordinatorUntilLeasePublication(t *testing.T) {
	var events []string
	closed := 0
	removed := 0
	leaseClosed := 0
	deps := testDependencies(nil, nil, nil, &events)
	deps.acquireCoordinator = func(context.Context, hostenv.WSLLayout) (coordinator, error) {
		events = append(events, "coordinator")
		return &fakeCoordinator{closed: &closed}, nil
	}
	deps.createLease = func(_ hostenv.WSLLayout, runID string) (lease, error) {
		if closed != 0 {
			t.Fatal("coordinator released before lease publication")
		}
		events = append(events, "lease:"+runID)
		return &fakeLease{removed: &removed, closed: &leaseClosed}, nil
	}
	guard, err := beginRun(context.Background(), testLayout(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if closed != 0 {
		t.Fatal("coordinator was not retained after reconciliation")
	}
	runID := strings.Repeat("2", 32)
	if err := guard.Adopt(runID); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("coordinator close count = %d, want 1", closed)
	}
	if err := guard.Close(false); err != nil {
		t.Fatal(err)
	}
	if removed != 0 || leaseClosed != 1 {
		t.Fatalf("failed container cleanup should leave lease path: removed=%d closed=%d", removed, leaseClosed)
	}
}

func TestRunGuardSerializesLeaseRemovalAndUnlock(t *testing.T) {
	var events []string
	guard := &RunGuard{
		layout: testLayout(),
		deps: dependencies{acquireCoordinator: func(context.Context, hostenv.WSLLayout) (coordinator, error) {
			events = append(events, "coordinator-acquire")
			return orderedCoordinator{events: &events}, nil
		}},
		lease: orderedLease{events: &events},
	}
	if err := guard.Close(true); err != nil {
		t.Fatal(err)
	}
	want := []string{"coordinator-acquire", "lease-remove", "lease-close", "coordinator-close"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("lease removal ordering = %v, want %v", events, want)
	}
}

func TestAdoptLeavesLeaseEvidenceWhenCoordinatorReleaseFails(t *testing.T) {
	removed, closed, coordinatorClosed := 0, 0, 0
	guard := &RunGuard{
		layout:      testLayout(),
		coordinator: &fakeCoordinator{closed: &coordinatorClosed, err: errors.New("unlock failed")},
		deps: dependencies{createLease: func(hostenv.WSLLayout, string) (lease, error) {
			return &fakeLease{removed: &removed, closed: &closed}, nil
		}},
	}
	if err := guard.Adopt(strings.Repeat("4", 32)); err == nil || !strings.Contains(err.Error(), "unlock failed") {
		t.Fatalf("Adopt() error = %v", err)
	}
	if removed != 0 || closed != 1 || coordinatorClosed != 1 {
		t.Fatalf("failed coordinator release cleanup removed=%d closed=%d coordinator=%d", removed, closed, coordinatorClosed)
	}
}

func TestCommandRequiresExactLayoutAndPrintsReport(t *testing.T) {
	layout := testLayout()
	cmd := command{
		currentLayout: func() (hostenv.WSLLayout, error) { return layout, nil },
		checkLayout:   func(hostenv.WSLLayout) (wslfs.Plan, error) { return wslfs.Plan{Layout: layout}, nil },
		reconcile: func(_ context.Context, _ hostenv.WSLLayout, apply bool) (Report, error) {
			return Report{Namespace: layout.StateNamespace, Applied: apply, Entries: []Entry{{
				ContainerID: strings.Repeat("3", 64), RunID: strings.Repeat("3", 32), Tool: "go", Running: true,
			}}}, nil
		},
	}
	var out bytes.Buffer
	if err := cmd.run(context.Background(), []string{"--check"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read-only check", "orphan-running:", "active=0 orphaned=1 reconciled=0", "cb wsl cleanup --apply"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}
