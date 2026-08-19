// Package dockervol wraps the `docker volume` subcommands container-bin uses.
// It is a leaf: it knows nothing about the registry, tool profiles or paths,
// which lets both the run path (which must create volumes) and the state/gc
// and diagnostic paths (which must list and remove them) depend on it without
// depending on each other.
package dockervol

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

func Names() ([]string, error) {
	cmd := exec.Command("docker", "volume", "ls", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker volume ls: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "cb-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func ExistsSet() (map[string]bool, error) {
	names, err := Names()
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m, nil
}

func Remove(name string) error {
	cmd := exec.Command("docker", "volume", "rm", name)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

type Managed struct {
	Name   string
	Labels map[string]string
}

func LabeledManaged() ([]Managed, error) {
	cmd := exec.Command("docker", "volume", "ls", "--filter", "label=cb.managed=true", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker volume ls labels: %w", err)
	}
	var result []Managed
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		labels, err := Labels(name)
		if err != nil {
			return nil, err
		}
		result = append(result, Managed{Name: name, Labels: labels})
	}
	return result, nil
}

func Labels(name string) (map[string]string, error) {
	cmd := exec.Command("docker", "volume", "inspect", "--format", "{{range $k, $v := .Labels}}{{$k}}={{$v}}{{println}}{{end}}", name)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect volume %s: %w", name, err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m, nil
}

func EnsureManaged(name string, labels map[string]string) error {
	args := []string{"volume", "create"}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, name)
	cmd := exec.Command("docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ensure volume %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveQuiet is a best-effort cleanup helper for self-test's own
// temporary volumes only — unlike Remove (shared with cb gc/
// uninstall, which intentionally show their output), a self-test cleanup call
// commonly targets a volume that was never created (e.g. because an earlier
// check failed before python/node ever wrote to it), so "no such volume" is
// an expected, uninteresting outcome in both plain-text and --json mode, not
// something worth printing to the user.
func RemoveQuiet(name string) {
	_ = exec.Command("docker", "volume", "rm", name).Run()
}
