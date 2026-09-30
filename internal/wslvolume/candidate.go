package wslvolume

import (
	"errors"
	"fmt"
	"strings"
)

// ProveCandidate reconstructs and validates the exact immutable volume
// identity represented by discovery output. Namespace discovery alone never
// establishes ownership; state, GC, backup, and restore consumers must use
// this proof before adopting or mutating a candidate.
func ProveCandidate(scope Scope, candidate Candidate) (Volume, error) {
	if !validNamespace(scope.namespace) || scope.prefix != "cb-"+scope.namespace+"-" {
		return Volume{}, errors.New("native WSL candidate proof requires a valid scope")
	}
	observed := engineVolume{
		Name: candidate.name, Driver: candidate.driver, Scope: candidate.scope, Labels: cloneLabels(candidate.labels),
	}
	if err := validateDiscoveredVolume(scope, observed); err != nil {
		return Volume{}, err
	}

	owner := observed.Labels["cb.owner"]
	group, logical, ok := strings.Cut(owner, "/")
	if !ok || strings.ContainsRune(logical, '/') {
		return Volume{}, fmt.Errorf("native WSL volume candidate %s has invalid owner %q", observed.Name, owner)
	}
	var expected Volume
	var err error
	switch observed.Labels["cb.kind"] {
	case "shared":
		expected, err = scope.Shared(group, logical)
	case "project":
		expected, err = scope.Project(group, logical, observed.Labels["cb.project_path"])
	default:
		return Volume{}, fmt.Errorf("native WSL volume candidate %s has unsupported kind %q", observed.Name, observed.Labels["cb.kind"])
	}
	if err != nil {
		return Volume{}, fmt.Errorf("reconstruct native WSL volume candidate %s: %w", observed.Name, err)
	}
	if err := requireExactVolume(expected, observed); err != nil {
		return Volume{}, fmt.Errorf("prove native WSL volume candidate %s: %w", observed.Name, err)
	}
	return expected, nil
}
