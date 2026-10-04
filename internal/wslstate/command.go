// Package wslstate implements the native-WSL state inventory and explicit
// cleanup surface. Discovery never establishes ownership: every candidate is
// reconstructed and exactly proven before it is reported or removed.
package wslstate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/hostenv"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/wslfs"
	"github.com/AviBackToBlack/container-bin/internal/wslproject"
	"github.com/AviBackToBlack/container-bin/internal/wslvolume"
)

const projectWorkspace = "/workspace/project"

type dependencies struct {
	currentLayout         func() (hostenv.WSLLayout, error)
	checkLayout           func(hostenv.WSLLayout) (wslfs.Plan, error)
	checkRegistryRecovery func(hostenv.WSLLayout) error
	loadPolicy            func() (policy.Policy, error)
	loadRegistry          func(string, registry.Authenticator) (registry.Registry, string, error)
	lstat                 func(string) (os.FileInfo, error)
	getwd                 func() (string, error)
	selectProject         func(string, registry.Tool) (wslproject.Project, bool, error)
	planStateful          func(wslvolume.Scope, registry.Tool, wslproject.Project, string) ([]wslvolume.Volume, error)
	planPython            func(wslvolume.Scope, wslproject.Project, bool) (wslvolume.PythonState, error)
	proveMissing          func(string) error
	discover              func(context.Context, wslvolume.Scope) ([]wslvolume.Volume, error)
	remove                func(context.Context, wslvolume.Volume) error
}

type classification struct {
	status string
	volume wslvolume.Volume
	path   string
}

// Run executes cb state or cb gc against the fixed native-WSL installation.
func Run(ctx context.Context, args []string, out io.Writer) error {
	return run(ctx, args, out, productionDependencies())
}

func productionDependencies() dependencies {
	return dependencies{
		currentLayout:         wslfs.CurrentLayout,
		checkLayout:           wslfs.Check,
		checkRegistryRecovery: wslfs.CheckRegistryRecovery,
		loadPolicy:            policy.Load,
		loadRegistry:          registry.LoadAtReadOnly,
		lstat:                 os.Lstat,
		getwd:                 os.Getwd,
		selectProject:         wslproject.SelectForTool,
		planStateful: func(scope wslvolume.Scope, tool registry.Tool, project wslproject.Project, workspace string) ([]wslvolume.Volume, error) {
			bindings, err := wslvolume.PlanStatefulToolVolumes(scope, tool, project, workspace)
			if err != nil {
				return nil, err
			}
			volumes := make([]wslvolume.Volume, 0, len(bindings))
			for _, binding := range bindings {
				volumes = append(volumes, binding.Volume())
			}
			return volumes, nil
		},
		planPython:   wslvolume.PlanPythonVolumes,
		proveMissing: wslproject.ProveMissingProject,
		discover:     discoverProven,
		remove:       wslvolume.Remove,
	}
}

func discoverProven(ctx context.Context, scope wslvolume.Scope) ([]wslvolume.Volume, error) {
	candidates, err := wslvolume.Discover(ctx, scope)
	if err != nil {
		return nil, err
	}
	volumes := make([]wslvolume.Volume, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		volume, err := wslvolume.ProveCandidate(scope, candidate)
		if err != nil {
			return nil, err
		}
		if seen[volume.Name()] {
			return nil, fmt.Errorf("duplicate proven native WSL volume %q", volume.Name())
		}
		seen[volume.Name()] = true
		volumes = append(volumes, volume)
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name() < volumes[j].Name() })
	return volumes, nil
}

func run(ctx context.Context, args []string, out io.Writer, deps dependencies) error {
	if ctx == nil {
		return errors.New("native WSL state command requires a context")
	}
	if out == nil {
		return errors.New("native WSL state command requires an output writer")
	}
	if len(args) == 0 || (args[0] != "state" && args[0] != "gc") {
		return errors.New("usage: cb state | cb gc [TOOL|STATE_GROUP] [--orphans] [--apply]")
	}
	if args[0] == "state" && len(args) != 1 {
		return errors.New("usage: cb state")
	}
	var options gcOptions
	if args[0] == "gc" {
		var err error
		options, err = parseGC(args[1:])
		if err != nil {
			return err
		}
	}
	if deps.currentLayout == nil || deps.checkLayout == nil || deps.checkRegistryRecovery == nil || deps.loadPolicy == nil || deps.loadRegistry == nil || deps.lstat == nil || deps.getwd == nil || deps.selectProject == nil || deps.planStateful == nil || deps.planPython == nil || deps.proveMissing == nil || deps.discover == nil || deps.remove == nil {
		return errors.New("native WSL state command dependencies are incomplete")
	}

	layout, err := deps.currentLayout()
	if err != nil {
		return fmt.Errorf("derive native WSL layout: %w", err)
	}
	checked, err := deps.checkLayout(layout)
	if err != nil {
		return fmt.Errorf("validate native WSL layout: %w", err)
	}
	if checked.Layout != layout || len(checked.MissingDirectories) != 0 {
		return errors.New("native WSL installation layout is incomplete; run `cb wsl install --apply`")
	}
	if err := deps.checkRegistryRecovery(layout); err != nil {
		return fmt.Errorf("validate native WSL registry recovery state: %w", err)
	}
	info, err := deps.lstat(layout.RegistryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("native WSL registry is not installed; run `cb wsl install --apply`")
		}
		return fmt.Errorf("inspect native WSL registry: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("native WSL registry is not a regular non-symlink file")
	}
	machinePolicy, err := deps.loadPolicy()
	if err != nil {
		return fmt.Errorf("load native WSL machine policy: %w", err)
	}
	reg, loadedPath, err := deps.loadRegistry(layout.RegistryPath, machinePolicy.AuthenticateRegistry)
	if err != nil {
		return fmt.Errorf("load native WSL registry: %w", err)
	}
	if loadedPath != layout.RegistryPath {
		return fmt.Errorf("native WSL registry loader returned path %q, expected %q", loadedPath, layout.RegistryPath)
	}
	scope, err := wslvolume.New(layout)
	if err != nil {
		return err
	}
	cwd := ""
	if args[0] == "state" || !options.orphans {
		cwd, err = deps.getwd()
		if err != nil {
			return fmt.Errorf("determine native WSL working directory: %w", err)
		}
	}

	if args[0] == "state" {
		return show(ctx, out, layout, scope, reg, cwd, deps)
	}
	return gc(ctx, out, scope, reg, cwd, options, deps)
}

type gcOptions struct {
	apply   bool
	orphans bool
	filter  string
}

func parseGC(args []string) (gcOptions, error) {
	var result gcOptions
	for _, arg := range args {
		switch arg {
		case "--apply":
			result.apply = true
		case "--dry-run":
		case "--orphans":
			result.orphans = true
		default:
			if strings.HasPrefix(arg, "-") {
				return gcOptions{}, fmt.Errorf("unknown option %q", arg)
			}
			if result.filter != "" {
				return gcOptions{}, errors.New("usage: cb gc [TOOL|STATE_GROUP] [--orphans] [--apply]")
			}
			result.filter = strings.ToLower(arg)
		}
	}
	return result, nil
}

func show(ctx context.Context, out io.Writer, layout hostenv.WSLLayout, scope wslvolume.Scope, reg registry.Registry, cwd string, deps dependencies) error {
	current, shared, err := expectedVolumes(scope, reg, cwd, "", deps)
	if err != nil {
		return err
	}
	actual, err := deps.discover(ctx, scope)
	if err != nil {
		return fmt.Errorf("discover and prove native WSL volumes: %w", err)
	}
	classified, err := classify(actual, current, shared, deps.lstat, deps.proveMissing)
	if err != nil {
		return err
	}
	var report strings.Builder
	fmt.Fprintf(&report, "distribution: %s\nnamespace:    %s\n", layout.Distro, layout.StateNamespace)
	fmt.Fprintf(&report, "%-9s  %-64s  %s\n", "STATUS", "VOLUME", "OWNER")
	fmt.Fprintf(&report, "%-9s  %-64s  %s\n", "---------", "----------------------------------------------------------------", "-----")
	for _, item := range classified {
		owner := item.volume.Labels()["cb.owner"]
		if item.path != "" {
			owner += " (" + item.path + ")"
		}
		fmt.Fprintf(&report, "%-9s  %-64s  %s\n", item.status, item.volume.Name(), owner)
	}
	if len(classified) == 0 {
		fmt.Fprintln(&report, "(no proven ContainerBin volumes found in this native WSL namespace)")
	}
	fmt.Fprintln(&report, "\nORPHAN requires a proven project volume whose recorded Linux project path no longer exists. Unsafe paths and shared volumes are never auto-deleted.")
	if _, err := io.WriteString(out, report.String()); err != nil {
		return fmt.Errorf("write native WSL state report: %w", err)
	}
	return nil
}

func classify(actual []wslvolume.Volume, current, shared map[string]wslvolume.Volume, lstat func(string) (os.FileInfo, error), proveMissing func(string) error) ([]classification, error) {
	result := make([]classification, 0, len(actual))
	for _, volume := range actual {
		labels := volume.Labels()
		item := classification{volume: volume}
		if exact, ok := current[volume.Name()]; ok && exact.Matches(volume.Name(), labels) {
			item.status = "CURRENT"
		} else if exact, ok := shared[volume.Name()]; ok && exact.Matches(volume.Name(), labels) {
			item.status = "SHARED"
			if labels["cb.owner"] == wslvolume.PythonStateGroup+"/compat-venv" {
				item.status = "COMPAT"
			}
		} else if labels["cb.kind"] == "shared" {
			item.status = "MANAGED"
		} else {
			item.path = labels["cb.project_path"]
			status, err := inspectProjectPath(item.path, lstat, proveMissing)
			if err != nil {
				return nil, err
			}
			item.status = status
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].volume.Name() < result[j].volume.Name() })
	return result, nil
}

func gc(ctx context.Context, out io.Writer, scope wslvolume.Scope, reg registry.Registry, cwd string, options gcOptions, deps dependencies) error {
	actual, err := deps.discover(ctx, scope)
	if err != nil {
		return fmt.Errorf("discover and prove native WSL volumes: %w", err)
	}
	byName := make(map[string]wslvolume.Volume, len(actual))
	for _, volume := range actual {
		byName[volume.Name()] = volume
	}
	var candidates []wslvolume.Volume
	if options.orphans {
		_, stateGroup := resolveFilter(reg, options.filter)
		for _, volume := range actual {
			labels := volume.Labels()
			if labels["cb.kind"] != "project" || !ownerMatches(labels["cb.owner"], options.filter, stateGroup) {
				continue
			}
			projectPath := labels["cb.project_path"]
			status, statErr := inspectProjectPath(projectPath, deps.lstat, deps.proveMissing)
			if statErr != nil {
				return statErr
			}
			if status == "ORPHAN" {
				candidates = append(candidates, volume)
			}
		}
	} else {
		current, _, err := expectedVolumes(scope, reg, cwd, options.filter, deps)
		if err != nil {
			return err
		}
		if len(current) == 0 {
			return errors.New("no project-scoped state matches the current directory/filter")
		}
		for name, expected := range current {
			if observed, ok := byName[name]; ok && expected.Matches(observed.Name(), observed.Labels()) {
				candidates = append(candidates, observed)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name() < candidates[j].Name() })
	if len(candidates) == 0 {
		message := "No matching project volumes currently exist.\n"
		if options.orphans {
			message = "No proven orphan project volumes found.\n"
		}
		_, err := io.WriteString(out, message)
		return err
	}
	if !options.apply {
		var report strings.Builder
		if options.orphans {
			fmt.Fprintln(&report, "Dry run: proven project volumes whose recorded Linux project path no longer exists:")
		} else {
			fmt.Fprintln(&report, "Dry run: the following CURRENT project volumes would be removed:")
		}
		for _, volume := range candidates {
			labels := volume.Labels()
			detail := labels["cb.owner"]
			if options.orphans {
				detail += "  " + labels["cb.project_path"]
			}
			fmt.Fprintf(&report, "  %-64s  %s\n", volume.Name(), detail)
		}
		fmt.Fprintln(&report, "\nShared volumes are never included. Re-run with --apply to delete these exact proven volumes.")
		_, err := io.WriteString(out, report.String())
		return err
	}
	for _, volume := range candidates {
		if options.orphans {
			projectPath := volume.Labels()["cb.project_path"]
			status, err := inspectProjectPath(projectPath, deps.lstat, deps.proveMissing)
			if err != nil {
				return err
			}
			if status != "ORPHAN" {
				return fmt.Errorf("native WSL project path %s changed before removal; refusing stale orphan plan", projectPath)
			}
		}
		if err := deps.remove(ctx, volume); err != nil {
			return fmt.Errorf("remove native WSL volume %s: %w", volume.Name(), err)
		}
	}
	var report strings.Builder
	for _, volume := range candidates {
		fmt.Fprintf(&report, "Removed %s\n", volume.Name())
	}
	_, err = io.WriteString(out, report.String())
	return err
}

func inspectProjectPath(recorded string, lstat func(string) (os.FileInfo, error), proveMissing func(string) error) (string, error) {
	info, err := lstat(recorded)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "UNSAFE", nil
		}
		return "MANAGED", nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect recorded native WSL project path %s: %w", recorded, err)
	}
	if err := proveMissing(recorded); err != nil {
		return "UNSAFE", nil
	}
	return "ORPHAN", nil
}

func expectedVolumes(scope wslvolume.Scope, reg registry.Registry, cwd, filter string, deps dependencies) (map[string]wslvolume.Volume, map[string]wslvolume.Volume, error) {
	current := make(map[string]wslvolume.Volume)
	shared := make(map[string]wslvolume.Volume)
	resolved, _ := resolveFilter(reg, filter)
	names := make([]string, 0, len(reg.Tools))
	for name := range reg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := reg.Tools[name]
		if !toolMatchesFilter(tool, filter, resolved) {
			continue
		}
		switch tool.Provider {
		case "stateful":
			var project wslproject.Project
			if len(tool.ProjectVolumes) > 0 {
				var err error
				project, _, err = deps.selectProject(cwd, tool)
				if err != nil {
					return nil, nil, fmt.Errorf("select native WSL project for %s: %w", tool.Name, err)
				}
			}
			volumes, err := deps.planStateful(scope, tool, project, projectWorkspace)
			if err != nil {
				return nil, nil, fmt.Errorf("plan native WSL state for %s: %w", tool.Name, err)
			}
			for _, volume := range volumes {
				target := shared
				if volume.Labels()["cb.kind"] == "project" {
					target = current
				}
				if err := addExpected(target, volume); err != nil {
					return nil, nil, err
				}
			}
		case "python":
			project, found, err := deps.selectProject(cwd, tool)
			if err != nil {
				return nil, nil, fmt.Errorf("select native WSL project for %s: %w", tool.Name, err)
			}
			state, err := deps.planPython(scope, project, found)
			if err != nil {
				return nil, nil, fmt.Errorf("plan native WSL Python state for %s: %w", tool.Name, err)
			}
			if state.Venv.Labels()["cb.kind"] == "project" {
				if err := addExpected(current, state.Venv); err != nil {
					return nil, nil, err
				}
			} else if err := addExpected(shared, state.Venv); err != nil {
				return nil, nil, err
			}
			if err := addExpected(shared, state.PipCache); err != nil {
				return nil, nil, err
			}
		}
	}
	return current, shared, nil
}

func toolMatchesFilter(tool registry.Tool, filter, resolved string) bool {
	if filter == "" || filter == tool.Name || resolved == tool.Name || filter == tool.StateGroup {
		return true
	}
	return tool.Provider == "python" && (filter == "python" || filter == wslvolume.PythonStateGroup)
}

func addExpected(target map[string]wslvolume.Volume, volume wslvolume.Volume) error {
	if prior, ok := target[volume.Name()]; ok && !prior.Matches(volume.Name(), volume.Labels()) {
		return fmt.Errorf("native WSL state planning produced conflicting identity %q", volume.Name())
	}
	target[volume.Name()] = volume
	return nil
}

func resolveFilter(reg registry.Registry, filter string) (resolved, stateGroup string) {
	if filter == "" {
		return "", ""
	}
	tool, resolved, ok := reg.Resolve(filter)
	if !ok {
		return "", ""
	}
	if tool.Provider == "python" {
		return resolved, wslvolume.PythonStateGroup
	}
	return resolved, tool.StateGroup
}

func ownerMatches(owner, filter, stateGroup string) bool {
	if filter == "" {
		return true
	}
	owner = strings.ToLower(owner)
	for _, candidate := range []string{filter, stateGroup} {
		candidate = strings.ToLower(candidate)
		if candidate != "" && (owner == candidate || strings.HasPrefix(owner, candidate+"/")) {
			return true
		}
	}
	return false
}
