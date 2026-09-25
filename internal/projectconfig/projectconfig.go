// Package projectconfig discovers, validates and explicitly trusts add-only
// project registry overlays. Trust is user-owned state stored outside the
// project and bound to both the canonical project root and exact overlay bytes.
package projectconfig

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/atomicio"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/policy"
	"github.com/AviBackToBlack/container-bin/internal/registry"
	"github.com/AviBackToBlack/container-bin/internal/toml"
)

const (
	Filename          = ".container-bin.toml"
	TrustSchema       = 1
	maxConfigFileSize = 1 << 20
)

type Status string

const (
	Absent    Status = "absent"
	Trusted   Status = "trusted"
	Untrusted Status = "untrusted"
	Changed   Status = "changed"
	Invalid   Status = "invalid"
)

type Location struct {
	Root string
	Path string
}

type Overlay struct {
	Location
	Digest   string
	Registry registry.Registry
}

type trustEntry struct {
	Root   string
	Digest string
	Tools  []string
}

type trustStore struct {
	Entries map[string]trustEntry
}

type Context struct {
	Status    Status
	Location  Location
	Overlay   Overlay
	Effective registry.Registry
	Err       error
}

func DefaultTrustPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(dir, "ContainerBin", "project-trust.toml"), nil
}

// Find searches from the canonical working directory toward the filesystem
// root. A project overlay file itself must be a regular file, never a symlink.
func Find(start string) (Location, bool, error) {
	return find(start, filepath.EvalSymlinks)
}

func find(start string, resolve func(string) (string, error)) (Location, bool, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return Location{}, false, fmt.Errorf("make project search path absolute %q: %w", start, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return Location{}, false, fmt.Errorf("inspect project search path %q: %w", abs, err)
	}
	if !info.IsDir() {
		return Location{}, false, fmt.Errorf("project search path %q is not a directory", abs)
	}
	_, literalFound, err := findFromRoot(abs)
	if err != nil {
		return Location{}, false, err
	}
	resolved, err := resolve(abs)
	if err != nil {
		if literalFound {
			return Location{}, false, fmt.Errorf("resolve project search path %q for overlay trust: %w", abs, err)
		}
		return Location{}, false, nil
	}
	root, err := pathmap.CanonicalPath(resolved)
	if err != nil {
		return Location{}, false, fmt.Errorf("canonicalize project search path %q: %w", resolved, err)
	}
	return findFromRoot(root)
}

func findFromRoot(root string) (Location, bool, error) {
	for dir := root; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, Filename)
		info, err := os.Lstat(candidate)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return Location{}, false, fmt.Errorf("project overlay %s must be a regular file, not a symlink or directory", candidate)
			}
			return Location{Root: dir, Path: candidate}, true, nil
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return Location{}, false, fmt.Errorf("inspect project overlay %s: %w", candidate, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return Location{}, false, nil
}

func Load(loc Location) (Overlay, error) {
	b, err := readBounded(loc.Path)
	if err != nil {
		return Overlay{}, err
	}
	reg, err := registry.ParseTOML(string(b))
	if err != nil {
		return Overlay{}, fmt.Errorf("parse project overlay %s: %w", loc.Path, err)
	}
	if err := validateOverlay(reg); err != nil {
		return Overlay{}, fmt.Errorf("project overlay %s: %w", loc.Path, err)
	}
	sum := sha256.Sum256(b)
	return Overlay{Location: loc, Digest: hex.EncodeToString(sum[:]), Registry: reg}, nil
}

func validateOverlay(reg registry.Registry) error {
	if len(reg.Defaults) != 0 {
		return errors.New("[defaults.FAMILY] sections are not allowed; project overlays are add-only")
	}
	for name, tool := range reg.Tools {
		if tool.DefaultFamily != "" || tool.DefaultVersion != "" || tool.DefaultAlias != "" {
			return fmt.Errorf("tool %q: default alias metadata is not allowed in a project overlay", name)
		}
		if tool.Provider == "python" {
			return fmt.Errorf("tool %q: provider %q is not allowed; it implicitly uses the shared cross-project pip cache", name, tool.Provider)
		}
		if len(tool.HostMounts) != 0 {
			return fmt.Errorf("tool %q: host_mounts are not allowed in the initial project overlay capability set", name)
		}
		if len(tool.EnvPrefixes) != 0 {
			return fmt.Errorf("tool %q: env_prefixes are not allowed; list exact env_names instead", name)
		}
		if len(tool.SharedVolumes) != 0 {
			return fmt.Errorf("tool %q: shared_volumes are not allowed; project state must use project_volumes", name)
		}
	}
	return nil
}

// Merge applies the overlay as an add-only layer. A project tool cannot shadow
// either a concrete global profile or a virtual default alias.
func Merge(global registry.Registry, overlay Overlay) (registry.Registry, error) {
	merged := registry.Registry{
		SchemaVersion: global.SchemaVersion,
		Tools:         make(map[string]registry.Tool, len(global.Tools)+len(overlay.Registry.Tools)),
		Defaults:      make(map[string]string, len(global.Defaults)),
	}
	for name, tool := range global.Tools {
		merged.Tools[name] = tool
	}
	for family, version := range global.Defaults {
		merged.Defaults[family] = version
	}
	for name, tool := range overlay.Registry.Tools {
		if _, resolved, ok := global.Resolve(name); ok {
			return registry.Registry{}, fmt.Errorf("project tool %q collides with global tool or alias %q", name, resolved)
		}
		// The approved overlay root is the maximum host filesystem boundary for
		// every tool it contributes. Runtime marker discovery must not widen it
		// to an ancestor repository or make sibling overlays share state.
		tool.TrustedProjectRoot = overlay.Root
		merged.Tools[name] = tool
	}
	return merged, nil
}

func Inspect(global registry.Registry, start, trustPath string) Context {
	loc, found, err := Find(start)
	if err != nil {
		return Context{Status: Invalid, Err: err}
	}
	if !found {
		return Context{Status: Absent, Effective: global}
	}
	return inspectLocation(global, loc, trustPath)
}

// InspectDefault avoids adding a user-configuration-directory dependency to
// ordinary projects: the trust-store path is resolved only after an overlay is
// actually discovered.
func InspectDefault(global registry.Registry, start string) (Context, string) {
	loc, found, err := Find(start)
	if err != nil {
		return Context{Status: Invalid, Err: err}, ""
	}
	if !found {
		return Context{Status: Absent, Effective: global}, ""
	}
	trustPath, err := DefaultTrustPath()
	if err != nil {
		return Context{Status: Invalid, Location: loc, Err: err}, ""
	}
	return inspectLocation(global, loc, trustPath), trustPath
}

func inspectLocation(global registry.Registry, loc Location, trustPath string) Context {
	overlay, err := Load(loc)
	if err != nil {
		return Context{Status: Invalid, Location: loc, Err: err}
	}
	merged, err := Merge(global, overlay)
	if err != nil {
		return Context{Status: Invalid, Location: loc, Overlay: overlay, Err: err}
	}
	if err := validateExternalStore(loc.Root, trustPath); err != nil {
		return Context{Status: Invalid, Location: loc, Overlay: overlay, Effective: merged, Err: err}
	}
	store, err := loadStore(trustPath)
	if err != nil {
		return Context{Status: Invalid, Location: loc, Overlay: overlay, Err: fmt.Errorf("load project trust store: %w", err)}
	}
	entry, ok := store.Entries[rootID(loc.Root)]
	if !ok {
		return Context{Status: Untrusted, Location: loc, Overlay: overlay, Effective: merged}
	}
	if entry.Root != loc.Root || entry.Digest != overlay.Digest || !equalStrings(entry.Tools, overlay.Registry.ToolNames()) {
		return Context{Status: Changed, Location: loc, Overlay: overlay, Effective: merged}
	}
	return Context{Status: Trusted, Location: loc, Overlay: overlay, Effective: merged}
}

func (c Context) Registry(global registry.Registry) (registry.Registry, error) {
	switch c.Status {
	case Absent:
		return global, nil
	case Trusted:
		return c.Effective, nil
	case Untrusted:
		return registry.Registry{}, fmt.Errorf("project overlay %s is not trusted; review it with `cb trust --check`, then run `cb trust` interactively or `cb trust --yes` explicitly", strconv.Quote(c.Location.Path))
	case Changed:
		return registry.Registry{}, fmt.Errorf("project overlay %s changed since it was trusted; review the new digest with `cb trust --check` and trust it again explicitly", strconv.Quote(c.Location.Path))
	case Invalid:
		if c.Err == nil {
			return registry.Registry{}, errors.New("project overlay is invalid without a diagnostic")
		}
		return registry.Registry{}, fmt.Errorf("project overlay is invalid: %s", strconv.Quote(c.Err.Error()))
	default:
		return registry.Registry{}, errors.New("project overlay has an unknown trust status")
	}
}

func (c Context) Summary() string {
	switch c.Status {
	case Absent:
		return "absent"
	case Invalid:
		if c.Err == nil {
			return "invalid (no diagnostic)"
		}
		return fmt.Sprintf("invalid (%s)", strconv.Quote(c.Err.Error()))
	default:
		return fmt.Sprintf("%s root=%s overlay=%s sha256:%s", c.Status, strconv.Quote(c.Location.Root), strconv.Quote(c.Location.Path), c.Overlay.Digest)
	}
}

func (c Context) PrintReview(out io.Writer, machinePolicy policy.Policy) error {
	if c.Status == Absent {
		return errors.New("no .container-bin.toml found in the current directory or its parents")
	}
	if c.Status == Invalid {
		if c.Err == nil {
			return errors.New("project overlay is invalid without a diagnostic")
		}
		return fmt.Errorf("project overlay is invalid: %s", strconv.Quote(c.Err.Error()))
	}
	review := bufio.NewWriter(out)
	fmt.Fprintf(review, "project overlay: %s\n", strconv.Quote(c.Location.Path))
	fmt.Fprintf(review, "canonical root: %s\n", strconv.Quote(c.Location.Root))
	fmt.Fprintf(review, "digest:         sha256:%s\n", c.Overlay.Digest)
	fmt.Fprintf(review, "trust status:   %s\n", c.Status)
	fmt.Fprintf(review, "machine policy: %s\n", machinePolicy.Summary())
	fmt.Fprintln(review, "capabilities:   add-only tools; exact env_names and project_volumes allowed")
	fmt.Fprintln(review, "forbidden:      defaults, host_mounts, env_prefixes, shared_volumes")
	for _, name := range c.Overlay.Registry.ToolNames() {
		tool := c.Overlay.Registry.Tools[name]
		fmt.Fprintf(review, "\ntool %s\n", name)
		fmt.Fprintf(review, "  shim: %s\n", strconv.Quote(name+".exe"))
		fmt.Fprintf(review, "  image: %s\n", printableValue(tool.Image))
		fmt.Fprintf(review, "  provider: %s\n", printableValue(tool.Provider))
		fmt.Fprintf(review, "  role: %s\n", printableValue(tool.Role))
		fmt.Fprintf(review, "  command: %s\n", printableList(tool.Command))
		fmt.Fprintf(review, "  args_prefix: %s\n", printableList(tool.ArgsPrefix))
		fmt.Fprintf(review, "  path_next: %s\n", printableList(tool.PathNext))
		fmt.Fprintf(review, "  path_equals: %s\n", printableList(tool.PathEquals))
		fmt.Fprintf(review, "  path_last: %t\n", tool.PathLast)
		fmt.Fprintf(review, "  path_last_if_any: %s\n", printableList(tool.PathLastIfAny))
		fmt.Fprintf(review, "  env_names: %s\n", printableList(tool.EnvNames))
		fmt.Fprintf(review, "  env_set: %s\n", printableList(tool.EnvSet))
		fmt.Fprintf(review, "  project_markers: %s\n", printableList(tool.ProjectMarkers))
		fmt.Fprintf(review, "  project_root_mode: %s\n", printableValue(tool.ProjectRootMode))
		fmt.Fprintf(review, "  state_group: %s\n", printableValue(tool.StateGroup))
		fmt.Fprintf(review, "  project_volumes: %s\n", printableList(tool.ProjectVolumes))
		fmt.Fprintf(review, "  cwd_mode: %s\n", printableValue(tool.CwdMode))
	}
	if err := review.Flush(); err != nil {
		return fmt.Errorf("write project overlay review: %w", err)
	}
	return nil
}

// Trust reviews and records the current overlay. Non-interactive callers must
// pass --yes; --check is strictly read-only.
func Trust(c Context, storePath string, args []string, in io.Reader, out io.Writer, interactive bool, machinePolicy policy.Policy, installShims func([]string) error) error {
	check, yes, err := parseTrustArgs(args)
	if err != nil {
		return err
	}
	if err := c.PrintReview(out, machinePolicy); err != nil {
		return err
	}
	if check {
		return nil
	}
	if !yes {
		if !interactive {
			return errors.New("stdin is non-interactive; no trust was recorded (rerun with --yes only after reviewing `cb trust --check`)")
		}
		if _, err := fmt.Fprint(out, "\nType 'trust' to approve this exact root and digest: "); err != nil {
			return fmt.Errorf("write project trust prompt: %w", err)
		}
		scanner := bufio.NewScanner(io.LimitReader(in, 256))
		if !scanner.Scan() || scanner.Text() != "trust" {
			return errors.New("project overlay was not trusted")
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read trust confirmation: %w", err)
		}
	}
	// The review and interactive prompt are intentionally outside any project
	// file lock. Re-discover and re-read the overlay after approval so a change
	// made while the user was reviewing cannot be recorded as trusted.
	current, err := reloadReviewedOverlay(c)
	if err != nil {
		return err
	}
	c.Overlay = current
	if err := validateExternalStore(c.Location.Root, storePath); err != nil {
		return err
	}
	names := c.Overlay.Registry.ToolNames()
	store, err := loadStoreForMutation(storePath)
	if err != nil {
		return err
	}
	if err := installShims(names); err != nil {
		return fmt.Errorf("install project shims before recording trust: %w", err)
	}
	current, err = reloadReviewedOverlay(c)
	if err != nil {
		return fmt.Errorf("project shims were installed but remain inert: %w", err)
	}
	c.Overlay = current
	store.Entries[rootID(c.Location.Root)] = trustEntry{Root: c.Location.Root, Digest: c.Overlay.Digest, Tools: names}
	if err := saveStore(storePath, store); err != nil {
		return fmt.Errorf("record project trust after shims were installed (the shims remain inert): %w", err)
	}
	fmt.Fprintf(out, "trusted project overlay sha256:%s at %s\n", c.Overlay.Digest, strconv.Quote(c.Location.Root))
	return nil
}

func reloadReviewedOverlay(c Context) (Overlay, error) {
	loc, found, err := Find(c.Location.Root)
	if err != nil {
		return Overlay{}, fmt.Errorf("recheck project overlay after approval: %w", err)
	}
	if !found || loc != c.Location {
		return Overlay{}, errors.New("project overlay location changed during review; no trust was recorded")
	}
	current, err := Load(loc)
	if err != nil {
		return Overlay{}, fmt.Errorf("recheck project overlay after approval: %w", err)
	}
	if current.Digest != c.Overlay.Digest {
		return Overlay{}, errors.New("project overlay bytes changed during review; no trust was recorded (review the new digest and try again)")
	}
	return current, nil
}

func Untrust(start, storePath string, out io.Writer) error {
	loc, found, err := Find(start)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no .container-bin.toml found in the current directory or its parents")
	}
	return untrustLocation(loc, storePath, out)
}

func UntrustDefault(start string, out io.Writer) error {
	loc, found, err := Find(start)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no .container-bin.toml found in the current directory or its parents")
	}
	storePath, err := DefaultTrustPath()
	if err != nil {
		return err
	}
	return untrustLocation(loc, storePath, out)
}

func untrustLocation(loc Location, storePath string, out io.Writer) error {
	if err := validateExternalStore(loc.Root, storePath); err != nil {
		return err
	}
	store, err := loadStoreForMutation(storePath)
	if err != nil {
		return err
	}
	id := rootID(loc.Root)
	if _, ok := store.Entries[id]; !ok {
		return fmt.Errorf("project overlay at %s is not trusted", strconv.Quote(loc.Root))
	}
	delete(store.Entries, id)
	if err := saveStore(storePath, store); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed trust for project overlay at %s\n", strconv.Quote(loc.Root))
	fmt.Fprintln(out, "existing shim files are left inert; without a matching trusted overlay they fail closed")
	return nil
}

func parseTrustArgs(args []string) (check, yes bool, err error) {
	for _, arg := range args {
		switch arg {
		case "--check":
			if check {
				return false, false, errors.New("--check may be specified only once")
			}
			check = true
		case "--yes":
			if yes {
				return false, false, errors.New("--yes may be specified only once")
			}
			yes = true
		default:
			return false, false, fmt.Errorf("unknown trust option %q", arg)
		}
	}
	if check && yes {
		return false, false, errors.New("--check and --yes are mutually exclusive")
	}
	return check, yes, nil
}

func loadStore(path string) (trustStore, error) {
	store := trustStore{Entries: map[string]trustEntry{}}
	b, err := readBounded(path)
	if errors.Is(err, os.ErrNotExist) {
		backup := path + ".bak"
		b, err = readBounded(backup)
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		if err != nil {
			return trustStore{}, fmt.Errorf("%s is missing and its backup %s is unusable: %w", path, backup, err)
		}
		parsed, err := parseStore(b)
		if err != nil {
			return trustStore{}, fmt.Errorf("%s is missing and its backup %s is unusable: %w; remove the backup to fall back to no project trust, or repair it and retry", path, backup, err)
		}
		return parsed, nil
	}
	if err != nil {
		return trustStore{}, err
	}
	return parseStore(b)
}

func loadStoreForMutation(path string) (trustStore, error) {
	if _, err := atomicio.RecoverFromBackup(path, func(backup string) error {
		b, err := readBounded(backup)
		if err != nil {
			return err
		}
		_, err = parseStore(b)
		return err
	}); err != nil {
		return trustStore{}, err
	}
	return loadStore(path)
}

func saveStore(path string, store trustStore) error {
	if store.Entries == nil {
		store.Entries = map[string]trustEntry{}
	}
	data, err := marshalStore(store)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicio.WriteFile(path, data, 0600)
}

func parseStore(b []byte) (trustStore, error) {
	store := trustStore{Entries: map[string]trustEntry{}}
	seenVersion := false
	seenKeys := map[string]bool{}
	current := ""
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		line := strings.TrimSpace(toml.StripComment(raw))
		if line == "" {
			continue
		}
		section, header, err := toml.ParseSectionHeader(raw)
		if err != nil {
			return trustStore{}, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if header {
			if !strings.HasPrefix(section, "projects.") {
				return trustStore{}, fmt.Errorf("line %d: unsupported section %q", lineNo, section)
			}
			id := strings.TrimPrefix(section, "projects.")
			if !validDigest(id) {
				return trustStore{}, fmt.Errorf("line %d: project section ID must be a lowercase SHA-256 digest", lineNo)
			}
			if _, duplicate := store.Entries[id]; duplicate {
				return trustStore{}, fmt.Errorf("line %d: duplicate project section %q", lineNo, id)
			}
			store.Entries[id] = trustEntry{}
			current = id
			seenKeys = map[string]bool{}
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return trustStore{}, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if current == "" {
			if key != "trust_version" || seenVersion {
				return trustStore{}, fmt.Errorf("line %d: only one trust_version key is allowed before project sections", lineNo)
			}
			version, err := strconv.Atoi(value)
			if err != nil || version != TrustSchema {
				return trustStore{}, fmt.Errorf("line %d: unsupported trust_version %q (supported: %d)", lineNo, value, TrustSchema)
			}
			seenVersion = true
			continue
		}
		if seenKeys[key] {
			return trustStore{}, fmt.Errorf("line %d: duplicate key %q", lineNo, key)
		}
		seenKeys[key] = true
		entry := store.Entries[current]
		switch key {
		case "root":
			entry.Root, err = toml.ParseQuoted(value)
		case "overlay_sha256":
			entry.Digest, err = toml.ParseQuoted(value)
		case "tools":
			entry.Tools, err = toml.ParseStringArray(value)
		default:
			return trustStore{}, fmt.Errorf("line %d: unsupported project trust key %q", lineNo, key)
		}
		if err != nil {
			return trustStore{}, fmt.Errorf("line %d %s: %w", lineNo, key, err)
		}
		store.Entries[current] = entry
	}
	if err := scanner.Err(); err != nil {
		return trustStore{}, err
	}
	if !seenVersion {
		return trustStore{}, errors.New("trust_version is required")
	}
	for id, entry := range store.Entries {
		if err := validateEntry(id, entry); err != nil {
			return trustStore{}, err
		}
	}
	return store, nil
}

func marshalStore(store trustStore) ([]byte, error) {
	ids := make([]string, 0, len(store.Entries))
	for id, entry := range store.Entries {
		if err := validateEntry(id, entry); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "trust_version = %d\n", TrustSchema)
	for _, id := range ids {
		entry := store.Entries[id]
		fmt.Fprintf(&b, "\n[projects.%s]\n", id)
		fmt.Fprintf(&b, "root = %s\n", toml.Quote(entry.Root))
		fmt.Fprintf(&b, "overlay_sha256 = %s\n", toml.Quote(entry.Digest))
		fmt.Fprintf(&b, "tools = %s\n", toml.Array(entry.Tools))
	}
	return []byte(b.String()), nil
}

func validateEntry(id string, entry trustEntry) error {
	if !validDigest(id) || rootID(entry.Root) != id {
		return fmt.Errorf("project trust entry %q does not match its canonical root", id)
	}
	if !filepath.IsAbs(entry.Root) || filepath.Clean(entry.Root) != entry.Root {
		return fmt.Errorf("project trust entry %q root must be an absolute clean path", id)
	}
	if !validDigest(entry.Digest) {
		return fmt.Errorf("project trust entry %q has an invalid overlay SHA-256", id)
	}
	if len(entry.Tools) == 0 {
		return fmt.Errorf("project trust entry %q must name at least one tool", id)
	}
	if !sort.StringsAreSorted(entry.Tools) {
		return fmt.Errorf("project trust entry %q tools must be sorted", id)
	}
	seen := map[string]bool{}
	for _, name := range entry.Tools {
		if !registry.ValidToolName(name) || registry.ReservedToolName(name) || seen[name] {
			return fmt.Errorf("project trust entry %q contains invalid or duplicate tool %q", id, name)
		}
		seen[name] = true
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxConfigFileSize {
		return nil, fmt.Errorf("%s exceeds the %d-byte configuration limit", path, maxConfigFileSize)
	}
	return b, nil
}

func rootID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateExternalStore(root, storePath string) error {
	for _, candidate := range []string{storePath, storePath + ".bak"} {
		inside, err := within(root, candidate)
		if err != nil {
			return fmt.Errorf("validate external trust-store location %s: %w", strconv.Quote(candidate), err)
		}
		if inside {
			return fmt.Errorf("trust store path %s resolves inside project root %s; refusing a project-controlled trust record", strconv.Quote(candidate), strconv.Quote(root))
		}
	}
	return nil
}

func within(root, candidate string) (bool, error) {
	var err error
	root, err = canonicalForContainment(root)
	if err != nil {
		return false, err
	}
	candidate, err = canonicalForContainment(candidate)
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "windows" {
		root, candidate = strings.ToLower(root), strings.ToLower(candidate)
	}
	return relativeWithin(root, candidate), nil
}

func relativeWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalForContainment resolves the nearest existing ancestor so a
// symlink/junction parent cannot redirect an apparently external trust path
// back into the project.
func canonicalForContainment(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	current := abs
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", value)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	return filepath.Clean(resolved), nil
}

func printableList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func printableValue(value string) string {
	if value == "" {
		return "(none)"
	}
	return strconv.Quote(value)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
