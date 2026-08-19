package registry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/atomicio"
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
	defaults := DefaultToolSections()
	var names []string
	for name := range defaults {
		if _, exists := reg.Tools[name]; !exists {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("\n# Added by container-bin " + version + "\n")
	for _, name := range names {
		b.WriteString(defaults[name])
	}
	return atomicio.WriteFile(path, []byte(b.String()), 0644)
}

func Load() (Registry, string, error) {
	path, err := Path()
	if err != nil {
		return Registry{}, "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		rec, err := atomicio.RecoverFromBackup(path, validateBackup)
		if err != nil {
			return Registry{}, path, err
		}
		if !rec {
			return Default(), path, nil
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return Registry{}, path, err
		}
	} else if err != nil {
		return Registry{}, path, err
	}
	reg, err := ParseTOML(string(data))
	return reg, path, err
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
	lines := strings.SplitAfter(string(data), "\n")
	var out strings.Builder
	skip := false
	for _, raw := range lines {
		trim := strings.TrimSpace(strings.TrimSuffix(raw, "\n"))
		if strings.HasPrefix(trim, "[tools.") && strings.HasSuffix(trim, "]") {
			name := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(trim, "[tools."), "]"))
			skip = remove[name]
		}
		if !skip {
			out.WriteString(raw)
		}
	}
	if _, err := ParseTOML(out.String()); err != nil {
		return fmt.Errorf("refusing registry rewrite: %w", err)
	}
	return atomicio.WriteFile(cfgPath, []byte(out.String()), 0644)
}

func InstallShims(reg Registry) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	names := make([]string, 0, len(reg.Tools))
	for name := range reg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dst := filepath.Join(dir, name+".exe")
		_ = os.Remove(dst)
		if err := os.Link(exe, dst); err != nil {
			if err := copyFile(exe, dst); err != nil {
				return fmt.Errorf("create %s: hardlink failed and copy fallback failed: %w", dst, err)
			}
			fmt.Printf("installed %-10s (copy fallback) -> %s\n", name, dst)
		} else {
			fmt.Printf("installed %-10s (hardlink)      -> %s\n", name, dst)
		}
	}
	fmt.Printf("\nRegistry:\n  %s\n\nAdd this directory near the front of PATH:\n  %s\n", filepath.Join(dir, "container-bin.toml"), dir)
	return nil
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
