// Package cli implements the cb subcommands that compose several lower layers:
// registry mutation, path mapping, lock resolution, docker execution and
// diagnostics. It sits directly beneath main, which owns only argv dispatch,
// exit-code policy and the mutation-lock signal wrapper.
package cli

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

func Trace(reg registry.Registry, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb trace TOOL [ARGS...]")
	}
	name := strings.ToLower(args[0])
	t, ok := reg.Tools[name]
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
		root, found = pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
		if !found {
			root = cwd
		}
		workspaceRoot = pathmap.WorkspaceRootFor(t, root)
	}

	mapped, mounts, err := pathmap.MapToolArgs(t, root, cwd, workspaceRoot, raw)
	if err != nil {
		return err
	}
	fmt.Printf("tool:       %s\n", t.Name)
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
	root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(pt))
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

func discoverNPMGlobalBins(t registry.Tool) ([]string, error) {
	var globalVol, logicalName string
	for _, spec := range t.SharedVolumes {
		logical, dst, err := registry.ParseVolumeBinding(spec)
		if err != nil {
			return nil, err
		}
		if dst == "/cb/npm-global" {
			logicalName = logical
			globalVol = pathmap.StatefulSharedVolumeID(t.StateGroup, logicalName)
			break
		}
	}
	if globalVol == "" {
		return nil, fmt.Errorf("tool %q has no npm-global shared volume", t.Name)
	}
	image, err := lockfile.RuntimeImageForTool(t)
	if err != nil {
		return nil, err
	}
	script := `if [ -d /cb/npm-global/bin ]; then for f in /cb/npm-global/bin/*; do [ -e "$f" ] || continue; basename "$f"; done; fi`
	mount, err := dockerrun.MountSpec("volume", globalVol, "/cb/npm-global")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("docker", "run", "--rm", "--mount", mount, image, "sh", "-lc", script)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect npm global bin: %w", err)
	}
	seen := map[string]bool{}
	var bins []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.ToLower(strings.TrimSpace(line))
		// Untrusted names discovered inside the container: skip anything that
		// is not a safe shim name or that would shadow cb / Windows devices.
		if name == "" || !registry.ValidToolName(name) || registry.ReservedToolName(name) || seen[name] {
			continue
		}
		seen[name] = true
		bins = append(bins, name)
	}
	sort.Strings(bins)
	return bins, nil
}

func renderExposedToolSection(sourceName string, source registry.Tool, name string) string {
	return fmt.Sprintf("\n# Exposed from %s global prefix by cb expose %s\n[tools.%s]\nimage = %s\nprovider = \"stateful\"\ncommand = [%s]\nstate_group = %s\nshared_volumes = %s\nenv_set = %s\nenv_prefixes = %s\nenv_names = %s\n",
		sourceName, sourceName, name,
		toml.Quote(source.Image),
		toml.Quote("/cb/npm-global/bin/"+name),
		toml.Quote(source.StateGroup),
		toml.Array(source.SharedVolumes),
		toml.Array(source.EnvSet),
		toml.Array(source.EnvPrefixes),
		toml.Array(source.EnvNames),
	)
}

func Expose(reg registry.Registry, cfgPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb expose TOOL [BINARY ...] (TOOL is an npm-shaped stateful profile already in the registry, e.g. npm or npm22)")
	}
	sourceName := strings.ToLower(args[0])
	source, ok := reg.Tools[sourceName]
	if !ok {
		return fmt.Errorf("tool %q not found; cb expose exposes global binaries from an npm-shaped profile already in the registry", sourceName)
	}
	if source.Provider != "stateful" {
		return fmt.Errorf("tool %q is not a stateful npm-shaped profile", sourceName)
	}
	bins, err := discoverNPMGlobalBins(source)
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
	var selected []string
	for _, b := range bins {
		if len(requested) == 0 || requested[b] {
			selected = append(selected, b)
		}
	}
	if len(selected) == 0 {
		return errors.New("no matching globally installed npm binaries found; try: npm install -g <package>")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var add strings.Builder
	added := 0
	for _, name := range selected {
		if _, exists := reg.Tools[name]; exists {
			fmt.Printf("skip %-16s already exists in registry (state_group=%s)\n", name, reg.Tools[name].StateGroup)
			continue
		}
		section := renderExposedToolSection(sourceName, source, name)
		add.WriteString(section)
		added++
		fmt.Printf("exposed %-16s /cb/npm-global/bin/%s\n", name, name)
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

func Inspect(reg registry.Registry, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: cb inspect TOOL")
	}
	name := strings.ToLower(args[0])
	t, ok := reg.Tools[name]
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
		root, found = pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
		if !found {
			root = cwd
		}
		workspaceRoot = pathmap.WorkspaceRootFor(t, root)
	}

	fmt.Printf("name:       %s\nimage:      %s\nprovider:   %s\n", t.Name, t.Image, t.Provider)
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
		t, ok := reg.Tools[name]
		if !ok {
			return fmt.Errorf("tool %q not found", name)
		}
		if len(t.Command) != 1 || !strings.HasPrefix(t.Command[0], "/cb/npm-global/bin/") {
			return fmt.Errorf("%s is not an npm-exposed tool", name)
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
		if _, ok := reg.Tools[name]; !ok {
			return fmt.Errorf("tool %q not found", name)
		}
		if _, ok := builtins[name]; ok {
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
	if len(args) > 1 {
		return errors.New("usage: cb backup [BACKUP.zip]")
	}
	dir := filepath.Dir(cfgPath)
	path := ""
	if len(args) == 1 {
		path = args[0]
	} else {
		backupDir := filepath.Join(dir, "backups")
		if err := os.MkdirAll(backupDir, 0755); err != nil {
			return err
		}
		path = filepath.Join(backupDir, "container-bin-backup-"+time.Now().Format("20060102-150405")+".zip")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
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
	meta, _ := zw.Create("backup-info.txt")
	fmt.Fprintf(meta, "container-bin %s\ncreated=%s\nsource=%s\n", version, time.Now().Format(time.RFC3339), dir)
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("backup: %s\n", abs)
	return nil
}

func Restore(cfgPath string, args []string) error {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--apply") {
		return errors.New("usage: cb restore BACKUP.zip [--apply]")
	}
	apply := len(args) == 2
	zr, err := zip.OpenReader(args[0])
	if err != nil {
		return err
	}
	defer zr.Close()
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.Name != "container-bin.toml" && f.Name != "container-bin.lock" {
			continue
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
	fmt.Printf("restore source: %s\n", args[0])
	fmt.Printf("  container-bin.toml: %d bytes\n", len(cfg))
	if b, ok := files["container-bin.lock"]; ok {
		fmt.Printf("  container-bin.lock: %d bytes\n", len(b))
	} else {
		fmt.Println("  container-bin.lock: absent")
	}
	if !apply {
		fmt.Println("\nDry run only. Re-run with --apply to restore registry/lock.")
		return nil
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
	fmt.Println("restored registry/lock atomically; run `cb install` to reconcile shims")
	return nil
}

func Lock(reg registry.Registry, cfgPath string, args []string) error {
	path := lockfile.PathFor(cfgPath)
	if len(args) == 1 && args[0] == "--check" {
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
	if len(args) != 0 {
		return errors.New("usage: cb lock [--check]")
	}
	lf := &lockfile.LockFile{Version: 1, Images: map[string]lockfile.LockEntry{}}
	for _, image := range lockfile.ConfiguredImages(reg) {
		fmt.Printf("locking  %s\n", image)
		e, err := lockfile.ResolveImage(image, true)
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

func Update(reg registry.Registry, cfgPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: cb update TOOL | cb update --all")
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
	if args[0] == "--all" {
		images = lockfile.ConfiguredImages(reg)
	} else {
		name := strings.ToLower(args[0])
		t, ok := reg.Tools[name]
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
		e, err := lockfile.ResolveImage(image, true)
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
