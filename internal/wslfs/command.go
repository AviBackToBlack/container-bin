package wslfs

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
)

// Plan is the read-only result of validating the fixed native-WSL layout.
type Plan struct {
	Layout             hostenv.WSLLayout
	MissingDirectories []string
}

type command struct {
	currentLayout func() (hostenv.WSLLayout, error)
	check         func(hostenv.WSLLayout) (Plan, error)
	prepare       func(hostenv.WSLLayout) error
}

// CurrentLayout derives the fixed native-WSL layout for the current process.
// Callers must still validate the returned paths with Check or Prepare before
// using them.
func CurrentLayout() (hostenv.WSLLayout, error) {
	return currentLayout()
}

// Run exposes only the fixed native-WSL filesystem preflight. It deliberately
// does not enable tool execution, install a binary, create shims or contact
// Docker; those remain separate qualification-gated slices.
func Run(args []string, out io.Writer) error {
	return (command{
		currentLayout: currentLayout,
		check:         Check,
		prepare:       Prepare,
	}).run(args, out)
}

func (c command) run(args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "prepare" || (args[1] != "--check" && args[1] != "--apply") {
		return errors.New("usage: cb wsl prepare (--check | --apply)")
	}
	if c.currentLayout == nil || c.check == nil || c.prepare == nil {
		return errors.New("native WSL layout command is incomplete")
	}
	layout, err := c.currentLayout()
	if err != nil {
		return err
	}
	if args[1] == "--apply" {
		if err := c.prepare(layout); err != nil {
			return err
		}
	}
	plan, err := c.check(layout)
	if err != nil {
		return err
	}
	if plan.Layout != layout {
		return errors.New("native WSL layout check returned an inconsistent identity")
	}
	if args[1] == "--apply" && len(plan.MissingDirectories) != 0 {
		return errors.New("native WSL layout preparation completed without creating every required directory")
	}
	return printPlan(out, plan, args[1] == "--apply")
}

func printPlan(out io.Writer, plan Plan, applied bool) error {
	var report strings.Builder
	if applied {
		fmt.Fprintln(&report, "native WSL layout prepared and revalidated")
	} else {
		fmt.Fprintln(&report, "native WSL layout check (read-only; no files changed)")
	}
	fmt.Fprintf(&report, "distribution:  %s\n", plan.Layout.Distro)
	fmt.Fprintf(&report, "namespace:     %s\n", plan.Layout.StateNamespace)
	fmt.Fprintf(&report, "binary:        %s\n", plan.Layout.BinaryPath)
	fmt.Fprintf(&report, "management:    %s\n", plan.Layout.ManagementShim)
	fmt.Fprintf(&report, "registry:      %s\n", plan.Layout.RegistryPath)
	fmt.Fprintf(&report, "lockfile:      %s\n", plan.Layout.LockPath)
	fmt.Fprintf(&report, "state:         %s\n", plan.Layout.StateDir)
	if len(plan.MissingDirectories) == 0 {
		fmt.Fprintln(&report, "status:        LAYOUT READY")
	} else {
		fmt.Fprintln(&report, "status:        LAYOUT PREPARATION REQUIRED")
		for _, path := range plan.MissingDirectories {
			fmt.Fprintf(&report, "create:        %s\n", path)
		}
		fmt.Fprintln(&report, "apply:         cb wsl prepare --apply")
	}
	if _, err := io.WriteString(out, report.String()); err != nil {
		return fmt.Errorf("write native WSL layout report: %w", err)
	}
	return nil
}
