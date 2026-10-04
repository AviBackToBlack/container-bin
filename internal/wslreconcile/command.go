package wslreconcile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
)

type command struct {
	currentLayout func() (hostenv.WSLLayout, error)
	checkLayout   func(hostenv.WSLLayout) (wslfs.Plan, error)
	reconcile     func(context.Context, hostenv.WSLLayout, bool) (Report, error)
}

// Run checks or applies proof-bound retained-container reconciliation for the
// current native-WSL namespace.
func Run(ctx context.Context, args []string, out io.Writer) error {
	return (command{
		currentLayout: wslfs.CurrentLayout,
		checkLayout:   wslfs.Check,
		reconcile: func(ctx context.Context, layout hostenv.WSLLayout, apply bool) (Report, error) {
			return reconcile(ctx, layout, apply, productionDependencies())
		},
	}).run(ctx, args, out)
}

func (c command) run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 || (args[0] != "--check" && args[0] != "--apply") {
		return errors.New("usage: cb wsl cleanup (--check | --apply)")
	}
	if ctx == nil || out == nil {
		return errors.New("native WSL cleanup requires a context and output writer")
	}
	if c.currentLayout == nil || c.checkLayout == nil || c.reconcile == nil {
		return errors.New("native WSL cleanup command is incomplete")
	}
	layout, err := c.currentLayout()
	if err != nil {
		return err
	}
	plan, err := c.checkLayout(layout)
	if err != nil {
		return err
	}
	if plan.Layout != layout || len(plan.MissingDirectories) != 0 {
		return errors.New("native WSL cleanup requires a complete, exact managed layout; run cb wsl prepare --apply")
	}
	report, err := c.reconcile(ctx, layout, args[0] == "--apply")
	if err != nil {
		return err
	}
	if report.Namespace != layout.StateNamespace || report.Applied != (args[0] == "--apply") {
		return errors.New("native WSL cleanup returned an inconsistent report")
	}
	return printReport(out, report)
}

func printReport(out io.Writer, report Report) error {
	var text strings.Builder
	if report.Applied {
		fmt.Fprintln(&text, "native WSL retained-container cleanup (apply)")
	} else {
		fmt.Fprintln(&text, "native WSL retained-container cleanup (read-only check)")
	}
	fmt.Fprintf(&text, "namespace: %s\n", report.Namespace)
	for _, entry := range report.Entries {
		status := "orphan-stopped"
		if entry.Active {
			status = "active"
		} else if entry.Running {
			status = "orphan-running"
		}
		if entry.Removed {
			status = "reconciled"
		}
		fmt.Fprintf(&text, "%s: container=%s run=%s tool=%s\n", status, entry.ContainerID, entry.RunID, entry.Tool)
	}
	for _, entry := range report.Leases {
		status := "lease-residue"
		if entry.Active {
			status = "lease-active"
		} else if entry.Removed {
			status = "lease-reaped"
		}
		fmt.Fprintf(&text, "%s: run=%s\n", status, entry.RunID)
	}
	fmt.Fprintf(&text, "totals: active=%d orphaned=%d reconciled=%d lease_active=%d lease_residue=%d lease_reaped=%d\n",
		report.ActiveCount(), report.OrphanCount(), report.RemovedCount(),
		report.ActiveLeaseOnlyCount(), report.OrphanedLeaseCount(), report.ReapedLeaseCount())
	if !report.Applied && (report.OrphanCount() != 0 || report.OrphanedLeaseCount() != 0) {
		fmt.Fprintln(&text, "apply: cb wsl cleanup --apply")
	}
	if _, err := io.WriteString(out, text.String()); err != nil {
		return fmt.Errorf("write native WSL cleanup report: %w", err)
	}
	return nil
}
