// Package state implements `cb state` and `cb gc`: reconciling the Docker
// volumes container-bin manages against the tool profiles and project root
// that apply to the current working directory.
package state

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/AviBackToBlack/container-bin/internal/dockervol"
	"github.com/AviBackToBlack/container-bin/internal/pathmap"
	"github.com/AviBackToBlack/container-bin/internal/registry"
)

type Volume struct {
	Name    string
	Kind    string
	Owner   string
	Root    string
	Current bool
}

func currentProjectState(reg registry.Registry) (map[string]Volume, map[string]Volume, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	cwd, err = pathmap.CanonicalPath(cwd)
	if err != nil {
		return nil, nil, err
	}
	project := map[string]Volume{}
	shared := map[string]Volume{}

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
				project[name] = Volume{Name: name, Kind: "project", Owner: t.StateGroup + "/" + logical, Root: root, Current: true}
			}
			for _, spec := range t.SharedVolumes {
				logical, _, err := registry.ParseVolumeBinding(spec)
				if err != nil {
					return nil, nil, err
				}
				name := pathmap.StatefulSharedVolumeID(t.StateGroup, logical)
				shared[name] = Volume{Name: name, Kind: "shared", Owner: t.StateGroup + "/" + logical}
			}
		case "python":
			root, found := pathmap.FindProjectRoot(cwd, pathmap.ProjectMarkersFor(t))
			if found {
				name := pathmap.PythonEnvID(root, true)
				project[name] = Volume{Name: name, Kind: "project", Owner: "python313/venv", Root: root, Current: true}
			} else {
				shared["cb-python-313-global"] = Volume{Name: "cb-python-313-global", Kind: "compat", Owner: "python313/global"}
			}
			shared["cb-pip-cache"] = Volume{Name: "cb-pip-cache", Kind: "shared", Owner: "python313/pip-cache"}
		}
	}
	return project, shared, nil
}

func Show(reg registry.Registry) error {
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

func GC(reg registry.Registry, args []string) error {
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
