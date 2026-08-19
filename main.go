package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/atomicio"
	"github.com/AviBackToBlack/container-bin/internal/dockerrun"
	"github.com/AviBackToBlack/container-bin/internal/dockervol"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/mutationlock"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/toml"
)

// version is injected at release time via:
//
//	go build -ldflags "-X main.version=v1.2.3"
//
// Local/dev builds report "dev".
var version = "dev"

func main() {
	reg, cfgPath, err := registry.Load()
	if err != nil {
		fatalf("registry: %v", err)
	}

	invoked := invokedName(os.Args[0])
	if invoked != "cb" && invoked != "container-bin" && !strings.HasPrefix(invoked, "cb-v") {
		tool, ok := reg.Tools[invoked]
		if !ok {
			fatalf("no tool profile for %q (registry: %s)", invoked, cfgPath)
		}
		code, err := dockerrun.RunTool(tool, os.Args[1:])
		if err != nil {
			fatalf("%v", err)
		}
		os.Exit(code)
	}

	if len(os.Args) < 2 {
		usage(cfgPath)
		return
	}
	switch os.Args[1] {
	case "install":
		if err := withMutationLock(cfgPath, func() error {
			if err := registry.EnsureFile(cfgPath); err != nil {
				return err
			}
			if err := registry.AppendMissingDefaultTools(cfgPath, version); err != nil {
				return err
			}
			// Reload in case the file was just created or upgraded.
			reg, _, err = registry.Load()
			if err != nil {
				return err
			}
			return registry.InstallShims(reg)
		}); err != nil {
			fatalf("install: %v", err)
		}
	case "setup":
		if err := withMutationLock(cfgPath, func() error {
			return setupCommand(cfgPath)
		}); err != nil {
			fatalf("setup: %v", err)
		}
	case "doctor":
		if err := doctor(reg, cfgPath); err != nil {
			fatalf("doctor: %v", err)
		}
	case "bugreport":
		if err := bugreportCommand(reg, cfgPath); err != nil {
			fatalf("bugreport: %v", err)
		}
	case "backup":
		if err := backupCommand(cfgPath, os.Args[2:]); err != nil {
			fatalf("backup: %v", err)
		}
	case "restore":
		if err := withMutationLock(cfgPath, func() error {
			return restoreCommand(cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("restore: %v", err)
		}
	case "self-test":
		jsonOut, release, err := parseSelfTestArgs(os.Args[2:])
		if err != nil {
			fatalf("self-test: %v", err)
		}
		if err := selfTestCommand(reg, jsonOut, release); err != nil {
			fatalf("self-test: %v", err)
		}
	case "list":
		registry.ListTools(reg, cfgPath)
	case "trace":
		if err := traceTool(reg, os.Args[2:]); err != nil {
			fatalf("trace: %v", err)
		}
	case "env":
		if err := showEnv(reg); err != nil {
			fatalf("env: %v", err)
		}
	case "state":
		if err := showState(reg); err != nil {
			fatalf("state: %v", err)
		}
	case "inspect":
		if err := inspectTool(reg, os.Args[2:]); err != nil {
			fatalf("inspect: %v", err)
		}
	case "gc":
		if err := gcState(reg, os.Args[2:]); err != nil {
			fatalf("gc: %v", err)
		}
	case "expose":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return exposeTool(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("expose: %v", err)
		}
	case "unexpose":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return unexposeTools(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("unexpose: %v", err)
		}
	case "uninstall":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return uninstallTools(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("uninstall: %v", err)
		}
	case "lock":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return lockCommand(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("lock: %v", err)
		}
	case "update":
		if err := withMutationLock(cfgPath, func() error {
			reg, _, err := registry.Load()
			if err != nil {
				return err
			}
			return updateCommand(reg, cfgPath, os.Args[2:])
		}); err != nil {
			fatalf("update: %v", err)
		}
	case "config":
		fmt.Println(cfgPath)
	case "version", "--version", "-V":
		fmt.Printf("container-bin %s\n", version)
	default:
		usage(cfgPath)
		osExit(exitUsage)
	}
}

// invokedName derives the dispatch name from argv[0]. Windows filenames are
// case-insensitive, so both base and extension are lowered before trimming —
// cmd.exe can hand us PYTHON.EXE, which must still dispatch as "python".
func invokedName(argv0 string) string {
	base := strings.ToLower(filepath.Base(argv0))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func usage(cfg string) {
	fmt.Printf(`container-bin (cb) %s — Docker-backed Windows CLI shims

Commands:
  cb setup     initialize/upgrade registry, install shims, then run doctor
  cb install   create/update shims from the tool registry
  cb doctor    validate Docker, PATH, shims, registry, lock and managed volumes
  cb bugreport assemble a paste-ready diagnostic report with best-effort redaction
  cb backup    back up registry + lock to a zip
  cb restore   validate/restore a backup (dry-run unless --apply)
  cb self-test [--json] [--release] run offline end-to-end compatibility checks
  cb list      list configured tool profiles
  cb trace     show raw/normalized/mapped argv for a tool without running it
  cb env       show project root and Python environment selected for cwd
  cb state     list container-bin Docker volumes and mark current/shared state
  cb inspect   show a tool profile plus resolved project/state information
  cb gc        dry-run cleanup; supports --orphans for labeled missing projects
  cb expose    expose binaries from a managed global tool store (npm)
  cb unexpose  remove dynamically exposed tool profiles/shims
  cb uninstall remove custom tool profiles/shims
  cb lock      create/check immutable image digest lockfile
  cb update    explicitly refresh one or all locked images
  cb config    print registry path
  cb version   print container-bin version

Registry:
  %s
`, version, cfg)
}

func dockerOSTypeVerdict(raw string) (status, message string) {
	normalized := strings.Join(strings.Fields(raw), " ")
	switch strings.ToLower(normalized) {
	case "linux":
		return "ok", "docker is in Linux-container mode"
	case "windows":
		return "fail", "docker is in Windows-container mode; ContainerBin runs Linux images only — switch to Linux containers from the Docker Desktop tray menu"
	default:
		if normalized == "" {
			return "warn", "container mode could not be determined"
		}
		return "warn", "unrecognized container mode: " + normalized
	}
}

func shimDirACLVerdict(raw string, currentUserSID string) (status, message string) {
	writeRights := []string{"FullControl", "Modify", "Write", "CreateFiles", "AppendData", "Delete", "TakeOwnership", "ChangePermissions"}
	isWriteCapable := func(rights string) bool {
		for _, r := range writeRights {
			if strings.Contains(rights, r) {
				return true
			}
		}
		return false
	}
	isTrusted := func(sid string) bool {
		if sid != "" && sid == currentUserSID {
			return true
		}
		switch sid {
		case "S-1-5-18", "S-1-5-32-544":
			return true
		}
		return false
	}

	untrusted := map[string]bool{}
	denied := map[string]bool{}
	hasValidLine := false
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		sid := strings.TrimSpace(parts[0])
		accessType := strings.TrimSpace(parts[1])
		rights := strings.TrimSpace(parts[2])
		if accessType != "Allow" && accessType != "Deny" {
			continue
		}
		hasValidLine = true
		if isTrusted(sid) || !isWriteCapable(rights) {
			continue
		}
		if accessType == "Allow" {
			untrusted[sid] = true
		} else {
			denied[sid] = true
		}
	}
	for sid := range denied {
		delete(untrusted, sid)
	}

	if !hasValidLine {
		return "warn", "could not determine shim directory permissions"
	}
	if len(untrusted) == 0 {
		return "ok", "shim directory is not writable by other users"
	}

	sids := make([]string, 0, len(untrusted))
	for sid := range untrusted {
		sids = append(sids, sid)
	}
	sort.Strings(sids)
	return "warn", fmt.Sprintf("shim directory is writable by other principal(s): %s; a local attacker could replace container-bin.toml or an installed shim — restrict permissions to your account and Administrators", strings.Join(sids, ", "))
}

func reparsePointVerdict(subject, literalPath, resolvedPath string) (status, message string) {
	literalPath = filepath.Clean(literalPath)
	resolvedPath = filepath.Clean(resolvedPath)
	if strings.EqualFold(literalPath, resolvedPath) {
		return "ok", fmt.Sprintf("%s does not sit behind a reparse point", subject)
	}
	return "warn", fmt.Sprintf("%s does not resolve to itself (literal: %s, resolved: %s); if this is a junction or symlink, that is supported (see docs/windows-paths.md P1), but links inside the tree do not traverse from inside the container (P3) — filepath.EvalSymlinks can also produce a different form for reasons other than a reparse point (e.g. expanding an 8.3 short name), so this may be informational only", subject, literalPath, resolvedPath)
}

func networkStorageVerdict(subject, path, driveType string) (status, message string) {
	if strings.HasPrefix(path, `\\`) {
		return "warn", fmt.Sprintf("%s is on a UNC path (%s); Docker Desktop cannot bind-mount a UNC source; see docs/windows-paths.md P8", subject, path)
	}
	if driveType == "" || driveType == "Unknown" || driveType == "NoRootDirectory" {
		// "Unknown"/"NoRootDirectory" are DriveInfo's own admission that it could
		// not classify the drive — that is the same "the probe did not actually
		// succeed" situation as an empty lookup result, not a confident answer of
		// any kind, so it must not fall through to the ok branch below.
		return "warn", fmt.Sprintf("could not determine whether %s is on network storage", subject)
	}
	if driveType == "Network" {
		return "warn", fmt.Sprintf("%s is on a mapped network drive (%s); Docker Desktop cannot reliably bind-mount a mapped network drive; see docs/windows-paths.md P10", subject, path)
	}
	return "ok", fmt.Sprintf("%s is on a %s drive (%s)", subject, driveType, path)
}

func windowsDriveType(driveLetter string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`try { [System.IO.DriveInfo]::new($env:CB_DOCTOR_DRIVE).DriveType } catch { '' }`)
	cmd.Env = append(os.Environ(), "CB_DOCTOR_DRIVE="+driveLetter)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func doctor(reg registry.Registry, cfgPath string) error {
	failures := 0
	warnings := 0
	ok := func(format string, args ...any) { fmt.Printf("OK       "+format+"\n", args...) }
	warn := func(format string, args ...any) { warnings++; fmt.Printf("WARN     "+format+"\n", args...) }
	fail := func(format string, args ...any) { failures++; fmt.Printf("FAIL     "+format+"\n", args...) }

	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		fail("Docker CLI not found in PATH")
	} else {
		ok("Docker CLI: %s", dockerPath)
		cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
		out, err := cmd.Output()
		if err != nil {
			fail("Docker engine unreachable: %v", err)
		} else {
			ok("Docker engine: %s", strings.TrimSpace(string(out)))
			cmd = exec.Command("docker", "info", "--format", "{{.OSType}}")
			out, err = cmd.Output()
			if err != nil {
				warn("docker info failed: %v", err)
			} else {
				status, msg := dockerOSTypeVerdict(string(out))
				switch status {
				case "ok":
					ok("%s", msg)
				case "warn":
					warn("%s", msg)
				case "fail":
					fail("%s", msg)
				}
			}
		}
	}

	if reg.SchemaVersion != 1 {
		fail("registry schema=%d (supported=1)", reg.SchemaVersion)
	} else {
		ok("registry schema 1: %s", cfgPath)
	}

	lf, lockPath, err := lockfile.LoadForRegistry()
	if err != nil {
		fail("lockfile invalid: %v", err)
	} else if lf == nil {
		warn("lockfile missing: %s (runtime is UNLOCKED)", lockPath)
	} else {
		missing := 0
		for _, image := range lockfile.ConfiguredImages(reg) {
			e, found := lf.Images[image]
			if !found {
				missing++
				continue
			}
			if exec.Command("docker", "image", "inspect", e.Resolved).Run() != nil {
				missing++
			}
		}
		if missing == 0 {
			ok("lockfile complete and exact images local: %s", lockPath)
		} else {
			fail("lockfile has %d missing/unavailable image(s); run `cb lock --check`", missing)
		}
	}

	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	dir := filepath.Dir(exe)
	pathParts := filepath.SplitList(os.Getenv("PATH"))
	inPath := false
	for _, part := range pathParts {
		if strings.EqualFold(strings.TrimRight(part, `\\/`), strings.TrimRight(dir, `\\/`)) {
			inPath = true
			break
		}
	}
	if inPath {
		ok("shim directory is in PATH: %s", dir)
	} else {
		fail("shim directory is not in PATH: %s", dir)
	}

	shimProblems := 0
	for name := range reg.Tools {
		shim := filepath.Join(dir, name+".exe")
		a, e1 := os.Stat(exe)
		b, e2 := os.Stat(shim)
		if e1 != nil || e2 != nil {
			shimProblems++
			continue
		}
		if !os.SameFile(a, b) {
			warn("shim %s is a copy, not a hardlink", name)
		}
	}
	if shimProblems == 0 {
		ok("all %d registry shims exist", len(reg.Tools))
	} else {
		fail("%d registry shim(s) missing; run `cb install`", shimProblems)
	}

	// NTFS ACLs and PowerShell's Get-Acl are Windows-specific concepts, unlike the
	// Docker/registry/shim checks above — gate this the same way the python-alias
	// check below is gated, so a non-Windows run does not report a spurious
	// "could not inspect" warning for a probe that could never have succeeded there.
	if runtime.GOOS == "windows" {
		currentUserSID := ""
		if u, err := user.Current(); err == nil {
			currentUserSID = u.Uid
		}
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			`$acl = Get-Acl -LiteralPath $env:CB_DOCTOR_ACL_DIR; `+
				`$acl.Access | ForEach-Object { `+
				`$sid = $_.IdentityReference.Value; `+
				`try { $sid = $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value } catch {}; `+
				`"$sid|$($_.AccessControlType)|$($_.FileSystemRights)" }`)
		cmd.Env = append(os.Environ(), "CB_DOCTOR_ACL_DIR="+dir)
		out, err := cmd.Output()
		if err != nil {
			warn("could not inspect shim directory permissions: %v", err)
		} else {
			status, msg := shimDirACLVerdict(string(out), currentUserSID)
			if status == "ok" {
				ok("%s", msg)
			} else {
				warn("%s", msg)
			}
		}
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		cwd, cwdErr = filepath.Abs(cwd)
	}
	if cwdErr != nil {
		warn("could not determine current directory: %v", cwdErr)
	} else if resolved, evalErr := filepath.EvalSymlinks(cwd); evalErr != nil {
		// Report an unsuccessful resolution as indeterminate, not as an ok "no
		// reparse point" pass — an unresolved path proves nothing either way,
		// matching how every other unsuccessful probe in this function is
		// reported (docker info, the ACL probe, etc.).
		warn("could not determine whether current directory sits behind a reparse point: %v", evalErr)
	} else {
		status, msg := reparsePointVerdict("current directory", cwd, resolved)
		if status == "ok" {
			ok("%s", msg)
		} else {
			warn("%s", msg)
		}
	}

	if runtime.GOOS == "windows" {
		checkNetworkStorage := func(subject, path string) {
			driveType := ""
			if !strings.HasPrefix(path, `\\`) {
				if dt, err := windowsDriveType(filepath.VolumeName(path)); err == nil {
					driveType = dt
				}
			}
			status, msg := networkStorageVerdict(subject, path, driveType)
			if status == "ok" {
				ok("%s", msg)
			} else {
				warn("%s", msg)
			}
		}
		checkNetworkStorage("shim directory", dir)
		if cwdErr == nil {
			checkNetworkStorage("current directory", cwd)
		}
	}

	if runtime.GOOS == "windows" {
		out, _ := exec.Command("where.exe", "python").Output()
		// where.exe prints one full path per line; paths may contain spaces,
		// so this must split on newlines, never on whitespace.
		var lines []string
		for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 && strings.EqualFold(filepath.Clean(lines[0]), filepath.Join(dir, "python.exe")) {
			ok("python resolves to container-bin first")
		} else {
			warn("python does not resolve to container-bin first: %s", strings.TrimSpace(string(out)))
		}
		// Scan every result: the alias warning matters most when the
		// WindowsApps stub is the FIRST match (i.e. it wins over the shim).
		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), `microsoft\windowsapps\python`) {
				warn("Windows App Execution Alias for python is present on PATH; disable it in Windows Settings > Apps > App execution aliases")
				break
			}
		}
	}

	managed, err := dockervol.LabeledManaged()
	if err != nil {
		warn("managed volume inspection failed: %v", err)
	} else {
		ok("managed labeled volumes inspectable: %d", len(managed))
	}

	fmt.Printf("\nDoctor summary: %d failure(s), %d warning(s)\n", failures, warnings)
	if failures > 0 {
		return fmt.Errorf("%d critical check(s) failed", failures)
	}
	return nil
}

// captureStdout redirects the process's real os.Stdout to a temp file for
// the duration of fn, then restores it and returns everything fn wrote.
// Safe only because cb calls this serially with no concurrent os.Stdout
// writers — same precondition RM-5d's --json redirect documents at its own
// call site, restated here because this is a different function.
//
// fn's returned error is intentionally discarded: this helper's purpose is to
// capture output, and its own error return is reserved for I/O problems
// creating, closing or reading the temp file.
func captureStdout(fn func() error) (string, error) {
	f, err := os.CreateTemp("", "cb-bugreport-")
	if err != nil {
		return "", err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	real := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = real }()

	_ = fn()

	if cerr := f.Close(); cerr != nil {
		return "", cerr
	}
	data, rerr := os.ReadFile(tmpPath)
	if rerr != nil {
		return "", rerr
	}
	return string(data), nil
}

var (
	kvSecretPattern    = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api_key|apikey|access_key|accesskey|auth|credential|private_key|privatekey|client_secret)\b\s*[:=]\s*\S+`)
	awsKeyPattern      = regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`)
	githubTokenPattern = regexp.MustCompile(`\bgh[pours]_[A-Za-z0-9]{36,}\b`)
	bearerTokenPattern = regexp.MustCompile(`(?i)\b(Bearer)\s+[A-Za-z0-9._~+/=-]+\b`)
)

// redactSecrets applies a small, fixed set of best-effort redactions to text
// that is expected to be safe-by-construction (curated diagnostic output).
// It is not a general secret scanner and must not be documented as one.
// Pattern order is intentional and documented in the handoff: KV-style
// assignments, AWS access key IDs, GitHub tokens, then Bearer tokens.
func redactSecrets(text string) string {
	text = kvSecretPattern.ReplaceAllString(text, "$1=«redacted»")
	text = awsKeyPattern.ReplaceAllString(text, "«redacted»")
	text = githubTokenPattern.ReplaceAllString(text, "«redacted»")
	// $1 preserves the matched text's own casing ("Bearer"/"bearer"/"BEARER")
	// instead of normalizing it — the pattern is case-insensitive so it
	// catches all of them, but the replacement shouldn't silently rewrite
	// the source text's casing for a keyword that isn't itself the secret.
	text = bearerTokenPattern.ReplaceAllString(text, "$1 «redacted»")
	return text
}

// bugreportCommand assembles a paste-ready diagnostic block. It returns nil
// when the report is successfully assembled and printed, even if doctor()
// found failures — that signal is in the captured text itself. Only genuine
// capture or assembly errors are returned as bugreportCommand's own error.
func bugreportCommand(reg registry.Registry, cfgPath string) error {
	var b strings.Builder
	b.WriteString("container-bin bugreport\n")
	b.WriteString(fmt.Sprintf("generated: %s\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("cb version: %s\n", version))

	b.WriteString("\nHost:\n")
	if runtime.GOOS == "windows" {
		raw, err := windowsHostVersionInfo()
		if err != nil {
			b.WriteString("  windows:    could not determine\n")
			b.WriteString("  powershell: could not determine\n")
		} else {
			winVer, psVer := parseWindowsHostVersionInfo(raw)
			if winVer == "" {
				b.WriteString("  windows:    could not determine\n")
			} else {
				b.WriteString(fmt.Sprintf("  windows:    %s\n", winVer))
			}
			if psVer == "" {
				b.WriteString("  powershell: could not determine\n")
			} else {
				b.WriteString(fmt.Sprintf("  powershell: %s\n", psVer))
			}
		}
	} else {
		b.WriteString("  platform: not running on Windows\n")
	}

	registryText, err := captureStdout(func() error { registry.ListTools(reg, cfgPath); return nil })
	if err != nil {
		return fmt.Errorf("capture registry: %w", err)
	}
	b.WriteString("\nRegistry:\n")
	b.WriteString(registryText)

	doctorText, err := captureStdout(func() error { return doctor(reg, cfgPath) })
	if err != nil {
		return fmt.Errorf("capture doctor: %w", err)
	}
	b.WriteString("\nDoctor:\n")
	b.WriteString(doctorText)

	fmt.Print(redactSecrets(b.String()))
	fmt.Println("\nredaction is best-effort; review this report before posting it publicly")
	return nil
}

func setupCommand(cfgPath string) error {
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
	return doctor(reg, cfgPath)
}

func traceTool(reg registry.Registry, args []string) error {
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
	root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
	if !found {
		root = cwd
	}
	raw := append([]string(nil), args[1:]...)
	normalized := pathmap.NormalizeToolArgs(t, raw)
	workspaceRoot := pathmap.WorkspaceRootFor(t, root)
	mapped, mounts, err := pathmap.MapToolArgs(t, root, cwd, workspaceRoot, raw)
	if err != nil {
		return err
	}
	fmt.Printf("tool:       %s\n", t.Name)
	fmt.Printf("image:      %s\n", t.Image)
	fmt.Printf("provider:   %s\n", t.Provider)
	fmt.Printf("cwd:        %s\n", cwd)
	fmt.Printf("root:       %s\n", root)
	fmt.Printf("workspace:  %s\n", workspaceRoot)
	fmt.Printf("raw:        %#v\n", raw)
	fmt.Printf("normalized: %#v\n", normalized)
	fmt.Printf("mapped:     %#v\n", mapped)
	if len(mounts) == 0 {
		fmt.Printf("mounts:     (none beyond %s)\n", workspaceRoot)
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
	return nil
}

func showEnv(reg registry.Registry) error {
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

const (
	exitUsage       = 2
	exitCbFailure   = 120
	exitInterrupted = 130
)

// osExit is an indirection so unit tests can observe the exit code. A test
// stub that returns instead of terminating will cause fatalf to continue to
// its caller, so stubs must either panic or otherwise halt the goroutine.
var osExit = os.Exit

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "container-bin: "+format+"\n", args...)
	osExit(exitCbFailure)
}

type stateVolume struct {
	Name    string
	Kind    string
	Owner   string
	Root    string
	Current bool
}

func currentProjectState(reg registry.Registry) (map[string]stateVolume, map[string]stateVolume, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return nil, nil, err
	}
	project := map[string]stateVolume{}
	shared := map[string]stateVolume{}

	for _, t := range reg.Tools {
		switch t.Provider {
		case "stateful":
			root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
			if !found {
				root = cwd
			}
			for _, spec := range t.ProjectVolumes {
				logical, _, err := registry.ParseVolumeBinding(spec)
				if err != nil {
					return nil, nil, err
				}
				name := pathmap.StatefulProjectVolumeID(t.StateGroup, logical, root, found)
				project[name] = stateVolume{Name: name, Kind: "project", Owner: t.StateGroup + "/" + logical, Root: root, Current: true}
			}
			for _, spec := range t.SharedVolumes {
				logical, _, err := registry.ParseVolumeBinding(spec)
				if err != nil {
					return nil, nil, err
				}
				name := pathmap.StatefulSharedVolumeID(t.StateGroup, logical)
				shared[name] = stateVolume{Name: name, Kind: "shared", Owner: t.StateGroup + "/" + logical}
			}
		case "python":
			root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
			if found {
				name := pathmap.PythonEnvID(root, true)
				project[name] = stateVolume{Name: name, Kind: "project", Owner: "python313/venv", Root: root, Current: true}
			} else {
				shared["cb-python-313-global"] = stateVolume{Name: "cb-python-313-global", Kind: "compat", Owner: "python313/global"}
			}
			shared["cb-pip-cache"] = stateVolume{Name: "cb-pip-cache", Kind: "shared", Owner: "python313/pip-cache"}
		}
	}
	return project, shared, nil
}

func showState(reg registry.Registry) error {
	actual, err := dockervol.Names()
	if err != nil {
		return err
	}
	project, shared, err := currentProjectState(reg)
	if err != nil {
		return err
	}
	fmt.Printf("%-9s  %-48s  %s\n", "STATUS", "VOLUME", "OWNER")
	fmt.Printf("%-9s  %-48s  %s\n", "---------", "------------------------------------------------", "-----")
	for _, name := range actual {
		if v, ok := project[name]; ok {
			fmt.Printf("%-9s  %-48s  %s\n", "CURRENT", name, v.Owner)
			continue
		}
		if v, ok := shared[name]; ok {
			fmt.Printf("%-9s  %-48s  %s\n", strings.ToUpper(v.Kind), name, v.Owner)
			continue
		}
		labels, _ := dockervol.Labels(name)
		if labels["cb.managed"] == "true" {
			owner := labels["cb.owner"]
			switch labels["cb.kind"] {
			case "shared":
				fmt.Printf("%-9s  %-48s  %s\n", "SHARED", name, owner)
				continue
			case "compat":
				fmt.Printf("%-9s  %-48s  %s\n", "COMPAT", name, owner)
				continue
			case "project":
				path := labels["cb.project_path"]
				if path != "" {
					if _, e := os.Stat(path); os.IsNotExist(e) {
						fmt.Printf("%-9s  %-48s  %s (%s)\n", "ORPHAN", name, owner, path)
						continue
					}
				}
				fmt.Printf("%-9s  %-48s  %s\n", "MANAGED", name, owner)
				continue
			}
		}
		fmt.Printf("%-9s  %-48s  %s\n", "OTHER", name, "legacy/unlabeled or other project state")
	}
	if len(actual) == 0 {
		fmt.Println("(no container-bin volumes found)")
	}
	fmt.Println("\nORPHAN detection applies only to v0.8+ labeled project volumes. Legacy/unlabeled volumes are never auto-deleted.")
	return nil
}

func gcState(reg registry.Registry, args []string) error {
	apply := false
	orphans := false
	filter := ""
	for _, a := range args {
		switch a {
		case "--apply":
			apply = true
		case "--dry-run":
		case "--orphans":
			orphans = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option %q", a)
			}
			if filter != "" {
				return errors.New("usage: cb gc [TOOL|STATE_GROUP] [--orphans] [--apply]")
			}
			filter = strings.ToLower(a)
		}
	}
	if orphans {
		vols, err := dockervol.LabeledManaged()
		if err != nil {
			return err
		}
		var candidates []dockervol.Managed
		for _, v := range vols {
			if v.Labels["cb.kind"] != "project" {
				continue
			}
			path := v.Labels["cb.project_path"]
			if path == "" {
				continue
			}
			if filter != "" && filter != strings.ToLower(v.Labels["cb.owner"]) && !strings.HasPrefix(strings.ToLower(v.Labels["cb.owner"]), filter+"/") {
				continue
			}
			if _, err := os.Stat(path); os.IsNotExist(err) {
				candidates = append(candidates, v)
			}
		}
		if len(candidates) == 0 {
			fmt.Println("No labeled orphan project volumes found.")
			return nil
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
		if !apply {
			fmt.Println("Dry run: labeled project volumes whose recorded host project path no longer exists:")
			for _, v := range candidates {
				fmt.Printf("  %-48s  %-24s  %s\n", v.Name, v.Labels["cb.owner"], v.Labels["cb.project_path"])
			}
			fmt.Println("\nRe-run with --orphans --apply to delete. Legacy/unlabeled volumes are never included.")
			return nil
		}
		for _, v := range candidates {
			if err := dockervol.Remove(v.Name); err != nil {
				return err
			}
		}
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return err
	}
	candidates := map[string]string{}
	for _, t := range reg.Tools {
		if filter != "" && filter != t.Name && filter != t.StateGroup && !(filter == "python" && t.Provider == "python") {
			continue
		}
		switch t.Provider {
		case "stateful":
			root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
			if !found {
				root = cwd
			}
			for _, spec := range t.ProjectVolumes {
				logical, _, e := registry.ParseVolumeBinding(spec)
				if e != nil {
					return e
				}
				candidates[pathmap.StatefulProjectVolumeID(t.StateGroup, logical, root, found)] = t.StateGroup + "/" + logical
			}
		case "python":
			root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
			if found {
				candidates[pathmap.PythonEnvID(root, true)] = "python313/venv"
			}
		}
	}
	if len(candidates) == 0 {
		return errors.New("no project-scoped state matches the current directory/filter")
	}
	exists, err := dockervol.ExistsSet()
	if err != nil {
		return err
	}
	var names []string
	for n := range candidates {
		if exists[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Println("No matching project volumes currently exist.")
		return nil
	}
	if !apply {
		fmt.Println("Dry run: the following CURRENT project volumes would be removed:")
		for _, n := range names {
			fmt.Printf("  %-48s  %s\n", n, candidates[n])
		}
		fmt.Println("\nShared caches/global state are never included. Re-run with --apply to delete these volumes.")
		return nil
	}
	for _, n := range names {
		if err := dockervol.Remove(n); err != nil {
			return err
		}
	}
	return nil
}

func discoverNPMGlobalBins(t registry.Tool) ([]string, error) {
	globalVol := ""
	for _, spec := range t.SharedVolumes {
		logical, _, err := registry.ParseVolumeBinding(spec)
		if err != nil {
			return nil, err
		}
		if logical == "npm-global" {
			globalVol = pathmap.StatefulSharedVolumeID(t.StateGroup, logical)
			break
		}
	}
	if globalVol == "" {
		return nil, errors.New("npm profile has no npm-global shared volume")
	}
	script := `if [ -d /cb/npm-global/bin ]; then for f in /cb/npm-global/bin/*; do [ -e "$f" ] || continue; basename "$f"; done; fi`
	mount, err := dockerrun.MountSpec("volume", globalVol, "/cb/npm-global")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("docker", "run", "--rm", "--mount", mount, t.Image, "sh", "-lc", script)
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

func exposeTool(reg registry.Registry, cfgPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cb expose npm [BINARY ...]")
	}
	if strings.ToLower(args[0]) != "npm" {
		return errors.New("v0.8 supports only: cb expose npm [BINARY ...]")
	}
	npm, ok := reg.Tools["npm"]
	if !ok {
		return errors.New("npm tool is not configured")
	}
	bins, err := discoverNPMGlobalBins(npm)
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
			fmt.Printf("skip %-16s already exists in registry\n", name)
			continue
		}
		section := fmt.Sprintf("\n# Exposed from npm global prefix by cb expose npm\n[tools.%s]\nimage = %s\nprovider = \"stateful\"\ncommand = [%s]\nstate_group = %s\nshared_volumes = %s\nenv_set = %s\nenv_prefixes = %s\nenv_names = %s\n", name, toml.Quote(npm.Image), toml.Quote("/cb/npm-global/bin/"+name), toml.Quote(npm.StateGroup), toml.Array(npm.SharedVolumes), toml.Array(npm.EnvSet), toml.Array(npm.EnvPrefixes), toml.Array(npm.EnvNames))
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

func inspectTool(reg registry.Registry, args []string) error {
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
	root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
	if !found {
		root = cwd
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
	fmt.Printf("cwd:        %s\nroot:       %s\nworkspace:  %s\n", cwd, root, pathmap.WorkspaceRootFor(t, root))
	if t.StateGroup != "" {
		fmt.Printf("state_group: %s\n", t.StateGroup)
	}
	for _, spec := range t.ProjectVolumes {
		logical, dst, e := registry.ParseVolumeBinding(spec)
		if e != nil {
			return e
		}
		fmt.Printf("project_volume: %s -> %s\n", pathmap.StatefulProjectVolumeID(t.StateGroup, logical, root, found), pathmap.StatefulWorkspaceDestination(dst, pathmap.WorkspaceRootFor(t, root)))
	}
	for _, spec := range t.SharedVolumes {
		logical, dst, e := registry.ParseVolumeBinding(spec)
		if e != nil {
			return e
		}
		fmt.Printf("shared_volume:  %s -> %s\n", pathmap.StatefulSharedVolumeID(t.StateGroup, logical), dst)
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

func unexposeTools(reg registry.Registry, cfgPath string, args []string) error {
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

func uninstallTools(reg registry.Registry, cfgPath string, args []string) error {
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

func backupCommand(cfgPath string, args []string) error {
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

func restoreCommand(cfgPath string, args []string) error {
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

type selfTestCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // "pass", "fail", or "skip"
	Message string `json:"message"`
}

type selfTestReport struct {
	SchemaVersion int             `json:"schema_version"`
	CBVersion     string          `json:"cb_version"`
	GeneratedAt   string          `json:"generated_at"` // RFC3339, UTC
	Checks        []selfTestCheck `json:"checks"`
	Environment   []selfTestCheck `json:"environment,omitempty"`
	Passed        int             `json:"passed"`
	Failed        int             `json:"failed"`
	Skipped       int             `json:"skipped"`
	OK            bool            `json:"ok"`
}

type toolSelfTestOutcome struct {
	ImageLocalErr   *string
	PersistWriteErr *string
	PersistReadErr  *string
	ExternalPathErr *string
	ModulesWriteErr *string
	ModulesReadErr  *string
	RelativePathErr *string
	ChdirErr        *string
}

type selfTestStep struct {
	id      string
	tool    string
	depID   string
	errFunc func(toolSelfTestOutcome) *string
}

// selfTestSteps must list every step's dependency (depID) before the step
// itself — buildSelfTestReport looks dependencies up in checksByID, which is
// populated in this slice's order, so a step whose depID has not been
// inserted yet would see a zero-value selfTestCheck instead of the real
// dependency's status. TestSelfTestStepsDependencyOrder pins this invariant.
var selfTestSteps = []selfTestStep{
	{id: "python-image-local", tool: "python", depID: "docker", errFunc: func(o toolSelfTestOutcome) *string { return o.ImageLocalErr }},
	{id: "python-persist-write", tool: "python", depID: "python-image-local", errFunc: func(o toolSelfTestOutcome) *string { return o.PersistWriteErr }},
	{id: "python-persist-read", tool: "python", depID: "python-persist-write", errFunc: func(o toolSelfTestOutcome) *string { return o.PersistReadErr }},
	{id: "python-external-path", tool: "python", depID: "python-image-local", errFunc: func(o toolSelfTestOutcome) *string { return o.ExternalPathErr }},
	{id: "node-image-local", tool: "node", depID: "docker", errFunc: func(o toolSelfTestOutcome) *string { return o.ImageLocalErr }},
	{id: "node-modules-write", tool: "node", depID: "node-image-local", errFunc: func(o toolSelfTestOutcome) *string { return o.ModulesWriteErr }},
	{id: "node-modules-read", tool: "node", depID: "node-modules-write", errFunc: func(o toolSelfTestOutcome) *string { return o.ModulesReadErr }},
	{id: "jq-image-local", tool: "jq", depID: "docker", errFunc: func(o toolSelfTestOutcome) *string { return o.ImageLocalErr }},
	{id: "jq-relative-path", tool: "jq", depID: "jq-image-local", errFunc: func(o toolSelfTestOutcome) *string { return o.RelativePathErr }},
	{id: "terraform-image-local", tool: "terraform", depID: "docker", errFunc: func(o toolSelfTestOutcome) *string { return o.ImageLocalErr }},
	{id: "terraform-chdir", tool: "terraform", depID: "terraform-image-local", errFunc: func(o toolSelfTestOutcome) *string { return o.ChdirErr }},
}

// buildSelfTestReport is the pure decision point: dockerCheck must already be
// fully populated by the caller (runSelfTestChecks always does), so this
// function has a single source of truth for docker's outcome instead of a
// second dockerAvailable flag that could disagree with it.
func buildSelfTestReport(cbVersion string, now time.Time, dockerCheck selfTestCheck, toolOutcomes map[string]toolSelfTestOutcome, environment []selfTestCheck) selfTestReport {
	checks := []selfTestCheck{dockerCheck}
	checksByID := map[string]selfTestCheck{dockerCheck.ID: dockerCheck}

	for _, step := range selfTestSteps {
		dep := checksByID[step.depID]
		check := selfTestCheck{ID: step.id}
		switch {
		case dockerCheck.Status != "pass":
			check.Status = "skip"
			check.Message = skipMessage(dockerCheck)
		case dep.Status != "pass":
			check.Status = "skip"
			check.Message = skipMessage(dep)
		default:
			outcome, ok := toolOutcomes[step.tool]
			if !ok {
				// A tool absent from the registry is a configuration gap, not
				// a benign skip: the previous (pre-JSON-report) self-test
				// failed outright the moment it reached a missing tool's
				// first check ("tool %s missing"). Reporting this as "fail"
				// instead of "skip" preserves that fail-closed guarantee —
				// self-test's own OK/exit-code cannot go green while one of
				// the tools it claims to verify was never actually tested.
				check.Status = "fail"
				check.Message = fmt.Sprintf("%s not registered in container-bin.toml", step.tool)
			} else if errPtr := step.errFunc(outcome); errPtr != nil {
				check.Status = "fail"
				check.Message = *errPtr
			} else {
				check.Status = "pass"
				check.Message = passMessageFor(step.id)
			}
		}
		checks = append(checks, check)
		checksByID[step.id] = check
	}

	report := selfTestReport{
		SchemaVersion: 1,
		CBVersion:     cbVersion,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		Checks:        checks,
		Environment:   environment,
	}
	for _, c := range checks {
		switch c.Status {
		case "pass":
			report.Passed++
		case "fail":
			report.Failed++
		case "skip":
			report.Skipped++
		}
	}
	report.OK = report.Failed == 0
	return report
}

func skipMessage(dep selfTestCheck) string {
	if dep.Status == "skip" && strings.HasPrefix(dep.Message, "skipped: ") {
		return dep.Message
	}
	if dep.Status == "fail" {
		if dep.ID == "docker" {
			return "skipped: docker unavailable"
		}
		return "skipped: " + dep.ID + " failed"
	}
	if dep.Status == "skip" {
		return "skipped: " + dep.ID + " skipped"
	}
	return "skipped: " + dep.ID + " " + dep.Status
}

func passMessageFor(id string) string {
	if strings.HasSuffix(id, "-image-local") {
		return "image present"
	}
	return "ok"
}

func parseSelfTestArgs(args []string) (jsonOut, release bool, err error) {
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--release":
			release = true
		default:
			return false, false, fmt.Errorf("usage: cb self-test [--json] [--release]")
		}
	}
	return jsonOut, release, nil
}

func dockerEngineVersionCheck(dockerCheck selfTestCheck) selfTestCheck {
	if dockerCheck.Status != "pass" {
		msg := dockerCheck.Message
		if strings.HasPrefix(msg, "skipped: ") {
			msg = strings.TrimPrefix(msg, "skipped: ")
		}
		return selfTestCheck{ID: "docker-engine-version", Status: "skip", Message: "skipped: " + msg}
	}
	return selfTestCheck{ID: "docker-engine-version", Status: "pass", Message: dockerCheck.Message}
}

func windowsHostVersionInfo() (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`"windows=$([System.Environment]::OSVersion.VersionString)"; "powershell=$($PSVersionTable.PSVersion.ToString())"`)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseWindowsHostVersionInfo(raw string) (windowsVersion, powershellVersion string) {
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "windows":
			windowsVersion = val
		case "powershell":
			powershellVersion = val
		}
	}
	return
}

func envCheckFromVerdict(id, status, message string) selfTestCheck {
	check := selfTestCheck{ID: id}
	switch status {
	case "ok":
		check.Status = "pass"
		check.Message = message
	case "warn":
		// A "warn" verdict is still mapped to this report's "skip" status (no
		// new status value — see the schema-stability note on
		// selfTestReport.Environment), but unlike a genuine "could not
		// determine" skip (always messaged "skipped: ..."), a warn can be a
		// real, actionable qualification finding — a UNC/mapped-drive shim
		// directory, a reparse-point cwd. The "warn: " prefix makes the two
		// distinguishable by a CI consumer without demanding a new enum
		// value in an already-shipped, versioned schema.
		check.Status = "skip"
		check.Message = "warn: " + message
	case "fail":
		check.Status = "fail"
		check.Message = message
	}
	return check
}

func networkStorageCheck(id, subject, path string) selfTestCheck {
	driveType := ""
	if path != "" && !strings.HasPrefix(path, `\\`) {
		if dt, err := windowsDriveType(filepath.VolumeName(path)); err == nil {
			driveType = dt
		}
	}
	status, msg := networkStorageVerdict(subject, path, driveType)
	return envCheckFromVerdict(id, status, msg)
}

func dockerOSTypeCheck(dockerCheck selfTestCheck) selfTestCheck {
	// Matches doctor()'s and dockerEngineVersionCheck's own precedent: don't
	// shell out to `docker info` when `docker version` already failed — the
	// call would just fail (or hang) a second time for the same reason.
	if dockerCheck.Status != "pass" {
		return selfTestCheck{ID: "docker-os-type", Status: "skip", Message: "skipped: docker unavailable"}
	}
	out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").Output()
	if err != nil {
		return selfTestCheck{ID: "docker-os-type", Status: "skip", Message: "skipped: could not determine container mode"}
	}
	status, msg := dockerOSTypeVerdict(strings.TrimSpace(string(out)))
	return envCheckFromVerdict("docker-os-type", status, msg)
}

func buildEnvironmentChecks(dockerCheck selfTestCheck, cwd string) []selfTestCheck {
	var env []selfTestCheck

	if runtime.GOOS == "windows" {
		raw, err := windowsHostVersionInfo()
		if err != nil {
			env = append(env, selfTestCheck{ID: "windows-version", Status: "skip", Message: "skipped: could not determine"})
			env = append(env, selfTestCheck{ID: "powershell-version", Status: "skip", Message: "skipped: could not determine"})
		} else {
			winVer, psVer := parseWindowsHostVersionInfo(raw)
			if winVer == "" {
				env = append(env, selfTestCheck{ID: "windows-version", Status: "skip", Message: "skipped: could not determine"})
			} else {
				env = append(env, selfTestCheck{ID: "windows-version", Status: "pass", Message: winVer})
			}
			if psVer == "" {
				env = append(env, selfTestCheck{ID: "powershell-version", Status: "skip", Message: "skipped: could not determine"})
			} else {
				env = append(env, selfTestCheck{ID: "powershell-version", Status: "pass", Message: psVer})
			}
		}
	} else {
		env = append(env, selfTestCheck{ID: "windows-version", Status: "skip", Message: "skipped: not running on Windows"})
		env = append(env, selfTestCheck{ID: "powershell-version", Status: "skip", Message: "skipped: not running on Windows"})
	}

	env = append(env, dockerEngineVersionCheck(dockerCheck))

	env = append(env, dockerOSTypeCheck(dockerCheck))

	absCwd, cwdErr := filepath.Abs(cwd)
	if cwdErr != nil {
		env = append(env, selfTestCheck{ID: "cwd-reparse-point", Status: "skip", Message: "skipped: could not determine current directory"})
	} else if resolved, evalErr := filepath.EvalSymlinks(absCwd); evalErr != nil {
		env = append(env, selfTestCheck{ID: "cwd-reparse-point", Status: "skip", Message: "skipped: could not determine whether current directory sits behind a reparse point"})
	} else {
		status, msg := reparsePointVerdict("current directory", absCwd, resolved)
		env = append(env, envCheckFromVerdict("cwd-reparse-point", status, msg))
	}

	if runtime.GOOS == "windows" {
		exe, exeErr := os.Executable()
		if exeErr == nil {
			exe, exeErr = filepath.Abs(exe)
		}
		if exeErr != nil {
			// Unlike doctor() (which never changes directory), self-test has
			// already chdir'd into its temp scratch project by this point —
			// silently discarding this error the way doctor() does would let
			// filepath.Dir("") resolve against that temp directory instead of
			// the real shim directory, reporting a misleading "ok" for the
			// wrong path rather than a harmless no-op.
			env = append(env, selfTestCheck{ID: "shim-dir-network-storage", Status: "skip", Message: "skipped: could not determine shim directory"})
		} else {
			shimDir := filepath.Dir(exe)
			env = append(env, networkStorageCheck("shim-dir-network-storage", "shim directory", shimDir))
		}
		if cwdErr != nil {
			env = append(env, selfTestCheck{ID: "cwd-network-storage", Status: "skip", Message: "skipped: could not determine current directory"})
		} else {
			env = append(env, networkStorageCheck("cwd-network-storage", "current directory", absCwd))
		}
	} else {
		env = append(env, selfTestCheck{ID: "shim-dir-network-storage", Status: "skip", Message: "skipped: not running on Windows"})
		env = append(env, selfTestCheck{ID: "cwd-network-storage", Status: "skip", Message: "skipped: not running on Windows"})
	}

	return env
}

func selfTestCommand(reg registry.Registry, jsonOut, release bool) error {
	tmp, err := os.MkdirTemp("", "cb-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	old, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine current directory: %w", err)
	}
	defer os.Chdir(old)
	project := filepath.Join(tmp, "project")
	external := filepath.Join(tmp, "external")
	if err := os.MkdirAll(project, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(external, 0755); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(project, "package.json"), []byte(`{"name":"cb-selftest","version":"1.0.0"}`), 0644)
	os.WriteFile(filepath.Join(project, "pyproject.toml"), []byte("[project]\nname='cb-selftest'\nversion='0.0.0'\n"), 0644)
	os.WriteFile(filepath.Join(project, "data.json"), []byte(`{"ok":true}`), 0644)
	os.WriteFile(filepath.Join(external, "outside.py"), []byte("print('outside-ok')\n"), 0644)
	os.MkdirAll(filepath.Join(project, "tf"), 0755)
	os.WriteFile(filepath.Join(project, "tf", "main.tf"), []byte("terraform {}\n"), 0644)
	if err := os.Chdir(project); err != nil {
		return err
	}

	report, err := runSelfTestChecksAndCleanup(reg, project, external, jsonOut, release, old)
	if err != nil {
		return err
	}

	if jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	} else {
		for _, c := range report.Checks {
			fmt.Printf("%-9s%s: %s\n", strings.ToUpper(c.Status), c.ID, c.Message)
		}
		if release && len(report.Environment) > 0 {
			fmt.Println("\nEnvironment:")
			for _, c := range report.Environment {
				fmt.Printf("%-9s%s: %s\n", strings.ToUpper(c.Status), c.ID, c.Message)
			}
		}
		hasLocal := false
		for _, c := range report.Checks {
			if strings.HasSuffix(c.ID, "-image-local") && c.Status == "pass" {
				hasLocal = true
				break
			}
		}
		if hasLocal {
			fmt.Printf("\nSelf-test: %d passed, %d failed, %d skipped (temporary project state cleaned)\n", report.Passed, report.Failed, report.Skipped)
		} else {
			fmt.Printf("\nSelf-test: %d passed, %d failed, %d skipped\n", report.Passed, report.Failed, report.Skipped)
		}
	}
	if report.Failed > 0 {
		return fmt.Errorf("%d check(s) failed", report.Failed)
	}
	return nil
}

func runSelfTestChecksAndCleanup(reg registry.Registry, project, external string, jsonOut, release bool, cwd string) (selfTestReport, error) {
	// Redirect process-level stdout/stderr around the check-and-cleanup phase so
	// that tools whose containers write to stdout (jq, terraform) and the
	// docker volume rm cleanup output cannot corrupt a --json report. cb runs
	// self-test serially from main(), so this process-global mutation is safe.
	if jsonOut {
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return selfTestReport{}, err
		}
		realStdout, realStderr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = devNull, devNull
		// Register the restore first so it runs last; any cleanup defers
		// registered after it still execute while stdout/stderr are redirected.
		defer func() {
			os.Stdout, os.Stderr = realStdout, realStderr
			devNull.Close()
		}()
	}

	root, _ := pathmap.CanonicalPath(project)
	defer dockervol.RemoveQuiet(pathmap.PythonEnvID(root, true))
	defer dockervol.RemoveQuiet(pathmap.StatefulProjectVolumeID("node24", "node-modules", root, true))

	return runSelfTestChecks(reg, project, external, release, cwd)
}

func runSelfTestChecks(reg registry.Registry, project, external string, release bool, cwd string) (selfTestReport, error) {
	dockerCheck := selfTestCheck{ID: "docker"}
	dockerAvailable := false
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		dockerCheck.Status = "fail"
		dockerCheck.Message = "docker unavailable"
	} else {
		dockerAvailable = true
		dockerCheck.Status = "pass"
		dockerCheck.Message = "docker " + strings.TrimSpace(string(out))
	}

	toolOutcomes := map[string]toolSelfTestOutcome{}
	if dockerAvailable {
		for _, name := range []string{"python", "node", "jq", "terraform"} {
			if t, ok := reg.Tools[name]; ok {
				toolOutcomes[name] = runSelfTestTool(t, name, project, external)
			}
		}
	}

	var env []selfTestCheck
	if release {
		env = buildEnvironmentChecks(dockerCheck, cwd)
	}

	return buildSelfTestReport(version, time.Now(), dockerCheck, toolOutcomes, env), nil
}

func runSelfTestTool(t registry.Tool, name, project, external string) toolSelfTestOutcome {
	o := toolSelfTestOutcome{}
	if err := dockerrun.EnsureImageLocalForTool(t); err != nil {
		s := err.Error()
		o.ImageLocalErr = &s
		return o
	}
	switch name {
	case "python":
		code, err := dockerrun.RunTool(t, []string{"-c", "open('/venv/.cb-selftest','w').write('ok')"})
		if err != nil || code != 0 {
			s := selfTestRunError(name, err, code)
			o.PersistWriteErr = &s
		} else {
			code, err = dockerrun.RunTool(t, []string{"-c", "assert open('/venv/.cb-selftest').read()=='ok'"})
			if err != nil || code != 0 {
				s := selfTestRunError(name, err, code)
				o.PersistReadErr = &s
			}
		}
		code, err = dockerrun.RunTool(t, []string{filepath.Join(external, "outside.py")})
		if err != nil || code != 0 {
			s := selfTestRunError(name, err, code)
			o.ExternalPathErr = &s
		}
	case "node":
		code, err := dockerrun.RunTool(t, []string{"-e", "require('fs').mkdirSync('node_modules',{recursive:true}); require('fs').writeFileSync('node_modules/.cb-selftest','ok')"})
		if err != nil || code != 0 {
			s := selfTestRunError(name, err, code)
			o.ModulesWriteErr = &s
		} else {
			code, err = dockerrun.RunTool(t, []string{"-e", "if(require('fs').readFileSync('node_modules/.cb-selftest','utf8')!=='ok')process.exit(9)"})
			if err != nil || code != 0 {
				s := selfTestRunError(name, err, code)
				o.ModulesReadErr = &s
			}
		}
	case "jq":
		code, err := dockerrun.RunTool(t, []string{".", `.\data.json`})
		if err != nil || code != 0 {
			s := selfTestRunError(name, err, code)
			o.RelativePathErr = &s
		}
	case "terraform":
		code, err := dockerrun.RunTool(t, []string{`-chdir=.\tf`, "validate"})
		if err != nil || code != 0 {
			s := selfTestRunError(name, err, code)
			o.ChdirErr = &s
		}
	}
	return o
}

func selfTestRunError(name string, err error, code int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%s exited %d", name, code)
}

// withMutationLock is not re-entrant; call sites must not nest another
// withMutationLock-wrapped operation while holding the lock.
func withMutationLock(cfgPath string, fn func() error) error {
	release, err := mutationlock.Acquire(cfgPath, mutationlock.Wait)
	if err != nil {
		return err
	}
	defer release()

	c := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(c, os.Interrupt)
	go func() {
		select {
		case <-c:
			release()
			osExit(exitInterrupted)
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(c)
		close(done)
	}()

	return fn()
}

func lockCommand(reg registry.Registry, cfgPath string, args []string) error {
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

func updateCommand(reg registry.Registry, cfgPath string, args []string) error {
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
