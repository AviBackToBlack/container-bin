// Package wslinstall composes the fixed native-WSL layout, machine policy,
// registry lifecycle, managed binary and symlink reconciliation. It does not
// enable ordinary tool execution; runtime wiring remains separately gated.
package wslinstall

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
	"github.com/AviBackToBlack/container-bin/internal/wslshim"
)

type State string

const (
	Ready  State = "ready"
	Create State = "create"
	Update State = "update"
)

type Plan struct {
	Layout             hostenv.WSLLayout
	MissingDirectories []string
	SourceBinary       string
	Registry           State
	Binary             State
	Management         State
	ToolShims          []wslshim.Shim
}

// LockFunc serializes one mutating WSL install transaction. Main supplies its
// signal-aware mutation-lock wrapper so Ctrl+C cannot strand a live lock.
type LockFunc func(string, func() error) error

type command struct {
	prepareCommand       func([]string, io.Writer) error
	currentLayout        func() (hostenv.WSLLayout, error)
	checkLayout          func(hostenv.WSLLayout) (wslfs.Plan, error)
	prepareLayout        func(hostenv.WSLLayout) error
	loadPolicy           func() (policy.Policy, error)
	loadRegistry         func(string, registry.Authenticator) (registry.Registry, string, error)
	loadRegistryReadOnly func(string, registry.Authenticator) (registry.Registry, string, error)
	ensureRegistry       func(string, os.FileMode) error
	appendDefaults       func(string, string, os.FileMode) error
	executable           func() (string, error)
	lstat                func(string) (os.FileInfo, error)
	binaryState          func(hostenv.WSLLayout, string) (State, error)
	installBinary        func(hostenv.WSLLayout, string) error
	inspectNames         func(hostenv.WSLLayout, []string) (wslshim.Result, error)
	reconcileManagement  func(hostenv.WSLLayout) (wslshim.Shim, error)
	reconcileNames       func(hostenv.WSLLayout, []string) (wslshim.Result, error)
	withLock             LockFunc
}

// Run serves the only native-WSL management lifecycle currently exposed.
func Run(args []string, out io.Writer, version string, withLock LockFunc) error {
	return (command{
		prepareCommand:       wslfs.Run,
		currentLayout:        wslfs.CurrentLayout,
		checkLayout:          wslfs.Check,
		prepareLayout:        wslfs.Prepare,
		loadPolicy:           policy.Load,
		loadRegistry:         registry.LoadAt,
		loadRegistryReadOnly: registry.LoadAtReadOnly,
		ensureRegistry:       registry.EnsureFileMode,
		appendDefaults:       registry.AppendMissingDefaultToolsMode,
		executable:           os.Executable,
		lstat:                os.Lstat,
		binaryState:          BinaryState,
		installBinary:        InstallBinary,
		inspectNames:         wslshim.InspectNames,
		reconcileManagement:  wslshim.ReconcileManagement,
		reconcileNames:       wslshim.Reconcile,
		withLock:             withLock,
	}).run(args, out, version)
}

func (c command) run(args []string, out io.Writer, version string) error {
	if len(args) > 0 && args[0] == "prepare" {
		if c.prepareCommand == nil {
			return errors.New("native WSL layout command is incomplete")
		}
		return c.prepareCommand(args, out)
	}
	if len(args) != 2 || args[0] != "install" || (args[1] != "--check" && args[1] != "--apply") {
		return errors.New("usage: cb wsl prepare (--check | --apply) | cb wsl install (--check | --apply)")
	}
	if c.currentLayout == nil || c.checkLayout == nil || c.loadPolicy == nil || c.loadRegistryReadOnly == nil || c.executable == nil || c.lstat == nil || c.binaryState == nil || c.inspectNames == nil {
		return errors.New("native WSL install command is incomplete")
	}
	layout, err := c.currentLayout()
	if err != nil {
		return err
	}
	source, err := c.executable()
	if err != nil {
		return fmt.Errorf("locate native WSL bootstrap executable: %w", err)
	}
	if args[1] == "--check" {
		plan, err := c.inspect(layout, source, true)
		if err != nil {
			return err
		}
		return printPlan(out, plan, false)
	}
	if c.prepareLayout == nil || c.loadRegistry == nil || c.ensureRegistry == nil || c.appendDefaults == nil || c.installBinary == nil || c.reconcileManagement == nil || c.reconcileNames == nil || c.withLock == nil {
		return errors.New("native WSL install mutation is incomplete")
	}
	if err := c.prepareLayout(layout); err != nil {
		return fmt.Errorf("prepare native WSL layout: %w", err)
	}
	var applied Plan
	err = c.withLock(layout.RegistryPath, func() error {
		layoutPlan, err := c.checkLayout(layout)
		if err != nil {
			return fmt.Errorf("revalidate native WSL layout under mutation lock: %w", err)
		}
		if len(layoutPlan.MissingDirectories) != 0 {
			return errors.New("native WSL layout changed after preparation")
		}
		if _, err := c.binaryState(layout, source); err != nil {
			return fmt.Errorf("preflight native WSL bootstrap executable: %w", err)
		}
		machinePolicy, err := c.loadPolicy()
		if err != nil {
			return fmt.Errorf("load native WSL machine policy: %w", err)
		}
		reg, path, err := c.loadRegistry(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
		if err != nil {
			return fmt.Errorf("load or recover native WSL registry: %w", err)
		}
		if path != layout.RegistryPath {
			return fmt.Errorf("native WSL registry loader returned path %q, expected %q", path, layout.RegistryPath)
		}
		if !machinePolicy.RequireRegistrySignature {
			if err := c.ensureRegistry(layout.RegistryPath, 0o600); err != nil {
				return fmt.Errorf("create native WSL registry: %w", err)
			}
			if err := c.appendDefaults(layout.RegistryPath, version, 0o600); err != nil {
				return fmt.Errorf("upgrade native WSL registry: %w", err)
			}
			reg, path, err = c.loadRegistry(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
			if err != nil {
				return fmt.Errorf("reload native WSL registry after upgrade: %w", err)
			}
			if path != layout.RegistryPath {
				return fmt.Errorf("native WSL registry loader returned path %q, expected %q", path, layout.RegistryPath)
			}
		}
		if _, err := c.inspectNames(layout, reg.ToolNames()); err != nil {
			return fmt.Errorf("preflight native WSL tool shims: %w", err)
		}
		if err := c.installBinary(layout, source); err != nil {
			return fmt.Errorf("install native WSL managed binary: %w", err)
		}
		if _, err := c.reconcileManagement(layout); err != nil {
			return fmt.Errorf("reconcile native WSL management shim: %w", err)
		}
		if _, err := c.reconcileNames(layout, reg.ToolNames()); err != nil {
			return fmt.Errorf("reconcile native WSL tool shims: %w", err)
		}
		applied, err = c.inspectWith(layout, source, reg, machinePolicy)
		return err
	})
	if err != nil {
		return err
	}
	if !applied.ready() {
		return errors.New("native WSL installation completed without reaching the ready state")
	}
	return printPlan(out, applied, true)
}

func (c command) inspect(layout hostenv.WSLLayout, source string, readOnly bool) (Plan, error) {
	if _, err := c.checkLayout(layout); err != nil {
		return Plan{}, fmt.Errorf("validate native WSL layout before config inspection: %w", err)
	}
	machinePolicy, err := c.loadPolicy()
	if err != nil {
		return Plan{}, fmt.Errorf("load native WSL machine policy: %w", err)
	}
	loader := c.loadRegistry
	if readOnly {
		loader = c.loadRegistryReadOnly
	}
	reg, path, err := loader(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
	if err != nil {
		return Plan{}, fmt.Errorf("load native WSL registry: %w", err)
	}
	if path != layout.RegistryPath {
		return Plan{}, fmt.Errorf("native WSL registry loader returned path %q, expected %q", path, layout.RegistryPath)
	}
	return c.inspectWith(layout, source, reg, machinePolicy)
}

func (c command) inspectWith(layout hostenv.WSLLayout, source string, reg registry.Registry, machinePolicy policy.Policy) (Plan, error) {
	layoutPlan, err := c.checkLayout(layout)
	if err != nil {
		return Plan{}, fmt.Errorf("validate native WSL layout: %w", err)
	}
	result := Plan{Layout: layout, MissingDirectories: layoutPlan.MissingDirectories, SourceBinary: source, Registry: Ready}
	if _, err := c.lstat(layout.RegistryPath); errors.Is(err, fs.ErrNotExist) {
		result.Registry = Create
	} else if err != nil {
		return Plan{}, fmt.Errorf("inspect native WSL registry %s: %w", layout.RegistryPath, err)
	} else if !machinePolicy.RequireRegistrySignature && reg.NeedsDefaultUpgrade() {
		result.Registry = Update
	}
	result.Binary, err = c.binaryState(layout, source)
	if err != nil {
		return Plan{}, err
	}
	if _, err := c.lstat(layout.ManagementShim); errors.Is(err, fs.ErrNotExist) {
		result.Management = Create
	} else if err != nil {
		return Plan{}, fmt.Errorf("inspect native WSL management shim %s: %w", layout.ManagementShim, err)
	} else {
		result.Management = Ready
	}
	shims, err := c.inspectNames(layout, reg.ToolNames())
	if err != nil {
		return Plan{}, fmt.Errorf("inspect native WSL tool shims: %w", err)
	}
	result.ToolShims = shims.Shims
	return result, nil
}

func (p Plan) ready() bool {
	if len(p.MissingDirectories) != 0 || p.Registry != Ready || p.Binary != Ready || p.Management != Ready {
		return false
	}
	for _, shim := range p.ToolShims {
		if shim.State != wslshim.Ready {
			return false
		}
	}
	return true
}

func printPlan(out io.Writer, plan Plan, applied bool) error {
	var report strings.Builder
	if applied {
		fmt.Fprintln(&report, "native WSL installation applied and revalidated")
	} else {
		fmt.Fprintln(&report, "native WSL installation check (read-only; no files changed)")
	}
	fmt.Fprintf(&report, "distribution:  %s\n", plan.Layout.Distro)
	fmt.Fprintf(&report, "namespace:     %s\n", plan.Layout.StateNamespace)
	fmt.Fprintf(&report, "source binary: %s\n", plan.SourceBinary)
	fmt.Fprintf(&report, "registry:      %-6s %s\n", plan.Registry, plan.Layout.RegistryPath)
	fmt.Fprintf(&report, "binary:        %-6s %s\n", plan.Binary, plan.Layout.BinaryPath)
	fmt.Fprintf(&report, "management:    %-6s %s\n", plan.Management, plan.Layout.ManagementShim)
	ready, create := 0, 0
	for _, shim := range plan.ToolShims {
		if shim.State == wslshim.Ready {
			ready++
		} else {
			create++
		}
	}
	fmt.Fprintf(&report, "tool shims:    %d ready, %d create\n", ready, create)
	for _, directory := range plan.MissingDirectories {
		fmt.Fprintf(&report, "directory:     create %s\n", directory)
	}
	if plan.ready() {
		fmt.Fprintln(&report, "status:        INSTALLATION READY")
	} else {
		fmt.Fprintln(&report, "status:        APPLY REQUIRED")
		fmt.Fprintln(&report, "apply:         cb wsl install --apply")
	}
	fmt.Fprintln(&report, "frontend:      GATED (runtime wiring and WSL E2E are not enabled)")
	if _, err := io.WriteString(out, report.String()); err != nil {
		return fmt.Errorf("write native WSL installation report: %w", err)
	}
	return nil
}
