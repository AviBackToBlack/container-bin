// Package diag implements container-bin's diagnostics: cb doctor, cb self-test
// and cb bugreport, together with the pure verdict functions that turn raw
// probe output into a status plus a human-readable message.
package diag

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AviBackToBlack/container-bin/internal/dockerrun"
	"github.com/AviBackToBlack/container-bin/internal/dockervol"
	"github.com/AviBackToBlack/container-bin/internal/lockfile"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

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

// hostMountVerdict reports whether a declared host_mounts source resolves to
// a usable path right now. A missing source is a per-machine/per-user state
// fact (the directory may simply not exist yet on this machine), not a
// container-bin defect, so this warns rather than fails -- the same
// warn-not-fail precedent as dockerOSTypeVerdict.
func hostMountVerdict(toolName, source, target, canonicalPath string, exists bool) (status, message string) {
	if !exists {
		return "warn", fmt.Sprintf("%s: host_mounts source %q for %s does not exist: %s", toolName, source, target, canonicalPath)
	}
	return "ok", fmt.Sprintf("%s: host_mounts source %q for %s exists: %s", toolName, source, target, canonicalPath)
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

func Doctor(reg registry.Registry, cfgPath string) error {
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

	// Sorted, not map-order: reg.Tools iteration order is otherwise
	// nondeterministic, and cb bugreport embeds this output verbatim, which
	// would make diffing two reports noisier than necessary for no benefit.
	hostMountToolNames := make([]string, 0, len(reg.Tools))
	for name, t := range reg.Tools {
		if len(t.HostMounts) > 0 {
			hostMountToolNames = append(hostMountToolNames, name)
		}
	}
	sort.Strings(hostMountToolNames)
	for _, name := range hostMountToolNames {
		t := reg.Tools[name]
		for _, spec := range t.HostMounts {
			source, target, _, err := registry.ParseHostMount(spec)
			if err != nil {
				warn("%s: host_mounts entry %q is invalid: %v", name, spec, err)
				continue
			}
			expanded, err := dockerrun.ExpandHostMountSource(source)
			if err != nil {
				warn("%s: host_mounts source %q could not be resolved: %v", name, source, err)
				continue
			}
			canon, err := pathmap.CanonicalPath(expanded)
			if err != nil {
				warn("%s: host_mounts source %q could not be canonicalized: %v", name, source, err)
				continue
			}
			_, statErr := os.Stat(canon)
			status, msg := hostMountVerdict(name, source, target, canon, statErr == nil)
			if status == "ok" {
				ok("%s", msg)
			} else {
				warn("%s", msg)
			}
			if runtime.GOOS == "windows" {
				driveType := ""
				if !strings.HasPrefix(canon, `\\`) {
					if dt, err := windowsDriveType(filepath.VolumeName(canon)); err == nil {
						driveType = dt
					}
				}
				nsStatus, nsMsg := networkStorageVerdict("host_mounts source for "+target, canon, driveType)
				if nsStatus != "ok" {
					warn("%s", nsMsg)
				}
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

// Bugreport assembles a paste-ready diagnostic block. It returns nil
// when the report is successfully assembled and printed, even if Doctor()
// found failures — that signal is in the captured text itself. Only genuine
// capture or assembly errors are returned as Bugreport's own error.
func Bugreport(reg registry.Registry, cfgPath, version string) error {
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

	doctorText, err := captureStdout(func() error { return Doctor(reg, cfgPath) })
	if err != nil {
		return fmt.Errorf("capture doctor: %w", err)
	}
	b.WriteString("\nDoctor:\n")
	b.WriteString(doctorText)

	fmt.Print(redactSecrets(b.String()))
	fmt.Println("\nredaction is best-effort; review this report before posting it publicly")
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
// Node 22 self-test steps are deliberately deferred to a separate scoping pass.
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

func ParseSelfTestArgs(args []string) (jsonOut, release bool, err error) {
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
	// Matches Doctor()'s and dockerEngineVersionCheck's own precedent: don't
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
			// Unlike Doctor() (which never changes directory), self-test has
			// already chdir'd into its temp scratch project by this point —
			// silently discarding this error the way Doctor() does would let
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

func SelfTest(reg registry.Registry, jsonOut, release bool, version string) error {
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

	report, err := runSelfTestChecksAndCleanup(reg, project, external, jsonOut, release, old, version)
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

func runSelfTestChecksAndCleanup(reg registry.Registry, project, external string, jsonOut, release bool, cwd, version string) (selfTestReport, error) {
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

	return runSelfTestChecks(reg, project, external, release, cwd, version)
}

func runSelfTestChecks(reg registry.Registry, project, external string, release bool, cwd, version string) (selfTestReport, error) {
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
