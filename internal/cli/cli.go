// Package cli implements the cb subcommands that compose several lower layers:
// registry mutation, path mapping, lock resolution, docker execution and
// diagnostics. It sits directly beneath main, which owns only argv dispatch,
// exit-code policy and the mutation-lock signal wrapper.
package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/atomicio"
	"github.com/AviBackToBlack/container-bin/internal/diag"
	"github.com/AviBackToBlack/container-bin/internal/dockerrun"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/statearchive"
	"github.com/AviBackToBlack/container-bin/internal/toml"
)

func Setup(cfgPath, version string) error {
	if err := registry.EnsureFile(cfgPath); err != nil {
		return err
	}
	if err := registry.AppendMissingDefaultTools(cfgPath, version); err != nil {
		return err
	}
	reg, _, err := registry.Load()
	if err != nil {
		return err
	}
	if err := registry.InstallShims(reg); err != nil {
		return err
	}
	fmt.Println("\nRunning doctor after setup...")
	return diag.Doctor(reg, cfgPath)
}

func renderAddedToolSection(name, image string) string {
	return fmt.Sprintf("\n# Added by cb add\n[tools.%s]\nimage = %s\nprovider = \"stateless\"\n", name, toml.Quote(image))
}

// Add appends the smallest useful profile: a stateless tool that runs its
// image entrypoint. More privileged behavior (environment, state, mounts and
// path rules) remains an explicit registry edit rather than inferred defaults.
func Add(reg registry.Registry, cfgPath string, args []string) error {
	return add(reg, cfgPath, args, registry.InstallShims)
}

func add(reg registry.Registry, cfgPath string, args []string, install func(registry.Registry) error) error {
	if len(args) != 3 || args[1] != "--image" || args[0] == "" || args[2] == "" {
		return errors.New("usage: cb add TOOL --image IMAGE")
	}
	name := strings.ToLower(args[0])
	image := args[2]
	if !registry.ValidToolName(name) {
		return fmt.Errorf("invalid tool name %q (use lowercase letters, digits, '-' or '_')", args[0])
	}
	if registry.ReservedToolName(name) {
		return fmt.Errorf("tool name %q is reserved and cannot be installed as a shim", name)
	}
	if _, exists := reg.Tools[name]; exists {
		return fmt.Errorf("tool %q already exists; edit its registry section explicitly", name)
	}
	if strings.ContainsAny(image, " \t\r\n") {
		return errors.New("image reference must not contain whitespace")
	}
	if strings.HasPrefix(image, "-") {
		return errors.New("image reference must not start with '-'")
	}
	lockExists := false
	if _, err := os.Stat(lockfile.PathFor(cfgPath)); err == nil {
		lockExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check lockfile before adding profile: %w", err)
	}

	if err := registry.EnsureFile(cfgPath); err != nil {
		return err
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	combined := append(append([]byte{}, data...), []byte(renderAddedToolSection(name, image))...)
	newReg, err := registry.ParseTOML(string(combined))
	if err != nil {
		return fmt.Errorf("refusing registry update: %w", err)
	}
	if err := atomicio.WriteFile(cfgPath, combined, 0644); err != nil {
		return err
	}
	if err := install(newReg); err != nil {
		return fmt.Errorf("profile %q was added, but shim installation failed: %w; run `cb install` to retry", name, err)
	}

	fmt.Printf("added %s -> %s (stateless)\n", name, image)
	if lockExists {
		fmt.Printf("lockfile is now incomplete; run `cb update %s` or `cb lock` before using the shim\n", name)
	} else {
		fmt.Println("run `cb lock` to pin configured images before relying on the shim")
	}
	return nil
}

func Trace(reg registry.Registry, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb trace TOOL [ARGS...]")
	}
	name := strings.ToLower(args[0])
	t, resolved, ok := reg.Resolve(name)
	if !ok {
		return fmt.Errorf("no tool profile for %q", name)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return err
	}

	raw := append([]string(nil), args[1:]...)
	normalized := pathmap.NormalizeToolArgs(t, raw)

	var root, workspaceRoot string
	var found bool
	if t.CwdMode == "isolated" {
		root = dockerrun.IsolatedRoot
		workspaceRoot = "/root"
		found = false
	} else {
		root, found = pathmap.FindProjectRootForTool(cwd, t)
		if !found {
			root = cwd
		}
		workspaceRoot = pathmap.WorkspaceRootFor(t, root)
	}

	mapped, mounts, err := pathmap.MapToolArgs(t, root, cwd, workspaceRoot, raw)
	if err != nil {
		return err
	}
	fmt.Printf("tool:       %s\n", name)
	if resolved != name {
		fmt.Printf("resolved:   %s\n", resolved)
	}
	fmt.Printf("image:      %s\n", t.Image)
	fmt.Printf("provider:   %s\n", t.Provider)
	fmt.Printf("cwd:        %s\n", cwd)
	if t.CwdMode == "isolated" {
		fmt.Printf("cwd_mode:   isolated\n")
		fmt.Printf("workdir:    /root\n")
		fmt.Printf("project_bind_mount: (none)\n")
	} else {
		fmt.Printf("root:       %s\n", root)
		fmt.Printf("workspace:  %s\n", workspaceRoot)
	}
	fmt.Printf("raw:        %#v\n", raw)
	fmt.Printf("normalized: %#v\n", normalized)
	fmt.Printf("mapped:     %#v\n", mapped)
	if len(mounts) == 0 {
		if t.CwdMode == "isolated" {
			fmt.Printf("mounts:     (none beyond explicit host/cb mounts)\n")
		} else {
			fmt.Printf("mounts:     (none beyond %s)\n", workspaceRoot)
		}
	} else {
		fmt.Printf("mounts:     %#v\n", mounts)
	}
	fmt.Printf("path_equals: %#v\n", t.PathEquals)
	if t.Provider == "stateful" {
		fmt.Printf("state_group: %s\n", t.StateGroup)
		for _, spec := range t.ProjectVolumes {
			name, dst, _ := registry.ParseVolumeBinding(spec)
			dst = pathmap.StatefulWorkspaceDestination(dst, workspaceRoot)
			fmt.Printf("project_volume: %s -> %s\n", pathmap.StatefulProjectVolumeID(t.StateGroup, name, root, found), dst)
		}
		for _, spec := range t.SharedVolumes {
			name, dst, _ := registry.ParseVolumeBinding(spec)
			fmt.Printf("shared_volume:  %s -> %s\n", pathmap.StatefulSharedVolumeID(t.StateGroup, name), dst)
		}
	}
	for _, spec := range t.HostMounts {
		source, target, mode, err := registry.ParseHostMount(spec)
		if err != nil {
			return err
		}
		expanded, err := dockerrun.ExpandHostMountSource(source)
		if err == nil {
			expanded, err = pathmap.CanonicalPath(expanded)
		}
		switch {
		case err != nil:
			fmt.Printf("host_mount:  %s -> %s (%s) [resolve error: %v]\n", source, target, mode, err)
		case strings.HasPrefix(expanded, `\\`):
			fmt.Printf("host_mount:  %s -> %s (%s) [would fail: resolves to a UNC path, which Docker Desktop cannot share]\n", expanded, target, mode)
		default:
			if _, statErr := os.Stat(expanded); statErr != nil {
				fmt.Printf("host_mount:  %s -> %s (%s) [would fail: source does not exist]\n", expanded, target, mode)
			} else {
				fmt.Printf("host_mount:  %s -> %s (%s)\n", expanded, target, mode)
			}
		}
	}
	return nil
}

func Env(reg registry.Registry) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return err
	}
	pt, ok := reg.Tools["python"]
	if !ok {
		return errors.New("python tool not configured")
	}
	root, found := pathmap.FindProjectRootForTool(cwd, pt)
	if !found {
		root = cwd
	}
	fmt.Printf("cwd:          %s\nproject:      %v\nroot:         %s\n", cwd, found, root)
	if t, ok := reg.Tools["python"]; ok {
		fmt.Printf("python image: %s\npython env:   %s\npip cache:    cb-pip-cache\n", t.Image, pathmap.PythonEnvID(root, found))
	} else {
		fmt.Println("python:       not configured")
	}
	return nil
}

func Default(reg registry.Registry, cfgPath string, args []string) error {
	if reg.SchemaVersion < 2 {
		return errors.New("runtime defaults require registry schema 2; run `cb install` to upgrade")
	}
	if len(args) == 0 {
		if len(reg.Defaults) == 0 {
			return errors.New("no runtime defaults are configured; run `cb install` to upgrade the registry")
		}
		families := make([]string, 0, len(reg.Defaults))
		for family := range reg.Defaults {
			families = append(families, family)
		}
		sort.Strings(families)
		for _, family := range families {
			info, _ := reg.DefaultInfo(family)
			fmt.Printf("%-10s %s  aliases=%s  versions=%s\n", info.Family, info.Selected, strings.Join(info.Aliases, ","), strings.Join(info.Versions, ","))
		}
		return nil
	}
	if len(args) != 3 || args[0] != "set" {
		return errors.New("usage: cb default | cb default set FAMILY VERSION")
	}
	family, version := strings.ToLower(args[1]), strings.ToLower(args[2])
	if err := registry.SetDefaultVersion(cfgPath, family, version); err != nil {
		return err
	}
	info, _ := reg.DefaultInfo(family)
	fmt.Printf("default %s = %s (aliases: %s)\n", family, version, strings.Join(info.Aliases, ", "))
	return nil
}

type exposeStore struct {
	kind             string
	volumeName       string
	mountTarget      string
	binDirectory     string
	installHint      string
	companionTargets []string
	companionMounts  []exposeMount
}

type exposeMount struct {
	volumeName  string
	mountTarget string
}

type exposedBin struct {
	name    string
	command string
}

func exposeStoreForMountTarget(dst string) (exposeStore, bool) {
	switch dst {
	case "/cb/npm-global":
		return exposeStore{kind: "npm", mountTarget: dst, binDirectory: dst + "/bin", installHint: "npm install -g <package>"}, true
	case "/go/bin":
		return exposeStore{kind: "Go", mountTarget: dst, binDirectory: dst, installHint: "go install <module>@latest"}, true
	case "/cb/cargo-global":
		return exposeStore{kind: "Cargo", mountTarget: dst, binDirectory: dst + "/bin", installHint: "cargo install <crate>"}, true
	case "/cb/uv-bin":
		return exposeStore{kind: "uv tool", mountTarget: dst, binDirectory: dst, installHint: "uv tool install <package>", companionTargets: []string{"/cb/uv-tools"}}, true
	default:
		return exposeStore{}, false
	}
}

func exposeStoreFor(t registry.Tool) (exposeStore, error) {
	var found *exposeStore
	volumesByTarget := map[string]string{}
	for _, spec := range t.SharedVolumes {
		logical, dst, err := registry.ParseVolumeBinding(spec)
		if err != nil {
			return exposeStore{}, err
		}
		dst = path.Clean(dst)
		volumeName := pathmap.StatefulSharedVolumeID(t.StateGroup, logical)
		volumesByTarget[dst] = volumeName
		candidate, ok := exposeStoreForMountTarget(dst)
		if !ok {
			continue
		}
		if found != nil {
			return exposeStore{}, fmt.Errorf("tool %q declares more than one supported global binary store; expose source is ambiguous", t.Name)
		}
		candidate.volumeName = volumeName
		found = &candidate
	}
	if found == nil {
		return exposeStore{}, fmt.Errorf("tool %q has no supported global binary store (/cb/npm-global, /go/bin, /cb/cargo-global or /cb/uv-bin)", t.Name)
	}
	for _, target := range found.companionTargets {
		volumeName, ok := volumesByTarget[target]
		if !ok {
			return exposeStore{}, fmt.Errorf("tool %q %s global store requires a shared volume mounted at %s", t.Name, found.kind, target)
		}
		found.companionMounts = append(found.companionMounts, exposeMount{volumeName: volumeName, mountTarget: target})
	}
	return *found, nil
}

func discoverGlobalBins(t registry.Tool, store exposeStore) ([]exposedBin, error) {
	image, err := lockfile.RuntimeImageForTool(t)
	if err != nil {
		return nil, err
	}
	script := `if [ -d "$1" ]; then for f in "$1"/*; do [ -f "$f" ] && [ -x "$f" ] || continue; printf '%s\000' "${f##*/}"; done; fi`
	dockerArgs, err := exposeDiscoveryArgs(store, image, script)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("docker", dockerArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect %s global bin: %w", store.kind, err)
	}
	return parseExposedBins(out, store)
}

func exposeDiscoveryArgs(store exposeStore, image, script string) ([]string, error) {
	dockerArgs := []string{"run", "--rm"}
	mounts := append([]exposeMount{{volumeName: store.volumeName, mountTarget: store.mountTarget}}, store.companionMounts...)
	for _, volume := range mounts {
		mount, err := dockerrun.MountSpecMode("volume", volume.volumeName, volume.mountTarget, "ro")
		if err != nil {
			return nil, err
		}
		dockerArgs = append(dockerArgs, "--mount", mount)
	}
	dockerArgs = append(dockerArgs, image, "sh", "-c", script, "cb-expose", store.binDirectory)
	return dockerArgs, nil
}

func parseExposedBins(out []byte, store exposeStore) ([]exposedBin, error) {
	seen := map[string]string{}
	var bins []exposedBin
	for _, raw := range bytes.Split(out, []byte{0}) {
		binary := string(raw)
		name := strings.ToLower(binary)
		// Untrusted names discovered inside the container: skip anything that
		// is not a safe shim name or that would shadow cb / Windows devices.
		if name == "" || !registry.ValidToolName(name) || registry.ReservedToolName(name) {
			continue
		}
		if previous, ok := seen[name]; ok {
			if previous != binary {
				return nil, fmt.Errorf("global binaries %q and %q differ only by case and cannot share a Windows shim", previous, binary)
			}
			continue
		}
		seen[name] = binary
		bins = append(bins, exposedBin{name: name, command: store.binDirectory + "/" + binary})
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].name < bins[j].name })
	return bins, nil
}

func selectExposedBins(bins []exposedBin, requested map[string]bool) (selected []exposedBin, missing []string) {
	if len(requested) == 0 {
		return bins, nil
	}
	found := map[string]bool{}
	for _, bin := range bins {
		if requested[bin.name] {
			selected = append(selected, bin)
			found[bin.name] = true
		}
	}
	for name := range requested {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return selected, missing
}

func renderExposedToolSection(sourceName string, source registry.Tool, name, command string) string {
	projectRootMode := ""
	if source.ProjectRootMode != "" {
		projectRootMode = fmt.Sprintf("project_root_mode = %s\n", toml.Quote(source.ProjectRootMode))
	}
	return fmt.Sprintf("\n# Exposed from %s global store by cb expose %s\n[tools.%s]\nimage = %s\nprovider = \"stateful\"\nrole = \"exposed\"\ncommand = [%s]\nproject_markers = %s\n%sstate_group = %s\nshared_volumes = %s\nenv_set = %s\nenv_prefixes = %s\nenv_names = %s\n",
		sourceName, sourceName, name,
		toml.Quote(source.Image),
		toml.Quote(command),
		toml.Array(source.ProjectMarkers),
		projectRootMode,
		toml.Quote(source.StateGroup),
		toml.Array(source.SharedVolumes),
		toml.Array(source.EnvSet),
		toml.Array(source.EnvPrefixes),
		toml.Array(source.EnvNames),
	)
}

func Expose(reg registry.Registry, cfgPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb expose TOOL [BINARY ...] (TOOL has a supported global binary store, e.g. npm, npm22, go, cargo or uv)")
	}
	sourceName := strings.ToLower(args[0])
	source, resolvedSource, ok := reg.Resolve(sourceName)
	if !ok {
		return fmt.Errorf("tool %q not found; cb expose requires a stateful profile with a supported global binary store", sourceName)
	}
	if source.Provider != "stateful" {
		return fmt.Errorf("tool %q is not a stateful profile", sourceName)
	}
	store, err := exposeStoreFor(source)
	if err != nil {
		return err
	}
	bins, err := discoverGlobalBins(source, store)
	if err != nil {
		return err
	}
	requested := map[string]bool{}
	for _, a := range args[1:] {
		name := strings.ToLower(a)
		// Discovery drops reserved names silently; an explicit request for
		// one deserves a visible explanation instead of "no matching binaries".
		if registry.ReservedToolName(name) {
			fmt.Printf("skip %-16s reserved name cannot be exposed as a shim\n", name)
			continue
		}
		requested[name] = true
	}
	if len(args) > 1 && len(requested) == 0 {
		return errors.New("all requested names are reserved and cannot be exposed")
	}
	selected, missing := selectExposedBins(bins, requested)
	for _, name := range missing {
		fmt.Printf("skip %-16s not found in %s global store\n", name, store.kind)
	}
	if len(selected) == 0 {
		return fmt.Errorf("no matching globally installed %s binaries found; try: %s", store.kind, store.installHint)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var add strings.Builder
	added := 0
	for _, bin := range selected {
		if existing, _, exists := reg.Resolve(bin.name); exists {
			fmt.Printf("skip %-16s already exists in registry (state_group=%s)\n", bin.name, existing.StateGroup)
			continue
		}
		section := renderExposedToolSection(resolvedSource, source, bin.name, bin.command)
		add.WriteString(section)
		added++
		fmt.Printf("exposed %-16s %s\n", bin.name, bin.command)
	}
	if added == 0 {
		return nil
	}
	combined := append(append([]byte{}, data...), []byte(add.String())...)
	if _, err := registry.ParseTOML(string(combined)); err != nil {
		return fmt.Errorf("refusing registry update: %w", err)
	}
	if err := atomicio.WriteFile(cfgPath, combined, 0644); err != nil {
		return err
	}
	newReg, _, err := registry.Load()
	if err != nil {
		return fmt.Errorf("reload registry: %w", err)
	}
	return registry.InstallShims(newReg)
}

func isManagedExposedTool(name string, t registry.Tool) bool {
	if t.Provider != "stateful" || t.Role != "exposed" || len(t.Command) != 1 {
		return false
	}
	commandDir := path.Dir(t.Command[0])
	if !strings.EqualFold(path.Base(t.Command[0]), name) {
		return false
	}
	store, err := exposeStoreFor(t)
	return err == nil && store.binDirectory == commandDir
}

func Inspect(reg registry.Registry, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: cb inspect TOOL")
	}
	name := strings.ToLower(args[0])
	t, resolved, ok := reg.Resolve(name)
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return err
	}

	var root, workspaceRoot string
	var found bool
	if t.CwdMode == "isolated" {
		root = ""
		found = false
		workspaceRoot = "/root"
	} else {
		root, found = pathmap.FindProjectRootForTool(cwd, t)
		if !found {
			root = cwd
		}
		workspaceRoot = pathmap.WorkspaceRootFor(t, root)
	}

	fmt.Printf("name:       %s\n", name)
	if resolved != name {
		fmt.Printf("resolved:   %s\n", resolved)
	}
	fmt.Printf("image:      %s\nprovider:   %s\n", t.Image, t.Provider)
	lock, lockPath, lerr := lockfile.LoadForRegistry()
	if lerr != nil {
		fmt.Printf("lock:       ERROR (%v)\n", lerr)
	} else if lock == nil {
		fmt.Printf("lock:       UNLOCKED (%s missing)\n", lockPath)
	} else if e, ok := lock.Images[t.Image]; ok && e.Configured == t.Image {
		fmt.Printf("locked:     %s\nstatus:     LOCKED\n", e.Resolved)
	} else {
		fmt.Printf("lock:       STALE/UNLOCKED (no matching entry for configured image)\n")
	}
	if t.Role != "" {
		fmt.Printf("role:       %s\n", t.Role)
	}
	if len(t.Command) > 0 {
		fmt.Printf("command:    %#v\n", t.Command)
	}
	if t.CwdMode == "isolated" {
		fmt.Printf("cwd_mode:   isolated\n")
	}
	fmt.Printf("cwd:        %s\n", cwd)
	if t.CwdMode == "isolated" {
		fmt.Printf("workdir:    /root\n")
		fmt.Printf("project_bind_mount: (none)\n")
	} else {
		fmt.Printf("root:       %s\n", root)
		fmt.Printf("workspace:  %s\n", workspaceRoot)
	}
	if t.StateGroup != "" {
		fmt.Printf("state_group: %s\n", t.StateGroup)
	}
	for _, spec := range t.ProjectVolumes {
		logical, dst, e := registry.ParseVolumeBinding(spec)
		if e != nil {
			return e
		}
		fmt.Printf("project_volume: %s -> %s\n", pathmap.StatefulProjectVolumeID(t.StateGroup, logical, root, found), pathmap.StatefulWorkspaceDestination(dst, workspaceRoot))
	}
	for _, spec := range t.SharedVolumes {
		logical, dst, e := registry.ParseVolumeBinding(spec)
		if e != nil {
			return e
		}
		fmt.Printf("shared_volume:  %s -> %s\n", pathmap.StatefulSharedVolumeID(t.StateGroup, logical), dst)
	}
	for _, spec := range t.HostMounts {
		source, target, mode, e := registry.ParseHostMount(spec)
		if e != nil {
			return e
		}
		fmt.Printf("host_mount:  %s -> %s (%s)\n", source, target, mode)
	}
	if t.Provider == "python" {
		fmt.Printf("python_env: %s\npip_cache:  cb-pip-cache\n", pathmap.PythonEnvID(root, found))
	}
	if len(t.PathEquals) > 0 {
		fmt.Printf("path_equals: %#v\n", t.PathEquals)
	}
	if len(t.PathNext) > 0 {
		fmt.Printf("path_next:   %#v\n", t.PathNext)
	}
	if len(t.EnvNames) > 0 {
		fmt.Printf("env_names:   %#v\n", t.EnvNames)
	}
	if len(t.EnvPrefixes) > 0 {
		fmt.Printf("env_prefixes:%#v\n", t.EnvPrefixes)
	}
	return nil
}

func Unexpose(reg registry.Registry, cfgPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb unexpose TOOL [TOOL...]")
	}
	remove := map[string]bool{}
	for _, a := range args {
		name := strings.ToLower(a)
		t, resolved, ok := reg.Resolve(name)
		if !ok {
			return fmt.Errorf("tool %q not found", name)
		}
		if resolved != name {
			return fmt.Errorf("tool %q is an alias for %q; unexpose requires a concrete tool name", name, resolved)
		}
		if !isManagedExposedTool(name, t) {
			return fmt.Errorf("%s is not marked as a cb-exposed tool; older generated profiles must be recreated with cb uninstall followed by cb expose", name)
		}
		remove[name] = true
	}
	if err := registry.RewriteWithoutTools(cfgPath, remove); err != nil {
		return err
	}
	for name := range remove {
		if err := registry.RemoveShim(name); err != nil {
			return err
		}
	}
	return nil
}

func Uninstall(reg registry.Registry, cfgPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb uninstall TOOL [TOOL...]")
	}
	remove := map[string]bool{}
	builtins := registry.Default().Tools
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("unknown option %q", a)
		}
		name := strings.ToLower(a)
		_, resolved, ok := reg.Resolve(name)
		if !ok {
			return fmt.Errorf("tool %q not found", name)
		}
		if resolved != name {
			return fmt.Errorf("tool %q is an alias for %q; uninstall requires a concrete tool name", name, resolved)
		}
		if _, ok := builtins[resolved]; ok {
			return fmt.Errorf("%s is a built-in profile managed by cb install; edit the registry manually if you intentionally want to disable it", name)
		}
		remove[name] = true
	}
	if err := registry.RewriteWithoutTools(cfgPath, remove); err != nil {
		return err
	}
	for name := range remove {
		if err := registry.RemoveShim(name); err != nil {
			return err
		}
	}
	return nil
}

// --- v0.9 image locking ----------------------------------------------------

func Backup(cfgPath string, args []string, version string) error {
	pathArg, stateNames, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	dir := filepath.Dir(cfgPath)
	created := time.Now()
	path := ""
	if pathArg != "" {
		path = pathArg
	} else {
		backupDir := filepath.Join(dir, "backups")
		if err := os.MkdirAll(backupDir, 0755); err != nil {
			return err
		}
		path = filepath.Join(backupDir, "container-bin-backup-"+created.Format("20060102-150405")+".zip")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("backup %q already exists; choose a different filename", path)
		}
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	zw := zip.NewWriter(f)
	add := func(src, name string, required bool) error {
		b, err := os.ReadFile(src)
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		if err != nil {
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	if err := add(cfgPath, "container-bin.toml", true); err != nil {
		zw.Close()
		f.Close()
		return err
	}
	if err := add(lockfile.PathFor(cfgPath), "container-bin.lock", false); err != nil {
		zw.Close()
		f.Close()
		return err
	}
	stateCount := 0
	if len(stateNames) > 0 {
		manifest, err := statearchive.Backup(zw, stateNames, version, created)
		if err != nil {
			zw.Close()
			f.Close()
			return err
		}
		stateCount = len(manifest.Volumes)
	}
	meta, err := zw.Create("backup-info.txt")
	if err != nil {
		zw.Close()
		f.Close()
		return err
	}
	if _, err := fmt.Fprintf(meta, "container-bin %s\ncreated=%s\nsource=%s\nstate_volumes=%d\n", version, created.Format(time.RFC3339), dir, stateCount); err != nil {
		zw.Close()
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	complete = true
	abs, _ := filepath.Abs(path)
	fmt.Printf("backup: %s\n", abs)
	if stateCount > 0 {
		fmt.Printf("state:  %d explicitly selected managed volume(s)\n", stateCount)
	}
	return nil
}

func parseBackupArgs(args []string) (path string, state []string, err error) {
	stateIndex := -1
	for i, arg := range args {
		if arg == "--state" {
			if stateIndex != -1 {
				return "", nil, errors.New("--state may be specified only once")
			}
			stateIndex = i
		}
	}
	if stateIndex == -1 {
		if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
			return "", nil, errors.New("usage: cb backup [BACKUP.zip] [--state VOLUME ...]")
		}
		if len(args) == 1 {
			path = args[0]
		}
		return path, nil, nil
	}
	if stateIndex > 1 || stateIndex == len(args)-1 {
		return "", nil, errors.New("usage: cb backup [BACKUP.zip] --state VOLUME [VOLUME ...]")
	}
	if stateIndex == 1 {
		if strings.HasPrefix(args[0], "-") {
			return "", nil, errors.New("backup path must precede --state")
		}
		path = args[0]
	}
	for _, name := range args[stateIndex+1:] {
		if strings.HasPrefix(name, "-") {
			return "", nil, fmt.Errorf("invalid state volume name %q", name)
		}
		state = append(state, name)
	}
	return path, state, nil
}

func Restore(cfgPath string, args []string) error {
	backupPath, apply, restoreState, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(backupPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.Name != "container-bin.toml" && f.Name != "container-bin.lock" {
			continue
		}
		if _, duplicate := files[f.Name]; duplicate {
			return fmt.Errorf("backup contains duplicate entry %q", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return err
		}
		files[f.Name] = b
	}
	cfg, ok := files["container-bin.toml"]
	if !ok {
		return errors.New("backup does not contain container-bin.toml")
	}
	if _, err := registry.ParseTOML(string(cfg)); err != nil {
		return fmt.Errorf("backup registry invalid: %w", err)
	}
	if lock, ok := files["container-bin.lock"]; ok {
		tmp, err := os.CreateTemp("", "cb-lock-*.tmp")
		if err != nil {
			return err
		}
		name := tmp.Name()
		tmp.Close()
		defer os.Remove(name)
		if err := os.WriteFile(name, lock, 0600); err != nil {
			return err
		}
		if _, err := lockfile.Load(name); err != nil {
			return fmt.Errorf("backup lock invalid: %w", err)
		}
	}
	var stateBackup *statearchive.Archive
	var statePlan []statearchive.Plan
	if restoreState {
		stateBackup, err = statearchive.Open(zr.File)
		if err != nil {
			return err
		}
		statePlan, err = stateBackup.Plan()
		if err != nil {
			return err
		}
	}
	fmt.Printf("restore source: %s\n", backupPath)
	fmt.Printf("  container-bin.toml: %d bytes\n", len(cfg))
	if b, ok := files["container-bin.lock"]; ok {
		fmt.Printf("  container-bin.lock: %d bytes\n", len(b))
	} else {
		fmt.Println("  container-bin.lock: absent")
	}
	if restoreState {
		fmt.Printf("  state manifest: %d volume(s), checksums valid\n", len(statePlan))
		for _, plan := range statePlan {
			fmt.Printf("    %-13s %s\n", plan.Status, plan.Name)
		}
	} else {
		for _, f := range zr.File {
			if f.Name == statearchive.ManifestName {
				fmt.Println("  state manifest: present but not selected (add --state)")
				break
			}
		}
	}
	if !apply {
		fmt.Println("\nDry run only. Re-run with --apply to perform the reported restore.")
		return nil
	}
	if restoreState {
		if err := stateBackup.Restore(); err != nil {
			return err
		}
	}
	if err := atomicio.WriteFile(cfgPath, cfg, 0644); err != nil {
		return err
	}
	lockPath := lockfile.PathFor(cfgPath)
	if b, ok := files["container-bin.lock"]; ok {
		if err := atomicio.WriteFile(lockPath, b, 0644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(lockPath)
		_ = os.Remove(lockPath + ".bak")
	}
	if restoreState {
		fmt.Printf("restored %d state volume(s); registry/lock restored atomically; run `cb install` to reconcile shims\n", len(statePlan))
	} else {
		fmt.Println("restored registry/lock atomically; run `cb install` to reconcile shims")
	}
	return nil
}

func parseRestoreArgs(args []string) (path string, apply, state bool, err error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", false, false, errors.New("usage: cb restore BACKUP.zip [--state] [--apply]")
	}
	path = args[0]
	for _, arg := range args[1:] {
		switch arg {
		case "--apply":
			if apply {
				return "", false, false, errors.New("--apply may be specified only once")
			}
			apply = true
		case "--state":
			if state {
				return "", false, false, errors.New("--state may be specified only once")
			}
			state = true
		default:
			return "", false, false, fmt.Errorf("unknown restore option %q", arg)
		}
	}
	return path, apply, state, nil
}

func Lock(reg registry.Registry, cfgPath string, args []string) error {
	path := lockfile.PathFor(cfgPath)
	check, localTools, err := parseLockArgs(args)
	if err != nil {
		return err
	}
	if check {
		lf, err := lockfile.Load(path)
		if err != nil {
			return err
		}
		if lf == nil {
			return fmt.Errorf("lockfile missing: %s (run `cb lock`)", path)
		}
		missing := 0
		for _, image := range lockfile.ConfiguredImages(reg) {
			e, ok := lf.Images[image]
			if !ok || e.Configured != image {
				fmt.Printf("MISSING  %s\n", image)
				missing++
				continue
			}
			cmd := exec.Command("docker", "image", "inspect", e.Resolved)
			if err := cmd.Run(); err != nil {
				fmt.Printf("ABSENT   %s -> %s\n", image, e.Resolved)
				missing++
			} else {
				fmt.Printf("OK       %s -> %s\n", image, e.Resolved)
			}
		}
		if missing > 0 {
			return fmt.Errorf("lock check failed: %d image(s) missing/unlocked", missing)
		}
		fmt.Printf("lock OK: %s\n", path)
		return nil
	}
	localImages := map[string]bool{}
	for name := range localTools {
		t, ok := reg.Tools[name]
		if !ok {
			return fmt.Errorf("tool %q not found", name)
		}
		localImages[t.Image] = true
	}
	lf := &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{}}
	for _, image := range lockfile.ConfiguredImages(reg) {
		fmt.Printf("locking  %s\n", image)
		var e lockfile.LockEntry
		if localImages[image] {
			e, err = lockfile.ResolveLocalImage(image)
		} else {
			e, err = lockfile.ResolveRepositoryImage(image)
		}
		if err != nil {
			return err
		}
		lf.Images[image] = e
		fmt.Printf("  -> %s\n", e.Resolved)
	}
	if err := lockfile.Write(path, lf); err != nil {
		return err
	}
	fmt.Printf("\nlockfile: %s\n", path)
	return nil
}

func parseLockArgs(args []string) (bool, map[string]bool, error) {
	localTools := map[string]bool{}
	if len(args) == 0 {
		return false, localTools, nil
	}
	if len(args) == 1 && args[0] == "--check" {
		return true, localTools, nil
	}
	if len(args)%2 != 0 {
		return false, nil, errors.New("usage: cb lock [--check] | cb lock [--local TOOL ...]")
	}
	for i := 0; i < len(args); i += 2 {
		if args[i] != "--local" || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
			return false, nil, errors.New("usage: cb lock [--check] | cb lock [--local TOOL ...]")
		}
		localTools[strings.ToLower(args[i+1])] = true
	}
	return false, localTools, nil
}

func Update(reg registry.Registry, cfgPath string, args []string) error {
	target, mode, err := parseUpdateArgs(args)
	if err != nil {
		return err
	}
	path := lockfile.PathFor(cfgPath)
	lf, err := lockfile.Load(path)
	if err != nil {
		return err
	}
	if lf == nil {
		lf = &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{}}
	}
	var images []string
	if target == "--all" {
		images = lockfile.ConfiguredImages(reg)
	} else {
		name := strings.ToLower(target)
		t, _, ok := reg.Resolve(name)
		if !ok {
			return fmt.Errorf("tool %q not found", name)
		}
		images = []string{t.Image}
	}
	seen := map[string]bool{}
	for _, image := range images {
		if seen[image] {
			continue
		}
		seen[image] = true
		old := lf.Images[image]
		fmt.Printf("updating %s\n", image)
		var e lockfile.LockEntry
		if mode == "local" || (mode == "" && lockfile.IsLocalResolved(old.Resolved)) {
			e, err = lockfile.ResolveLocalImage(image)
		} else {
			e, err = lockfile.ResolveRepositoryImage(image)
		}
		if err != nil {
			return err
		}
		lf.Images[image] = e
		if old.Resolved == "" {
			fmt.Printf("  new: %s\n", e.Resolved)
		} else if old.Resolved == e.Resolved {
			fmt.Printf("  unchanged: %s\n", e.Resolved)
		} else {
			fmt.Printf("  old: %s\n  new: %s\n", old.Resolved, e.Resolved)
		}
	}
	if err := lockfile.Write(path, lf); err != nil {
		return err
	}
	fmt.Printf("lockfile: %s\n", path)
	return nil
}

func parseUpdateArgs(args []string) (target, mode string, err error) {
	if len(args) == 1 && args[0] != "--local" && args[0] != "--registry" {
		return args[0], "", nil
	}
	if len(args) == 2 && (args[0] == "--local" || args[0] == "--registry") && args[1] != "" && args[1] != "--all" && !strings.HasPrefix(args[1], "-") {
		return args[1], strings.TrimPrefix(args[0], "--"), nil
	}
	return "", "", errors.New("usage: cb update TOOL | cb update --all | cb update --local TOOL | cb update --registry TOOL")
}

// Install creates or upgrades the registry file and reconciles the shim set
// from it. It reloads the registry after the upgrade because EnsureFile or
// AppendMissingDefaultTools may have just created or extended the file.
func Install(cfgPath, version string) error {
	if err := registry.EnsureFile(cfgPath); err != nil {
		return err
	}
	if err := registry.AppendMissingDefaultTools(cfgPath, version); err != nil {
		return err
	}
	// Reload in case the file was just created or upgraded.
	reg, _, err := registry.Load()
	if err != nil {
		return err
	}
	return registry.InstallShims(reg)
}
