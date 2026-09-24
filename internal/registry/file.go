package registry

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/atomicio"
	"github.com/AviBackToBlack/container-bin/internal/toml"
)

func Path() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "container-bin.toml"), nil
}

func EnsureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicio.WriteFile(path, []byte(DefaultTOML), 0644)
}

// AppendMissingDefaultTools upgrades an existing registry non-destructively.
// Existing tool sections are never rewritten; missing built-in sections are
// appended, preserving user profiles and comments (e.g. jq2 from earlier tests).
func AppendMissingDefaultTools(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		return err
	}
	if reg.SchemaVersion == 1 {
		return upgradeV1Registry(path, data, reg, version)
	}
	defaults := DefaultToolSections()
	var names []string
	for name := range defaults {
		if _, exists := reg.Tools[name]; !exists {
			names = append(names, name)
		}
	}
	familySections := DefaultFamilySections()
	var families []string
	for family := range familySections {
		if _, exists := reg.Defaults[family]; !exists {
			families = append(families, family)
		}
	}
	if len(names) == 0 && len(families) == 0 {
		return nil
	}
	sort.Strings(names)
	sort.Strings(families)
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("\n# Added by container-bin " + version + "\n")
	for _, family := range families {
		b.WriteString(familySections[family])
	}
	for _, name := range names {
		b.WriteString(defaults[name])
	}
	if _, err := ParseTOML(b.String()); err != nil {
		return fmt.Errorf("refusing registry upgrade: %w", err)
	}
	return atomicio.WriteFile(path, []byte(b.String()), 0644)
}

type v1DefaultMigration struct {
	oldName string
	newName string
	family  string
	version string
	alias   string
}

var v1NodeMigrations = []v1DefaultMigration{
	{oldName: "node", newName: "node24", family: "node", version: "24", alias: "node"},
	{oldName: "npm", newName: "npm24", family: "node", version: "24", alias: "npm"},
	{oldName: "npx", newName: "npx24", family: "node", version: "24", alias: "npx"},
	{oldName: "node22", newName: "node22", family: "node", version: "22", alias: "node"},
	{oldName: "npm22", newName: "npm22", family: "node", version: "22", alias: "npm"},
	{oldName: "npx22", newName: "npx22", family: "node", version: "22", alias: "npx"},
}

// upgradeV1Registry introduces versioned Node 24 profiles and explicit alias
// metadata in one atomic rewrite. Only semantically stock built-ins are
// migrated: a customized legacy profile may not actually represent Node 24,
// so assigning it a version label would violate the registry's fail-closed
// contract.
func upgradeV1Registry(path string, data []byte, reg Registry, cbVersion string) error {
	desired := Default()
	migrations := map[string]v1DefaultMigration{}
	existingNames := map[string]bool{}
	for name := range reg.Tools {
		existingNames[name] = true
	}
	for _, migration := range v1NodeMigrations {
		tool, exists := reg.Tools[migration.oldName]
		if !exists {
			continue
		}
		if migration.oldName != migration.newName {
			if _, collision := reg.Tools[migration.newName]; collision {
				return fmt.Errorf("cannot migrate [tools.%s]: [tools.%s] already exists; reconcile the profiles manually, then rerun cb install", migration.oldName, migration.newName)
			}
		}
		want := desired.Tools[migration.newName]
		tool.Name, want.Name = "", ""
		want.DefaultFamily, want.DefaultVersion, want.DefaultAlias = "", "", ""
		if !reflect.DeepEqual(tool, want) {
			return fmt.Errorf("cannot automatically version customized [tools.%s]; rename it to an explicit versioned profile and add default alias metadata manually, then rerun cb install", migration.oldName)
		}
		migrations[migration.oldName] = migration
		delete(existingNames, migration.oldName)
		existingNames[migration.newName] = true
	}

	newline := "\n"
	if strings.Contains(string(data), "\r\n") {
		newline = "\r\n"
	}
	var out strings.Builder
	foundSchema := false
	for lineNo, raw := range strings.SplitAfter(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\n")
		line = strings.TrimSuffix(line, "\r")
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "schema_version") {
			foundSchema = true
			out.WriteString(fmt.Sprintf("schema_version = %d%s", MaxSchemaVersion, newline))
			continue
		}
		section, isHeader, err := toml.ParseSectionHeader(line)
		if err != nil {
			return fmt.Errorf("refusing registry v1 to v2 upgrade at line %d: %w", lineNo+1, err)
		}
		if isHeader && strings.HasPrefix(section, "tools.") {
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(section, "tools.")))
			if migration, ok := migrations[name]; ok {
				out.WriteString("[tools." + migration.newName + "]")
				withoutComment := toml.StripComment(line)
				if len(withoutComment) < len(line) {
					out.WriteString(" " + strings.TrimSpace(line[len(withoutComment):]))
				}
				out.WriteString(newline)
				out.WriteString("default_family = \"" + migration.family + "\"" + newline)
				out.WriteString("default_version = \"" + migration.version + "\"" + newline)
				out.WriteString("default_alias = \"" + migration.alias + "\"" + newline)
				continue
			}
		}
		out.WriteString(raw)
	}
	if !foundSchema {
		body := out.String()
		out.Reset()
		out.WriteString(fmt.Sprintf("schema_version = %d%s", MaxSchemaVersion, newline))
		out.WriteString(body)
		if len(body) > 0 && !strings.HasSuffix(body, "\n") {
			out.WriteString(newline)
		}
	}
	if out.Len() > 0 {
		text := out.String()
		if !strings.HasSuffix(text, "\n") {
			out.WriteString(newline)
		}
	}
	out.WriteString(newline + "# Added by container-bin " + cbVersion + newline)
	familySections := DefaultFamilySections()
	var families []string
	for family := range familySections {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		out.WriteString(strings.ReplaceAll(familySections[family], "\n", newline))
	}
	sections := DefaultToolSections()
	var missing []string
	for name := range sections {
		if !existingNames[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		out.WriteString(strings.ReplaceAll(sections[name], "\n", newline))
	}
	if _, err := ParseTOML(out.String()); err != nil {
		return fmt.Errorf("refusing registry v1 to v2 upgrade: %w", err)
	}
	return atomicio.WriteFile(path, []byte(out.String()), 0644)
}

func Load() (Registry, string, error) {
	path, err := Path()
	if err != nil {
		return Registry{}, "", err
	}
	return loadPath(path, true)
}

// LoadReadOnly reads a valid backup in place when the primary registry is
// missing. Unlike Load, it never renames recovery state and is safe for
// commands whose contract forbids filesystem mutation.
func LoadReadOnly() (Registry, string, error) {
	path, err := Path()
	if err != nil {
		return Registry{}, "", err
	}
	return loadPath(path, false)
}

func loadPath(path string, recoverBackup bool) (Registry, string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if recoverBackup {
			recovered, recoverErr := atomicio.RecoverFromBackup(path, validateBackup)
			if recoverErr != nil {
				return Registry{}, path, recoverErr
			}
			if !recovered {
				return Default(), path, nil
			}
			data, err = os.ReadFile(path)
		} else {
			data, err = os.ReadFile(path + ".bak")
			if os.IsNotExist(err) {
				return Default(), path, nil
			}
		}
	}
	if err != nil {
		return Registry{}, path, err
	}
	reg, err := ParseTOML(string(data))
	return reg, path, err
}

func SetDefaultVersion(path, family, version string, validators ...func(Registry) error) error {
	family, version = strings.ToLower(family), strings.ToLower(version)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	reg, err := ParseTOML(string(data))
	if err != nil {
		return err
	}
	info, ok := reg.DefaultInfo(family)
	if !ok {
		return fmt.Errorf("default family %q not found", family)
	}
	available := false
	for _, candidate := range info.Versions {
		if candidate == version {
			available = true
			break
		}
	}
	if !available {
		return fmt.Errorf("version %q is not configured for default family %q (available: %s)", version, family, strings.Join(info.Versions, ", "))
	}
	if info.Selected == version {
		return nil
	}

	newline := "\n"
	if strings.Contains(string(data), "\r\n") {
		newline = "\r\n"
	}
	var out strings.Builder
	inFamily, replaced := false, false
	for lineNo, raw := range strings.SplitAfter(string(data), "\n") {
		line := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		trim := strings.TrimSpace(line)
		section, isHeader, err := toml.ParseSectionHeader(line)
		if err != nil {
			return fmt.Errorf("refusing default update at line %d: %w", lineNo+1, err)
		}
		if isHeader {
			inFamily = strings.EqualFold(section, "defaults."+family)
		}
		if inFamily && strings.HasPrefix(trim, "version") {
			key := strings.TrimSpace(strings.SplitN(trim, "=", 2)[0])
			if key == "version" {
				withoutComment := toml.StripComment(line)
				valueEnd := len(strings.TrimRight(withoutComment, " \t"))
				indentEnd := len(line) - len(strings.TrimLeft(line, " \t"))
				out.WriteString(line[:indentEnd] + "version = \"" + version + "\"" + line[valueEnd:] + newline)
				replaced = true
				continue
			}
		}
		out.WriteString(raw)
	}
	if !replaced {
		return fmt.Errorf("default family %q has no writable version key", family)
	}
	updated, err := ParseTOML(out.String())
	if err != nil {
		return fmt.Errorf("refusing default update: %w", err)
	}
	for _, validate := range validators {
		if validate != nil {
			if err := validate(updated); err != nil {
				return fmt.Errorf("refusing default update: %w", err)
			}
		}
	}
	return atomicio.WriteFile(path, []byte(out.String()), 0644)
}

func validateBackup(bak string) error {
	b, err := os.ReadFile(bak)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return errors.New("backup is empty")
	}
	// parseRegistryTOML already rejects a tool-less registry, so this needs no
	// separate check for one; keeping the rule in a single place stops the two
	// copies from drifting apart later.
	_, err = ParseTOML(string(b))
	return err
}

func RewriteWithoutTools(cfgPath string, remove map[string]bool) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if _, err := ParseTOML(string(data)); err != nil {
		return fmt.Errorf("refusing registry rewrite: %w", err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	keepProvenance, dropProvenance := exposedProvenanceLines(lines, remove)
	var out strings.Builder
	skip := false
	for lineNo, raw := range lines {
		if dropProvenance[lineNo] {
			continue
		}
		section, isHeader, err := toml.ParseSectionHeader(raw)
		if err != nil {
			return fmt.Errorf("refusing registry rewrite at line %d: %w", lineNo+1, err)
		}
		if isHeader {
			skip = false
			if strings.HasPrefix(section, "tools.") {
				name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(section, "tools.")))
				skip = remove[name]
			}
		}
		if !skip || keepProvenance[lineNo] {
			out.WriteString(raw)
		}
	}
	if _, err := ParseTOML(out.String()); err != nil {
		return fmt.Errorf("refusing registry rewrite: %w", err)
	}
	return atomicio.WriteFile(cfgPath, []byte(out.String()), 0644)
}

func toolHeaderName(raw string) (string, bool) {
	section, isHeader, err := toml.ParseSectionHeader(raw)
	if err != nil || !isHeader {
		return "", false
	}
	if !strings.HasPrefix(section, "tools.") {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(section, "tools."))), true
}

func exposedProvenanceLines(lines []string, remove map[string]bool) (keep, drop map[int]bool) {
	keep = map[int]bool{}
	drop = map[int]bool{}
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, "# Exposed from ") || !strings.Contains(trimmed, " by cb expose") {
			continue
		}
		header := i + 1
		for header < len(lines) && strings.TrimSpace(lines[header]) == "" {
			header++
		}
		if header >= len(lines) {
			continue
		}
		name, ok := toolHeaderName(lines[header])
		if !ok {
			continue
		}
		target := keep
		if remove[name] {
			target = drop
		}
		for line := i; line < header; line++ {
			target[line] = true
		}
	}
	return keep, drop
}

func InstallShims(reg Registry) error {
	if err := installShimNames(reg.ToolNames(), false); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	fmt.Printf("\nRegistry:\n  %s\n\nAdd this directory near the front of PATH:\n  %s\n", filepath.Join(dir, "container-bin.toml"), dir)
	return nil
}

// InstallAdditionalShimNames installs project-overlay shims without replacing
// an unrelated existing executable. Existing current ContainerBin hardlinks or
// byte-identical copy-fallback shims are safe to refresh.
func InstallAdditionalShimNames(names []string) error {
	return installShimNames(names, true)
}

func installShimNames(names []string, refuseUnrelated bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	names, err = normalizedShimNames(names)
	if err != nil {
		return err
	}
	if refuseUnrelated {
		for _, name := range names {
			dst := filepath.Join(dir, name+".exe")
			if err := verifyExistingManagedShim(exe, dst); err != nil {
				return err
			}
		}
	}
	for _, name := range names {
		if !ValidToolName(name) || ReservedToolName(name) {
			return fmt.Errorf("refusing to install invalid or reserved shim name %q", name)
		}
		dst := filepath.Join(dir, name+".exe")
		mode, err := installShim(exe, dst, os.Link, copyFile, os.Rename)
		if err != nil {
			return err
		}
		if mode == "hardlink" {
			fmt.Printf("installed %-10s (hardlink)      -> %s\n", name, dst)
		} else {
			fmt.Printf("installed %-10s (copy fallback) -> %s\n", name, dst)
		}
	}
	return nil
}

func normalizedShimNames(names []string) ([]string, error) {
	normalized := make([]string, len(names))
	for i, name := range names {
		normalized[i] = strings.ToLower(name)
	}
	sort.Strings(normalized)
	for i, name := range normalized {
		if !ValidToolName(name) || ReservedToolName(name) {
			return nil, fmt.Errorf("refusing to install invalid or reserved shim name %q", name)
		}
		if i > 0 && normalized[i-1] == name {
			return nil, fmt.Errorf("refusing duplicate shim name %q", name)
		}
	}
	return normalized, nil
}

func verifyExistingManagedShim(exe, shim string) error {
	shimInfo, err := os.Stat(shim)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing shim %s: %w", shim, err)
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return err
	}
	if os.SameFile(exeInfo, shimInfo) {
		return nil
	}
	exeDigest, err := fileSHA256(exe)
	if err != nil {
		return err
	}
	shimDigest, err := fileSHA256(shim)
	if err != nil {
		return err
	}
	if exeDigest != shimDigest {
		return fmt.Errorf("refusing to replace existing %s because it is not the current ContainerBin executable; verify and remove or rename that file before retrying", shim)
	}
	return nil
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return zero, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

type fileOperation func(string, string) error

func installShim(exe, dst string, linkFile, copyFallback, replaceFile fileOperation) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary shim for %s: %w", dst, err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close temporary shim for %s: %w", dst, err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return "", fmt.Errorf("prepare temporary shim for %s: %w", dst, err)
	}
	defer os.Remove(tmpPath)

	mode := "hardlink"
	linkErr := linkFile(exe, tmpPath)
	if linkErr != nil {
		mode = "copy"
		_ = os.Remove(tmpPath)
		if err := copyFallback(exe, tmpPath); err != nil {
			return "", fmt.Errorf("create %s: hardlink failed (%v) and copy fallback failed: %w", dst, linkErr, err)
		}
	}
	if err := replaceFile(tmpPath, dst); err != nil {
		return "", fmt.Errorf("replace %s: %w", dst, err)
	}
	return mode, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}

func RemoveShim(name string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(exe), strings.ToLower(name)+".exe")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("removed shim %-16s %s\n", name, path)
	return nil
}
