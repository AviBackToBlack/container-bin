package wslrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
	"github.com/AviBackToBlack/container-bin/internal/wslshim"
)

type frontendDependencies struct {
	currentLayout         func() (hostenv.WSLLayout, error)
	checkLayout           func(hostenv.WSLLayout) (wslfs.Plan, error)
	checkRegistryRecovery func(hostenv.WSLLayout) error
	loadPolicy            func() (policy.Policy, error)
	loadRegistry          func(string, registry.Authenticator) (registry.Registry, string, error)
	inspectShims          func(hostenv.WSLLayout, []string) (wslshim.Result, error)
	lstat                 func(string) (os.FileInfo, error)
	executable            func() (string, error)
	absPath               func(string) (string, error)
	evalSymlinks          func(string) (string, error)
	getwd                 func() (string, error)
	interactive           func() bool
	environ               func() []string
	plan                  planDependencies
	run                   runDependencies
}

func runFrontend(ctx context.Context, invoked string, args []string, deps frontendDependencies) (int, error) {
	if ctx == nil {
		return 0, errors.New("native WSL frontend requires a context")
	}
	if !registry.ValidToolName(invoked) || registry.ReservedToolName(invoked) {
		return 0, fmt.Errorf("invalid native WSL tool invocation name %q", invoked)
	}
	if deps.currentLayout == nil || deps.checkLayout == nil || deps.checkRegistryRecovery == nil || deps.loadPolicy == nil ||
		deps.loadRegistry == nil || deps.inspectShims == nil || deps.lstat == nil || deps.executable == nil || deps.absPath == nil || deps.evalSymlinks == nil || deps.getwd == nil || deps.interactive == nil || deps.environ == nil {
		return 0, errors.New("native WSL frontend dependencies are incomplete")
	}
	layout, err := deps.currentLayout()
	if err != nil {
		return 0, fmt.Errorf("derive native WSL layout: %w", err)
	}
	checked, err := deps.checkLayout(layout)
	if err != nil {
		return 0, fmt.Errorf("validate native WSL layout: %w", err)
	}
	if checked.Layout != layout || len(checked.MissingDirectories) != 0 {
		return 0, errors.New("native WSL installation layout is incomplete; run `cb wsl install --apply`")
	}
	if err := deps.checkRegistryRecovery(layout); err != nil {
		return 0, fmt.Errorf("validate native WSL registry recovery state: %w", err)
	}
	if info, err := deps.lstat(layout.RegistryPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, errors.New("native WSL registry is not installed; run `cb wsl install --apply`")
		}
		return 0, fmt.Errorf("inspect native WSL registry: %w", err)
	} else if !info.Mode().IsRegular() {
		return 0, errors.New("native WSL registry is not a regular file")
	}
	shimResult, err := deps.inspectShims(layout, []string{invoked})
	if err != nil {
		return 0, fmt.Errorf("validate native WSL managed tool shim: %w", err)
	}
	if len(shimResult.Shims) != 1 || shimResult.Shims[0].Name != invoked || shimResult.Shims[0].State != wslshim.Ready {
		return 0, fmt.Errorf("native WSL tool shim %q is not installed; run `cb wsl install --apply`", invoked)
	}
	executable, err := deps.executable()
	if err != nil {
		return 0, fmt.Errorf("locate native WSL running executable: %w", err)
	}
	executable, err = deps.absPath(executable)
	if err != nil {
		return 0, fmt.Errorf("canonicalize native WSL running executable: %w", err)
	}
	executable, err = deps.evalSymlinks(executable)
	if err != nil {
		return 0, fmt.Errorf("resolve native WSL running executable: %w", err)
	}
	if executable != layout.BinaryPath {
		return 0, fmt.Errorf("native WSL tool execution requires managed binary %s, running executable is %s", layout.BinaryPath, executable)
	}
	machinePolicy, err := deps.loadPolicy()
	if err != nil {
		return 0, fmt.Errorf("load native WSL machine policy: %w", err)
	}
	reg, path, err := deps.loadRegistry(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
	if err != nil {
		return 0, fmt.Errorf("load native WSL registry: %w", err)
	}
	if path != layout.RegistryPath {
		return 0, fmt.Errorf("native WSL registry loader returned path %q, expected %q", path, layout.RegistryPath)
	}
	tool, _, ok := reg.Resolve(invoked)
	if !ok {
		return 0, fmt.Errorf("no tool profile for %q (registry: %s)", invoked, layout.RegistryPath)
	}
	cwd, err := deps.getwd()
	if err != nil {
		return 0, fmt.Errorf("determine native WSL working directory: %w", err)
	}
	plan, err := buildToolPlan(tool, args, machinePolicy, layout, cwd, deps.interactive(), deps.environ(), deps.plan)
	if err != nil {
		return 0, err
	}
	return executeTool(ctx, plan, deps.run)
}
